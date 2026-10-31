package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestSkillOnlyMentionsRealCommandsAndFlags keeps SKILL.md honest as commands change: every
// "holectl ..." invocation in it must resolve, and every --flag must exist on that command.
func TestSkillOnlyMentionsRealCommandsAndFlags(t *testing.T) {
	data, err := os.ReadFile("../../skills/holectl/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	root := buildTree()
	invocation := regexp.MustCompile("holectl ((?:[a-z][a-z-]*)(?: [a-z][a-z-]*)*)([^`\n|]*)")
	flagRe := regexp.MustCompile(`(?:^|\s)--([a-z][a-z-]*)`)
	checked := 0
	for _, m := range invocation.FindAllStringSubmatch(string(data), -1) {
		words := strings.Fields(m[1])
		cmd := root
		for _, w := range words {
			next := cmd.find(w)
			if next == nil {
				break
			}
			cmd = next
		}
		if cmd == root {
			t.Errorf("SKILL.md mentions unknown command %q", "holectl "+m[1])
			continue
		}
		if len(cmd.Subs) > 0 {
			continue // "holectl <group>" in prose
		}
		known := map[string]bool{}
		for _, f := range flagSpecs(cmd, false) {
			known[f.Name] = true
		}
		for _, fm := range flagRe.FindAllStringSubmatch(m[0], -1) {
			if !known[fm[1]] {
				t.Errorf("SKILL.md: %q has no flag --%s (in %q)", cmd.fullPath(), fm[1], strings.TrimSpace(m[0]))
			}
		}
		checked++
	}
	if checked < 30 {
		t.Fatalf("only %d invocations checked; regexp broken?", checked)
	}
}
