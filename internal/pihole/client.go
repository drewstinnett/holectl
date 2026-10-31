// Package pihole is a small client for the Pi-hole v6 REST API (/api/*).
//
// It deliberately returns raw JSON rather than typed structs: the CLI passes responses through
// unmodified, so nothing the server sends is lost to a struct that doesn't know about it yet.
package pihole

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Options configures a Client.
type Options struct {
	URL        string        // Pi-hole base URL, e.g. https://pi.hole (a trailing /api or /admin is stripped)
	Password   string        // web password or app password; empty for servers without one
	Insecure   bool          // skip TLS verification (Pi-hole ships a self-signed certificate)
	Timeout    time.Duration // per-request timeout; 0 means 30s
	Store      SessionStore  // where to cache the session between runs; nil keeps it in memory
	DryRun     bool          // don't send mutating requests; call OnDryRun instead
	OnDryRun   func(PlannedRequest)
	UserAgent  string
	HTTPClient *http.Client // overrides Insecure/Timeout when set (tests)
}

// Request is one API call. Path is relative to /api and must already be escaped (see Seg).
type Request struct {
	Method      string
	Path        string
	Query       url.Values
	Body        any    // JSON-encoded when non-nil
	RawBody     []byte // sent as-is with ContentType, takes precedence over Body
	ContentType string
	Mutating    bool // treat as mutating for dry-run even though the method is GET
	NoAuth      bool
}

// PlannedRequest describes a request that dry-run mode declined to send.
type PlannedRequest struct {
	Method      string          `json:"method"`
	URL         string          `json:"url"`
	ContentType string          `json:"content_type,omitempty"`
	Body        json.RawMessage `json:"body,omitempty"`
	BodyBytes   int             `json:"body_bytes,omitempty"`
}

// Client talks to one Pi-hole server.
type Client struct {
	base    string
	opts    Options
	http    *http.Client
	session *Session
	// cached is true when session came from the store rather than a login in this process,
	// so a 401 means "expired" and is worth one re-login.
	cached bool
}

// New builds a client. It does not contact the server.
func New(opts Options) (*Client, error) {
	base, err := NormalizeURL(opts.URL)
	if err != nil {
		return nil, err
	}
	hc := opts.HTTPClient
	if hc == nil {
		timeout := opts.Timeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if opts.Insecure {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user opted in
		}
		hc = &http.Client{Transport: tr, Timeout: timeout}
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "holectl"
	}
	return &Client{base: base, opts: opts, http: hc}, nil
}

// NormalizeURL turns user input like "pi.hole", "http://10.0.0.2/admin/" or
// "https://pi.hole/api" into a base URL without a trailing path.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("no Pi-hole URL configured (set it with `holectl config init --url ...` or HOLECTL_URL)")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid Pi-hole URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid Pi-hole URL %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid Pi-hole URL %q: missing host", raw)
	}
	p := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/api", "/admin"} {
		p = strings.TrimSuffix(p, suffix)
	}
	u.Path, u.RawQuery, u.Fragment = p, "", ""
	return strings.TrimRight(u.String(), "/"), nil
}

// BaseURL is the normalized server URL, used as the session cache key.
func (c *Client) BaseURL() string { return c.base }

// Seg escapes one path segment, including any '/' inside it (regex domains, list URLs).
func Seg(s string) string { return url.PathEscape(s) }

// URL is the full URL a request would hit.
func (c *Client) URL(req Request) string {
	u := c.base + "/api" + req.Path
	if len(req.Query) > 0 {
		u += "?" + req.Query.Encode()
	}
	return u
}

// Do sends a request and returns the response for any 2xx status. The caller closes the body.
// Non-2xx responses become *APIError (or *AuthError for 401).
func (c *Client) Do(ctx context.Context, req Request) (*http.Response, error) {
	body, ctype, err := encodeBody(req)
	if err != nil {
		return nil, err
	}
	// Login (the only NoAuth request) is exempt: dry-run reads still need a session.
	if c.opts.DryRun && !req.NoAuth && (req.Method != http.MethodGet || req.Mutating) {
		if c.opts.OnDryRun != nil {
			c.opts.OnDryRun(planned(c.URL(req), req.Method, ctype, body))
		}
		return nil, ErrDryRun
	}
	if !req.NoAuth {
		if err := c.ensureSession(ctx); err != nil {
			return nil, err
		}
	}
	resp, err := c.send(ctx, req, body, ctype)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && !req.NoAuth && c.cached && c.opts.Password != "" {
		drain(resp)
		c.forgetSession()
		if _, err := c.Login(ctx, 0); err != nil {
			return nil, err
		}
		if resp, err = c.send(ctx, req, body, ctype); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		apiErr := parseAPIError(resp.StatusCode, data)
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, &AuthError{Reason: c.unauthorizedReason(), Err: apiErr}
		}
		return nil, apiErr
	}
	return resp, nil
}

// JSON sends a request and returns the response body. An empty body (204) returns nil.
func (c *Client) JSON(ctx context.Context, req Request) (json.RawMessage, error) {
	resp, err := c.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &NetworkError{Err: err}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	return data, nil
}

// Get is shorthand for a GET returning JSON.
func (c *Client) Get(ctx context.Context, path string, q url.Values) (json.RawMessage, error) {
	return c.JSON(ctx, Request{Method: http.MethodGet, Path: path, Query: q})
}

func (c *Client) unauthorizedReason() string {
	if c.opts.Password == "" {
		return "the server requires a password (set one with `holectl config set password ...`, " +
			"password_command, or HOLECTL_PASSWORD)"
	}
	return "unauthorized"
}

func (c *Client) send(ctx context.Context, req Request, body []byte, ctype string) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	hreq, err := http.NewRequestWithContext(ctx, req.Method, c.URL(req), rdr)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("User-Agent", c.opts.UserAgent)
	hreq.Header.Set("Accept", "application/json")
	if ctype != "" {
		hreq.Header.Set("Content-Type", ctype)
	}
	if !req.NoAuth && c.session != nil && c.session.SID != "" {
		hreq.Header.Set("X-FTL-SID", c.session.SID)
		if c.session.CSRF != "" {
			hreq.Header.Set("X-FTL-CSRF", c.session.CSRF)
		}
	}
	resp, err := c.http.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &NetworkError{Err: err}
	}
	return resp, nil
}

func encodeBody(req Request) ([]byte, string, error) {
	if req.RawBody != nil {
		return req.RawBody, req.ContentType, nil
	}
	if req.Body == nil {
		return nil, "", nil
	}
	b, err := json.Marshal(req.Body)
	if err != nil {
		return nil, "", fmt.Errorf("encoding request body: %w", err)
	}
	return b, "application/json", nil
}

func planned(u, method, ctype string, body []byte) PlannedRequest {
	p := PlannedRequest{Method: method, URL: u, ContentType: ctype}
	if body == nil {
		return p
	}
	if ctype == "application/json" && json.Valid(body) {
		p.Body = body
	} else {
		p.BodyBytes = len(body)
	}
	return p
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}
