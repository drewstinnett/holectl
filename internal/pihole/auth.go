package pihole

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Session is an authenticated API session. Pi-hole caps concurrent sessions
// (webserver.api.max_sessions), so the CLI reuses one across runs instead of logging in per call.
type Session struct {
	SID      string    `json:"sid"`
	CSRF     string    `json:"csrf,omitempty"`
	Validity int       `json:"validity"`
	Created  time.Time `json:"created"`
}

// SessionStore persists sessions keyed by server base URL.
type SessionStore interface {
	Load(server string) (*Session, error)
	Save(server string, s *Session) error
	Delete(server string) error
}

// FileStore keeps sessions in a 0600 JSON file.
type FileStore struct {
	Path string
	mu   sync.Mutex
}

func (f *FileStore) read() (map[string]*Session, error) {
	m := map[string]*Session{}
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		// A corrupt cache is not worth failing a command over; it is only a cache.
		return map[string]*Session{}, nil //nolint:nilerr
	}
	return m, nil
}

func (f *FileStore) write(m map[string]*Session) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// Load returns the cached session for server, or nil.
func (f *FileStore) Load(server string) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return nil, err
	}
	return m[server], nil
}

// Save stores s for server.
func (f *FileStore) Save(server string, s *Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return err
	}
	m[server] = s
	return f.write(m)
}

// Delete removes the session for server.
func (f *FileStore) Delete(server string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return err
	}
	if _, ok := m[server]; !ok {
		return nil
	}
	delete(m, server)
	return f.write(m)
}

func (c *Client) ensureSession(ctx context.Context) error {
	if c.session != nil {
		return nil
	}
	if c.opts.Store != nil {
		if s, err := c.opts.Store.Load(c.base); err == nil && s != nil && s.SID != "" {
			c.session, c.cached = s, true
			return nil
		}
	}
	if c.opts.Password == "" {
		// Maybe the server has no password. Send unauthenticated; a 401 explains itself.
		return nil
	}
	_, err := c.Login(ctx, 0)
	return err
}

func (c *Client) forgetSession() {
	c.session, c.cached = nil, false
	if c.opts.Store != nil {
		_ = c.opts.Store.Delete(c.base)
	}
}

// Login opens a new session with the configured password. totp is the 2FA code, or 0.
// It returns the raw /api/auth response.
func (c *Client) Login(ctx context.Context, totp int) (json.RawMessage, error) {
	body := map[string]any{"password": c.opts.Password}
	if totp != 0 {
		body["totp"] = totp
	}
	raw, err := c.JSON(ctx, Request{Method: http.MethodPost, Path: "/auth", Body: body, NoAuth: true})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return nil, loginError(apiErr)
		}
		var authErr *AuthError
		if errors.As(err, &authErr) && errors.As(authErr.Err, &apiErr) {
			return nil, loginError(apiErr)
		}
		return nil, err
	}
	var resp struct {
		Session struct {
			Valid    bool    `json:"valid"`
			SID      *string `json:"sid"`
			CSRF     *string `json:"csrf"`
			Validity int     `json:"validity"`
			Message  *string `json:"message"`
		} `json:"session"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("unexpected /api/auth response: %w", err)
	}
	if !resp.Session.Valid {
		msg := "login failed"
		if resp.Session.Message != nil {
			msg += ": " + *resp.Session.Message
		}
		return nil, &AuthError{Reason: msg}
	}
	s := &Session{Validity: resp.Session.Validity, Created: time.Now().UTC()}
	if resp.Session.SID != nil {
		s.SID = *resp.Session.SID
	}
	if resp.Session.CSRF != nil {
		s.CSRF = *resp.Session.CSRF
	}
	c.session, c.cached = s, false
	if c.opts.Store != nil && s.SID != "" {
		_ = c.opts.Store.Save(c.base, s)
	}
	return raw, nil
}

func loginError(apiErr *APIError) error {
	switch apiErr.Key {
	case "totp_missing", "totp_invalid", "totp_reused":
		return &AuthError{
			Reason: "two-factor authentication required: run `holectl auth login --totp <code>` " +
				"(the session is then cached) or configure an app password",
			Err: apiErr,
		}
	}
	return &AuthError{Reason: "login failed", Err: apiErr}
}

// Logout ends the current session (DELETE /api/auth) and clears the cache.
// It is a no-op when there is no session to end.
func (c *Client) Logout(ctx context.Context) error {
	if c.session == nil && c.opts.Store != nil {
		if s, _ := c.opts.Store.Load(c.base); s != nil {
			c.session = s
		}
	}
	if c.session == nil || c.session.SID == "" {
		c.forgetSession()
		return nil
	}
	_, err := c.JSON(ctx, Request{Method: http.MethodDelete, Path: "/auth"})
	c.forgetSession()
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return nil // already expired server-side
	}
	return err
}

// HasSession reports whether a session was opened or loaded during this process.
func (c *Client) HasSession() bool { return c.session != nil && c.session.SID != "" }
