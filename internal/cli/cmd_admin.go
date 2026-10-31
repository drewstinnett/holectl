package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drewstinnett/holectl/internal/pihole"
)

// configPath turns "dns.upstreams" or "dns/upstreams" into escaped segments and the key list.
func configPath(p string) (string, []string) {
	p = strings.Trim(strings.ReplaceAll(p, ".", "/"), "/")
	keys := strings.Split(p, "/")
	segs := make([]string, len(keys))
	for i, k := range keys {
		segs[i] = pihole.Seg(k)
	}
	return strings.Join(segs, "/"), keys
}

// parseValue reads a config value as JSON when it is valid JSON, otherwise as a plain string.
func parseValue(s string, forceString bool) any {
	if forceString {
		return s
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	if err := dec.Decode(&v); err == nil && !dec.More() {
		return v
	}
	return s
}

func restartQ(noRestart bool) url.Values {
	if !noRestart {
		return nil
	}
	return url.Values{"restart": {"false"}}
}

// readData reads a body argument: inline text, @file, or @- for stdin.
func (c *Ctx) readData(arg string) ([]byte, error) {
	switch {
	case arg == "@-" || arg == "-":
		return io.ReadAll(c.Stdin)
	case strings.HasPrefix(arg, "@"):
		return os.ReadFile(arg[1:])
	}
	return []byte(arg), nil
}

func serverConfigCmd() *Command {
	return &Command{
		Name:    "server-config",
		Aliases: []string{"sc", "settings"},
		Summary: "Read and change the Pi-hole's own configuration (pihole.toml)",
		Long: `Paths use dots or slashes: dns.upstreams == dns/upstreams. Values are parsed as JSON when
valid (true, 42, ["1.1.1.1"], "text") and taken as plain strings otherwise.
Some changes restart FTL; --no-restart defers that.`,
		Subs: []*Command{
			scGetCmd(),
			scSetCmd(),
			scPatchCmd(),
			scArrayCmd(http.MethodPut, "add", "Append a value to an array setting, e.g. add dns.upstreams 9.9.9.9"),
			scArrayCmd(http.MethodDelete, "remove", "Remove a value from an array setting"),
			getLeaf("properties", "Config properties metadata (which settings exist and their flags)", "/config/_properties"),
		},
	}
}

func noRestartFlag(fs *flag.FlagSet, p *bool) {
	fs.BoolVar(p, "no-restart", false, "don't restart FTL even if the change requires it")
}

func scGetCmd() *Command {
	var detailed bool
	return &Command{
		Name:      "get",
		Summary:   "Show the whole config, or one section or value",
		Args:      "[path]",
		MaxArgs:   1,
		Endpoints: []string{"GET /api/config", "GET /api/config/{path}"},
		Flags: func(fs *flag.FlagSet) {
			fs.BoolVar(&detailed, "detailed", false, "include descriptions, types, defaults and allowed values")
		},
		Run: func(c *Ctx, args []string) error {
			q := url.Values{}
			if detailed {
				q.Set("detailed", "true")
			}
			p := "/config"
			if len(args) == 1 {
				seg, _ := configPath(args[0])
				p += "/" + seg
			}
			return c.show(c.get(p, q))
		},
	}
}

func scSetCmd() *Command {
	var noRestart, asString bool
	return &Command{
		Name:      "set",
		Summary:   "Set one value (PATCH), e.g. set dns.upstreams '[\"1.1.1.1\",\"9.9.9.9\"]'",
		Args:      "<path> <value>",
		MinArgs:   2,
		MaxArgs:   2,
		Mutates:   true,
		Endpoints: []string{"PATCH /api/config"},
		Flags: func(fs *flag.FlagSet) {
			noRestartFlag(fs, &noRestart)
			fs.BoolVar(&asString, "string", false, "treat the value as a string even if it parses as JSON")
		},
		Run: func(c *Ctx, args []string) error {
			_, keys := configPath(args[0])
			v := parseValue(args[1], asString)
			for i := len(keys) - 1; i >= 0; i-- {
				v = map[string]any{keys[i]: v}
			}
			return c.show(c.call(http.MethodPatch, "/config", restartQ(noRestart), map[string]any{"config": v}))
		},
	}
}

func scPatchCmd() *Command {
	var (
		noRestart bool
		file      string
	)
	return &Command{
		Name:    "patch",
		Summary: "PATCH a JSON document of changes (from --file or stdin)",
		Long: `The document may be {"config": {...}} or just the {...} inside it, e.g.
  echo '{"dns":{"queryLogging":false}}' | holectl server-config patch`,
		Mutates:   true,
		Endpoints: []string{"PATCH /api/config"},
		Flags: func(fs *flag.FlagSet) {
			noRestartFlag(fs, &noRestart)
			fs.StringVar(&file, "file", "", "JSON file (default stdin)")
		},
		Run: func(c *Ctx, _ []string) error {
			src := "@-"
			if file != "" {
				src = "@" + file
			}
			data, err := c.readData(src)
			if err != nil {
				return err
			}
			var doc map[string]any
			if err := json.Unmarshal(data, &doc); err != nil {
				return usagef(c.cmd, "invalid JSON: %v", err)
			}
			if _, ok := doc["config"]; !ok || len(doc) != 1 {
				doc = map[string]any{"config": doc}
			}
			return c.show(c.call(http.MethodPatch, "/config", restartQ(noRestart), doc))
		},
	}
}

// scArrayCmd adds to (PUT) or removes from (DELETE) an array-valued setting.
func scArrayCmd(method, name, summary string) *Command {
	var noRestart bool
	cmd := &Command{
		Name:      name,
		Summary:   summary,
		Args:      "<path> <value>",
		MinArgs:   2,
		MaxArgs:   2,
		Mutates:   true,
		Endpoints: []string{method + " /api/config/{path}/{value}"},
		Flags:     func(fs *flag.FlagSet) { noRestartFlag(fs, &noRestart) },
		Run: func(c *Ctx, args []string) error {
			seg, _ := configPath(args[0])
			return c.show(c.call(method, "/config/"+seg+"/"+pihole.Seg(args[1]), restartQ(noRestart), nil))
		},
	}
	if method == http.MethodDelete {
		cmd.Aliases = []string{"rm", "delete"}
	}
	return cmd
}

func actionCmd() *Command {
	post := func(name, summary, path string) *Command {
		return &Command{
			Name:      name,
			Summary:   summary,
			Mutates:   true,
			Endpoints: []string{"POST /api" + path},
			Run:       func(c *Ctx, _ []string) error { return c.show(c.call(http.MethodPost, path, nil, nil)) },
		}
	}
	return &Command{
		Name:    "action",
		Summary: "Run server actions: update gravity, restart DNS, flush logs and tables",
		Subs: []*Command{
			{
				Name:    "gravity",
				Aliases: []string{"update-gravity"},
				Summary: "Re-download all lists and rebuild the gravity database (can take minutes)",
				Long: `Output streams line by line on a terminal. In json mode the lines are collected and printed as
{"success": true, "output": [...]} when gravity finishes. Use a generous --timeout (e.g. 10m).`,
				Mutates:   true,
				Endpoints: []string{"POST /api/action/gravity"},
				Run:       runGravity,
			},
			post("restart-dns", "Restart the DNS resolver (pihole restartdns)", "/action/restartdns"),
			post("flush-logs", "Flush the DNS logs and the in-memory query log", "/action/flush/logs"),
			post("flush-arp", "Flush the network table (ARP cache)", "/action/flush/arp"),
			post("flush-network", "Flush the network table and all its device data", "/action/flush/network"),
		},
	}
}

func runGravity(c *Ctx, _ []string) error {
	cl, err := c.Client()
	if err != nil {
		return err
	}
	resp, err := cl.Do(c, pihole.Request{Method: http.MethodPost, Path: "/action/gravity",
		Query: url.Values{"color": {"false"}}})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.LastIndex(line, "\r"); i >= 0 {
			line = line[i+1:] // keep the final state of progress-bar lines
		}
		line = strings.TrimRight(line, " ")
		if c.format == "json" {
			lines = append(lines, line)
		} else if !c.G.Quiet {
			_, _ = fmt.Fprintln(c.Stdout, line)
		}
	}
	if err := sc.Err(); err != nil {
		return &pihole.NetworkError{Err: err}
	}
	if c.format == "json" {
		return c.printValue(map[string]any{"success": true, "output": append([]string{}, lines...)})
	}
	return nil
}

