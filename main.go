// Command holectl is a CLI for the Pi-hole v6 REST API.
package main

import (
	"context"
	_ "embed"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/drewstinnett/holectl/internal/cli"
)

//go:embed skills/holectl/SKILL.md
var skill string

// version is set with -ldflags "-X main.version=...".
var version = ""

func main() {
	cli.SkillMarkdown = skill
	cli.Version = resolveVersion()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func resolveVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 12 {
			return "dev-" + s.Value[:12]
		}
	}
	return "dev"
}
