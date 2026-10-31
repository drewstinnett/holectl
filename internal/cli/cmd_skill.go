package cli

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
)

// skillTarget is where SKILL.md goes: --dir, else ./.claude/skills with --project, else
// $CLAUDE_CONFIG_DIR/skills, else ~/.claude/skills.
type skillTarget struct {
	dir     string
	project bool
}

func (st *skillTarget) register(fs *flag.FlagSet) {
	fs.StringVar(&st.dir, "dir", "", "skills directory (default $CLAUDE_CONFIG_DIR/skills or ~/.claude/skills)")
	fs.BoolVar(&st.project, "project", false, "use ./.claude/skills (this project only) instead of your user skills")
}

func (st *skillTarget) path() (string, error) {
	dir := st.dir
	switch {
	case dir != "":
	case st.project:
		dir = filepath.Join(".claude", "skills")
	case os.Getenv("CLAUDE_CONFIG_DIR") != "":
		dir = filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "skills")
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".claude", "skills")
	}
	return filepath.Join(dir, "holectl", "SKILL.md"), nil
}

// skillState compares the installed file with the embedded skill: missing, current or outdated.
func skillState(path string) (string, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "missing", nil
	case err != nil:
		return "", err
	case bytes.Equal(data, []byte(SkillMarkdown)):
		return "current", nil
	}
	return "outdated", nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil { //nolint:gosec // a skill is not a secret
		return err
	}
	return os.Rename(tmp, path)
}

func skillCmd() *Command {
	return &Command{
		Name:    "skill",
		Summary: "Install, update or inspect the agent skill (SKILL.md) that teaches agents to use holectl",
		Long: `The skill is built into this binary, so it always matches this version's commands.
Re-run 'holectl skill install' after upgrading holectl to bring an installed copy up to date.`,
		Subs: []*Command{skillInstallCmd(), skillStatusCmd(), skillUninstallCmd(), {
			Name:    "print",
			Summary: "Print SKILL.md to stdout",
			Local:   true,
			Run: func(c *Ctx, _ []string) error {
				_, err := io.WriteString(c.Stdout, SkillMarkdown)
				return err
			},
		}},
	}
}

func skillInstallCmd() *Command {
	var st skillTarget
	return &Command{
		Name:    "install",
		Aliases: []string{"update"},
		Summary: "Install the skill, or update an installed copy to this version (safe to re-run)",
		Long: `Reports "action": "installed", "updated" or "unchanged". Updating overwrites the installed
SKILL.md, so keep local edits in a separate skill.`,
		Mutates: true,
		Local:   true,
		Flags:   st.register,
		Run: func(c *Ctx, _ []string) error {
			path, err := st.path()
			if err != nil {
				return err
			}
			state, err := skillState(path)
			if err != nil {
				return err
			}
			action := map[string]string{"missing": "installed", "outdated": "updated", "current": "unchanged"}[state]
			if action != "unchanged" && !c.G.DryRun {
				if err := writeFileAtomic(path, []byte(SkillMarkdown)); err != nil {
					return err
				}
			}
			return c.printValue(map[string]any{"path": path, "action": action, "version": Version, keyDryRun: c.G.DryRun})
		},
	}
}

func skillStatusCmd() *Command {
	var st skillTarget
	return &Command{
		Name:    "status",
		Summary: "Show whether the skill is installed and matches this binary",
		Local:   true,
		Flags:   st.register,
		Run: func(c *Ctx, _ []string) error {
			path, err := st.path()
			if err != nil {
				return err
			}
			state, err := skillState(path)
			if err != nil {
				return err
			}
			return c.printValue(map[string]any{
				"path": path, "installed": state != "missing", "up_to_date": state == "current", "version": Version,
			})
		},
	}
}

func skillUninstallCmd() *Command {
	var st skillTarget
	return &Command{
		Name:    "uninstall",
		Aliases: []string{"remove", "rm"},
		Summary: "Remove the installed skill",
		Mutates: true,
		Local:   true,
		Flags:   st.register,
		Run: func(c *Ctx, _ []string) error {
			path, err := st.path()
			if err != nil {
				return err
			}
			state, err := skillState(path)
			if err != nil {
				return err
			}
			if state != "missing" && !c.G.DryRun {
				if err := os.Remove(path); err != nil {
					return err
				}
				_ = os.Remove(filepath.Dir(path)) // only succeeds if nothing else lives there
			}
			return c.printValue(map[string]any{"path": path, "removed": state != "missing", keyDryRun: c.G.DryRun})
		},
	}
}
