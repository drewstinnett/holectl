package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/drewstinnett/holectl/internal/pihole"
)

// getLeaf is a command that GETs one path and prints the response.
func getLeaf(name, summary, path string, cols ...string) *Command {
	return &Command{
		Name:      name,
		Summary:   summary,
		Endpoints: []string{"GET /api" + path},
		Columns:   cols,
		Run:       func(c *Ctx, _ []string) error { return c.show(c.get(path, nil)) },
	}
}

func infoCmd() *Command {
	return &Command{
		Name:    "info",
		Summary: "Server information: version, system, host, FTL, database, sensors, messages",
		Subs: []*Command{
			getLeaf("version", "Versions of Pi-hole core, web, FTL and docker image, with available updates", "/info/version"),
			getLeaf("system", "Uptime, memory, CPU and load", "/info/system"),
			getLeaf("host", "Host OS, kernel and model", "/info/host"),
			getLeaf("ftl", "FTL process details: privacy level, clients, query counts, PID", "/info/ftl"),
			getLeaf("database", "Long-term database size, query count, SQLite version", "/info/database"),
			getLeaf("sensors", "Temperature sensors and thresholds", "/info/sensors"),
			getLeaf("metrics", "DNS and DHCP internal metrics (cache, replies)", "/info/metrics"),
			getLeaf("client", "What the server sees of this client (address, user agent)", "/info/client"),
			getLeaf("login", "Login page info: whether DNS is running and HTTPS port (no auth needed)", "/info/login"),
			{
				Name:    "messages",
				Summary: "Pi-hole diagnosis messages (warnings shown in the web UI)",
				Subs: []*Command{
					getLeaf("list", "List diagnosis messages", "/info/messages",
						"id", "timestamp", "type", "plain"),
					getLeaf("count", "Count diagnosis messages", "/info/messages/count"),
					{
						Name:      "remove",
						Aliases:   []string{"rm", "delete", "dismiss"},
						Summary:   "Dismiss diagnosis messages by ID",
						Args:      "<id>...",
						MinArgs:   1,
						MaxArgs:   Unlimited,
						Mutates:   true,
						Endpoints: []string{"DELETE /api/info/messages/{id}"},
						Run: func(c *Ctx, args []string) error {
							return c.deleteEach(args, func(id string) string { return "/info/messages/" + pihole.Seg(id) })
						},
					},
				},
			},
		},
	}
}

func detailedLeaf(name, summary, path string) *Command {
	var detailed bool
	return &Command{
		Name:      name,
		Summary:   summary,
		Endpoints: []string{"GET /api" + path},
		Flags:     func(fs *flag.FlagSet) { fs.BoolVar(&detailed, "detailed", false, "include extra detail") },
		Run: func(c *Ctx, _ []string) error {
			q := url.Values{}
			if detailed {
				q.Set("detailed", "true")
			}
			return c.show(c.get(path, q))
		},
	}
}

func networkCmd() *Command {
	var maxDevices, maxAddresses int
	return &Command{
		Name:    "network",
		Summary: "Network table (devices seen), gateway, routes and interfaces",
		Subs: []*Command{
			{
				Name:    "devices",
				Summary: "Devices seen on the network",
				Subs: []*Command{
					{
						Name:      "list",
						Summary:   "List devices with their addresses and query counts",
						Endpoints: []string{"GET /api/network/devices"},
						Columns:   []string{"id", "hwaddr", "interface", "macVendor", "numQueries", "lastQuery", "ips"},
						Flags: func(fs *flag.FlagSet) {
							fs.IntVar(&maxDevices, "max-devices", 0, "maximum devices (server default 10)")
							fs.IntVar(&maxAddresses, "max-addresses", 0, "maximum addresses per device (server default 3)")
						},
						Run: func(c *Ctx, _ []string) error {
							q := url.Values{}
							if maxDevices > 0 {
								q.Set("max_devices", strconv.Itoa(maxDevices))
							}
							if maxAddresses > 0 {
								q.Set("max_addresses", strconv.Itoa(maxAddresses))
							}
							return c.show(c.get("/network/devices", q))
						},
					},
					{
						Name:      "remove",
						Aliases:   []string{"rm", "delete"},
						Summary:   "Delete devices from the network table by ID",
						Args:      "<id>...",
						MinArgs:   1,
						MaxArgs:   Unlimited,
						Mutates:   true,
						Endpoints: []string{"DELETE /api/network/devices/{id}"},
						Run: func(c *Ctx, args []string) error {
							return c.deleteEach(args, func(id string) string { return "/network/devices/" + pihole.Seg(id) })
						},
					},
				},
			},
			detailedLeaf("gateway", "Default gateway(s)", "/network/gateway"),
			detailedLeaf("routes", "Routing table", "/network/routes"),
			detailedLeaf("interfaces", "Network interfaces and addresses", "/network/interfaces"),
		},
	}
}

