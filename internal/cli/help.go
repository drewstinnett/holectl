package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

type flagSpec struct {
	Name    string `json:"name"`
	Short   string `json:"short,omitempty"`
	Type    string `json:"type"`
	Default string `json:"default,omitempty"`
	Usage   string `json:"usage"`
}

type commandSpec struct {
	Command   string     `json:"command"`
	Summary   string     `json:"summary"`
	Usage     string     `json:"usage"`
	Args      string     `json:"args,omitempty"`
	MinArgs   int        `json:"min_args"`
	MaxArgs   int        `json:"max_args"`
	Mutates   bool       `json:"mutates"`
	Local     bool       `json:"local,omitempty"`
	Endpoints []string   `json:"endpoints,omitempty"`
	Flags     []flagSpec `json:"flags"`
	Details   string     `json:"details,omitempty"`
}

func flagType(f *flag.Flag) string {
	if t, ok := f.Value.(interface{ Type() string }); ok {
		return t.Type()
	}
	if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
		return "bool"
	}
	switch fmt.Sprintf("%T", f.Value) {
	case "*flag.intValue", "*flag.int64Value", "*flag.uintValue", "*flag.uint64Value":
		return "int"
	case "*flag.durationValue":
		return "duration"
	case "*flag.float64Value":
		return "float"
	}
	return "string"
}

// flagSpecs lists a command's flags; local=true excludes globals.
func flagSpecs(cmd *Command, local bool) []flagSpec {
	fs := cmd.newFlagSet(&Globals{})
	shorts := map[string]string{}
	fs.VisitAll(func(f *flag.Flag) {
		if strings.HasPrefix(f.Usage, shorthandPrefix) {
			shorts[strings.TrimPrefix(f.Usage, shorthandPrefix)] = f.Name
		}
	})
	out := []flagSpec{}
	fs.VisitAll(func(f *flag.Flag) {
		if strings.HasPrefix(f.Usage, shorthandPrefix) || (local && isGlobalFlag(f.Name)) {
			return
		}
		def := f.DefValue
		if def == "false" || def == "0" || def == "0s" || def == "[]" {
			def = ""
		}
		out = append(out, flagSpec{Name: f.Name, Short: shorts[f.Name], Type: flagType(f), Default: def, Usage: f.Usage})
	})
	return out
}

func usageLine(cmd *Command) string {
	u := cmd.fullPath()
	if len(cmd.Subs) > 0 {
		return u + " <command>"
	}
	if len(flagSpecs(cmd, true)) > 0 {
		u += " [flags]"
	}
	if cmd.Args != "" {
		u += " " + cmd.Args
	}
	return u
}

func printHelp(w io.Writer, cmd *Command) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	defer func() { _ = tw.Flush() }()
	if cmd.Summary != "" {
		_, _ = fmt.Fprintf(tw, "%s\n\n", cmd.Summary)
	}
	if cmd.Long != "" {
		_, _ = fmt.Fprintf(tw, "%s\n\n", strings.TrimSpace(cmd.Long))
	}
	_, _ = fmt.Fprintf(tw, "Usage:\n  %s\n", usageLine(cmd))
	if len(cmd.Subs) > 0 {
		_, _ = fmt.Fprintf(tw, "\nCommands:\n")
		for _, s := range cmd.Subs {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\n", s.Name, s.Summary)
		}
	}
	if len(cmd.Endpoints) > 0 {
		_, _ = fmt.Fprintf(tw, "\nAPI:\n")
		for _, e := range cmd.Endpoints {
			_, _ = fmt.Fprintf(tw, "  %s\n", e)
		}
	}
	if local := flagSpecs(cmd, true); len(local) > 0 {
		_, _ = fmt.Fprintf(tw, "\nFlags:\n")
		writeFlags(tw, local)
	}
	if cmd.parent == nil {
		_, _ = fmt.Fprintf(tw, "\nGlobal flags (accepted anywhere):\n")
		writeFlags(tw, globalSpecs())
		_, _ = fmt.Fprintf(tw, "\nRun 'holectl help --json' for a machine-readable description of every command.\n")
	} else {
		_, _ = fmt.Fprintf(tw, "\nGlobal flags: -o/--output, --field, --url, --config, --timeout, -k/--insecure, "+
			"-n/--dry-run, --no-session-cache, -q/--quiet\n")
	}
}

func writeFlags(w io.Writer, specs []flagSpec) {
	for _, f := range specs {
		name := "--" + f.Name
		if f.Short != "" {
			name = "-" + f.Short + ", " + name
		}
		if f.Type != "bool" {
			name += " " + f.Type
		}
		usage := f.Usage
		if f.Default != "" {
			usage += fmt.Sprintf(" (default %s)", f.Default)
		}
		_, _ = fmt.Fprintf(w, "  %s\t%s\n", name, usage)
	}
}

func globalSpecs() []flagSpec {
	root := &Command{Name: "holectl"}
	all := flagSpecs(root, false)
	return all
}

func helpCmd() *Command {
	var asJSON bool
	return &Command{
		Name:    "help",
		Summary: "Show help for a command, or describe every command as JSON (--json)",
		Args:    "[command...]",
		MaxArgs: Unlimited,
		Local:   true,
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&asJSON, "json", false, "emit the whole command tree as JSON for agents")
		},
		Run: func(c *Ctx, args []string) error {
			if asJSON {
				return writeHelpJSON(c.Stdout, c.root)
			}
			target := c.root
			for _, a := range args {
				next := target.find(a)
				if next == nil {
					return usagef(c.cmd, "unknown command %q", strings.Join(args, " "))
				}
				target = next
			}
			printHelp(c.Stdout, target)
			return nil
		},
	}
}

func writeHelpJSON(w io.Writer, root *Command) error {
	var cmds []commandSpec
	for _, l := range root.leaves() {
		cmds = append(cmds, commandSpec{
			Command:   strings.TrimPrefix(l.fullPath(), "holectl "),
			Summary:   l.Summary,
			Usage:     usageLine(l),
			Args:      l.Args,
			MinArgs:   l.MinArgs,
			MaxArgs:   l.MaxArgs,
			Mutates:   l.Mutates,
			Local:     l.Local,
			Endpoints: l.Endpoints,
			Flags:     flagSpecs(l, true),
			Details:   strings.TrimSpace(l.Long),
		})
	}
	doc := map[string]any{
		"name":         "holectl",
		"version":      Version,
		"description":  "CLI for the Pi-hole v6 REST API",
		"global_flags": globalSpecs(),
		"exit_codes": map[string]string{
			"0": "success (including --dry-run)",
			"1": "other error",
			"2": "usage error (bad flags or arguments)",
			"3": "authentication failed or required",
			"4": "not found",
			"5": "API rejected the request",
			"6": "network error or timeout",
		},
		"output": "JSON responses are the Pi-hole API's own, unmodified. Errors go to stderr as " +
			`{"error":{"code","exit_code","message","key","hint","status"}} when output is json.`,
		"commands": cmds,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}