func teleporterCmd() *Command {
	var (
		out  string
		only listFlag
	)
	importKeys := []string{"config", "dhcp_leases", "gravity.group", "gravity.adlist", "gravity.adlist_by_group",
		"gravity.domainlist", "gravity.domainlist_by_group", "gravity.client", "gravity.client_by_group"}
	return &Command{
		Name:    "teleporter",
		Summary: "Back up and restore Pi-hole settings as a zip archive",
		Subs: []*Command{
			{
				Name:      "export",
				Summary:   "Download a teleporter backup",
				Endpoints: []string{"GET /api/teleporter"},
				Flags: func(fs *flag.FlagSet) {
					fs.StringVar(&out, "file", "", "write to this path, or - for stdout (default: server's filename in the current directory)")
				},
				Run: func(c *Ctx, _ []string) error { return runExport(c, out) },
			},
			{
				Name:    "import",
				Summary: "Restore a teleporter backup ('-' reads the zip from stdin)",
				Long: "Restores everything unless --only is given. --only accepts: " + strings.Join(importKeys, ", ") +
					`, or "gravity" for all gravity tables.`,
				Args:      "<file.zip>",
				MinArgs:   1,
				MaxArgs:   1,
				Mutates:   true,
				Endpoints: []string{"POST /api/teleporter"},
				Flags: func(fs *flag.FlagSet) {
					fs.Var(&only, "only", "restore only these parts; repeatable or comma-separated")
				},
				Run: func(c *Ctx, args []string) error { return runImport(c, args[0], only, importKeys) },
			},
		},
	}
}

