# Releases and update trust

WorkTime release publication is a manual, fail-closed operation. It is not a
general-purpose build from an arbitrary ref: an official release must be built
from the exact revision selected by a `workflow_dispatch` run on
`refs/heads/main` in `Siyet/worktime`.

Runtime update mutations have no implicit owner. Only browser sessions whose
lowercased email is listed explicitly in `WORKTIME_ADMIN_EMAILS` may change the
instance policy or request installation; API tokens always receive `403`. Setting
`WORKTIME_UPDATE_CHECKS=0` disables both GitHub and Sigstore TUF egress while
preserving the last verified status stored on disk.

## Build identity

`internal/buildinfo` has four linker-injected values:

- `Version`: a stable release version such as `v1.2.3`;
- `Revision`: the full Git revision selected by the release run;
- `BuiltAt`: the UTC RFC 3339 build time.
- `Packaging`: `native`, `docker`, or the fail-closed `dev` default.

`make build` accepts the corresponding `VERSION`, `REVISION`, and `BUILT_AT`
variables and passes them through `-ldflags -X`. An unqualified local build
reports version and packaging mode `dev`; it must never masquerade as a published
or self-replacing version. Native release jobs also pass `PACKAGING=native`. Run
`worktime --version` to inspect the linked values without opening the database or
starting the HTTP server.

The application build version is intentionally independent of
`internal/mcpserver.serverVersion`. Changing an application release does not
claim that the MCP implementation speaks a new protocol version.

## Signed release manifest

The update service consumes only these two fixed assets from the latest GitHub
release:

```text
https://github.com/Siyet/worktime/releases/latest/download/release-manifest.json
https://github.com/Siyet/worktime/releases/latest/download/release-manifest.sigstore.json
```

GitHub REST release metadata is discovery information, not an authority for an
update. The JSON manifest has schema version 1 and the following closed shape:

```json
{
  "schema_version": 1,
  "generation": 42,
  "version": "v1.2.3",
  "revision": "0123456789abcdef0123456789abcdef01234567",
  "published_at": "2026-08-30T12:00:00Z",
  "changelog_url": "https://github.com/Siyet/worktime/releases/tag/v1.2.3",
  "image": {
    "name": "ghcr.io/siyet/worktime",
    "digest": "sha256:..."
  },
  "assets": [
    {
      "os": "linux",
      "arch": "amd64",
      "name": "worktime-linux-amd64",
      "url": "https://github.com/Siyet/worktime/releases/download/v1.2.3/worktime-linux-amd64",
      "sha256": "...",
      "size": 12345678
    }
  ]
}
```

`generation` is a monotonically increasing integer from the release workflow,
used to reject signed rollback to older metadata. Every native artifact records
its operating system, Go architecture, exact download URL, SHA-256 digest and byte
size. The multi-platform GHCR image is pushed first; its registry digest is then
included in the manifest before the manifest is signed.

The Sigstore bundle must prove all of the following:

- issuer: `https://token.actions.githubusercontent.com`;
- certificate identity:
  `https://github.com/Siyet/worktime/.github/workflows/release.yml@refs/heads/main`;
- a valid certificate-transparency timestamp, observer timestamp and Rekor entry;
- the digest of the exact bounded manifest bytes being parsed.

That certificate identity is the update trust boundary. It proves which workflow
produced the bytes, not that the binary is behaviorally safe. Changing the release
workflow therefore has the same security impact as changing code that executes on
the server.

The runtime caps the manifest at 256 KiB and the Sigstore bundle at 512 KiB before
JSON decoding. It accepts only the two Linux assets in the closed schema, checks
their signed byte sizes and SHA-256 digests, and persists the highest verified
generation/version plus the last checked time and changelog URL, so Settings keeps
the last verified display status across a restart while replay and downgrade remain
blocked. Persisted discovery is display-only: `apply_ready` remains false until the
current server process completes a fresh manifest and Sigstore verification, and
automatic policy cannot bypass that gate. The embedded Sigstore TUF root
rotates into a cache below the database directory; an expired or unverifiable root
fails closed and never turns GitHub REST metadata into update authority.

