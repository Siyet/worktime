<p align="center">
  <img src="web/public/favicon.svg" width="84" alt="WorkTime logo" />
</p>

<h1 align="center">WorkTime</h1>

<p align="center">
  Free, open-source, self-hosted time tracker.<br />
  <sub>Single binary · local-first PWA · MCP server for AI agents</sub>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white" alt="Go 1.25" />
  <img src="https://img.shields.io/badge/Svelte-5-FF3E00?logo=svelte&logoColor=white" alt="Svelte 5" />
  <img src="https://img.shields.io/badge/SQLite-single%20file-003B57?logo=sqlite&logoColor=white" alt="SQLite" />
  <img src="https://img.shields.io/badge/PWA-offline--first-5A0FC8" alt="PWA" />
  <img src="https://img.shields.io/badge/MCP-for%20AI%20agents-46b478" alt="MCP" />
  <img src="https://img.shields.io/badge/license-MIT-e8a33d" alt="MIT" />
</p>

<p align="center">
  <a href="https://siyet.github.io/worktime/"><b>Live demo</b></a> — runs entirely in your browser (IndexedDB), no backend, data never leaves the page.
</p>

![Reports: daily stacked chart with weekends and time off, KPI row, per-project totals](docs/screenshots/reports.png)

## Why WorkTime

- **Single binary** - Go backend with the web UI embedded. Download, run, open `localhost:8080`. SQLite storage, no external services.
- **Local-first PWA** - works fully offline: timers keep running without a network connection and sync when it returns. Install it on your phone from the browser, no native app needed.
- **Multiple concurrent timers** - track parallel work honestly; running timers are data, not a UI hack.
- **Time off as data** - vacation, sick leave and days off are first-class: shown in charts, counted in averages, never blocking tracking.
- **Reports that answer questions** - interactive daily chart, custom report builder (group by, columns, rounding), CSV export and a printable PDF report.
- **MCP server** - AI agents start/stop timers, log time off and query reports via Model Context Protocol.
- **Automatic agent tracking** - Claude Code and Codex hooks track an agent session as one entry, named after the tracker task it is working on, and survive a crash without inflating the duration. Settings hands out a setup prompt that makes the agent install and verify it itself ([docs](docs/agent-tracking.md)).
- **Multi-user** - Google sign-in, per-user data isolation. No billing, no clients, no seat pricing. Free forever.
- **Six UI languages** - English, Russian, Spanish, German, French, Chinese; light and dark theme; 12/24h and date format preferences.
- **Tiny footprint** - ~16MB RSS on the server, ~25KB gzip frontend bundle, 11MB binary.

![Timer: running timer, day groups with entries](docs/screenshots/timer.png)

## Status

Early development. Core works (timers, projects, time off, sync, reports, MCP); expect rough edges.

## Self-hosting

### Binary

```sh
make build          # produces bin/worktime with the web UI embedded
./bin/worktime --version
WORKTIME_GOOGLE_CLIENT_ID=... WORKTIME_GOOGLE_CLIENT_SECRET=... ./bin/worktime
```

Local builds identify themselves as `dev`. Packagers can inject immutable build
metadata with `VERSION=v1.2.3 REVISION=<full git revision>
BUILT_AT=2026-08-30T12:00:00Z PACKAGING=native make build`. The packaging flag
is deliberately explicit: an ordinary local build is notification-only.

### Docker

```sh
docker build -t worktime .
docker run -d -p 8080:8080 -v worktime-data:/data \
  -e WORKTIME_BASE_URL=https://time.example.com \
  -e WORKTIME_GOOGLE_CLIENT_ID=... \
  -e WORKTIME_GOOGLE_CLIENT_SECRET=... \
  -e WORKTIME_ALLOWED_EMAILS=you@example.com,team@example.com \
  worktime
```

### Configuration

| Env var | Default | Purpose |
|---|---|---|
| `WORKTIME_ADDR` | `:8080` | Listen address |
| `WORKTIME_DB` | `worktime.db` | SQLite database path |
| `WORKTIME_BASE_URL` | `http://localhost:8080` | External URL, used for OAuth redirects and secure cookies |
| `WORKTIME_TRUST_PROXY` | off | Set to `1` only behind a trusted immediate proxy that overwrites `X-Forwarded-Proto` and `X-Forwarded-Host`; these values become part of same-origin validation |
| `WORKTIME_GOOGLE_CLIENT_ID` / `WORKTIME_GOOGLE_CLIENT_SECRET` | - | Google OAuth Web credentials; redirect URI is `$BASE_URL/auth/google/callback` |
| `WORKTIME_ALLOWED_EMAILS` | empty = everyone | Comma-separated allowlist of Google accounts |
| `WORKTIME_ADMIN_EMAILS` | empty = nobody | Comma-separated accounts allowed to check, configure and apply instance updates; API tokens are never administrators |
| `WORKTIME_UPDATE_CHECKS` | `1` | Set to `0` to disable all GitHub release and Sigstore TUF network checks |
| `WORKTIME_DEV_AUTH` | off | `1` auto-signs every request in as a local dev user. Never use in production |

Backup = copy the SQLite file (or the `/data` volume).

### Releases and updates

Release artifacts target native Linux on amd64 and arm64, plus the matching
multi-platform image at `ghcr.io/siyet/worktime`. Native Linux is the first
self-updating deployment target. Docker, macOS and Windows installations are
notification-only and must be updated by their operator until their update
mechanics are accepted and implemented separately.

Release metadata is a keyless Sigstore-signed manifest. The trusted signing
identity is this repository's release workflow on `refs/heads/main`; a valid
signature therefore grants the released binary the same code-execution authority
as that workflow. Publication is deliberately manual and requires GitHub immutable
releases to be enabled. The protected `release` environment approver verifies that
repository setting manually because the workflow token cannot read Administration
settings. Docker operators must deploy the `image.name@image.digest` reference from
that verified manifest; the version tag alone is not a trusted or immutable
identifier. See [the release contract](docs/releases.md).

### MCP

Create an API token in Settings, then connect any MCP client to `https://your-host/mcp` with header `Authorization: Bearer <token>` (streamable HTTP transport).

```sh
claude mcp add --transport http worktime https://your-host/mcp --header "Authorization: Bearer <token>"
```

Tools: `start_timer`, `stop_timer`, `stop_all_timers`, `list_running_timers`, `update_time_entry`, `add_time_entry`, `list_projects`, `create_project`, `add_time_off`, `list_time_off`, `time_report`, `set_agent_task`.

`update_time_entry` moves an entry to another project or renames it; with no `entry_id` it edits the timer running right now, which is how an agent files its own session under a project.

## Development

```sh
make dev-api   # run Go backend on :8080 (use WORKTIME_DEV_AUTH=1 to skip Google sign-in)
make dev-web   # run Vite dev server (proxies /api to :8080)
make build     # build web + single binary
make test      # run tests
make e2e       # build, then run the Playwright suite against the binary
```

## License

MIT
