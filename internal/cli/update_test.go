package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/drewstinnett/holectl/internal/update"
)

// fakeReleases runs holectl as version against a GitHub whose latest release is v0.2.0 and
// whose downloads all 404.
func fakeReleases(t *testing.T, version string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/"+update.Repo+"/releases/latest" {
			_, _ = w.Write([]byte(`{"tag_name":"v0.2.0"}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	exe := filepath.Join(t.TempDir(), "holectl")
	oldV, oldU, oldE := Version, newUpdater, executablePath
	Version = version
	newUpdater = func() *update.Client {
		return &update.Client{APIURL: srv.URL, DownloadURL: srv.URL, HTTP: srv.Client()}
	}
	executablePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { Version, newUpdater, executablePath = oldV, oldU, oldE })
	return exe
}

func TestUpdateCheckReportsAvailable(t *testing.T) {
	exe := fakeReleases(t, "0.1.0")
	out, errOut, code := run(t, nil, "", "update", "--check")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	jsonEq(t, []byte(out), fmt.Sprintf(`{"current":"0.1.0","latest":"v0.2.0","update_available":true,
		"action":"available","path":%q,"dry_run":false}`, exe))
}

func TestUpdateUpToDateIsUnchanged(t *testing.T) {
	exe := fakeReleases(t, "0.2.0")
	out, _, code := run(t, nil, "", "update")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	jsonEq(t, []byte(out), fmt.Sprintf(`{"current":"0.2.0","latest":"v0.2.0","update_available":false,
		"action":"unchanged","path":%q,"dry_run":false}`, exe))
}

func TestUpdateDevBuildNeedsForce(t *testing.T) {
	fakeReleases(t, "dev-0123456789ab")
	out, _, code := run(t, nil, "", "update")
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	jsonEq(t, []byte(out), `{"current":"dev-0123456789ab","latest":"v0.2.0","update_available":false,
		"action":"unchanged","dry_run":false,
		"note":"this is a development build; use --force to replace it with v0.2.0"}`)
}

func TestUpdateUnknownVersionIsNotFound(t *testing.T) {
	fakeReleases(t, "0.1.0")
	_, errOut, code := run(t, nil, "", "update", "--version", "9.9.9")
	if code != ExitNotFound {
		t.Fatalf("exit %d, want %d: %s", code, ExitNotFound, errOut)
	}
}
