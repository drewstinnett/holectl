// Package cli implements holectl's command tree on top of the standard library's flag package.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Unlimited as MaxArgs accepts any number of positional arguments.
const Unlimited = -1

// Command is a node in the command tree. Leaves have Run; groups have Subs.
type Command struct {
	Name      string
	Aliases   []string
	Summary   string
	Long      string
	Args      string // positional synopsis, e.g. "<domain>..."
	MinArgs   int
	MaxArgs   int
	Endpoints []string // API calls this command makes, e.g. "POST /api/domains/{type}/{kind}"
	Mutates   bool     // changes server state; honors --dry-run
	Columns   []string // preferred columns for table output
	Flags     func(fs *flag.FlagSet)
	Run       func(c *Ctx, args []string) error
	Subs      []*Command
	// Local commands never contact the Pi-hole.
	Local bool

	parent *Command
}

func (cmd *Command) path() string {
	if cmd.parent == nil {
		return cmd.Name
	}
	if cmd.parent.parent == nil {
		return cmd.Name
	}
	return cmd.parent.path() + " " + cmd.Name
}

func (cmd *Command) fullPath() string {
	if p := cmd.path(); p != "holectl" {
		return "holectl " + p
	}
	return "holectl"
}

func (cmd *Command) find(name string) *Command {
	for _, s := range cmd.Subs {
		if s.Name == name {
			return s
		}
		for _, a := range s.Aliases {
			if a == name {
				return s
			}
		}
	}
	return nil
}

func (cmd *Command) link() {
	for _, s := range cmd.Subs {
		s.parent = cmd
		s.link()
	}
}

func (cmd *Command) leaves() []*Command {
	if len(cmd.Subs) == 0 {
		return []*Command{cmd}
	}
	var out []*Command
	for _, s := range cmd.Subs {
		out = append(out, s.leaves()...)
	}
	return out
}

func (cmd *Command) newFlagSet(g *Globals) *flag.FlagSet {
	fs := flag.NewFlagSet(cmd.fullPath(), flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	g.register(fs)
	if cmd.Flags != nil {
		cmd.Flags(fs)
	}
	return fs
}

// Exit codes. They are part of the CLI's contract with scripts and agents.
const (
	ExitOK       = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitAuth     = 3
	ExitNotFound = 4
	ExitAPI      = 5
	ExitNetwork  = 6
)

type usageError struct {
	msg string
	cmd *Command
}

func (e *usageError) Error() string { return e.msg }

func usagef(cmd *Command, format string, a ...any) error {
	return &usageError{msg: fmt.Sprintf(format, a...), cmd: cmd}
}

// Version is set by main (ldflags or build info).
var Version = "dev"

// SkillMarkdown is the embedded agent skill, set by main.
var SkillMarkdown string

// Run executes holectl with args (excluding the program name) and returns the exit code.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := buildTree()
	g := &Globals{}
	c := &Ctx{Context: ctx, Stdin: stdin, Stdout: stdout, Stderr: stderr, G: g, root: root}

	cmd, rest, err := resolve(root, args)
	if err != nil {
		return c.fail(err)
	}
	c.cmd = cmd
	fs := cmd.newFlagSet(g)
	c.fs = fs
	pos, err := parseInterleaved(fs, rest)
	if errors.Is(err, flag.ErrHelp) || (err == nil && g.Help) {
		printHelp(stdout, cmd)
		return ExitOK
	}
	if err != nil {
		return c.fail(usagef(cmd, "%v", err))
	}
	if err := c.loadConfig(); err != nil {
		return c.fail(err)
	}
	if cmd.Run == nil {
		if len(pos) > 0 {
			return c.fail(usagef(cmd, "unknown command %q for %q", pos[0], cmd.fullPath()))
		}
		printHelp(stderr, cmd)
		return ExitUsage
	}
	if len(pos) < cmd.MinArgs || (cmd.MaxArgs != Unlimited && len(pos) > cmd.MaxArgs) {
		return c.fail(usagef(cmd, "%s expects %s", cmd.fullPath(), argCountDesc(cmd)))
	}
	if g.Timeout > 0 {
		var cancel context.CancelFunc
		c.Context, cancel = context.WithTimeout(c.Context, g.Timeout+5*time.Second)
		defer cancel()
	}
	notice := c.startUpdateNotice()
	err = cmd.Run(c, pos)
	if c.G.NoSessionCache && c.client != nil && c.client.HasSession() {
		_ = c.client.Logout(context.WithoutCancel(ctx))
	}
	defer notice()
	if err != nil {
		return c.fail(err)
	}
	return ExitOK
}

func argCountDesc(cmd *Command) string {
	switch {
	case cmd.MaxArgs == 0:
		return "no arguments"
	case cmd.MinArgs == cmd.MaxArgs:
		return fmt.Sprintf("%d argument(s): %s", cmd.MinArgs, cmd.Args)
	case cmd.MaxArgs == Unlimited:
		return fmt.Sprintf("at least %d argument(s): %s", cmd.MinArgs, cmd.Args)
	default:
		return fmt.Sprintf("%d to %d arguments: %s", cmd.MinArgs, cmd.MaxArgs, cmd.Args)
	}
}

// resolve walks args to the deepest matching command. Global flags may appear anywhere, so
// tokens that look like flags are set aside (with their value, for value-taking globals).
func resolve(root *Command, args []string) (*Command, []string, error) {
	cmd := root
	var rest []string
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if tok == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if strings.HasPrefix(tok, "-") && tok != "-" {
			rest = append(rest, tok)
			if globalTakesValue(tok) && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
			continue
		}
		if len(cmd.Subs) == 0 {
			rest = append(rest, args[i:]...)
			break
		}
		sub := cmd.find(tok)
		if sub == nil {
			return nil, nil, usagef(cmd, "unknown command %q for %q; valid: %s",
				tok, cmd.fullPath(), strings.Join(subNames(cmd), ", "))
		}
		cmd = sub
	}
	return cmd, rest, nil
}

func subNames(cmd *Command) []string {
	out := make([]string, 0, len(cmd.Subs))
	for _, s := range cmd.Subs {
		out = append(out, s.Name)
	}
	return out
}

// parseInterleaved parses flags that may appear before, between, or after positionals.
// Everything after a literal "--" is positional.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
