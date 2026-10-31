package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/drewstinnett/holectl/internal/config"
	"github.com/drewstinnett/holectl/internal/pihole"
)

// Globals are flags accepted by every command, anywhere on the command line.
type Globals struct {
	Output         string
	Field          string
	URL            string
	ConfigPath     string
	Timeout        time.Duration
	Insecure       bool
	DryRun         bool
	NoSessionCache bool
	Quiet          bool
	Help           bool
}

type globalFlag struct {
	name, short, usage string
	takesValue         bool
}

var globalFlags = []globalFlag{
	{"output", "o", "output format: auto (table on a terminal, json otherwise), json, table", true},
	{"field", "", "print only this dot-separated path of the response, e.g. blocking or domains.0.id", true},
	{"url", "", "Pi-hole URL (overrides config and HOLECTL_URL)", true},
	{"config", "", "config file path (default $HOLECTL_CONFIG or ~/.config/holectl/config.json)", true},
	{"timeout", "", "per-request timeout, e.g. 30s (overrides config)", true},
	{"insecure", "k", "skip TLS certificate verification", false},
	{"dry-run", "n", "print mutating requests instead of sending them", false},
	{"no-session-cache", "", "don't reuse or save the API session; log out when done", false},
	{"quiet", "q", "suppress normal output (errors still go to stderr)", false},
	{"help", "h", "show help", false},
}

func globalTakesValue(tok string) bool {
	if strings.Contains(tok, "=") {
		return false
	}
	name := strings.TrimLeft(tok, "-")
	for _, f := range globalFlags {
		if f.takesValue && (name == f.name || (f.short != "" && name == f.short)) {
			return true
		}
	}
	return false
}

func isGlobalFlag(name string) bool {
	for _, f := range globalFlags {
		if name == f.name || name == f.short {
			return true
		}
	}
	return false
}

const shorthandPrefix = "shorthand for --"

// keyDryRun is the JSON key every locally built response uses to say nothing was changed.
const keyDryRun = "dry_run"

func (g *Globals) register(fs *flag.FlagSet) {
	bind := func(name, short, usage string, fn func(name, usage string)) {
		fn(name, usage)
		if short != "" {
			fn(short, shorthandPrefix+name)
		}
	}
	for _, f := range globalFlags {
		switch f.name {
		case "output":
			bind(f.name, f.short, f.usage, func(n, u string) { fs.StringVar(&g.Output, n, "", u) })
		case "field":
			fs.StringVar(&g.Field, f.name, "", f.usage)
		case "url":
			fs.StringVar(&g.URL, f.name, "", f.usage)
		case "config":
			fs.StringVar(&g.ConfigPath, f.name, "", f.usage)
		case "timeout":
			fs.DurationVar(&g.Timeout, f.name, 0, f.usage)
		case "insecure":
			bind(f.name, f.short, f.usage, func(n, u string) { fs.BoolVar(&g.Insecure, n, false, u) })
		case "dry-run":
			bind(f.name, f.short, f.usage, func(n, u string) { fs.BoolVar(&g.DryRun, n, false, u) })
		case "no-session-cache":
			fs.BoolVar(&g.NoSessionCache, f.name, false, f.usage)
		case "quiet":
			bind(f.name, f.short, f.usage, func(n, u string) { fs.BoolVar(&g.Quiet, n, false, u) })
		case "help":
			bind(f.name, f.short, f.usage, func(n, u string) { fs.BoolVar(&g.Help, n, false, u) })
		}
	}
}

// Ctx is passed to every command's Run.
type Ctx struct {
	context.Context
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	G              *Globals

	root    *Command
	cmd     *Command
	fs      *flag.FlagSet
	cfg     *config.Config
	cfgPath string
	format  string
	client  *pihole.Client
	planned []pihole.PlannedRequest
}

