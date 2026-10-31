package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recorded struct {
	Method, Path, Query, ContentType string
	Body                             []byte
}

// fakePihole answers canned responses keyed "METHOD /api/path" and records every request.
type fakePihole struct {
	*httptest.Server
	mu        sync.Mutex
	reqs      []recorded
	responses map[string]string
	status    map[string]int
}

func newFake(t *testing.T) *fakePihole {
	t.Helper()
	f := &fakePihole{responses: map[string]string{}, status: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && path == "/api/auth" {
			_, _ = io.WriteString(w, `{"session":{"valid":true,"totp":false,"sid":"s1","csrf":"c1","validity":1800,"message":"ok"}}`)
			return
		}
		f.reqs = append(f.reqs, recorded{r.Method, path, r.URL.RawQuery, r.Header.Get("Content-Type"), body})
		if r.Header.Get("X-FTL-SID") != "s1" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"key":"unauthorized","message":"Unauthorized","hint":null}}`)
			return
		}
		key := r.Method + " " + path
		if s, ok := f.status[key]; ok {
			w.WriteHeader(s)
		}
		if resp, ok := f.responses[key]; ok {
			_, _ = io.WriteString(w, resp)
			return
		}
		if r.Method == http.MethodDelete || strings.HasSuffix(path, ":batchDelete") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, `{"took":0.001}`)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePihole) last(t *testing.T) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	return f.reqs[len(f.reqs)-1]
}

// run executes holectl against f with an isolated config and session cache.
func run(t *testing.T, f *fakePihole, stdin string, args ...string) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("HOLECTL_CONFIG", "")
	t.Setenv("HOLECTL_OUTPUT", "")
	if f != nil {
		t.Setenv("HOLECTL_URL", f.URL)
		t.Setenv("HOLECTL_PASSWORD", "pw")
	}
	var out, errOut bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func jsonEq(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("invalid JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if !bytes.Equal(gb, wb) {
		t.Fatalf("body = %s\nwant   %s", gb, wb)
	}
}

func TestBlockingDisableWithTimer(t *testing.T) {
	f := newFake(t)
	_, stderr, code := run(t, f, "", "blocking", "disable", "--for", "5m")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	r := f.last(t)
	if r.Method != "POST" || r.Path != "/api/dns/blocking" {
		t.Fatalf("request %s %s", r.Method, r.Path)
	}
	jsonEq(t, r.Body, `{"blocking":false,"timer":300}`)
}

func TestBlockingEnablePermanentSendsNullTimer(t *testing.T) {
	f := newFake(t)
	run(t, f, "", "blocking", "enable")
	jsonEq(t, f.last(t).Body, `{"blocking":true,"timer":null}`)
}

func TestDomainsAddResolvesGroupNamesAndAcceptsTrailingFlags(t *testing.T) {
	f := newFake(t)
	f.responses["GET /api/groups"] = `{"groups":[{"id":0,"name":"Default"},{"id":3,"name":"kids"}]}`
	_, stderr, code := run(t, f, "", "domains", "add", "a.example", "b.example",
		"--type", "deny", "--group", "kids,0", "--comment", "ads")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	r := f.last(t)
	if r.Path != "/api/domains/deny/exact" {
		t.Fatalf("path %s", r.Path)
	}
	jsonEq(t, r.Body, `{"domain":["a.example","b.example"],"comment":"ads","groups":[3,0],"enabled":true}`)
}

func TestDomainsAddWildcardAndStdin(t *testing.T) {
	f := newFake(t)
	_, stderr, code := run(t, f, "example.com\n# comment\n\nexample.org\n",
		"domains", "add", "--type", "allow", "--wildcard", "-")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	r := f.last(t)
	if r.Path != "/api/domains/allow/regex" {
		t.Fatalf("path %s", r.Path)
	}
	jsonEq(t, r.Body, `{"domain":["(\\.|^)example\\.com$","(\\.|^)example\\.org$"],"enabled":true}`)
}

func TestDomainsAddReportsPerItemErrors(t *testing.T) {
	f := newFake(t)
	f.responses["POST /api/domains/deny/exact"] = `{"domains":[],"processed":{"success":[],` +
		`"errors":[{"item":"bad..domain","error":"Invalid domain"}]}}`
	_, stderr, code := run(t, f, "", "domains", "add", "--type", "deny", "bad..domain")
	if code != ExitAPI {
		t.Fatalf("exit %d, want %d: %s", code, ExitAPI, stderr)
	}
}

func TestDomainsUpdateMergesExistingFields(t *testing.T) {
	f := newFake(t)
	f.responses["GET /api/domains/allow/exact/x.example"] =
		`{"domains":[{"id":7,"domain":"x.example","comment":"keep me","groups":[0,2],"enabled":true}]}`
	_, stderr, code := run(t, f, "", "domains", "update", "x.example", "--type", "allow", "--enabled=false")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	r := f.last(t)
	if r.Method != "PUT" || r.Path != "/api/domains/allow/exact/x.example" {
		t.Fatalf("request %s %s", r.Method, r.Path)
	}
	jsonEq(t, r.Body, `{"type":"allow","kind":"exact","comment":"keep me","groups":[0,2],"enabled":false}`)
}

func TestDomainsRemoveRegexIsPathEscaped(t *testing.T) {
	f := newFake(t)
	_, stderr, code := run(t, f, "", "domains", "rm", "--type", "deny", "--kind", "regex", `^ad/x\.`)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if r := f.last(t); r.Method != "DELETE" || r.Path != `/api/domains/deny/regex/%5Ead%2Fx%5C.` {
		t.Fatalf("request %s %s", r.Method, r.Path)
	}
}

func TestDomainsRemoveManyUsesBatchDelete(t *testing.T) {
	f := newFake(t)
	run(t, f, "", "domains", "remove", "--type", "allow", "a.example", "b.example")
	r := f.last(t)
	if r.Path != "/api/domains:batchDelete" {
		t.Fatalf("path %s", r.Path)
	}
	jsonEq(t, r.Body, `[{"item":"a.example","type":"allow","kind":"exact"},{"item":"b.example","type":"allow","kind":"exact"}]`)
}

func TestDryRunPrintsPlanAndSendsNothing(t *testing.T) {
	f := newFake(t)
	stdout, stderr, code := run(t, f, "", "--dry-run", "lists", "add", "https://example.com/list.txt")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(f.reqs) != 0 {
		t.Fatalf("requests sent: %+v", f.reqs)
	}
	var plan struct {
		DryRun   bool `json:"dry_run"`
		Requests []struct {
			Method, URL string
			Body        json.RawMessage
		}
	}
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("stdout %q: %v", stdout, err)
	}
	if !plan.DryRun || len(plan.Requests) != 1 || plan.Requests[0].Method != "POST" ||
		!strings.HasSuffix(plan.Requests[0].URL, "/api/lists?type=block") {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestDryRunStillReads(t *testing.T) {
	f := newFake(t)
	f.responses["GET /api/groups/kids"] = `{"groups":[{"name":"kids","comment":null,"enabled":true}]}`
	_, stderr, code := run(t, f, "", "-n", "groups", "rename", "kids", "children")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(f.reqs) != 1 || f.reqs[0].Method != "GET" {
		t.Fatalf("requests: %+v", f.reqs)
	}
}

func TestFieldExtraction(t *testing.T) {
	f := newFake(t)
	f.responses["GET /api/dns/blocking"] = `{"blocking":"disabled","timer":12.5,"took":0.1}`
	stdout, _, code := run(t, f, "", "blocking", "status", "--field", "blocking")
	if code != 0 || stdout != "disabled\n" {
		t.Fatalf("exit %d, stdout %q", code, stdout)
	}
	_, stderr, code := run(t, f, "", "blocking", "status", "--field", "nope")
	if code != ExitNotFound || !strings.Contains(stderr, "available: blocking, timer, took") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestJSONOutputPreservesServerResponse(t *testing.T) {
	f := newFake(t)
	f.responses["GET /api/stats/summary"] = `{"queries":{"total":10,"blocked":2},"zz":1,"aa":2}`
	stdout, _, _ := run(t, f, "", "stats", "summary")
	if !strings.Contains(stdout, `"zz": 1,`+"\n"+`  "aa": 2`) {
		t.Fatalf("key order or content changed:\n%s", stdout)
	}
}

func TestTableOutput(t *testing.T) {
	f := newFake(t)
	f.responses["GET /api/domains"] = `{"domains":[{"id":1,"domain":"a.example","unicode":"a.example","type":"deny",` +
		`"kind":"exact","comment":null,"groups":[0,1],"enabled":true,"date_added":1700000000}],"took":0.1}`
	stdout, _, code := run(t, f, "", "domains", "list", "-o", "table")
	if code != 0 {
		t.Fatal(code)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "DATE_ADDED") {
		t.Fatalf("table:\n%s", stdout)
	}
	if !strings.Contains(lines[1], "a.example") || !strings.Contains(lines[1], "0,1") ||
		!strings.Contains(lines[1], "2023-11-") || strings.Contains(lines[1], "unicode") {
		t.Fatalf("row: %q", lines[1])
	}
}

func TestDomainsListFiltersBySingleDomain(t *testing.T) {
	f := newFake(t)
	f.responses["GET /api/domains"] = `{"domains":[{"domain":"a.example","type":"deny"},{"domain":"b.example","type":"allow"}]}`
	stdout, _, _ := run(t, f, "", "domains", "list", "b.example")
	jsonEq(t, []byte(stdout), `{"domains":[{"domain":"b.example","type":"allow"}]}`)
}

func TestErrorsAreJSONWithStableExitCodes(t *testing.T) {
	f := newFake(t)
	f.status["GET /api/groups/nope"] = 404
	f.responses["GET /api/groups/nope"] = `{"error":{"key":"not_found","message":"Group not found","hint":null}}`
	_, stderr, code := run(t, f, "", "groups", "list", "nope")
	if code != ExitNotFound {
		t.Fatalf("exit %d", code)
	}
	var e struct {
		Error struct {
			Code     string `json:"code"`
			ExitCode int    `json:"exit_code"`
			Key      string `json:"key"`
			Status   int    `json:"status"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &e); err != nil {
		t.Fatalf("stderr %q: %v", stderr, err)
	}
	if e.Error.Code != "not_found" || e.Error.Key != "not_found" || e.Error.Status != 404 || e.Error.ExitCode != 4 {
		t.Fatalf("error = %+v", e.Error)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"nosuch"},
		{"domains", "add", "x.example"},                   // missing --type
		{"domains", "add", "--type", "maybe", "x"},        // bad choice
		{"search"},                                        // missing arg
		{"blocking", "status", "extra"},                   // too many args
		{"stats", "summary", "--until", "1h"},             // --until without --from
		{"server-config", "patch", "--file", "/dev/null"}, // invalid JSON
	} {
		_, stderr, code := run(t, newFake(t), "", args...)
		if code != ExitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, code, ExitUsage, stderr)
		}
	}
}

