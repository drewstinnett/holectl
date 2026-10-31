package cli

import (
	"flag"
	"net/url"
	"strconv"
)

// statsLeaf builds a stats command whose --from/--until switch it to the long-term database variant.
func statsLeaf(name, summary, path string, hasDB, hasBlocked bool, countName string) *Command {
	var (
		from, until string
		blocked     bool
		count       int
	)
	endpoints := []string{"GET /api/stats/" + path}
	if hasDB {
		endpoints = append(endpoints, "GET /api/stats/database/"+path)
	}
	return &Command{
		Name:      name,
		Summary:   summary,
		Endpoints: endpoints,
		Flags: func(fs *flag.FlagSet) {
			if hasDB {
				fs.StringVar(&from, "from", "", "start of range (unix, RFC 3339, YYYY-MM-DD, or ago like 24h/7d); "+
					"switches to the long-term database")
				fs.StringVar(&until, "until", "", "end of range (default now)")
			}
			if hasBlocked {
				fs.BoolVar(&blocked, "blocked", false, "rank blocked queries instead of permitted ones")
			}
			if countName != "" {
				fs.IntVar(&count, "count", 0, "number of entries (server default 10)")
			}
		},
		Run: func(c *Ctx, _ []string) error {
			q := url.Values{}
			if err := timeRange(q, from, until); err != nil {
				return usagef(c.cmd, "%v", err)
			}
			p := "/stats/" + path
			if q.Has("from") {
				p = "/stats/database/" + path
			}
			if blocked {
				q.Set("blocked", "true")
			}
			if count > 0 {
				q.Set(countName, strconv.Itoa(count))
			}
			return c.show(c.get(p, q))
		},
	}
}

func statsCmd() *Command {
	return &Command{
		Name:    "stats",
		Summary: "Query statistics: totals, top domains/clients, upstreams, query types",
		Long:    "Without --from these read FTL's in-memory data (last 24h); with --from they read the long-term database.",
		Subs: []*Command{
			statsLeaf("summary", "Overview: queries, blocked, percentage, clients, gravity size", "summary", true, false, ""),
			statsLeaf("upstreams", "Upstream DNS servers and how many queries each handled", "upstreams", true, false, ""),
			statsLeaf("top-domains", "Most queried (or with --blocked, most blocked) domains", "top_domains", true, true, "count"),
			statsLeaf("top-clients", "Most active (or with --blocked, most blocked) clients", "top_clients", true, true, "count"),
			statsLeaf("query-types", "Queries by type (A, AAAA, HTTPS, ...)", "query_types", true, false, ""),
			statsLeaf("recent-blocked", "Most recently blocked domains", "recent_blocked", false, false, "count"),
		},
	}
}

func historyCmd() *Command {
	var (
		from, until string
		clients     bool
		n           int
	)
	return &Command{
		Name:    "history",
		Summary: "Query activity over time (10-minute buckets), overall or per client",
		Endpoints: []string{"GET /api/history", "GET /api/history/clients",
			"GET /api/history/database", "GET /api/history/database/clients"},
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&clients, "clients", false, "per-client breakdown")
			fs.IntVar(&n, "limit", 0, "with --clients: number of clients (server default 20, 0 = all)")
			fs.StringVar(&from, "from", "", "start of range (switches to the long-term database)")
			fs.StringVar(&until, "until", "", "end of range (default now)")
		},
		Run: func(c *Ctx, _ []string) error {
			q := url.Values{}
			if err := timeRange(q, from, until); err != nil {
				return usagef(c.cmd, "%v", err)
			}
			p := "/history"
			if q.Has("from") {
				p += "/database"
			}
			if clients {
				p += "/clients"
				if c.Set("limit") && !q.Has("from") {
					q.Set("N", strconv.Itoa(n))
				}
			}
			return c.show(c.get(p, q))
		},
	}
}

// queryFilters are the /api/queries filters, keyed by API parameter name.
type queryFilters struct {
	from, until           string
	text                  map[string]*string
	length, start, cursor int
	disk                  bool
}

var queryTextFilters = []struct{ param, flag, usage string }{
	{"domain", "domain", "domain filter"},
	{"client_ip", "client-ip", "client IP filter"},
	{"client_name", "client-name", "client hostname filter"},
	{"upstream", "upstream", `upstream filter ("blocklist", "cache", or a server)`},
	{"type", "type", "query type filter, e.g. A, AAAA"},
	{"status", "status", "status filter, e.g. GRAVITY, FORWARDED, CACHE"},
	{"reply", "reply", "reply type filter, e.g. NXDOMAIN, IP"},
	{"dnssec", "dnssec", "DNSSEC status filter"},
}

func (qf *queryFilters) register(fs *flag.FlagSet) {
	fs.StringVar(&qf.from, "from", "", "only queries after this time")
	fs.StringVar(&qf.until, "until", "", "only queries before this time")
	qf.text = map[string]*string{}
	for _, t := range queryTextFilters {
		qf.text[t.param] = fs.String(t.flag, "", t.usage)
	}
	fs.IntVar(&qf.length, "length", 0, "number of queries to return (server default 100)")
	fs.IntVar(&qf.start, "start", 0, "offset for paging")
	fs.IntVar(&qf.cursor, "cursor", 0, "cursor from a previous response")
	fs.BoolVar(&qf.disk, "disk", false, "search the on-disk database")
}

func (qf *queryFilters) values(c *Ctx) (url.Values, error) {
	q := url.Values{}
	now := timeNow()
	for name, v := range map[string]string{"from": qf.from, "until": qf.until} {
		if v == "" {
			continue
		}
		t, err := parseWhen(v, now)
		if err != nil {
			return nil, usagef(c.cmd, "--%s: %v", name, err)
		}
		q.Set(name, strconv.FormatInt(t, 10))
	}
	for param, v := range qf.text {
		if *v != "" {
			q.Set(param, *v)
		}
	}
	for k, v := range map[string]int{"length": qf.length, "start": qf.start, "cursor": qf.cursor} {
		if c.Set(k) {
			q.Set(k, strconv.Itoa(v))
		}
	}
	if qf.disk {
		q.Set("disk", "true")
	}
	return q, nil
}

func queriesCmd() *Command {
	var qf queryFilters
	return &Command{
		Name:    "queries",
		Summary: "Search the query log",
		Subs: []*Command{
			{
				Name:    "list",
				Summary: "List queries, newest first, with optional filters (wildcard * allowed in filters)",
				Long: `Pages with --length/--start, or pass the returned cursor back with --cursor for stable paging.
Use --disk to search the long-term database instead of memory.`,
				Endpoints: []string{"GET /api/queries"},
				Columns:   []string{"time", "type", "domain", "client.ip", "client.name", "status", "reply.type", "upstream"},
				Flags:     qf.register,
				Run: func(c *Ctx, _ []string) error {
					q, err := qf.values(c)
					if err != nil {
						return err
					}
					return c.show(c.get("/queries", q))
				},
			},
			getLeaf("suggestions", "Values seen in the log, usable as query filters", "/queries/suggestions"),
		},
	}
}