func runExport(c *Ctx, out string) error {
	cl, err := c.Client()
	if err != nil {
		return err
	}
	resp, err := cl.Do(c, pihole.Request{Method: http.MethodGet, Path: "/teleporter"})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == "-" {
		if isTerminal(c.Stdout) {
			return usagef(c.cmd, "refusing to write a zip archive to a terminal; redirect stdout or use --file")
		}
		_, err := io.Copy(c.Stdout, resp.Body)
		return err
	}
	if out == "" {
		out = fmt.Sprintf("pi-hole_teleporter_%s.zip", time.Now().Format("2006-01-02_15-04-05"))
		if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
			if fn := filepath.Base(params["filename"]); fn != "" && fn != "." && fn != "/" {
				out = fn
			}
		}
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return c.printValue(map[string]any{"file": out, "bytes": n})
}

func runImport(c *Ctx, src string, only, known []string) error {
	var (
		data []byte
		err  error
		name = filepath.Base(src)
	)
	if src == "-" {
		data, err = io.ReadAll(c.Stdin)
		name = "teleporter.zip"
	} else {
		data, err = os.ReadFile(src)
	}
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	if _, err := fw.Write(data); err != nil {
		return err
	}
	if len(only) > 0 {
		sel, err := importSelection(only, known)
		if err != nil {
			return usagef(c.cmd, "%v", err)
		}
		b, _ := json.Marshal(sel)
		if err := mw.WriteField("import", string(b)); err != nil {
			return err
		}
	}
	if err := mw.Close(); err != nil {
		return err
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	return c.show(cl.JSON(c, pihole.Request{Method: http.MethodPost, Path: "/teleporter",
		RawBody: buf.Bytes(), ContentType: mw.FormDataContentType()}))
}

// importSelection builds the teleporter "import" object with every part explicitly true or false.
func importSelection(only, known []string) (map[string]any, error) {
	want := map[string]bool{}
	for _, o := range only {
		matched := false
		for _, k := range known {
			if k == o || strings.HasPrefix(k, o+".") {
				want[k], matched = true, true
			}
		}
		if !matched {
			return nil, fmt.Errorf("unknown --only value %q (known: %s)", o, strings.Join(known, ", "))
		}
	}
	sel := map[string]any{}
	gravity := map[string]bool{}
	for _, k := range known {
		if g, ok := strings.CutPrefix(k, "gravity."); ok {
			gravity[g] = want[k]
		} else {
			sel[k] = want[k]
		}
	}
	sel["gravity"] = gravity
	return sel, nil
}

func apiCmd() *Command {
	var (
		data  string
		query listFlag
	)
	return &Command{
		Name:    "api",
		Summary: "Call any API endpoint directly (escape hatch for anything not wrapped)",
		Long: `The path is relative to /api (a leading /api is accepted). Examples:
  holectl api GET /stats/summary
  holectl api GET /queries --query length=5 --query domain='*.example.com'
  holectl api POST /dns/blocking -d '{"blocking":false,"timer":60}'
  holectl api PATCH /config -d @changes.json
List what the server supports with 'holectl endpoints'.`,
		Args:      "<METHOD> <path>",
		MinArgs:   2,
		MaxArgs:   2,
		Mutates:   true,
		Endpoints: []string{"ANY /api/*"},
		Flags: func(fs *flag.FlagSet) {
			fs.StringVar(&data, "d", "", "request body: JSON text, @file, or @- for stdin")
			fs.Var(&query, "query", "query parameter key=value; repeatable (or put ?k=v in the path)")
		},
		Run: func(c *Ctx, args []string) error { return runAPI(c, args, data, query) },
	}
}

func runAPI(c *Ctx, args []string, data string, query []string) error {
	req, err := buildAPIRequest(c, args, data, query)
	if err != nil {
		return err
	}
	cl, err := c.Client()
	if err != nil {
		return err
	}
	resp, err := cl.Do(c, req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &pihole.NetworkError{Err: err}
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return c.print([]byte(`{"success":true}`))
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "json") || (ct == "" && json.Valid(body)) {
		return c.print(body)
	}
	if c.G.Quiet {
		return nil
	}
	_, err = c.Stdout.Write(body)
	return err
}

// buildAPIRequest turns `api` arguments into a request. The path is kept exactly as typed
// (already-escaped segments stay escaped); only a ?query suffix is split off.
func buildAPIRequest(c *Ctx, args []string, data string, query []string) (pihole.Request, error) {
	method := strings.ToUpper(args[0])
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return pihole.Request{}, usagef(c.cmd, "unsupported method %q", args[0])
	}
	p, rawQuery, _ := strings.Cut(args[1], "?")
	p = "/" + strings.TrimPrefix(strings.TrimPrefix(p, "/"), "api/")
	if p == "/api" {
		p = "/"
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return pihole.Request{}, usagef(c.cmd, "invalid query string: %v", err)
	}
	for _, kv := range query {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return pihole.Request{}, usagef(c.cmd, "--query wants key=value, got %q", kv)
		}
		q.Add(k, v)
	}
	req := pihole.Request{Method: method, Path: p, Query: q}
	if data != "" {
		body, err := c.readData(data)
		if err != nil {
			return req, err
		}
		req.RawBody, req.ContentType = body, "application/json"
	}
	return req, nil
}
