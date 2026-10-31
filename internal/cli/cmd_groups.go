package cli

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/drewstinnett/holectl/internal/pihole"
)

var (
	groupColumns  = []string{"id", "name", "enabled", "comment", "date_added", "date_modified"}
	clientColumns = []string{"id", "client", "name", "groups", "comment", "date_added"}
)

func groupsCmd() *Command {
	return &Command{
		Name:    "groups",
		Aliases: []string{"group"},
		Summary: "Manage groups (which tie clients to domains and lists)",
		Subs: []*Command{
			itemListCmd("groups", "name", "List groups, or one group by name", groupColumns),
			itemAddCmd("groups", "name", "name", "Create groups", groupColumns, false, true),
			groupsUpdateCmd(),
			groupsRenameCmd(),
			itemRemoveCmd("groups", "name", "Delete groups"),
		},
	}
}

// updateGroup PUTs a group with its current fields plus whatever flags were given.
func updateGroup(c *Ctx, name, newName string, f *commonFlags) error {
	p := "/groups/" + pihole.Seg(name)
	cur, err := c.fetchOne(p, nil, "groups", fmt.Sprintf("group %q", name))
	if err != nil {
		return err
	}
	body, err := c.mergeUpdate(cur, f, "name", "comment", "enabled")
	if err != nil {
		return err
	}
	if newName != "" {
		body["name"] = newName
	}
	return c.showProcessed(c.call(http.MethodPut, p, nil, body))
}

var groupUpdateEndpoints = []string{"GET /api/groups/{name}", "PUT /api/groups/{name}"}

func groupsUpdateCmd() *Command {
	var (
		f       commonFlags
		newName string
	)
	return &Command{
		Name:      "update",
		Summary:   "Change a group's name, comment or enabled state",
		Args:      "<name>",
		MinArgs:   1,
		MaxArgs:   1,
		Mutates:   true,
		Columns:   groupColumns,
		Endpoints: groupUpdateEndpoints,
		Flags: func(fs *flag.FlagSet) {
			f.updateFlags(fs, false, true)
			fs.StringVar(&newName, "name", "", "rename the group")
		},
		Run: func(c *Ctx, args []string) error { return updateGroup(c, args[0], newName, &f) },
	}
}

func groupsRenameCmd() *Command {
	var f commonFlags
	return &Command{
		Name:      "rename",
		Summary:   "Rename a group",
		Args:      "<old> <new>",
		MinArgs:   2,
		MaxArgs:   2,
		Mutates:   true,
		Columns:   groupColumns,
		Endpoints: groupUpdateEndpoints,
		Run:       func(c *Ctx, args []string) error { return updateGroup(c, args[0], args[1], &f) },
	}
}

func clientsCmd() *Command {
	return &Command{
		Name:    "clients",
		Aliases: []string{"client"},
		Summary: "Manage configured clients (by IP, subnet, MAC, hostname or :interface) and their groups",
		Subs: []*Command{
			itemListCmd("clients", "client", "List configured clients, or one client", clientColumns),
			itemAddCmd("clients", "client", "client", "Add clients (IP, CIDR subnet, MAC, hostname, or :interface)",
				clientColumns, true, false),
			clientsUpdateCmd(),
			itemRemoveCmd("clients", "client", "Remove configured clients"),
			clientsSuggestionsCmd(),
		},
	}
}

func clientsUpdateCmd() *Command {
	var f commonFlags
	return &Command{
		Name:      "update",
		Summary:   "Change a client's comment or groups",
		Args:      "<client>",
		MinArgs:   1,
		MaxArgs:   1,
		Mutates:   true,
		Columns:   clientColumns,
		Endpoints: []string{"GET /api/clients/{client}", "PUT /api/clients/{client}"},
		Flags:     func(fs *flag.FlagSet) { f.updateFlags(fs, true, false) },
		Run: func(c *Ctx, args []string) error {
			p := "/clients/" + pihole.Seg(args[0])
			cur, err := c.fetchOne(p, nil, "clients", fmt.Sprintf("client %q", args[0]))
			if err != nil {
				return err
			}
			body, err := c.mergeUpdate(cur, &f, "comment", "groups")
			if err != nil {
				return err
			}
			return c.showProcessed(c.call(http.MethodPut, p, nil, body))
		},
	}
}

func clientsSuggestionsCmd() *Command {
	var count int
	return &Command{
		Name:      "suggestions",
		Summary:   "Devices seen on the network that aren't configured clients yet",
		Endpoints: []string{"GET /api/clients/_suggestions"},
		Flags: func(fs *flag.FlagSet) {
			fs.IntVar(&count, "count", 0, "maximum suggestions (server default 50)")
		},
		Run: func(c *Ctx, _ []string) error {
			q := url.Values{}
			if count > 0 {
				q.Set("count", strconv.Itoa(count))
			}
			return c.show(c.get("/clients/_suggestions", q))
		},
	}
}
