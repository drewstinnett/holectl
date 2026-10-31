package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub serves one release, tag (latestTag unless a test changes it), whose assets are built for the running platform.
type fakeGitHub struct {
	*httptest.Server
	tag      string
	files    map[string][]byte
	apiCalls atomic.Int32
}

// latestTag is the release newFakeGitHub publishes.
const latestTag = "v0.2.0"

func newFakeGitHub(t *testing.T, bin []byte) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{tag: latestTag, files: map[string][]byte{}}
	asset := CurrentAsset()
	f.files[asset] = archiveFor(t, asset, bin)
	sum := sha256.Sum256(f.files[asset])
	f.files["checksums.txt"] = fmt.Appendf(nil, "%s  %s\n%s  holectl_plan9_386.tar.gz\n",
		hex.EncodeToString(sum[:]), asset, strings.Repeat("0", 64))
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/"+Repo+"/releases/latest" {
			f.apiCalls.Add(1)
			fmt.Fprintf(w, `{"tag_name":%q}`, f.tag)
			return
		}
		prefix := "/" + Repo + "/releases/download/" + f.tag + "/"
		if data, ok := f.files[strings.TrimPrefix(r.URL.Path, prefix)]; ok && strings.HasPrefix(r.URL.Path, prefix) {
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGitHub) client() *Client {
	return &Client{APIURL: f.URL, DownloadURL: f.URL, HTTP: f.Client()}
}

func archiveFor(t *testing.T, asset string, bin []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if strings.HasSuffix(asset, ".zip") {
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(binaryName())
		_, _ = w.Write(bin)
		_ = zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	readme := []byte("# holectl\n")
	_ = tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: int64(len(readme)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(readme)
	_ = tw.WriteHeader(&tar.Header{Name: binaryName(), Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(bin)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func fakeExe(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), binaryName())
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.0", "v0.2.0", true},
		{"v0.1.9", "v0.1.10", true},
		{"0.9.9", "1.0.0", true},
		{"0.2.0", "v0.2.0", false},
		{"0.3.0", "v0.2.0", false},
		{"dev", "v9.9.9", false},
		{"dev-0123456789ab", "v9.9.9", false},
		{"v0.1.0-3-gabcdef", "v0.2.0", false},
		{"0.1.0", "", false},
		{"0.1.0", "garbage", false},
	}
	for _, tc := range cases {
		if got := Newer(tc.current, tc.latest); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

// TestAssetNameMatchesGoReleaser keeps AssetName, .goreleaser.yaml and install.sh in agreement.
func TestAssetNameMatchesGoReleaser(t *testing.T) {
	cfg, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	const tmpl = `holectl_{{ .Os }}_{{ .Arch }}{{ with .Arm }}v{{ . }}{{ end }}`
	if !bytes.Contains(cfg, []byte(tmpl)) {
		t.Fatalf(".goreleaser.yaml no longer uses the archive name_template %s; update AssetName and install.sh", tmpl)
	}
	install, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[[3]string]string{
		{"linux", "amd64", ""}:   "holectl_linux_amd64.tar.gz",
		{"linux", "arm", "6"}:    "holectl_linux_armv6.tar.gz",
		{"linux", "arm", "7"}:    "holectl_linux_armv7.tar.gz",
		{"darwin", "arm64", ""}:  "holectl_darwin_arm64.tar.gz",
		{"windows", "amd64", ""}: "holectl_windows_amd64.zip",
	}
	for in, want := range cases {
		if got := AssetName(in[0], in[1], in[2]); got != want {
			t.Errorf("AssetName%v = %q, want %q", in, got, want)
		}
	}
	for _, arch := range []string{"amd64", "arm64", "armv6", "armv7"} {
		if !regexp.MustCompile(`(?m)arch=` + arch + `\b`).Match(install) {
			t.Errorf("install.sh never selects arch %s", arch)
		}
	}
}

func TestApplyReplacesExecutable(t *testing.T) {
	gh := newFakeGitHub(t, []byte("new binary"))
	exe := fakeExe(t)
	if err := gh.client().Apply(context.Background(), "v0.2.0", exe); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new binary" {
		t.Fatalf("exe = %q", got)
	}
	if st, _ := os.Stat(exe); runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("exe not executable: %v", st.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".holectl-update-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestApplyRefusesChecksumMismatch(t *testing.T) {
	gh := newFakeGitHub(t, []byte("new binary"))
	gh.files[CurrentAsset()] = archiveFor(t, CurrentAsset(), []byte("tampered"))
	exe := fakeExe(t)
	err := gh.client().Apply(context.Background(), "v0.2.0", exe)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary" {
		t.Fatalf("exe was modified: %q", got)
	}
}

func TestApplyUnknownTagIsNotFound(t *testing.T) {
	gh := newFakeGitHub(t, []byte("new binary"))
	err := gh.client().Apply(context.Background(), "v9.9.9", fakeExe(t))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestNoticeCachesForADay(t *testing.T) {
	gh := newFakeGitHub(t, nil)
	state := filepath.Join(t.TempDir(), "cache", "update-check.json")
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	c := gh.client()

	msg := c.Notice(context.Background(), "0.1.0", state, now)
	if !strings.Contains(msg, "0.2.0 is available (you have 0.1.0)") {
		t.Fatalf("notice = %q", msg)
	}
	gh.tag = "v0.3.0"
	if msg := c.Notice(context.Background(), "0.1.0", state, now.Add(time.Hour)); !strings.Contains(msg, "0.2.0") {
		t.Fatalf("fresh cache should answer without asking GitHub: %q", msg)
	}
	if n := gh.apiCalls.Load(); n != 1 {
		t.Fatalf("api calls = %d, want 1", n)
	}
	if msg := c.Notice(context.Background(), "0.1.0", state, now.Add(25*time.Hour)); !strings.Contains(msg, "0.3.0") {
		t.Fatalf("stale cache should re-check: %q", msg)
	}
	if msg := c.Notice(context.Background(), "0.3.0", state, now.Add(25*time.Hour)); msg != "" {
		t.Fatalf("up to date should print nothing: %q", msg)
	}
	if msg := c.Notice(context.Background(), "dev", state, now); msg != "" {
		t.Fatalf("dev build should print nothing: %q", msg)
	}
}

func TestNoticeOfflineChecksOncePerDay(t *testing.T) {
	gh := newFakeGitHub(t, nil)
	c := gh.client()
	gh.Close() // offline
	state := filepath.Join(t.TempDir(), "update-check.json")
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if msg := c.Notice(context.Background(), "0.1.0", state, now); msg != "" {
		t.Fatalf("offline notice = %q", msg)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatalf("failed check should still be recorded: %v", err)
	}
}

func TestNoticeCanceledRecordsNothing(t *testing.T) {
	gh := newFakeGitHub(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	state := filepath.Join(t.TempDir(), "update-check.json")
	if msg := gh.client().Notice(ctx, "0.1.0", state, time.Now()); msg != "" {
		t.Fatalf("notice = %q", msg)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled check should not be recorded: %v", err)
	}
}
