# holectl

`holectl` ("hole control") is a command-line client for the [Pi-hole](https://pi-hole.net) **v6** REST API.
It covers the whole API: blocking, stats, the query log, domains, lists, groups, clients, server
settings, DHCP, the network table, logs, gravity, teleporter backups and auth sessions. A raw `api`
command reaches anything else. It has **no dependencies** outside the Go standard library.

## Install

```bash
go install github.com/drewstinnett/holectl@latest
# or, from a checkout:
make install          # lint + test + build, then installs to ~/bin
```

## Configure

```bash
printf '%s' "$PIHOLE_PASSWORD" | holectl config init --url https://pi.hole --insecure --password-stdin
holectl auth status
```

The config lives at `~/.config/holectl/config.json` (mode 0600):

```json
{
  "url": "https://pi.hole",
  "password": "",
  "password_command": "security find-generic-password -s pihole -w",
  "insecure": true,
  "timeout": "30s",
  "output": "auto"
}
```

| Key | Meaning |
|---|---|
| `url` | Pi-hole base URL. A trailing `/api` or `/admin` is ignored; the scheme defaults to https. |
| `password` | Web password or an app password. |
| `password_command` | Shell command that prints the password. Used when `password` is empty. |
| `insecure` | Skip TLS verification. Pi-hole's built-in certificate is self-signed. |
| `timeout` | Per-request timeout. |
| `output` | `auto` (table on a terminal, JSON otherwise), `json` or `table`. |

Precedence: flags > environment (`HOLECTL_URL`, `HOLECTL_PASSWORD`, `HOLECTL_INSECURE`, `HOLECTL_OUTPUT`,
`HOLECTL_CONFIG`) > config file. Edit single keys with `holectl config set <key> <value>`.

Pi-hole limits how many API sessions can be open at once, so holectl caches its session in
`~/.cache/holectl/sessions.json` and reuses it, logging in again only when it expires. With 2FA on,
run `holectl auth login --totp <code>` once, or use an app password.

## Usage

```bash
holectl blocking disable --for 5m
holectl search doubleclick.net                     # why is this blocked?
holectl domains add --type allow --wildcard example.com
holectl stats top-domains --blocked --from 7d
holectl queries list --client-ip 192.168.1.20 --length 20
holectl lists add https://example.com/hosts.txt && holectl action gravity
holectl server-config set dns.upstreams '["1.1.1.1","9.9.9.9"]'
holectl teleporter export --file backup.zip
holectl api GET /info/ftl                          # anything not wrapped
```

Run `holectl --help` or `holectl <command> --help` for details.

## For agents and scripts

- **JSON is the API's own response**, unmodified, whenever stdout isn't a terminal (or with `-o json`).
  `--field path.to.value` extracts one value.
- **Self-describing:** `holectl help --json` lists every command with its args, typed flags, whether it
  mutates, and the API endpoints it calls.
- **`--dry-run` / `-n`** prints the exact method, URL and body of every mutating request instead of
  sending it. Reads still happen.
- **Stable exit codes:** 0 ok, 1 error, 2 usage, 3 auth, 4 not found, 5 API rejected, 6 network.
  In JSON mode, errors go to stderr as `{"error":{"code","exit_code","message","key","hint",...}}`.
- **Never interactive:** secrets come from `--password-stdin`, `password_command` or the environment,
  and `-` as an argument reads newline-separated items from stdin.
- Flags may come before or after positional arguments.

An agent skill ships with the binary:

```bash
holectl skill install            # install, or update after upgrading holectl (safe to re-run)
holectl skill status             # installed? up to date with this binary?
holectl skill install --project  # this project only: ./.claude/skills
holectl skill print              # or view it
```

The source is [`skills/holectl/SKILL.md`](skills/holectl/SKILL.md). A test checks that every command
and flag it mentions exists.

## Development

```bash
make check      # go vet, gofmt, golangci-lint (if installed), build, race tests
```

The tests run against an `httptest` fake Pi-hole. Commands map onto the
[Pi-hole v6 OpenAPI spec](https://github.com/pi-hole/FTL/tree/master/src/api/docs/content/specs);
`holectl endpoints` lists what a live server exposes.