## Native update transaction

The Linux updater requires a standalone native build on amd64 or arm64, a
writable executable directory, and filesystem support for
renameat2(RENAME_EXCHANGE). Unsupported/error results are notification-only;
there is no non-atomic replacement fallback.

Before replacement it closes the shared lifecycle gate, drains admitted HTTP,
MCP and background Store users, creates a WAL-consistent SQLite backup through
modernc's backup API, and runs the fully verified staged binary against a private
disposable restore. The preflight gets a strict environment without OAuth
credentials, tokens or proxy variables and has update egress disabled.

The durable states are:

    verified -> draining -> backup_complete -> preflight_complete -> swap_intent
             -> swapped -> bootstrapping -> committed
                                  \-> rollback_started -> rolled_back

Every state and executable exchange is fsynced. The process exchanges the two
binaries atomically and uses same-PID exec, so systemd observes a process
replacement rather than a detached helper. The new binary keeps admission closed,
opens and migrates the real database, constructs the runtime, binds the configured
listener under closed admission, and passes database readiness before recording
committed and serving traffic. A bind or readiness failure still has rollback
authority. A crash in bootstrapping exchanges the old binary back and restores the
immutable backup with SQLite's low-level restore API. Recovery validates actual
old/new hashes and refuses ambiguous file layouts instead of guessing. A committed
transaction is never automatically restored.

While Settings is open, the client compares the server against the version embedded
in the JavaScript build, not a freshly fetched baseline that could race an automatic
restart. It polls uncached health, version, and update status through handoff. A
reported failure or two healthy settled polls on an unchanged version end that
attempt and surface the failure instead of spinning indefinitely. Monitoring then
continues for a fresh retry or later automatic update. A new version refreshes the
service worker before reload.

## Publication invariants

Before creating a draft, the release job verifies the repository, main-branch ref,
exact checked-out revision, previously unused version/tag, and a strict increase in
both signed manifest generation and stable SemVer. Failure to prove an enforceable
prerequisite stops the run; there is no personal access token or GitHub App fallback.

All third-party Actions are pinned by full revision. The job receives only
`contents: write`, `packages: write`, and `id-token: write`. It creates one draft,
uploads and downloads every asset, compares the bytes, verifies the Sigstore
identity, and smoke-tests both Linux binaries before publishing. After publication
it verifies the immutable release and every asset. Cleanup stays armed until those
late checks finish. On failure it may delete the exact draft or public mutable
release created by that run only when the release ID, version tag, target revision,
and source revision match the run and the API explicitly reports
`immutable === false`. After deleting that owned release, it deletes the tag only
while the tag ref still points at that exact source revision.
A pre-existing release, an immutable release, or a response with missing/unknown
immutability is never touched.

Immutable releases and branch protection are manual repository prerequisites. The
ordinary workflow `GITHUB_TOKEN` has no Administration read permission and therefore
cannot prove those settings itself. Before approving the protected `release`
environment, its human approver must verify that immutable releases remain enabled
and that the dispatch targets protected `main`. The workflow still verifies the
published release and assets afterward; no long-lived PAT or GitHub App secret is
added to turn the manual administration check into hidden automation.

## Platform scope

The MVP self-apply path is native Linux on `amd64` and `arm64`. It relies on a
same-directory atomic binary exchange and same-process handoff. Filesystems that do
not support the required atomic exchange are notification-only.

Docker deployments are notification-only because the container image must be
replaced by its orchestrator. macOS and Windows are also notification-only because
their signing, replacement, and service lifecycle contracts are not part of this
MVP. Self-apply support for those targets requires explicit product acceptance and
separate follow-up issues; it must not be inferred from the presence of a newer
release.