func TestStatsFromSwitchesToDatabase(t *testing.T) {
	f := newFake(t)
	timeNow = func() time.Time { return time.Unix(1_700_000_000, 0) }
	t.Cleanup(func() { timeNow = time.Now })
	run(t, f, "", "stats", "top-domains", "--from", "24h", "--blocked", "--count", "5")
	r := f.last(t)
	if r.Path != "/api/stats/database/top_domains" ||
		r.Query != "blocked=true&count=5&from=1699913600&until=1700000000" {
		t.Fatalf("request %s?%s", r.Path, r.Query)
	}
}

func TestServerConfigSetBuildsNestedPatch(t *testing.T) {
	f := newFake(t)
	run(t, f, "", "server-config", "set", "dns.upstreams", `["1.1.1.1","9.9.9.9"]`, "--no-restart")
	r := f.last(t)
	if r.Method != "PATCH" || r.Path != "/api/config" || r.Query != "restart=false" {
		t.Fatalf("request %s %s?%s", r.Method, r.Path, r.Query)
	}
	jsonEq(t, r.Body, `{"config":{"dns":{"upstreams":["1.1.1.1","9.9.9.9"]}}}`)

	run(t, f, "", "server-config", "set", "webserver.port", "80o,443os", "--string")
	jsonEq(t, f.last(t).Body, `{"config":{"webserver":{"port":"80o,443os"}}}`)

	run(t, f, "", "sc", "add", "dns/upstreams", "1.1.1.1#53")
	if r := f.last(t); r.Method != "PUT" || r.Path != "/api/config/dns/upstreams/1.1.1.1%2353" {
		t.Fatalf("request %s %s", r.Method, r.Path)
	}
}

