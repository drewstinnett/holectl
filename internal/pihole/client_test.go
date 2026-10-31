package pihole

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

type fakeServer struct {
	*httptest.Server
	mu       sync.Mutex
	password string
	sid      string
	logins   int
	requests []string
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{password: "secret", sid: "sid-1"}
	f.Server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/api/auth" && r.Method == http.MethodPost {
		var body struct{ Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Password != f.password {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"key":"unauthorized","message":"Unauthorized","hint":null}}`)
			return
		}
		f.logins++
		_, _ = io.WriteString(w, `{"session":{"valid":true,"totp":false,"sid":"`+f.sid+
			`","csrf":"c","validity":1800,"message":"password correct"},"took":0.1}`)
		return
	}
	if r.Header.Get("X-FTL-SID") != f.sid {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"key":"unauthorized","message":"Unauthorized","hint":null}}`)
		return
	}
	switch r.URL.Path {
	case "/api/dns/blocking":
		_, _ = io.WriteString(w, `{"blocking":"enabled","timer":null,"took":0.1}`)
	case "/api/domains/deny/exact/missing.example":
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"key":"not_found","message":"Not found","hint":"/api/domains/deny/exact/missing.example"}}`)
	case "/api/auth":
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusTeapot)
	}
}

func newClient(t *testing.T, f *fakeServer, store SessionStore) *Client {
	t.Helper()
	c, err := New(Options{URL: f.URL, Password: "secret", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSessionIsCachedAndReused(t *testing.T) {
	f := newFakeServer(t)
	store := &FileStore{Path: filepath.Join(t.TempDir(), "sessions.json")}
	ctx := context.Background()

	if _, err := newClient(t, f, store).Get(ctx, "/dns/blocking", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := newClient(t, f, store).Get(ctx, "/dns/blocking", nil); err != nil {
		t.Fatal(err)
	}
	if f.logins != 1 {
		t.Fatalf("logins = %d, want 1 (second client should reuse the cached session)", f.logins)
	}
}

func TestExpiredCachedSessionLogsInAgain(t *testing.T) {
	f := newFakeServer(t)
	store := &FileStore{Path: filepath.Join(t.TempDir(), "sessions.json")}
	ctx := context.Background()
	if _, err := newClient(t, f, store).Get(ctx, "/dns/blocking", nil); err != nil {
		t.Fatal(err)
	}
	f.sid = "sid-2" // server forgot the old session

	if _, err := newClient(t, f, store).Get(ctx, "/dns/blocking", nil); err != nil {
		t.Fatalf("expected transparent re-login, got %v", err)
	}
	if f.logins != 2 {
		t.Fatalf("logins = %d, want 2", f.logins)
	}
	s, _ := store.Load(f.URL)
	if s == nil || s.SID != "sid-2" {
		t.Fatalf("cache not updated: %+v", s)
	}
}

func TestWrongPasswordIsAuthError(t *testing.T) {
	f := newFakeServer(t)
	c, _ := New(Options{URL: f.URL, Password: "nope"})
	_, err := c.Get(context.Background(), "/dns/blocking", nil)
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want *AuthError", err)
	}
}

func TestNoPasswordAgainstProtectedServer(t *testing.T) {
	f := newFakeServer(t)
	c, _ := New(Options{URL: f.URL})
	_, err := c.Get(context.Background(), "/dns/blocking", nil)
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("err = %v, want *AuthError", err)
	}
}

func TestAPIErrorIsParsed(t *testing.T) {
	f := newFakeServer(t)
	_, err := newClient(t, f, nil).Get(context.Background(), "/domains/deny/exact/missing.example", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Status != 404 || apiErr.Key != "not_found" {
		t.Fatalf("got %+v", apiErr)
	}
}

func TestDryRunSendsNoMutatingRequest(t *testing.T) {
	f := newFakeServer(t)
	var planned []PlannedRequest
	c, _ := New(Options{URL: f.URL, Password: "secret", DryRun: true,
		OnDryRun: func(p PlannedRequest) { planned = append(planned, p) }})
	_, err := c.JSON(context.Background(), Request{Method: http.MethodPost, Path: "/dns/blocking",
		Body: map[string]any{"blocking": false}})
	if !errors.Is(err, ErrDryRun) {
		t.Fatalf("err = %v, want ErrDryRun", err)
	}
	if len(f.requests) != 0 {
		t.Fatalf("requests sent: %v", f.requests)
	}
	if len(planned) != 1 || string(planned[0].Body) != `{"blocking":false}` {
		t.Fatalf("planned = %+v", planned)
	}
}

func TestLogoutClearsCache(t *testing.T) {
	f := newFakeServer(t)
	store := &FileStore{Path: filepath.Join(t.TempDir(), "sessions.json")}
	c := newClient(t, f, store)
	if _, err := c.Get(context.Background(), "/dns/blocking", nil); err != nil {
		t.Fatal(err)
	}
	if err := newClient(t, f, store).Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, _ := store.Load(f.URL); s != nil {
		t.Fatalf("session still cached: %+v", s)
	}
	if last := f.requests[len(f.requests)-1]; last != "DELETE /api/auth" {
		t.Fatalf("last request = %q", last)
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"pi.hole":                    "https://pi.hole",
		"http://10.0.0.2/admin/":     "http://10.0.0.2",
		"https://pi.hole/api":        "https://pi.hole",
		"https://pi.hole:8443/":      "https://pi.hole:8443",
		"http://host/pihole/admin":   "http://host/pihole",
		"  https://pi.hole/api/  \n": "https://pi.hole",
	}
	for in, want := range cases {
		got, err := NormalizeURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x", "https://"} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Errorf("NormalizeURL(%q) should fail", bad)
		}
	}
}

func TestSegEscapesSlashes(t *testing.T) {
	if got := Seg("https://a.b/c d.txt"); got != "https:%2F%2Fa.b%2Fc%20d.txt" {
		t.Fatalf("Seg = %q", got)
	}
}
