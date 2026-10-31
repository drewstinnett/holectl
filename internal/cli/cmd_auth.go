package cli

import (
	"flag"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/drewstinnett/holectl/internal/pihole"
)

func authCmd() *Command {
	return &Command{
		Name:    "auth",
		Summary: "Log in and out, and manage API sessions and app passwords",
		Long: `Sessions are cached in ~/.cache/holectl/sessions.json and reused across runs, because
Pi-hole caps concurrent sessions. You rarely need 'auth login': any command logs in on demand.
Use it to pass a 2FA code, which can't be supplied non-interactively later.`,
		Subs: []*Command{
			authLoginCmd(),
			authLogoutCmd(),
			getLeaf("status", "Show whether the current session is valid", "/auth"),
			getLeaf("sessions", "List all active API sessions", "/auth/sessions", "id", "current_session", "valid",
				"app", "cli", "remote_addr", "user_agent", "login_at", "valid_until"),
			{
				Name:      "revoke",
				Summary:   "Delete sessions by ID (see 'auth sessions')",
				Args:      "<id>...",
				MinArgs:   1,
				MaxArgs:   Unlimited,
				Mutates:   true,
				Endpoints: []string{"DELETE /api/auth/session/{id}"},
				Run: func(c *Ctx, args []string) error {
					return c.deleteEach(args, func(id string) string { return "/auth/session/" + pihole.Seg(id) })
				},
			},
			getLeaf("totp", "Generate a new TOTP secret suggestion (does not enable 2FA by itself)", "/auth/totp"),
			authAppPasswordCmd(),
		},
	}
}

func authLoginCmd() *Command {
	var totp int
	return &Command{
		Name:      "login",
		Summary:   "Open a session now (needed once with --totp when 2FA is enabled)",
		Endpoints: []string{"POST /api/auth"},
		Flags:     func(fs *flag.FlagSet) { fs.IntVar(&totp, "totp", 0, "current 2FA code") },
		Run: func(c *Ctx, _ []string) error {
			cl, err := c.Client()
			if err != nil {
				return err
			}
			return c.show(cl.Login(c, totp))
		},
	}
}

func authLogoutCmd() *Command {
	return &Command{
		Name:      "logout",
		Summary:   "End the cached session and forget it",
		Endpoints: []string{"DELETE /api/auth"},
		Run: func(c *Ctx, _ []string) error {
			cl, err := c.Client()
			if err != nil {
				return err
			}
			if err := cl.Logout(c); err != nil {
				return err
			}
			return c.printValue(map[string]bool{"success": true})
		},
	}
}

func authAppPasswordCmd() *Command {
	return &Command{
		Name:    "app-password",
		Summary: "Generate a new app password and its hash",
		Long: `The password is shown once. To activate it, set the returned hash:
  holectl server-config set webserver.api.app_pwhash '"<hash>"'
Activating a new app password invalidates all current sessions.`,
		Mutates:   true,
		Endpoints: []string{"GET /api/auth/app"},
		Run: func(c *Ctx, _ []string) error {
			cl, err := c.Client()
			if err != nil {
				return err
			}
			return c.show(cl.JSON(c, pihole.Request{Method: http.MethodGet, Path: "/auth/app", Mutating: true}))
		},
	}
}

// deleteEach issues one DELETE per item and reports per-item results.
func (c *Ctx) deleteEach(items []string, path func(string) string) error {
	results := make([]map[string]any, 0, len(items))
	var firstErr error
	for _, it := range items {
		_, err := c.call(http.MethodDelete, path(it), nil, nil)
		if err != nil && c.G.DryRun {
			continue
		}
		r := map[string]any{"item": it, "success": err == nil}
		if err != nil {
			r["error"] = err.Error()
			if firstErr == nil {
				firstErr = err
			}
		}
		results = append(results, r)
	}
	if c.G.DryRun {
		return c.printPlanned()
	}
	if len(items) == 1 && firstErr != nil {
		return firstErr
	}
	if err := c.printValue(map[string]any{"results": results}); err != nil {
		return err
	}
	if firstErr != nil {
		return &partialError{firstErr}
	}
	return nil
}

// partialError keeps the exit code of the first failure after results were already printed.
type partialError struct{ err error }

func (e *partialError) Error() string { return "some items failed: " + e.err.Error() }
func (e *partialError) Unwrap() error { return e.err }

func blockingCmd() *Command {
	var dur time.Duration
	set := func(enable bool) func(c *Ctx, _ []string) error {
		return func(c *Ctx, _ []string) error {
			body := map[string]any{"blocking": enable, "timer": nil}
			if dur > 0 {
				body["timer"] = dur.Seconds()
			}
			return c.show(c.call(http.MethodPost, "/dns/blocking", nil, body))
		}
	}
	forFlag := func(verb string) func(fs *flag.FlagSet) {
		return func(fs *flag.FlagSet) {
			fs.DurationVar(&dur, "for", 0, "revert after this long, e.g. 5m (default: "+verb+" permanently)")
		}
	}
	return &Command{
		Name:    "blocking",
		Summary: "Show or change DNS blocking (enable/disable ad blocking)",
		Subs: []*Command{
			{
				Name:      "status",
				Summary:   "Show whether blocking is enabled, and any active timer",
				Endpoints: []string{"GET /api/dns/blocking"},
				Run:       func(c *Ctx, _ []string) error { return c.show(c.get("/dns/blocking", nil)) },
			},
			{
				Name:      "enable",
				Summary:   "Enable blocking",
				Mutates:   true,
				Endpoints: []string{"POST /api/dns/blocking"},
				Flags:     forFlag("enable"),
				Run:       set(true),
			},
			{
				Name:      "disable",
				Summary:   "Disable blocking; use --for to re-enable automatically",
				Mutates:   true,
				Endpoints: []string{"POST /api/dns/blocking"},
				Flags:     forFlag("disable"),
				Run:       set(false),
			},
		},
	}
}

func searchCmd() *Command {
	var (
		partial, debug bool
		n              int
	)
	return &Command{
		Name:    "search",
		Summary: "Find a domain in allow/deny lists and subscribed blocklists (why is X blocked?)",
		Long: `Searches exact and regex domain entries and every gravity list for the domain.
Use --partial for substring matching.`,
		Args:      "<domain>",
		MinArgs:   1,
		MaxArgs:   1,
		Endpoints: []string{"GET /api/search/{domain}"},
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&partial, "partial", false, "substring match instead of exact")
			fs.IntVar(&n, "limit", 0, "maximum results (server default 20)")
			fs.BoolVar(&debug, "debug", false, "include debug information")
		},
		Run: func(c *Ctx, args []string) error {
			q := url.Values{}
			if partial {
				q.Set("partial", "true")
			}
			if n > 0 {
				q.Set("N", strconv.Itoa(n))
			}
			if debug {
				q.Set("debug", "true")
			}
			return c.show(c.get("/search/"+pihole.Seg(args[0]), q))
		},
	}
}
