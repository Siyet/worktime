# Agent time tracking (Claude Code)

Automatic, crash-resilient tracking of Claude Code working time. Instead of
trusting a stop event that may never arrive (Ctrl+C, `kill -9`, OOM, SSH drop,
plan limit mid-task), the design uses **heartbeats plus server-side
reconciliation**: a session is always closed at the last known moment of real
activity, never at the moment the system noticed it was gone.

## How it works

Three independent layers; each one is a fallback for the previous:

1. **Hooks -> REST.** Claude Code hooks send `start`, `heartbeat` and `stop`
   signals to worktime, keyed by Claude Code's `session_id`. All three
   endpoints are idempotent, so retries and replays are always safe.
2. **Server-side reconciliation.** A job inside the worktime binary runs every
   minute and closes any session whose last heartbeat is older than the grace
   period (`WORKTIME_AGENT_GRACE`, default `10m`), setting the end to the last
   heartbeat. This is the only layer that survives `SIGKILL`/OOM/network loss.
3. **Offline queue in the hook script.** If worktime is unreachable, the hook
   spools the request (with its original timestamp) to `~/.worktime/queue` and
   flushes the queue on the next event. A tracking failure never blocks the
   agent: the script always exits 0 and curl has a hard 3s timeout.

A session owns one *running* time entry at a time, so the PWA shows agent work
as a live timer. Heartbeats closer together than the idle threshold
(`WORKTIME_AGENT_IDLE`, default `10m`) count as continuous work; a larger gap
stops the current entry at the previous heartbeat and opens a new one, so idle
time in the middle of a session is never billed. Trailing idle is trimmed the
same way on stop and by reconciliation. Auto-compaction sends a heartbeat
(`PreCompact`), not a stop, so long sessions keep accumulating time under the
same `session_id`.

Entries land in `time_entries` through the same `server_seq` allocation as
`/api/sync` writes, so they reach every client over the normal pull path.

## Setup

1. Create an API token in WorkTime (Settings -> API tokens).
2. Copy `integrations/claude-code/wt-hook.sh` to `~/.worktime/wt-hook.sh` and
   make it executable (`chmod +x`). On Windows the script runs under Git Bash,
   which Claude Code uses for hooks.
3. Export the environment variables where Claude Code runs (shell profile):

   ```sh
   export WORKTIME_URL="https://wt.example.com"
   export WORKTIME_TOKEN="wt_..."   # never commit this
   ```

4. Merge `integrations/claude-code/settings.json.example` into
   `~/.claude/settings.json` (user-wide) or a project's
   `.claude/settings.local.json` (gitignored).

## API

All endpoints sit behind the usual Bearer (`wt_...`) auth. `{id}` is Claude
Code's `session_id` (a UUID). Timestamps are unix milliseconds UTC; a missing
or zero timestamp means "server now".

```
POST /api/agent/sessions/{id}/start
  { "started_at": 1730000000000, "source": "claude-code",
    "cwd": "...", "git_branch": "...", "model": "...", "project_id": "..." }
  -> upsert by id; a replay (--continue / --resume) refreshes metadata and
     counts as activity, it never duplicates sessions or entries

POST /api/agent/sessions/{id}/heartbeat
  { "at": 1730000600000, "activity": "prompt|tool|turn_end|compact" }
  -> advances the watermark (monotonic, out-of-order heartbeats cannot rewind
     it); an unknown id is created implicitly, a closed session is reopened
     with a new time entry

POST /api/agent/sessions/{id}/stop
  { "ended_at": 1730003600000, "reason": "clear|logout|prompt_input_exit|other" }
  -> closes the session and its running entry; no-op if already closed (404 if
     the session never existed)
```

The response is always the session state, including `time_entry_id` of the
current segment.

## Server configuration

| Env var | Default | Meaning |
|---|---|---|
| `WORKTIME_AGENT_GRACE` | `10m` | Heartbeat silence after which reconciliation closes the session at the last heartbeat (`end_reason = stale_heartbeat`). |
| `WORKTIME_AGENT_IDLE` | `10m` | Largest heartbeat gap still billed as continuous work; a larger gap splits the session into a new time entry. |

Both accept Go duration syntax (`90s`, `15m`). Grace should be a small multiple
of the heartbeat cadence; heartbeats fire on every prompt, tool use and turn
end, so during real work they are seconds apart.

## Behaviour in failure scenarios

- **Clean `/exit` or terminal close**: `SessionEnd` fires `stop`, the entry
  ends at the stop moment (or at the last heartbeat if the stop arrived after
  more than the idle threshold of silence).
- **`kill -9`, OOM, machine sleep, SSH drop**: no stop ever arrives;
  reconciliation closes the session within the grace period with
  `ended_at = last_heartbeat_at`.
- **Plan/usage limit mid-task**: same as above - whatever hook fires or not,
  the timer cannot stay open longer than the grace period after the last real
  activity.
- **worktime down or unreachable**: events queue on disk with their original
  timestamps and replay later; the server rebuilds the same timeline.
- **User edits in the PWA**: if the user manually stops or deletes the agent's
  running entry, the agent flow leaves that entry alone (the user's edit wins).

## Not implemented yet

- statusLine integration showing the running timer inside Claude Code.
- Cross-checking session counts against Claude Code OpenTelemetry metrics.
- Storing per-heartbeat `activity` kinds (accepted by the API, not persisted).