func TestTeleporterImportOnly(t *testing.T) {
	f := newFake(t)
	zip := filepath.Join(t.TempDir(), "backup.zip")
	if err := os.WriteFile(zip, []byte("PK fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := run(t, f, "", "teleporter", "import", zip, "--only", "config,gravity.group")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	r := f.last(t)
	_, params, err := mime.ParseMediaType(r.ContentType)
	if err != nil {
		t.Fatal(err)
	}
	form, err := multipart.NewReader(bytes.NewReader(r.Body), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if fh := form.File["file"]; len(fh) != 1 || fh[0].Filename != "backup.zip" {
		t.Fatalf("file part: %+v", form.File)
	}
	jsonEq(t, []byte(form.Value["import"][0]), `{"config":true,"dhcp_leases":false,"gravity":{"group":true,
		"adlist":false,"adlist_by_group":false,"domainlist":false,"domainlist_by_group":false,
		"client":false,"client_by_group":false}}`)
}

func TestAPIEscapeHatch(t *testing.T) {
	f := newFake(t)
	_, stderr, code := run(t, f, `{"blocking":true}`, "api", "post", "/api/dns/blocking", "-d", "@-")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	r := f.last(t)
	if r.Method != "POST" || r.Path != "/api/dns/blocking" {
		t.Fatalf("request %s %s", r.Method, r.Path)
	}
	jsonEq(t, r.Body, `{"blocking":true}`)

	run(t, f, "", "api", "GET", "queries?length=5", "--query", "domain=*.example")
	if r := f.last(t); r.Path != "/api/queries" || r.Query != "domain=%2A.example&length=5" {
		t.Fatalf("request %s?%s", r.Path, r.Query)
	}
}

func TestDeleteEachReportsResults(t *testing.T) {
	f := newFake(t)
	f.status["DELETE /api/dhcp/leases/10.0.0.9"] = 404
	f.responses["DELETE /api/dhcp/leases/10.0.0.9"] = `{"error":{"key":"not_found","message":"Lease not found"}}`
	stdout, _, code := run(t, f, "", "dhcp", "leases", "rm", "10.0.0.8", "10.0.0.9")
	if code != ExitNotFound {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stdout, `"item": "10.0.0.8",`+"\n"+`      "success": true`) ||
		!strings.Contains(stdout, `"success": false`) {
		t.Fatalf("stdout:\n%s", stdout)
	}
}

func TestConfigInitAndShow(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "c.json")
	t.Setenv("HOLECTL_URL", "")
	t.Setenv("HOLECTL_PASSWORD", "")
	var out, errOut bytes.Buffer
	code := Run(context.Background(), []string{"config", "init", "--config", cfgPath, "--url", "pi.hole",
		"--insecure", "--password-stdin", "--timeout", "15s"}, strings.NewReader("hunter2\n"), &out, &errOut)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	data, _ := os.ReadFile(cfgPath)
	jsonEq(t, data, `{"url":"pi.hole","password":"hunter2","insecure":true,"timeout":"15s"}`)
	if st, _ := os.Stat(cfgPath); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	out.Reset()
	Run(context.Background(), []string{"config", "show", "--config", cfgPath}, nil, &out, &errOut)
	if strings.Contains(out.String(), "hunter2") {
		t.Fatalf("password leaked: %s", out.String())
	}
	code = Run(context.Background(), []string{"config", "init", "--config", cfgPath, "--url", "x"}, nil, &out, &errOut)
	if code != ExitUsage {
		t.Fatalf("re-init without --force: exit %d", code)
	}
}

func TestMissingPasswordIsAuthExit(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("HOLECTL_CONFIG", filepath.Join(dir, "none.json"))
	t.Setenv("HOLECTL_URL", f.URL)
	t.Setenv("HOLECTL_PASSWORD", "")
	var out, errOut bytes.Buffer
	if code := Run(context.Background(), []string{"blocking", "status"}, nil, &out, &errOut); code != ExitAuth {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
}

func TestHelpJSONDescribesEveryCommand(t *testing.T) {
	stdout, _, code := run(t, nil, "", "help", "--json")
	if code != 0 {
		t.Fatal(code)
	}
	var doc struct {
		Commands []commandSpec `json:"commands"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Commands) < 80 {
		t.Fatalf("only %d commands", len(doc.Commands))
	}
	for _, c := range doc.Commands {
		if c.Summary == "" {
			t.Errorf("%s: no summary", c.Command)
		}
		if !c.Local && len(c.Endpoints) == 0 {
			t.Errorf("%s: talks to the server but lists no endpoints", c.Command)
		}
		for _, e := range c.Endpoints {
			if !strings.Contains(e, " /api") {
				t.Errorf("%s: malformed endpoint %q", c.Command, e)
			}
		}
	}
}

func TestEveryCommandHasHelp(t *testing.T) {
	for _, l := range buildTree().leaves() {
		args := append(strings.Fields(strings.TrimPrefix(l.fullPath(), "holectl ")), "--help")
		stdout, stderr, code := run(t, nil, "", args...)
		if code != 0 || !strings.Contains(stdout, "Usage:") {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
}

func TestParseWhen(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for in, want := range map[string]int64{
		"1690000000":           1690000000,
		"2h":                   1_700_000_000 - 7200,
		"-90m":                 1_700_000_000 - 5400,
		"7d":                   1_700_000_000 - 7*86400,
		"2023-11-14T22:13:20Z": 1_700_000_000,
		"now":                  1_700_000_000,
	} {
		got, err := parseWhen(in, now)
		if err != nil || got != want {
			t.Errorf("parseWhen(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parseWhen("yesterday-ish", now); err == nil {
		t.Error("expected error")
	}
}