func (c *Ctx) loadConfig() error {
	c.cfgPath = c.G.ConfigPath
	if c.cfgPath == "" {
		c.cfgPath = config.DefaultPath()
	}
	c.format = "auto"
	cfg, err := config.Load(c.cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.ApplyEnv(); err != nil {
		return err
	}
	c.cfg = cfg
	if cfg.Output != "" {
		c.format = cfg.Output
	}
	if c.G.Output != "" {
		c.format = c.G.Output
	}
	switch c.format {
	case "auto":
		c.format = "json"
		if isTerminal(c.Stdout) {
			c.format = "table"
		}
	case "json", "table":
	default:
		return usagef(c.cmd, "invalid --output %q: want auto, json or table", c.format)
	}
	return nil
}

// Set reports whether the named flag was given on the command line.
func (c *Ctx) Set(name string) bool {
	found := false
	c.fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// Client builds the API client on first use.
func (c *Ctx) Client() (*pihole.Client, error) {
	if c.client != nil {
		return c.client, nil
	}
	cfg := *c.cfg
	if c.G.URL != "" {
		cfg.URL = c.G.URL
	}
	if c.G.Insecure {
		cfg.Insecure = true
	}
	timeout := cfg.TimeoutDuration()
	if c.G.Timeout > 0 {
		timeout = c.G.Timeout
	}
	pw, err := cfg.ResolvePassword()
	if err != nil {
		return nil, err
	}
	var store pihole.SessionStore
	if !c.G.NoSessionCache {
		store = &pihole.FileStore{Path: config.SessionCachePath()}
	}
	cl, err := pihole.New(pihole.Options{
		URL:       cfg.URL,
		Password:  pw,
		Insecure:  cfg.Insecure,
		Timeout:   timeout,
		Store:     store,
		DryRun:    c.G.DryRun,
		OnDryRun:  func(p pihole.PlannedRequest) { c.planned = append(c.planned, p) },
		UserAgent: "holectl/" + Version,
	})
	if err != nil {
		return nil, usagef(c.cmd, "%v", err)
	}
	c.client = cl
	return cl, nil
}

// call sends one request and returns its JSON body.
func (c *Ctx) call(method, path string, q url.Values, body any) (json.RawMessage, error) {
	cl, err := c.Client()
	if err != nil {
		return nil, err
	}
	return cl.JSON(c, pihole.Request{Method: method, Path: path, Query: q, Body: body})
}

func (c *Ctx) get(path string, q url.Values) (json.RawMessage, error) {
	return c.call(http.MethodGet, path, q, nil)
}

// show is the common tail of a command: print the response, or the dry-run plan.
func (c *Ctx) show(raw json.RawMessage, err error) error {
	if err != nil {
		return err
	}
	if raw == nil {
		raw = json.RawMessage(`{"success":true}`)
	}
	return c.print(raw)
}

// items expands positional args, reading newline-separated values from stdin for "-".
// Blank lines and lines starting with # are skipped.
func (c *Ctx) items(args []string) ([]string, error) {
	var out []string
	for _, a := range args {
		if a != "-" {
			out = append(out, a)
			continue
		}
		sc := bufio.NewScanner(c.Stdin)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			out = append(out, line)
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
	}
	if len(out) == 0 {
		return nil, usagef(c.cmd, "no items given")
	}
	return out, nil
}

// groupIDs turns group names or numeric IDs into IDs, fetching /groups only when needed.
func (c *Ctx) groupIDs(refs []string) ([]int, error) {
	ids := make([]int, 0, len(refs))
	var byName map[string]int
	for _, r := range refs {
		if id, err := strconv.Atoi(r); err == nil {
			ids = append(ids, id)
			continue
		}
		if byName == nil {
			raw, err := c.get("/groups", nil)
			if err != nil {
				return nil, fmt.Errorf("resolving group names: %w", err)
			}
			var resp struct {
				Groups []struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				} `json:"groups"`
			}
			if err := json.Unmarshal(raw, &resp); err != nil {
				return nil, fmt.Errorf("resolving group names: %w", err)
			}
			byName = map[string]int{}
			for _, gr := range resp.Groups {
				byName[gr.Name] = gr.ID
			}
		}
		id, ok := byName[r]
		if !ok {
			return nil, &notFoundError{fmt.Sprintf("no group named %q", r)}
		}
		ids = append(ids, id)
	}
	return ids, nil
}

type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }

// fail reports err on stderr in the active format and returns its exit code.
func (c *Ctx) fail(err error) int {
	if errors.Is(err, pihole.ErrDryRun) {
		if perr := c.printPlanned(); perr != nil {
			err = perr
		} else {
			return ExitOK
		}
	}
	code, kind := classify(err)
	if c.format == "json" || (c.format == "" && !isTerminal(c.Stderr)) {
		e := map[string]any{"code": kind, "exit_code": code, "message": err.Error()}
		var apiErr *pihole.APIError
		if errors.As(err, &apiErr) {
			e["status"] = apiErr.Status
			if apiErr.Key != "" {
				e["key"] = apiErr.Key
			}
			if apiErr.Hint != nil {
				e["hint"] = apiErr.Hint
			}
		}
		if h := errorHint(err); h != "" {
			e["suggestion"] = h
		}
		b, _ := json.Marshal(map[string]any{"error": e})
		_, _ = fmt.Fprintln(c.Stderr, string(b))
		return code
	}
	_, _ = fmt.Fprintf(c.Stderr, "holectl: %v\n", err)
	if h := errorHint(err); h != "" {
		_, _ = fmt.Fprintf(c.Stderr, "hint: %s\n", h)
	}
	var ue *usageError
	if errors.As(err, &ue) && ue.cmd != nil {
		_, _ = fmt.Fprintf(c.Stderr, "run '%s --help' for usage\n", ue.cmd.fullPath())
	}
	return code
}

func classify(err error) (int, string) {
	var (
		ue      *usageError
		authErr *pihole.AuthError
		netErr  *pihole.NetworkError
		apiErr  *pihole.APIError
		nf      *notFoundError
	)
	switch {
	case errors.As(err, &ue):
		return ExitUsage, "usage"
	case errors.As(err, &authErr):
		return ExitAuth, "auth"
	case errors.As(err, &nf):
		return ExitNotFound, "not_found"
	case errors.As(err, &apiErr):
		if apiErr.Status == http.StatusNotFound {
			return ExitNotFound, "not_found"
		}
		if apiErr.Status == http.StatusForbidden {
			return ExitAuth, "auth"
		}
		return ExitAPI, "api"
	case errors.As(err, &netErr), errors.Is(err, context.DeadlineExceeded):
		return ExitNetwork, "network"
	}
	return ExitError, "error"
}

func errorHint(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "x509:") || strings.Contains(msg, "certificate"):
		return "Pi-hole uses a self-signed certificate by default: pass --insecure, " +
			"run `holectl config set insecure true`, or use an http:// URL"
	case strings.Contains(msg, "connection refused"):
		return "check the URL and port; Pi-hole v6 serves the API on the web interface's port (80/443 by default)"
	case strings.Contains(msg, "no_seats") || strings.Contains(msg, "seats"):
		return "the server is out of API sessions; run `holectl auth sessions` and `holectl auth revoke <id>`, " +
			"or raise webserver.api.max_sessions"
	}
	return ""
}
