package cli

import (
	"strings"
	"testing"
)

func TestLocalDNSRecordAddBuildsHostsEntryInEitherOrder(t *testing.T) {
	for _, args := range [][]string{{"nas.lan", "192.168.1.10"}, {"192.168.1.10", "nas.lan"}} {
		f := newFake(t)
		_, stderr, code := run(t, f, "", append([]string{"local-dns", "records", "add"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, stderr)
		}
		r := f.last(t)
		if r.Method != "PUT" || r.Path != "/api/config/dns/hosts/192.168.1.10%20nas.lan" {
			t.Fatalf("%v: request %s %s", args, r.Method, r.Path)
		}
	}
}

func TestLocalDNSRecordAddIPv6(t *testing.T) {
	f := newFake(t)
	if _, stderr, code := run(t, f, "", "dns", "aaaa", "add", "nas.lan", "fd00::10"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if p := f.last(t).Path; p != "/api/config/dns/hosts/fd00::10%20nas.lan" {
		t.Fatalf("path %s", p)
	}
}

func TestLocalDNSCNAMEAddKeepsAliasFirstAndTTL(t *testing.T) {
	f := newFake(t)
	_, stderr, code := run(t, f, "", "local-dns", "cname", "add", "media.lan", "nas.lan", "--ttl", "300")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	r := f.last(t)
	if r.Method != "PUT" || r.Path != "/api/config/dns/cnameRecords/media.lan%2Cnas.lan%2C300" {
		t.Fatalf("request %s %s", r.Method, r.Path)
	}
}

func TestLocalDNSRejectsMixedUpRecordKinds(t *testing.T) {
	cases := map[string][]string{
		"cname to an IP":       {"local-dns", "cname", "add", "media.lan", "192.168.1.10"},
		"record without an IP": {"local-dns", "records", "add", "media.lan", "nas.lan"},
		"record with two IPs":  {"local-dns", "records", "add", "10.0.0.1", "10.0.0.2"},
		"cname to itself":      {"local-dns", "cname", "add", "nas.lan", "nas.lan"},
	}
	for name, args := range cases {
		f := newFake(t)
		_, stderr, code := run(t, f, "", args...)
		if code != 2 {
			t.Errorf("%s: exit %d, want 2 (%s)", name, code, stderr)
		}
		f.mu.Lock()
		n := len(f.reqs)
		f.mu.Unlock()
		if n != 0 {
			t.Errorf("%s: sent %d requests", name, n)
		}
	}
}

func TestLocalDNSRemoveDeletesStoredEntries(t *testing.T) {
	f := newFake(t)
	f.responses["GET /api/config/dns/hosts"] = `{"config":{"dns":{"hosts":` +
		`["192.168.1.10 nas.lan","192.168.1.11 nas.lan","192.168.1.20 printer.lan"]}}}`
	f.responses["GET /api/config/dns/cnameRecords"] = `{"config":{"dns":{"cnameRecords":` +
		`["media.lan,nas.lan,300","other.lan,printer.lan"]}}}`

	if _, stderr, code := run(t, f, "", "local-dns", "records", "rm", "nas.lan", "192.168.1.11"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if p := f.last(t).Path; p != "/api/config/dns/hosts/192.168.1.11%20nas.lan" {
		t.Fatalf("path %s", p)
	}

	out, stderr, code := run(t, f, "", "local-dns", "records", "rm", "NAS.lan", "-o", "json")
	if code != 0 || strings.Count(out, `"success": true`) != 2 {
		t.Fatalf("exit %d: %s %s", code, out, stderr)
	}

	// The stored TTL is matched for the caller rather than having to be retyped.
	if _, stderr, code := run(t, f, "", "local-dns", "cname", "rm", "media.lan"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if p := f.last(t).Path; p != "/api/config/dns/cnameRecords/media.lan%2Cnas.lan%2C300" {
		t.Fatalf("path %s", p)
	}

	if _, _, code := run(t, f, "", "local-dns", "cname", "rm", "media.lan", "printer.lan"); code != 4 {
		t.Fatalf("wrong target: exit %d, want 4", code)
	}
}
