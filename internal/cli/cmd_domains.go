package cli

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"

	"github.com/drewstinnett/holectl/internal/pihole"
)

// typeKind is the --type/--kind pair every domains subcommand takes.
type typeKind struct {
	typ  *choice
	kind *choice
}

func newTypeKind() *typeKind {
	return &typeKind{typ: newChoice("allow", "deny"), kind: newChoice("exact", "regex")}
}

func (tk *typeKind) register(fs *flag.FlagSet, required bool) {
	req := " (optional filter)"
	if required {
		req = " (required)"
	}
	fs.Var(tk.typ, "type", "allow or deny"+req)
	fs.Var(tk.kind, "kind", "exact or regex (default exact)")
}

// need returns type and kind for commands that require a type; kind defaults to exact.
func (tk *typeKind) need(c *Ctx) (string, string, error) {
	if tk.typ.val == "" {
		return "", "", usagef(c.cmd, "--type allow|deny is required")
	}
	k := tk.kind.val
	if k == "" {
		k = "exact"
	}
	return tk.typ.val, k, nil
}

func domainPath(t, k, d string) string { return "/domains/" + t + "/" + k + "/" + pihole.Seg(d) }

func domainsCmd() *Command {
	return &Command{
		Name:    "domains",
		Aliases: []string{"domain"},
		Summary: "Manage allowed and denied domains (exact and regex)",
		Subs:    []*Command{domainsListCmd(), domainsAddCmd(), domainsUpdateCmd(), domainsRemoveCmd()},
	}
}

func domainsListCmd() *Command {
	tk := newTypeKind()
	return &Command{
		Name:      "list",
		Summary:   "List domain entries, optionally filtered by --type/--kind or a specific domain",
		Args:      "[domain]",
		MaxArgs:   1,
		Columns:   domainColumns,
		Endpoints: []string{"GET /api/domains[/{type}[/{kind}[/{domain}]]]"},
		Flags:     func(fs *flag.FlagSet) { tk.register(fs, false) },
		Run: func(c *Ctx, args []string) error {
			p := "/domains"
			switch {
			case tk.typ.val != "" && tk.kind.val != "":
				p += "/" + tk.typ.val + "/" + tk.kind.val
			case tk.typ.val != "":
				p += "/" + tk.typ.val
			case tk.kind.val != "":
				return usagef(c.cmd, "--kind needs --type")
			}
			if len(args) == 1 && tk.typ.val != "" && tk.kind.val != "" {
				return c.show(c.get(p+"/"+pihole.Seg(args[0]), nil))
			}
			raw, err := c.get(p, nil)
			if err == nil && len(args) == 1 {
				raw, err = filterList(raw, "domains", "domain", args[0])
			}
			return c.show(raw, err)
		},
	}
}

func domainsAddCmd() *Command {
	var (
		tk       = newTypeKind()
		f        commonFlags
		wildcard bool
	)
	return &Command{
		Name:    "add",
		Summary: "Add domains to the allow or deny list ('-' reads domains from stdin)",
		Long: `Examples:
  holectl domains add --type deny ads.example.com tracker.example.net
  holectl domains add --type allow --wildcard example.com     # example.com and all subdomains
  holectl domains add --type deny --kind regex '^ad[0-9]*\.'`,
		Args:      "<domain>...",
		MinArgs:   1,
		MaxArgs:   Unlimited,
		Mutates:   true,
		Columns:   domainColumns,
		Endpoints: []string{"POST /api/domains/{type}/{kind}"},
		Flags: func(fs *flag.FlagSet) {
			tk.register(fs, true)
			f.addFlags(fs, true, true)
			fs.BoolVar(&wildcard, "wildcard", false, "match the domain and all subdomains (stored as regex)")
		},
		Run: func(c *Ctx, args []string) error {
			t, k, items, err := domainItems(c, tk, args, wildcard)
			if err != nil {
				return err
			}
			body, err := c.addBody(&f, "domain", items, true)
			if err != nil {
				return err
			}
			return c.showProcessed(c.call(http.MethodPost, "/domains/"+t+"/"+k, nil, body))
		},
	}
}

// domainItems resolves type/kind and the domain list, rewriting to wildcard regexes if asked.
func domainItems(c *Ctx, tk *typeKind, args []string, wildcard bool) (string, string, []string, error) {
	t, k, err := tk.need(c)
	if err != nil {
		return "", "", nil, err
	}
	items, err := c.items(args)
	if err != nil {
		return "", "", nil, err
	}
	if wildcard {
		if tk.kind.val == "exact" {
			return "", "", nil, usagef(c.cmd, "--wildcard conflicts with --kind exact")
		}
		k = "regex"
		for i, d := range items {
			items[i] = wildcardRegex(d)
		}
	}
	return t, k, items, nil
}

func domainsUpdateCmd() *Command {
	var (
		tk     = newTypeKind()
		moveTo = newTypeKind()
		f      commonFlags
	)
	return &Command{
		Name:      "update",
		Summary:   "Change a domain's comment, groups, enabled state, or move it with --new-type/--new-kind",
		Args:      "<domain>",
		MinArgs:   1,
		MaxArgs:   1,
		Mutates:   true,
		Columns:   domainColumns,
		Endpoints: []string{"GET /api/domains/{type}/{kind}/{domain}", "PUT /api/domains/{type}/{kind}/{domain}"},
		Flags: func(fs *flag.FlagSet) {
			tk.register(fs, true)
			f.updateFlags(fs, true, true)
			fs.Var(moveTo.typ, "new-type", "move to this type (allow or deny)")
			fs.Var(moveTo.kind, "new-kind", "move to this kind (exact or regex)")
		},
		Run: func(c *Ctx, args []string) error {
			t, k, err := tk.need(c)
			if err != nil {
				return err
			}
			p := domainPath(t, k, args[0])
			cur, err := c.fetchOne(p, nil, "domains", fmt.Sprintf("%s %s domain %q", t, k, args[0]))
			if err != nil {
				return err
			}
			body, err := c.mergeUpdate(cur, &f, "comment", "groups", "enabled")
			if err != nil {
				return err
			}
			body["type"], body["kind"] = t, k
			if moveTo.typ.val != "" {
				body["type"] = moveTo.typ.val
			}
			if moveTo.kind.val != "" {
				body["kind"] = moveTo.kind.val
			}
			return c.showProcessed(c.call(http.MethodPut, p, nil, body))
		},
	}
}

func domainsRemoveCmd() *Command {
	var (
		tk       = newTypeKind()
		wildcard bool
	)
	return &Command{
		Name:      "remove",
		Aliases:   []string{"rm", "delete"},
		Summary:   "Remove domains from the allow or deny list ('-' reads from stdin)",
		Args:      "<domain>...",
		MinArgs:   1,
		MaxArgs:   Unlimited,
		Mutates:   true,
		Endpoints: []string{"DELETE /api/domains/{type}/{kind}/{domain}", "POST /api/domains:batchDelete"},
		Flags: func(fs *flag.FlagSet) {
			tk.register(fs, true)
			fs.BoolVar(&wildcard, "wildcard", false, "remove the wildcard (regex) entry created by add --wildcard")
		},
		Run: func(c *Ctx, args []string) error {
			t, k, items, err := domainItems(c, tk, args, wildcard)
			if err != nil {
				return err
			}
			return c.remove(items,
				func(d string) (string, url.Values) { return domainPath(t, k, d), nil },
				"/domains:batchDelete",
				func(d string) map[string]string { return map[string]string{"item": d, "type": t, "kind": k} })
		},
	}
}