func dhcpCmd() *Command {
	return &Command{
		Name:    "dhcp",
		Summary: "DHCP leases handed out by Pi-hole's DHCP server",
		Subs: []*Command{
			{
				Name:    "leases",
				Summary: "List or remove DHCP leases",
				Subs: []*Command{
					getLeaf("list", "List active DHCP leases", "/dhcp/leases", "ip", "name", "hwaddr", "expires", "clientid"),
					{
						Name:      "remove",
						Aliases:   []string{"rm", "delete"},
						Summary:   "Remove leases by IP address",
						Args:      "<ip>...",
						MinArgs:   1,
						MaxArgs:   Unlimited,
						Mutates:   true,
						Endpoints: []string{"DELETE /api/dhcp/leases/{ip}"},
						Run: func(c *Ctx, args []string) error {
							return c.deleteEach(args, func(ip string) string { return "/dhcp/leases/" + pihole.Seg(ip) })
						},
					},
				},
			},
		},
	}
}

func logLeaf(name, summary string) *Command {
	var (
		nextID   int
		follow   bool
		interval time.Duration
	)
	path := "/logs/" + name
	return &Command{
		Name:      name,
		Summary:   summary,
		Endpoints: []string{"GET /api" + path},
		Columns:   []string{"timestamp", "prio", "message"},
		Flags: func(fs *flag.FlagSet) {
			fs.IntVar(&nextID, "next-id", 0, "only lines from this ID on (from a previous response's nextID)")
			fs.BoolVar(&follow, "follow", false, "keep polling for new lines (one JSON object per line in json mode)")
			fs.DurationVar(&interval, "interval", 2*time.Second, "poll interval with --follow")
		},
		Run: func(c *Ctx, _ []string) error {
			q := url.Values{}
			if c.Set("next-id") {
				q.Set("nextID", strconv.Itoa(nextID))
			}
			if !follow {
				return c.show(c.get(path, q))
			}
			return c.followLog(path, q, interval)
		},
	}
}

func (c *Ctx) followLog(path string, q url.Values, interval time.Duration) error {
	for {
		raw, err := c.get(path, q)
		if err != nil {
			return err
		}
		var resp struct {
			Log    []json.RawMessage `json:"log"`
			NextID int               `json:"nextID"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return fmt.Errorf("parsing log response: %w", err)
		}
		for _, line := range resp.Log {
			if err := c.printLogLine(line); err != nil {
				return err
			}
		}
		q.Set("nextID", strconv.Itoa(resp.NextID))
		select {
		case <-c.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func (c *Ctx) printLogLine(line json.RawMessage) error {
	if c.G.Quiet {
		return nil
	}
	if c.format == "json" {
		_, err := fmt.Fprintln(c.Stdout, string(line))
		return err
	}
	var l struct {
		Timestamp float64 `json:"timestamp"`
		Prio      *string `json:"prio"`
		Message   string  `json:"message"`
	}
	if err := json.Unmarshal(line, &l); err != nil {
		return err
	}
	ts := time.Unix(int64(l.Timestamp), 0).Local().Format("2006-01-02 15:04:05")
	prio := ""
	if l.Prio != nil {
		prio = *l.Prio + " "
	}
	_, err := fmt.Fprintf(c.Stdout, "%s %s%s\n", ts, prio, strings.TrimRight(l.Message, "\n"))
	return err
}

func logsCmd() *Command {
	return &Command{
		Name:    "logs",
		Summary: "Read (or follow) the DNS, FTL and webserver logs",
		Subs: []*Command{
			logLeaf("dnsmasq", "DNS resolver log (pihole.log)"),
			logLeaf("ftl", "FTL engine log (FTL.log)"),
			logLeaf("webserver", "Embedded web server log (webserver.log)"),
		},
	}
}

func paddCmd() *Command {
	var full bool
	return &Command{
		Name:      "padd",
		Summary:   "Everything PADD (the status dashboard) shows, in one call",
		Endpoints: []string{"GET /api/padd"},
		Flags:     func(fs *flag.FlagSet) { fs.BoolVar(&full, "full", false, "include the full data set") },
		Run: func(c *Ctx, _ []string) error {
			q := url.Values{}
			if full {
				q.Set("full", "true")
			}
			return c.show(c.get("/padd", q))
		},
	}
}

func endpointsCmd() *Command {
	return &Command{
		Name:      "endpoints",
		Summary:   "List every endpoint the connected server's API exposes (use with 'holectl api')",
		Endpoints: []string{"GET /api/endpoints"},
		Run:       func(c *Ctx, _ []string) error { return c.show(c.get("/endpoints", nil)) },
	}
}
