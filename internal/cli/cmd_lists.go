package cli

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"

	"github.com/drewstinnett/holectl/internal/pihole"
)

var listColumns = []string{"id", "address", "type", "enabled", "groups", "number", "status", "date_updated", "comment"}

func listTypeQ(t string) url.Values {
	q := url.Values{}
	if t != "" {
		q.Set("type", t)
	}
	return q
}

func newListType() *choice { return newChoice("block", "allow") }

func listsCmd() *Command {
	return &Command{
		Name:    "lists",
		Aliases: []string{"adlists"},
		Summary: "Manage subscribed blocklists and allowlists (run 'action gravity' after changes)",
		Subs:    []*Command{listsListCmd(), listsAddCmd(), listsUpdateCmd(), listsRemoveCmd()},
	}
}

func listsListCmd() *Command {
	typ := newListType()
	return &Command{
		Name:      "list",
		Summary:   "List subscribed lists, optionally one address or one --type",
		Args:      "[address]",
		MaxArgs:   1,
		Columns:   listColumns,
		Endpoints: []string{"GET /api/lists", "GET /api/lists/{address}"},
		Flags:     func(fs *flag.FlagSet) { fs.Var(typ, "type", "block or allow (optional filter)") },
		Run: func(c *Ctx, args []string) error {
			if len(args) == 1 {
				return c.show(c.get("/lists/"+pihole.Seg(args[0]), listTypeQ(typ.val)))
			}
			return c.show(c.get("/lists", listTypeQ(typ.val)))
		},
	}
}

func listsAddCmd() *Command {
	var (
		typ = newListType()
		f   commonFlags
	)
	return &Command{
		Name:      "add",
		Summary:   "Subscribe to list URLs ('-' reads from stdin); then run 'holectl action gravity'",
		Args:      "<address>...",
		MinArgs:   1,
		MaxArgs:   Unlimited,
		Mutates:   true,
		Columns:   listColumns,
		Endpoints: []string{"POST /api/lists?type={type}"},
		Flags: func(fs *flag.FlagSet) {
			fs.Var(typ, "type", "block or allow (default block)")
			f.addFlags(fs, true, true)
		},
		Run: func(c *Ctx, args []string) error {
			items, err := c.items(args)
			if err != nil {
				return err
			}
			t := typ.val
			if t == "" {
				t = "block"
			}
			body, err := c.addBody(&f, "address", items, true)
			if err != nil {
				return err
			}
			return c.showProcessed(c.call(http.MethodPost, "/lists", listTypeQ(t), body))
		},
	}
}

func listsUpdateCmd() *Command {
	var (
		typ     = newListType()
		newType = newListType()
		f       commonFlags
	)
	return &Command{
		Name:      "update",
		Summary:   "Change a list's comment, groups, enabled state, or type",
		Args:      "<address>",
		MinArgs:   1,
		MaxArgs:   1,
		Mutates:   true,
		Columns:   listColumns,
		Endpoints: []string{"GET /api/lists/{address}", "PUT /api/lists/{address}?type={type}"},
		Flags: func(fs *flag.FlagSet) {
			fs.Var(typ, "type", "block or allow (required)")
			f.updateFlags(fs, true, true)
			fs.Var(newType, "new-type", "change the list type (block or allow)")
		},
		Run: func(c *Ctx, args []string) error {
			if typ.val == "" {
				return usagef(c.cmd, "--type block|allow is required")
			}
			p := "/lists/" + pihole.Seg(args[0])
			cur, err := c.fetchOne(p, listTypeQ(typ.val), "lists", fmt.Sprintf("%s list %q", typ.val, args[0]))
			if err != nil {
				return err
			}
			body, err := c.mergeUpdate(cur, &f, "comment", "groups", "enabled")
			if err != nil {
				return err
			}
			body["type"] = typ.val
			if newType.val != "" {
				body["type"] = newType.val
			}
			return c.showProcessed(c.call(http.MethodPut, p, listTypeQ(typ.val), body))
		},
	}
}

func listsRemoveCmd() *Command {
	typ := newListType()
	return &Command{
		Name:      "remove",
		Aliases:   []string{"rm", "delete"},
		Summary:   "Unsubscribe from lists ('-' reads from stdin)",
		Args:      "<address>...",
		MinArgs:   1,
		MaxArgs:   Unlimited,
		Mutates:   true,
		Endpoints: []string{"DELETE /api/lists/{address}?type={type}", "POST /api/lists:batchDelete"},
		Flags:     func(fs *flag.FlagSet) { fs.Var(typ, "type", "block or allow (required)") },
		Run: func(c *Ctx, args []string) error {
			if typ.val == "" {
				return usagef(c.cmd, "--type block|allow is required")
			}
			items, err := c.items(args)
			if err != nil {
				return err
			}
			return c.remove(items,
				func(a string) (string, url.Values) { return "/lists/" + pihole.Seg(a), listTypeQ(typ.val) },
				"/lists:batchDelete",
				func(a string) map[string]string { return map[string]string{"item": a, "type": typ.val} })
		},
	}
}
