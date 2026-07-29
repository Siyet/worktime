# WorkTime

Free, open-source, self-hosted time tracker. Go backend + SQLite, Svelte 5 PWA frontend, MCP server. Single binary with the web UI embedded.

## Architecture (load-bearing decisions)

- **Local-first**: the browser works only with IndexedDB (database `worktime`); a background engine syncs with the server. A running timer is a `time_entries` row with `stopped_at IS NULL` - elapsed is computed client-side from the timestamp, which is why offline "just works". Multiple concurrent running timers are a feature.
- **Theme**: dark amber is the default (`:root` in `web/src/app.css`); light is the `prefers-color-scheme: light` branch. Time off kinds: `sick` (red), `vacation` (teal), `dayoff` (purple).
- **Reports**: computed fully client-side from IndexedDB (`web/src/lib/report.ts`), so they work offline. `#/reports/print?from=&to=&projects=&print=1` renders the printable Russian report (light sheet via the `<doc-page>` web component in `lib/print/`).
- **Sync protocol**: single `POST /api/sync` does push + pull in one transaction. Pull cursor is `server_seq` (global monotonic counter, server-side). Conflicts resolve by last-write-wins on `updated_at` (client clock). Deletes are soft (`deleted_at` tombstones). IDs are client-generated UUIDv7. All timestamps are unix **milliseconds** UTC.
- **Auth**: Google OIDC -> session cookie; API tokens (`wt_` prefix, sha256-hashed) for MCP/scripts; `WORKTIME_DEV_AUTH=1` auto-signs in a local dev user (dev/e2e only).
- **MCP**: `/mcp`, streamable HTTP, stateless mode; a per-user server instance is built per request from the Bearer token. Tools call the store directly and write through `store.Sync` so changes reach clients via the normal pull path.
- SQLite runs with a **single connection** (`SetMaxOpenConns(1)`) - serializes everything, eliminates SQLITE_BUSY. Benchmarks in `docs/benchmark.md` show this is orders of magnitude above the real load; don't add pooling without re-benchmarking.

## Layout

| Path | What |
|---|---|
| `cmd/worktime` | server entrypoint |
| `cmd/benchdb`, `cmd/solidtime-import` | Phase-0 benchmark, solidtime importer |
| `internal/store` | SQLite: migrations (`user_version`-based), sync, queries, reports |
| `internal/api` | chi router, handlers, auth middleware, Google OIDC |
| `internal/mcpserver` | MCP tools |
| `web/src` | Svelte 5 app: `lib/db.ts` (IndexedDB), `lib/sync.svelte.ts` (engine), `lib/state/` (runes stores), `lib/report.ts` (report math, shared by Reports and print), `lib/components/` (Logo, ProjectSelect, Seg, DailyChart, TrashIcon), `pages/` |
| `web/e2e` | Playwright suite (34+ tests) |
| `design/` | design-system previews, synced with the claude.ai/design "WorkTime" project via DesignSync |
| `data/` | gitignored: local DBs and personal exports |

## Commands

```sh
make dev-api    # go run, :8080 (set WORKTIME_DEV_AUTH=1 to skip Google sign-in)
make dev-web    # vite dev server, proxies /api /auth /mcp to :8080
make build      # npm build + go build -> bin/worktime (frontend embedded via go:embed)
make test       # go test ./...
make e2e        # builds the binary, then npx playwright test (e2e runs against bin/worktime!)
```

Always run before finishing: `go vet ./...`, `cd web && npm run check`, `go test ./...`, and the e2e suite if frontend/sync/API changed. **e2e tests run against the embedded build** - rebuild `bin/worktime` (`make build`) after frontend changes or Playwright will test stale UI. On Windows the e2e fixture launches `bin/worktime.exe` - the Makefile picks the right name via `$(OS)`; a stray extension-less `bin/worktime` next to an old `.exe` means the suite silently tests the old build.

## Conventions

- **Svelte 5 runes only**: `$state`, `$derived`, `$effect`, `onclick={...}`. Never Svelte 4 syntax (`$:` reactive statements, stores-in-components, `on:click`).
- Reactive module files use the `.svelte.ts` suffix - required for runes outside components.
- Go: table names/queries in `internal/store` only; handlers never touch SQL. DTOs in `store` are the API schema (json tags) - changing them changes the wire format for existing offline clients, so treat as a migration.
- Migrations: append to the `migrations` slice in `internal/store/store.go`; never edit an existing entry (versioned by `PRAGMA user_version`).
- Comments in English. TypeScript strict; no single-letter identifiers.
- Commits: plain English, one sentence, no prefixes, no Co-Authored-By.

## Testing gotchas (hard-won, do not rediscover)

- **Never wait for the header text "synced" after a local mutation** - the pending counter refresh is fire-and-forget, so the header can read "synced" a few ms before the push actually happened. Use `pushBarrier(page, bodySubstring)` from `web/e2e/fixtures.ts` registered *before* the mutation.
- Each e2e test gets its own server + fresh DB from the fixtures; two "devices" = two browser contexts.
- Tests that intercept `/api/**` with `context.route` must create the context with `serviceWorkers: "block"`.
- Playwright's offline emulation may not propagate `navigator.onLine=false` to a page served by the service worker - assert `/offline|sync error/` in that scenario.
- IndexedDB writes in bulk must go through one transaction (`mergeServerRows` in `lib/db.ts`) - per-row transactions took tens of seconds on a 10k-row bootstrap.

## Design workflow

`design/` holds standalone HTML previews of the design system (tokens in `design/_shared.css` mirror `web/src/app.css` - keep them in sync). The bundle is synced with the claude.ai/design project "WorkTime" via the DesignSync tool: design iterations happen there, then get pulled back and implemented in Svelte. Push local changes back after implementation diverges.
