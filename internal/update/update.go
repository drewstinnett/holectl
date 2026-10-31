// Package update finds holectl releases on GitHub and replaces the running binary with one.
// It uses only the standard library, like the rest of holectl.
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// Repo is the GitHub repository releases come from.
const Repo = "drewstinnett/holectl"

// maxDownload caps any single download; a release archive is a few MB.
const maxDownload = 200 << 20

// ErrNotFound means the release, or an asset in it, does not exist.
var ErrNotFound = errors.New("not found")

// Client talks to GitHub. APIURL serves release metadata, DownloadURL serves release assets.
type Client struct {
	APIURL      string
	DownloadURL string
	HTTP        *http.Client
	Token       string // optional GitHub token; unauthenticated API calls are limited to 60/hour
}

// New returns a Client for github.com, authenticated with $GITHUB_TOKEN when it is set.
func New() *Client {
	return &Client{
		APIURL:      "https://api.github.com",
		DownloadURL: "https://github.com",
		HTTP:        &http.Client{Timeout: 2 * time.Minute},
		Token:       os.Getenv("GITHUB_TOKEN"),
	}
}

// Release is a published GitHub release.
type Release struct {
	Tag string `json:"tag_name"`
}

// HTTPError is a non-2xx response from GitHub.
type HTTPError struct {
	URL    string
	Status int
}

func (e *HTTPError) Error() string { return fmt.Sprintf("GET %s: HTTP %d", e.URL, e.Status) }

// Unwrap makes a 404 match ErrNotFound.
func (e *HTTPError) Unwrap() error {
	if e.Status == http.StatusNotFound {
		return ErrNotFound
	}
	return nil
}

// Latest returns the newest published release.
func (c *Client) Latest(ctx context.Context) (Release, error) {
	var rel Release
	body, err := c.get(ctx, c.APIURL+"/repos/"+Repo+"/releases/latest", true)
	if err != nil {
		return rel, err
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return rel, fmt.Errorf("parsing release: %w", err)
	}
	if rel.Tag == "" {
		return rel, errors.New("parsing release: no tag_name")
	}
	return rel, nil
}

func (c *Client) get(ctx context.Context, url string, api bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if api {
		req.Header.Set("Accept", "application/vnd.github+json")
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, &HTTPError{URL: url, Status: resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxDownload))
}

// parse accepts exactly "X.Y.Z" with an optional leading "v". Anything else (dev builds,
// "v0.1.0-3-gabc" from git describe) is not a release.
func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// IsRelease reports whether v is a release version rather than a development build.
func IsRelease(v string) bool {
	_, ok := parse(v)
	return ok
}

// Newer reports whether latest is a higher release than current. It is false whenever either
// is not a release version, so development builds are never offered an "update".
func Newer(current, latest string) bool {
	c, ok1 := parse(current)
	l, ok2 := parse(latest)
	if !ok1 || !ok2 {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// Same reports whether a and b name the same version, ignoring a leading "v".
func Same(a, b string) bool { return strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v") }

// AssetName is the release archive for a platform. It must match the archive name_template in
// .goreleaser.yaml and the names install.sh downloads.
func AssetName(goos, goarch, goarm string) string {
	name := "holectl_" + goos + "_" + goarch
	if goarch == "arm" {
		name += "v" + goarm
	}
	if goos == "windows" {
		return name + ".zip"
	}
	return name + ".tar.gz"
}

// CurrentAsset is AssetName for the running binary.
func CurrentAsset() string {
	return AssetName(runtime.GOOS, runtime.GOARCH, goarm())
}

func goarm() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "GOARM" && s.Value != "" {
				return s.Value[:1] // "7" or "7,softfloat"
			}
		}
	}
	return "7"
}

// Apply downloads tag's archive for this platform, verifies it against the release's
// checksums.txt and atomically replaces the executable at exe.
func (c *Client) Apply(ctx context.Context, tag, exe string) error {
	asset := CurrentAsset()
	base := c.DownloadURL + "/" + Repo + "/releases/download/" + tag + "/"
	sums, err := c.get(ctx, base+"checksums.txt", false)
	if err != nil {
		return fmt.Errorf("downloading checksums: %w", err)
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return err
	}
	archive, err := c.get(ctx, base+asset, false)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", asset, err)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s: refusing to install", asset)
	}
	bin, err := extract(archive, asset)
	if err != nil {
		return err
	}
	return replace(exe, bin)
}

func checksumFor(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s (platform not released?): %w", asset, ErrNotFound)
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "holectl.exe"
	}
	return "holectl"
}

func extract(archive []byte, asset string) ([]byte, error) {
	if strings.HasSuffix(asset, ".zip") {
		return extractZip(archive, asset)
	}
	return extractTar(archive, asset)
}

func extractZip(archive []byte, asset string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", asset, err)
	}
	for _, f := range zr.File {
		if path.Base(f.Name) != binaryName() || !f.Mode().IsRegular() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		return io.ReadAll(io.LimitReader(rc, maxDownload))
	}
	return nil, fmt.Errorf("%s does not contain %s", asset, binaryName())
}

func extractTar(archive []byte, asset string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", asset, err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s does not contain %s", asset, binaryName())
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", asset, err)
		}
		if path.Base(h.Name) == binaryName() && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

// replace writes bin next to exe and renames it into place, so a failure never leaves a
// half-written binary. Windows can't overwrite a running executable, but it can rename one.
func replace(exe string, bin []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".holectl-update-*")
	if err != nil {
		return permissionHint(exe, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(bin); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil { //nolint:gosec // an executable must be executable
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			return permissionHint(exe, err)
		}
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		return permissionHint(exe, err)
	}
	return nil
}

func permissionHint(exe string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("replacing %s: %w (rerun with sudo, or reinstall with install.sh into a writable directory)",
			exe, err)
	}
	return fmt.Errorf("replacing %s: %w", exe, err)
}
