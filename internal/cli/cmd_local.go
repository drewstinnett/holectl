package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/drewstinnett/holectl/internal/config"
)

func configCmd() *Command {
	return &Command{
		Name:    "config",
		Summary: "Manage holectl's local config file (not the Pi-hole's settings; see server-config)",
		Subs:    []*Command{configInitCmd(), configShowCmd(), configSetCmd(), configUnsetCmd(), configPathCmd()},
	}
}

func configInitCmd() *Command {
	var (
		cfg     config.Config
		pwStdin bool
		force   bool
	)
	return &Command{
		Name:    "init",
		Summary: "Create the config file from flags (non-interactive)",
		Long: `Writes the config file (0600). Prefer --password-stdin or --password-command over --password,
which is visible in the process list. Example:
  printf '%s' "$PW" | holectl config init --url https://pi.hole --insecure --password-stdin`,
		Mutates: true,
		Local:   true,
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&cfg.Password, "password", "", "web or app password (prefer --password-stdin)")
			fs.BoolVar(&pwStdin, "password-stdin", false, "read the password from stdin")
			fs.StringVar(&cfg.PasswordCommand, "password-command", "", "shell command that prints the password")
			fs.StringVar(&cfg.Output, "default-output", "", "default output format to save: auto, json, table")
			fs.BoolVar(&force, "force", false, "overwrite an existing config file")
		},
		Run: func(c *Ctx, _ []string) error {
			// --url, --insecure and --timeout are global flags; init saves whatever they were given.
			cfg.URL, cfg.Insecure = c.G.URL, c.G.Insecure
			if c.G.Timeout > 0 {
				cfg.Timeout = c.G.Timeout.String()
			}
			if _, err := os.Stat(c.cfgPath); err == nil && !force {
				return usagef(c.cmd, "%s already exists; pass --force to overwrite or use `holectl config set`", c.cfgPath)
			}
			if pwStdin {
				pw, err := readSecret(c.Stdin)
				if err != nil {
					return err
				}
				cfg.Password = pw
			}
			if cfg.URL == "" {
				return usagef(c.cmd, "--url is required")
			}
			if c.G.DryRun {
				return c.printValue(map[string]any{keyDryRun: true, "path": c.cfgPath, "config": cfg.Redacted()})
			}
			if err := cfg.Save(c.cfgPath); err != nil {
				return err
			}
			return c.printValue(map[string]any{"path": c.cfgPath, "config": cfg.Redacted()})
		},
	}
}

func readSecret(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 64*1024))
	if err != nil {
		return "", fmt.Errorf("reading password from stdin: %w", err)
	}
	pw := strings.TrimRight(string(b), "\r\n")
	if pw == "" {
		return "", errors.New("empty password on stdin")
	}
	return pw, nil
}

func configShowCmd() *Command {
	return &Command{
		Name:    "show",
		Summary: "Show the config file (password redacted) and active environment overrides",
		Local:   true,
		Run: func(c *Ctx, _ []string) error {
			fileCfg, err := config.Load(c.cfgPath)
			if err != nil {
				return err
			}
			_, statErr := os.Stat(c.cfgPath)
			var env []string
			for _, k := range []string{config.EnvURL, config.EnvPassword, config.EnvInsecure, config.EnvOutput} {
				if os.Getenv(k) != "" {
					env = append(env, k)
				}
			}
			return c.printValue(map[string]any{
				"path":          c.cfgPath,
				"exists":        statErr == nil,
				"config":        fileCfg.Redacted(),
				"env_overrides": append([]string{}, env...),
			})
		},
	}
}

func configSetCmd() *Command {
	var keys []string
	for _, k := range config.Keys {
		keys = append(keys, fmt.Sprintf("  %-17s %s", k.Name, k.Desc))
	}
	return &Command{
		Name:    "set",
		Summary: "Set one config key (use - as the value to read it from stdin)",
		Long:    "Keys:\n" + strings.Join(keys, "\n"),
		Args:    "<key> <value>",
		MinArgs: 2,
		MaxArgs: 2,
		Mutates: true,
		Local:   true,
		Run: func(c *Ctx, args []string) error {
			cfg, err := config.Load(c.cfgPath)
			if err != nil {
				return err
			}
			val := args[1]
			if val == "-" {
				if val, err = readSecret(c.Stdin); err != nil {
					return err
				}
			}
			if err := cfg.Set(args[0], val); err != nil {
				return usagef(c.cmd, "%v", err)
			}
			if !c.G.DryRun {
				if err := cfg.Save(c.cfgPath); err != nil {
					return err
				}
			}
			return c.printValue(map[string]any{"path": c.cfgPath, "config": cfg.Redacted(), keyDryRun: c.G.DryRun})
		},
	}
}

func configUnsetCmd() *Command {
	return &Command{
		Name:    "unset",
		Summary: "Clear one config key",
		Args:    "<key>",
		MinArgs: 1,
		MaxArgs: 1,
		Mutates: true,
		Local:   true,
		Run: func(c *Ctx, args []string) error {
			cfg, err := config.Load(c.cfgPath)
			if err != nil {
				return err
			}
			zero := ""
			if args[0] == "insecure" {
				zero = "false"
			}
			if err := cfg.Set(args[0], zero); err != nil {
				return usagef(c.cmd, "%v", err)
			}
			if !c.G.DryRun {
				if err := cfg.Save(c.cfgPath); err != nil {
					return err
				}
			}
			return c.printValue(map[string]any{"path": c.cfgPath, "config": cfg.Redacted(), keyDryRun: c.G.DryRun})
		},
	}
}

func configPathCmd() *Command {
	return &Command{
		Name:    "path",
		Summary: "Print the config file path",
		Local:   true,
		Run: func(c *Ctx, _ []string) error {
			if c.format == "json" {
				return c.printValue(map[string]string{"path": c.cfgPath, "session_cache": config.SessionCachePath()})
			}
			_, err := fmt.Fprintln(c.Stdout, c.cfgPath)
			return err
		},
	}
}

func versionCmd() *Command {
	return &Command{
		Name:    "version",
		Summary: "Print holectl's version (the server's is `holectl info version`)",
		Local:   true,
		Run: func(c *Ctx, _ []string) error {
			return c.printValue(map[string]string{
				"version": Version, "go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH,
			})
		},
	}
}
