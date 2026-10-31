package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"regexp"

	"github.com/drewstinnett/holectl/internal/pihole"
)

// commonFlags are the comment/group/enabled flags shared by every managed resource.
type commonFlags struct {
	comment  string
	groups   listFlag
	disabled bool    // add
	enabled  optBool // update
}

func (f *commonFlags) addFlags(fs *flag.FlagSet, withGroups, withEnabled bool) {
	fs.StringVar(&f.comment, "comment", "", "comment")
	if withGroups {
		fs.Var(&f.groups, "group", "group name or ID; repeatable or comma-separated (default: Default group)")
	}
	if withEnabled {
		fs.BoolVar(&f.disabled, "disabled", false, "add in a disabled state")
	}
}

func (f *commonFlags) updateFlags(fs *flag.FlagSet, withGroups, withEnabled bool) {
	fs.StringVar(&f.comment, "comment", "", "new comment (empty string clears it)")
	if withGroups {
		fs.Var(&f.groups, "group", "replace group membership: names or IDs, repeatable or comma-separated")
	}
	if withEnabled {
		fs.Var(&f.enabled, "enabled", "enable (--enabled) or disable (--enabled=false)")
	}
}

// addBody builds the POST body; items go under itemField as an array (the API accepts batches).
func (c *Ctx) addBody(f *commonFlags, itemField string, items []string, withEnabled bool) (map[string]any, error) {
	body := map[string]any{itemField: items}
	if f.comment != "" {
		body["comment"] = f.comment
	}
	if len(f.groups) > 0 {
		ids, err := c.groupIDs(f.groups)
		if err != nil {
			return nil, err
		}
		body["groups"] = ids
	}
	if withEnabled {
		body["enabled"] = !f.disabled
	}
	return body, nil
}

// mergeUpdate starts from the current object's fields so a PUT never resets what wasn't asked about.
func (c *Ctx) mergeUpdate(cur map[string]any, f *commonFlags, fields ...string) (map[string]any, error) {
	body := map[string]any{}
	for _, k := range fields {
		if v, ok := cur[k]; ok {
			body[k] = v
		}
	}
	if c.Set("comment") {
		body["comment"] = f.comment
	}
	if c.Set("group") {
		ids, err := c.groupIDs(f.groups)
		if err != nil {
			return nil, err
		}
		body["groups"] = ids
	}
	if f.enabled.set {
		body["enabled"] = f.enabled.val
	}
	return body, nil
}

// fetchOne GETs a single-item path and returns the first element of the response's key array.
func (c *Ctx) fetchOne(path string, q url.Values, key, what string) (map[string]any, error) {
	raw, err := c.get(path, q)
	if err != nil {
		return nil, err
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(resp[key], &items); err != nil || len(items) == 0 {
		return nil, &notFoundError{what + " not found"}
	}
	return items[0], nil
}

// showProcessed prints an add/update response and fails if the server reported per-item errors.
func (c *Ctx) showProcessed(raw json.RawMessage, err error) error {
	if err := c.show(raw, err); err != nil {
		return err
	}
	var resp struct {
		Processed struct {
			Errors []struct {
				Item  string `json:"item"`
				Error string `json:"error"`
			} `json:"errors"`
		} `json:"processed"`
	}
	if json.Unmarshal(raw, &resp) == nil && len(resp.Processed.Errors) > 0 {
		e := resp.Processed.Errors[0]
		return &partialError{&pihole.APIError{Status: http.StatusBadRequest, Key: "processing_error",
			Message: fmt.Sprintf("%d item(s) failed, first %q: %s", len(resp.Processed.Errors), e.Item, e.Error)}}
	}
	return nil
}

// remove deletes one item directly (so a missing item is a 404) or many in one batch call.
func (c *Ctx) remove(items []string, single func(string) (string, url.Values), batchPath string,
	entry func(string) map[string]string,
) error {
	if len(items) == 1 {
		p, q := single(items[0])
		return c.show(c.call(http.MethodDelete, p, q, nil))
	}
	batch := make([]map[string]string, 0, len(items))
	for _, it := range items {
		batch = append(batch, entry(it))
	}
	return c.show(c.call(http.MethodPost, batchPath, nil, batch))
}

// filterList narrows a {"<key>": [...]} response to entries whose field equals value.
func filterList(raw json.RawMessage, key, field, value string) (json.RawMessage, error) {
	n, err := parseNode(raw)
	if err != nil || n.kind != 'o' || n.fields[key] == nil {
		return raw, err
	}
	arr := n.fields[key]
	kept := arr.items[:0]
	for _, it := range arr.items {
		if v, err := it.lookup(field); err == nil && v.kind == 's' && v.scalar == value {
			kept = append(kept, it)
		}
	}
	arr.items = kept
	return n.marshal(), nil
}

var domainColumns = []string{"id", "domain", "type", "kind", "enabled", "groups", "comment", "date_added"}

// wildcardRegex is what the Pi-hole web UI stores for "add as wildcard".
func wildcardRegex(domain string) string {
	return `(\.|^)` + regexp.QuoteMeta(domain) + `$`
}

// itemListCmd lists a resource, or GETs one item of it: groups, clients.
func itemListCmd(resource, arg, summary string, cols []string) *Command {
	return &Command{
		Name:      "list",
		Summary:   summary,
		Args:      "[" + arg + "]",
		MaxArgs:   1,
		Columns:   cols,
		Endpoints: []string{"GET /api/" + resource, "GET /api/" + resource + "/{" + arg + "}"},
		Run: func(c *Ctx, args []string) error {
			if len(args) == 1 {
				return c.show(c.get("/"+resource+"/"+pihole.Seg(args[0]), nil))
			}
			return c.show(c.get("/"+resource, nil))
		},
	}
}

// itemRemoveCmd deletes items of a resource whose batch entries are just {"item": ...}.
func itemRemoveCmd(resource, arg, summary string) *Command {
	return &Command{
		Name:      "remove",
		Aliases:   []string{"rm", "delete"},
		Summary:   summary,
		Args:      "<" + arg + ">...",
		MinArgs:   1,
		MaxArgs:   Unlimited,
		Mutates:   true,
		Endpoints: []string{"DELETE /api/" + resource + "/{" + arg + "}", "POST /api/" + resource + ":batchDelete"},
		Run: func(c *Ctx, args []string) error {
			items, err := c.items(args)
			if err != nil {
				return err
			}
			return c.remove(items,
				func(n string) (string, url.Values) { return "/" + resource + "/" + pihole.Seg(n), nil },
				"/"+resource+":batchDelete",
				func(n string) map[string]string { return map[string]string{"item": n} })
		},
	}
}

// itemAddCmd POSTs a batch of new items under itemField.
func itemAddCmd(resource, itemField, arg, summary string, cols []string, withGroups, withEnabled bool) *Command {
	var f commonFlags
	return &Command{
		Name:      "add",
		Summary:   summary,
		Args:      "<" + arg + ">...",
		MinArgs:   1,
		MaxArgs:   Unlimited,
		Mutates:   true,
		Columns:   cols,
		Endpoints: []string{"POST /api/" + resource},
		Flags:     func(fs *flag.FlagSet) { f.addFlags(fs, withGroups, withEnabled) },
		Run: func(c *Ctx, args []string) error {
			items, err := c.items(args)
			if err != nil {
				return err
			}
			body, err := c.addBody(&f, itemField, items, withEnabled)
			if err != nil {
				return err
			}
			return c.showProcessed(c.call(http.MethodPost, "/"+resource, nil, body))
		},
	}
}
