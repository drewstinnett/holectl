package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drewstinnett/holectl/internal/config"
	"github.com/drewstinnett/holectl/internal/pihole"
	"github.com/drewstinnett/holectl/internal/update"
)

// EnvNoUpdateCheck turns off the passive "update available" notice.
const EnvNoUpdateCheck = "HOLECTL_NO_UPDATE_CHECK"

// noticeGrace is how long a finished command waits for an in-flight update check.
const noticeGrace = 500 * time.Millisecond

// Overridden in tests.
var (
	newUpdater     = update.New
	executablePath = func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(exe)
	}
)

func updateCmd() *Command {
	var (
		check, force bool
		want         string
	)
	return &Command{
		Name:    "update",
		Aliases: []string{"upgrade", "self-update"},
		Summary: "Update holectl itself to the latest GitHub release (checksum-verified)",
		Long: `Replaces this binary in place. --check (or --dry-run) only reports whether an update exists.
On a terminal, holectl also mentions a new release on stderr at most once a day;
set HOLECTL_NO_UPDATE_CHECK=1 to turn that off.`,
		Mutates: true,
		Local:   true,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&check, "check", false, "only report whether an update is available")
			fs.StringVar(&want, "version", "", "install this release, e.g. v0.3.0 (allows downgrades)")
			fs.BoolVar(&force, "force", false, "reinstall even when up to date, or replace a development build")
		},
		Run: func(c *Ctx, _ []string) error { return c.runUpdate(want, check, force) },
	}
}

func (c *Ctx) runUpdate(want string, check, force bool) error {
	u := newUpdater()
	tag := want
	if tag == "" {
		rel, err := u.Latest(c)
		if err != nil {
			return updateErr("finding the latest release", err)
		}
		tag = rel.Tag
	} else if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	needed := force || update.Newer(Version, tag) || (want != "" && !update.Same(Version, tag))
	out := map[string]any{
		"current": Version, "latest": tag, "update_available": needed, "action": "unchanged", keyDryRun: c.G.DryRun,
	}
	if !update.IsRelease(Version) && !force {
		out["update_available"] = false
		out["note"] = "this is a development build; use --force to replace it with " + tag
		return c.printValue(out)
	}
	exe, err := executablePath()
	if err != nil {
		return fmt.Errorf("locating the running binary: %w", err)
	}
	out["path"] = exe
	switch {
	case !needed:
	case check || c.G.DryRun:
		out["action"] = "available"
	default:
		if err := u.Apply(c, tag, exe); err != nil {
			return updateErr("updating to "+tag, err)
		}
		out["action"] = "updated"
	}
	return c.printValue(out)
}

func updateErr(what string, err error) error {
	var ue *url.Error
	switch {
	case errors.Is(err, update.ErrNotFound):
		return &notFoundError{fmt.Sprintf("%s: %v", what, err)}
	case errors.As(err, &ue):
		return &pihole.NetworkError{Err: fmt.Errorf("%s: %w", what, err)}
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (c *Ctx) wantUpdateNotice() bool {
	switch c.cmd.Name {
	case "update", "version", "help":
		return false
	}
	return isTerminal(c.Stderr) && !c.G.Quiet && update.IsRelease(Version) && os.Getenv(EnvNoUpdateCheck) == ""
}

// startUpdateNotice checks for a new release alongside the command. The returned func prints
// the notice to stderr, waiting at most noticeGrace for a check still in flight.
func (c *Ctx) startUpdateNotice() func() {
	if !c.wantUpdateNotice() {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(c.Context))
	ch := make(chan string, 1)
	go func() {
		ch <- newUpdater().Notice(ctx, Version, filepath.Join(config.CacheDir(), "update-check.json"), time.Now())
	}()
	return func() {
		defer cancel()
		select {
		case msg := <-ch:
			if msg != "" {
				_, _ = fmt.Fprintf(c.Stderr, "holectl: %s\n", msg)
			}
		case <-time.After(noticeGrace):
		}
	}
}
