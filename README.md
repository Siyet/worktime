# WorkTime

Free, open-source, self-hosted time tracker. A lightweight alternative to solidtime / Clockify.

## Why

- **Single binary** - Go backend with the web UI embedded. Download, run, open `localhost:8080`. SQLite storage, no external services.
- **Tiny footprint** - target: < 50MB RAM on the server, < 50KB gzip frontend bundle.
- **Local-first PWA** - the app works offline: timers keep running without a network connection and sync when it returns. Install it on your phone from the browser, no native app needed.
- **Multiple concurrent timers** - track parallel work honestly.
- **Time off** - sick days and vacations, tracked as first-class data without blocking time tracking.
- **MCP server** - let AI agents start/stop timers and query reports via Model Context Protocol.
- **Multi-user** - Google sign-in, personal data isolation. No billing, no clients, no seat pricing. Free forever.

## Status

Early development. Not ready for use yet.

## Development

```sh
make dev-api   # run Go backend on :8080
make dev-web   # run Vite dev server (proxies /api to :8080)
make build     # build web + single binary
make test      # run tests
```

## License

MIT
