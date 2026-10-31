package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/drewstinnett/holectl/internal/pihole"
)

// Local DNS (the web UI's "Local DNS" page) lives in two array settings with two different
// entry formats, which is exactly what these commands exist to hide:
//
//	dns.hosts         "192.168.1.10 nas.lan"         IP first, space separated (A/AAAA)
//	dns.cnameRecords  "media.lan,nas.lan[,300]"      alias first, comma separated, optional TTL
//
// Each add/remove is a PUT/DELETE of one whole entry on /api/config/dns/<setting>/<entry>.
const (
	hostsPath = "/config/dns/hosts"
	cnamePath = "/config/dns/cnameRecords"
)

func localDNSCmd() *Command {
	return &Command{
		Name:    "local-dns",
		Aliases: []string{"dns"},
		Summary: "Local DNS records and CNAMEs (the web UI's Local DNS page)",
		Long: `Two kinds of entry, matching the web UI's Local DNS tabs:
  records  A/AAAA: a hostname answers with an IP   holectl local-dns records add nas.lan 192.168.1.10
  cname    an alias answers with another hostname  holectl local-dns cname add media.lan nas.lan
holectl builds Pi-hole's stored entry strings for you; don't hand-write dns.hosts or dns.cnameRecords.`,
		Subs: []*Command{recordsCmd(), cnameCmd()},
	}
}

func recordsCmd() *Command {
	return &Command{
		Name:    "records",
		Aliases: []string{"record", "a", "aaaa", "hosts"},
		Summary: "A/AAAA records: hostname -> IP (stored in dns.hosts as \"IP hostname\")",
		Subs: []*Command{
			{
				Name:      "list",
				Summary:   `List local A/AAAA records (each entry is "IP hostname")`,
				Endpoints: []string{"GET /api" + hostsPath},
				Run:       func(c *Ctx, _ []string) error { return c.show(c.get(hostsPath, nil)) },
			},
			{
				Name:    "add",
				Summary: "Add an A (IPv4) or AAAA (IPv6) record, e.g. add nas.lan 192.168.1.10",
				Long: `Args may come in either order; the IP is recognized. Repeat with another IP for a second
address, or with an IPv6 address for an AAAA record. Adding an existing entry is rejected (exit 5).`,
				Args:      "<hostname> <ip>",
				MinArgs:   2,
				MaxArgs:   2,
				Mutates:   true,
				Endpoints: []string{"PUT /api" + hostsPath + "/{ip hostname}"},
				Run: func(c *Ctx, args []string) error {
					host, ip, err := hostAndIP(c, args[0], args[1])
					if err != nil {
						return err
					}
					return c.show(c.call(http.MethodPut, hostsPath+"/"+pihole.Seg(ip+" "+host), nil, nil))
				},
			},
			{
				Name:    "remove",
				Aliases: []string{"rm", "delete"},
				Summary: "Remove a hostname's records (all of them, or only the one with <ip>)",
				Args:    "<hostname> [ip]",
				MinArgs: 1,
				MaxArgs: 2,
				Mutates: true,
				Endpoints: []string{
					"GET /api" + hostsPath, "DELETE /api" + hostsPath + "/{ip hostname}",
				},
				Run: func(c *Ctx, args []string) error {
					return c.removeEntries(hostsPath, "hosts", "record", args, matchHost)
				},
			},
		},
	}
}

func cnameCmd() *Command {
	var ttl int
	return &Command{
		Name:    "cname",
		Aliases: []string{"cnames", "cname-records"},
		Summary: "CNAME records: alias -> target hostname (stored in dns.cnameRecords as \"alias,target[,ttl]\")",
		Subs: []*Command{
			{
				Name:      "list",
				Summary:   `List local CNAME records (each entry is "alias,target" or "alias,target,ttl")`,
				Endpoints: []string{"GET /api" + cnamePath},
				Run:       func(c *Ctx, _ []string) error { return c.show(c.get(cnamePath, nil)) },
			},
			{
				Name:    "add",
				Summary: "Add a CNAME so <alias> resolves to <target>, e.g. add media.lan nas.lan",
				Long: `Order matters: the alias (the new name) first, then the existing target. The target must be a
name Pi-hole can answer itself, normally a local DNS record (holectl local-dns records add);
a CNAME to an outside domain is not followed. Points at a hostname, never an IP: for an IP use
'holectl local-dns records add'.`,
				Args:      "<alias> <target>",
				MinArgs:   2,
				MaxArgs:   2,
				Mutates:   true,
				Endpoints: []string{"PUT /api" + cnamePath + "/{alias,target[,ttl]}"},
				Flags: func(fs *flag.FlagSet) {
					fs.IntVar(&ttl, "ttl", 0, "TTL in seconds for the answer (default: Pi-hole's)")
				},
				Run: func(c *Ctx, args []string) error {
					entry, err := cnameEntry(c, args[0], args[1], ttl)
					if err != nil {
						return err
					}
					return c.show(c.call(http.MethodPut, cnamePath+"/"+pihole.Seg(entry), nil, nil))
				},
			},
			{
				Name:    "remove",
				Aliases: []string{"rm", "delete"},
				Summary: "Remove an alias's CNAME (any target, or only the one pointing at [target])",
				Args:    "<alias> [target]",
				MinArgs: 1,
				MaxArgs: 2,
				Mutates: true,
				Endpoints: []string{
					"GET /api" + cnamePath, "DELETE /api" + cnamePath + "/{alias,target[,ttl]}",
				},
				Run: func(c *Ctx, args []string) error {
					return c.removeEntries(cnamePath, "cnameRecords", "CNAME", args, matchCNAME)
				},
			},
		},
	}
}

