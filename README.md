# holectl

`holectl` ("hole control") is a command-line client for the [Pi-hole](https://pi-hole.net) **v6** REST API.
It covers the whole API: blocking, stats, the query log, domains, lists, groups, clients, server
settings, DHCP, the network table, logs, gravity, teleporter backups and auth sessions. A raw `api`
command reaches anything else. It has **no dependencies** outside the Go standard library.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/drewstinnett/holectl/main/install.sh | sh
```

The script picks the right binary for Linux or macOS (amd64, arm64, and armv6/armv7 for Raspberry Pis)
and verifies it against the release's `checksums.txt`. It installs to `/usr/local/bin` when that's
writable, and to `~/.local/bin` otherwise. To change that, set `HOLECTL_INSTALL_DIR`; to install a
specific release, set `HOLECTL_VERSION`:

```bash
curl -fsSL https://raw.githubusercontent.com/drewstinnett/holectl/main/install.sh | HOLECTL_VERSION=v0.1.0 sh
```

Windows zips and every other build are on the [releases page](https://github.com/drewstinnett/holectl/releases).
Or build from source:

```bash
go install github.com/drewstinnett/holectl@latest
make install          # from a checkout: lint + test + build, then installs to ~/bin
```

### Updating

```bash
holectl update --check    # report only
holectl update            # download, verify the checksum, replace this binary in place
holectl update --version v0.1.0   # install a specific release (downgrades allowed)
```

On a terminal, holectl checks for a new release at most once a day and mentions it on stderr. The
check never runs when stderr is piped, so scripts and agents never see it. Set
`HOLECTL_NO_UPDATE_CHECK=1` to turn it off. Development builds (`go install` from a checkout, or
`make`) are never nagged; `holectl update --force` replaces one with the latest release. Set
`GITHUB_TOKEN` if you hit GitHub's unauthenticated API rate limit.

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
make snapshot   # build every release archive into dist/ with GoReleaser, without publishing
```

Releases are automatic. Every push to `main` runs CI. If CI passes, `scripts/next-version.sh` reads
the [Conventional Commits](https://www.conventionalcommits.org/) since the last tag and picks the
next semantic version:

| Commits | Bump |
|---|---|
| `type!:` or a `BREAKING CHANGE:` footer | major (minor while still 0.x) |
| `feat:` | minor |
| `fix:`, `perf:`, `refactor:`, `revert:`, `build:` | patch |
| only `docs:`, `chore:`, `ci:`, `test:` or `style:` | no release |

When a bump is due, the workflow tags the commit and GoReleaser publishes the binaries and `checksums.txt`.

The tests run against an `httptest` fake Pi-hole. Commands map onto the
[Pi-hole v6 OpenAPI spec](https://github.com/pi-hole/FTL/tree/master/src/api/docs/content/specs);
`holectl endpoints` lists what a live server exposes.
