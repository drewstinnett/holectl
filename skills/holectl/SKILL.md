---
name: holectl
description: Control a Pi-hole v6 DNS sink-hole with the holectl CLI. Use when asked to check or toggle ad blocking, allow or block a domain, find out why a domain is blocked, look at DNS query logs or top blocked domains/clients, manage blocklists, groups or clients, change Pi-hole settings, update gravity, read Pi-hole logs, or back up/restore a Pi-hole.
---

# holectl: Pi-hole from the command line

`holectl` wraps the whole Pi-hole v6 REST API (`/api/*`). Every command prints the API's own JSON,
unmodified, when stdout is not a terminal, so you can parse responses directly.

## Ground rules

1. **Pass `-o json`** on every call. Output is already JSON when piped, but being explicit is safer.
2. **Run mutations with `--dry-run` first** when the request is ambiguous or destructive. It prints the
   exact method, URL and body without sending anything (exit 0). Reads still run during a dry run.
3. **Check the exit code**, not just the output:

   | code | meaning | what to do |
   |---|---|---|
   | 0 | success (also for `--dry-run`) | |
   | 1 | other error | read the message |
   | 2 | usage error: bad flag or argument | run `holectl <cmd> --help` |
   | 3 | auth failed or required | see Setup; don't retry in a loop |
   | 4 | not found | the domain, list, group or field doesn't exist |
   | 5 | API rejected the request, or some batch items failed | read `error.message` / `error.hint` |
   | 6 | network error or timeout | check URL/TLS, raise `--timeout` |

   With `-o json`, errors go to **stderr** as
   `{"error":{"code":"not_found","exit_code":4,"message":"...","key":"...","hint":"...","status":404,"suggestion":"..."}}`.
4. **Never prompts.** Every input comes from flags, args or stdin, so nothing hangs waiting for a TTY.
5. Flags may come before or after positional args. Use `--` before an arg that starts with `-`.

## Discover before guessing

```bash
holectl help --json            # every command: args, flags with types/defaults, mutates, API endpoints
holectl <command> --help       # human help for one command
holectl endpoints -o json      # what the connected server's API actually exposes
```

## Setup

```bash
holectl config show -o json    # what's configured (password redacted), and which env vars override it
printf '%s' "$PIHOLE_PASSWORD" | holectl config init --url https://pi.hole --insecure --password-stdin
holectl auth status -o json    # verify: session.valid should be true
```

- Config: `~/.config/holectl/config.json` (keys: `url`, `password`, `password_command`, `insecure`,
  `timeout`, `output`). Environment overrides: `HOLECTL_URL`, `HOLECTL_PASSWORD`, `HOLECTL_INSECURE`,
  `HOLECTL_OUTPUT`, `HOLECTL_CONFIG`. Flags override both: `--url`, `--insecure`/`-k`, `--timeout`.
- Pi-hole's HTTPS certificate is self-signed by default. If you hit an x509 error, use `--insecure`.
- Sessions are cached and reused, because Pi-hole caps concurrent sessions. On a `no_seats` error, run
  `holectl auth sessions` and `holectl auth revoke <id>`.
- If 2FA is on, a human must run `holectl auth login --totp <code>` once; or use an app password
  (`holectl auth app-password`). Don't invent TOTP codes.

## Common tasks

```bash
# Blocking on/off
holectl blocking status -o json --field blocking          # -> enabled | disabled
holectl blocking disable --for 5m -o json                 # re-enables automatically
holectl blocking enable -o json

# "Why is example.com blocked?" / "Is it allowed?"
holectl search example.com -o json                        # exact/regex domain entries + gravity list matches
holectl search ads --partial --limit 50 -o json

# Allow / block domains (--type is required; --kind defaults to exact)
holectl domains add --type allow example.com --comment "needed for work" -o json
holectl domains add --type allow --wildcard example.com   # example.com and all subdomains (regex)
holectl domains add --type deny --kind regex '(^|\.)doubleclick\.net$'
holectl domains list --type deny -o json
holectl domains update example.com --type allow --enabled=false   # other fields are preserved
holectl domains update example.com --type deny --new-type allow   # move between lists
holectl domains remove --type allow example.com
printf 'a.example\nb.example\n' | holectl domains add --type deny -   # '-' reads items from stdin

# Statistics and logs (last 24h from memory; --from switches to the long-term database)
holectl stats summary -o json
holectl stats top-domains --blocked --count 20 -o json
holectl stats top-clients --from 7d -o json               # times: unix, RFC 3339, YYYY-MM-DD, or ago (24h, 7d)
holectl queries list --domain '*.example.com' --length 50 -o json
holectl queries list --client-ip 192.168.1.20 --status GRAVITY -o json
holectl history --clients -o json

# Blocklists (subscriptions): add, then rebuild gravity
holectl lists add --type block https://example.com/hosts.txt --comment "extra ads" -o json
holectl action gravity --timeout 10m -o json              # slow: downloads every list
holectl lists list -o json

# Groups and clients (--group accepts names or IDs)
holectl groups add kids --comment "kid devices" -o json
holectl clients add 192.168.1.50 --group kids -o json
holectl domains add --type deny tiktok.com --group kids -o json

# Server settings (pihole.toml); paths use dots or slashes
holectl server-config get dns.upstreams -o json
holectl server-config set dns.upstreams '["1.1.1.1","9.9.9.9"]' -o json   # values parse as JSON
holectl server-config add dns.upstreams 8.8.8.8 -o json                     # append to an array
holectl server-config get dns --detailed -o json                            # docs, types, allowed values

# Health and maintenance
holectl info version -o json         # also reports available updates
holectl info messages list -o json   # diagnosis warnings shown in the web UI
holectl action restart-dns -o json
holectl logs ftl -o json             # add --follow to stream (one JSON object per line)
holectl teleporter export --file backup.zip -o json
holectl update --check -o json       # is a newer holectl released? (`holectl update` installs it)
```

## Output tips

- `--field a.b.0.c` prints one value from the response: a bare string for scalars, JSON otherwise.
  Exit code 4 means the path doesn't exist, and the message lists the keys that do.
- Add and update responses carry `processed.success` / `processed.errors`. If any item failed, holectl
  still prints the response, then exits 5.
- Removing several items at once uses the API's batch delete. One missing item is a 404 (exit 4).
- `-o table` is for showing results to a human. Parse JSON, not tables.

## Anything else: the raw API

If no command fits, call the endpoint directly. The path is relative to `/api`:

```bash
holectl api GET /stats/database/summary --query from=1700000000 --query until=1700086400
holectl api POST /dns/blocking -d '{"blocking":false,"timer":30}' --dry-run
echo '{"config":{"dns":{"queryLogging":true}}}' | holectl api PATCH /config -d @-
```

## Be careful with

- `action flush-logs`, `action flush-network`, `teleporter import`, `server-config set/patch` (may
  restart FTL; `--no-restart` defers that), `auth app-password` (activating one invalidates every
  session), and removing groups. Confirm with the user and use `--dry-run` first.
- Disabling blocking without `--for` is permanent until someone re-enables it. Prefer a timer.
