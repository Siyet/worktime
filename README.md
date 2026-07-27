# WorkTime

Free, open-source, self-hosted time tracker. A lightweight alternative to solidtime / Clockify.

## Why

- **Single binary** - Go backend with the web UI embedded. Download, run, open `localhost:8080`. SQLite storage, no external services.
- **Tiny footprint** - ~16MB RSS on the server, ~24KB gzip frontend bundle, 11MB binary.
- **Local-first PWA** - the app works offline: timers keep running without a network connection and sync when it returns. Install it on your phone from the browser, no native app needed.
- **Multiple concurrent timers** - track parallel work honestly.
- **Time off** - sick days and vacations as first-class data; they never block time tracking.
- **MCP server** - let AI agents start/stop timers, log time off and query reports via Model Context Protocol.
- **Multi-user** - Google sign-in, personal data isolation. No billing, no clients, no seat pricing. Free forever.

## Status

Early development. Core works (timers, projects, time off, sync, MCP); expect rough edges.

## Self-hosting

### Binary

```sh
make build          # produces bin/worktime with the web UI embedded
WORKTIME_GOOGLE_CLIENT_ID=... WORKTIME_GOOGLE_CLIENT_SECRET=... ./bin/worktime
```

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
| `WORKTIME_GOOGLE_CLIENT_ID` / `WORKTIME_GOOGLE_CLIENT_SECRET` | - | Google OAuth Web credentials; redirect URI is `$BASE_URL/auth/google/callback` |
| `WORKTIME_ALLOWED_EMAILS` | empty = everyone | Comma-separated allowlist of Google accounts |
| `WORKTIME_DEV_AUTH` | off | `1` auto-signs every request in as a local dev user. Never use in production |

Backup = copy the SQLite file (or the `/data` volume).

### MCP

Create an API token in Settings, then connect any MCP client to `https://your-host/mcp` with header `Authorization: Bearer <token>` (streamable HTTP transport). Tools: `start_timer`, `stop_timer`, `stop_all_timers`, `list_running_timers`, `add_time_entry`, `list_projects`, `create_project`, `add_time_off`, `list_time_off`, `time_report`.

```sh
claude mcp add --transport http worktime https://your-host/mcp --header "Authorization: Bearer <token>"
```

## Development

```sh
make dev-api   # run Go backend on :8080 (use WORKTIME_DEV_AUTH=1 to skip Google sign-in)
make dev-web   # run Vite dev server (proxies /api to :8080)
make build     # build web + single binary
make test      # run tests
```

## License

MIT