// hostAndIP accepts "<hostname> <ip>" in either order and checks that exactly one is an IP.
func hostAndIP(c *Ctx, a, b string) (string, string, error) {
	aIP, bIP := net.ParseIP(a) != nil, net.ParseIP(b) != nil
	switch {
	case aIP && bIP:
		return "", "", usagef(c.cmd, "both %q and %q are IPs; give a hostname and an IP", a, b)
	case !aIP && !bIP:
		return "", "", usagef(c.cmd, "neither %q nor %q is an IP address; for hostname -> hostname use "+
			"'holectl local-dns cname add'", a, b)
	case aIP:
		a, b = b, a
	}
	if strings.ContainsAny(a, " ,") {
		return "", "", usagef(c.cmd, "invalid hostname %q", a)
	}
	return a, b, nil
}

func cnameEntry(c *Ctx, alias, target string, ttl int) (string, error) {
	for _, s := range []string{alias, target} {
		if net.ParseIP(s) != nil {
			return "", usagef(c.cmd, "%q is an IP; a CNAME points at a hostname. For hostname -> IP use "+
				"'holectl local-dns records add'", s)
		}
		if s == "" || strings.ContainsAny(s, " ,") {
			return "", usagef(c.cmd, "invalid hostname %q", s)
		}
	}
	if strings.EqualFold(alias, target) {
		return "", usagef(c.cmd, "alias and target are both %q", alias)
	}
	if ttl < 0 {
		return "", usagef(c.cmd, "--ttl must be positive")
	}
	entry := alias + "," + target
	if ttl > 0 {
		entry += "," + strconv.Itoa(ttl)
	}
	return entry, nil
}

// matchHost reports whether a dns.hosts entry ("IP host [host...]") serves name (and ip, if given).
func matchHost(entry string, args []string) bool {
	f := strings.Fields(entry)
	if len(f) < 2 {
		return false
	}
	name, ip := args[0], ""
	if len(args) == 2 {
		h, i := args[0], args[1]
		if net.ParseIP(h) != nil {
			h, i = i, h
		}
		name, ip = h, i
	}
	if ip != "" && !net.ParseIP(ip).Equal(net.ParseIP(f[0])) {
		return false
	}
	for _, h := range f[1:] {
		if strings.EqualFold(h, name) {
			return true
		}
	}
	return false
}

// matchCNAME reports whether a dns.cnameRecords entry ("alias[,alias...],target[,ttl]") has the
// alias (and target, if given).
func matchCNAME(entry string, args []string) bool {
	f := strings.Split(entry, ",")
	if len(f) > 2 {
		if _, err := strconv.Atoi(f[len(f)-1]); err == nil {
			f = f[:len(f)-1]
		}
	}
	if len(f) < 2 {
		return false
	}
	target := f[len(f)-1]
	if len(args) == 2 && !strings.EqualFold(target, args[1]) {
		return false
	}
	for _, a := range f[:len(f)-1] {
		if strings.EqualFold(a, args[0]) {
			return true
		}
	}
	return false
}

// removeEntries deletes every stored entry of the setting at path that match accepts, so a removal
// never depends on reproducing the stored string (TTL, IP, spacing) by hand.
func (c *Ctx) removeEntries(path, key, what string, args []string, match func(string, []string) bool) error {
	raw, err := c.get(path, nil)
	if err != nil {
		return err
	}
	var resp struct {
		Config struct {
			DNS map[string][]string `json:"dns"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("decoding %s: %w", path, err)
	}
	var hits []string
	for _, e := range resp.Config.DNS[key] {
		if match(e, args) {
			hits = append(hits, e)
		}
	}
	if len(hits) == 0 {
		return &notFoundError{fmt.Sprintf("no local %s matches %q", what, strings.Join(args, " "))}
	}
	return c.deleteEach(hits, func(e string) string { return path + "/" + pihole.Seg(e) })
}
