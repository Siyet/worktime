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

A session owns exactly one time entry, so the PWA shows agent work as a live
timer. Signals closer together than the idle threshold (`WORKTIME_AGENT_IDLE`,
default `10m`) count as continuous work; a larger gap is added to the entry's
`paused_ms` and subtracted from its duration, so idle time in the middle of a
session is never billed and the session still stays one row. `started_at` and
`stopped_at` remain real timestamps - a nine-to-six entry with an hour of
pauses shows "09:00-18:00" next to "8h 00m", and the entry editor spells the
difference out. Trailing idle is trimmed on stop and by reconciliation.
Auto-compaction sends a heartbeat (`PreCompact`), not a stop, so long sessions
keep accumulating time under the same `session_id`.

Two things still cut the entry in two, both about days rather than idling:

- a pause crossing the agent's **local midnight** (the hook sends its UTC
  offset with `start`). Reports place an entry on the day it started, so gluing
  the two would move this morning's work into yesterday;
- a pause longer than `WORKTIME_AGENT_MAX_PAUSE` (default `4h`), which is the
  same guard for a session whose timezone is unknown - an old hook that does
  not send `tz_offset_min` yet.

**Long tool calls.** `PreToolUse` sends a heartbeat with `activity=tool_start`.
Without it a twenty minute `Bash` or `Task` is indistinguishable from an empty
chair: `PostToolUse` and `SubagentStop` only fire once the gap has already
happened. After a `tool_start` the first `WORKTIME_AGENT_TOOL_MAX` (default
`30m`) of the gap are billed and the rest becomes a pause - a ceiling, not a
switch, so a hung tool cannot bill forever and a tool one minute over the limit
does not lose everything.

Entries land in `time_entries` through the same `server_seq` allocation as
`/api/sync` writes, so they reach every client over the normal pull path.

## Naming an entry: the task, not the directory

An agent entry is named after the tracker task it belongs to. Until the task is
known it carries a short session tag - `Claude Code #ab12cd34`, the first eight
hex characters of the session id - which is also what keeps two concurrent
Claude Code sessions visibly distinct rows. Nothing is guessed from the branch,
the prompt or an environment variable: guessing is exactly what used to produce
"Claude Code" and "Claude Code (main)" for the same work.

The agent sets the task itself, through the MCP tool:

```
set_agent_task(task_key: "MT-12345", task_title: "Slow AMaaS quote creation",
               session_id?: "...", cwd?: "...")
  -> { session_id, task_key, task_title, renamed_entries }
```

- **Call it as soon as the task number is known.** Look the title up in the
  tracker with whatever MCP connection you already have (Notion, GitLab,
  GitHub): WorkTime never goes to a tracker itself and stores no tracker
  tokens. Without the title the entry is named just `MT-12345`.
- It renames **every** entry of the session, not only the current one - a
  session cut by the local midnight would otherwise leave half its work under
  the technical name.
- Entries the user renamed by hand are left alone.
- The session is picked by explicit `session_id`, else the only active session,
  else the only active session with a matching `cwd`. Anything ambiguous is an
  error listing the candidates: attaching the wrong session silently is worse
  than asking.
- Calling it again with the same key changes nothing; with a different key it
  renames (a task can be corrected).

`list_running_timers` shows `session_tag` and `task_key` for agent rows, so an
agent can tell whether the call is still needed.

Two sessions attached to the same task stay two rows - different sessions are
different data - but the Timer page groups them into one line with a `×2` badge
that unfolds back into the individual sessions.

## Ownership of the entry

The server remembers the `server_seq` it last wrote to the entry. When the row
comes back with a different one, it was changed outside the agent flow, and the
session decides by the row's state:

| Row state | What happens |
|---|---|
| Deleted | The session lets it go and opens a new entry; the tombstone is never resurrected. |
| Stopped by the user | The session lets it go and opens a new entry - otherwise the rest of the session would be tracked nowhere. |
| Alive, but edited (name, project, tags, boundaries) | The session adopts it and keeps writing. If the description is no longer the automatic one, it is treated as chosen by the user and never rewritten. |

This is why stopping an agent's row by hand - including `stop_all_timers` while
an agent is working - splits its work in two rows rather than losing it.

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
   `.claude/settings.local.json` (gitignored). **Merging it again after an
   upgrade is not optional**: the `SessionStart` matcher and the `PreToolUse`
   hook live in your settings file, and an old copy silently keeps the old
   behaviour.

Set `WORKTIME_HOOK_LOG=~/wt-hook.log` to record every event the hook sees; it
is the fastest way to tell whether a hook fires at all.

Upgrading an existing setup: see `integrations/claude-code/UPGRADING.md`.

## API

All endpoints sit behind the usual Bearer (`wt_...`) auth. `{id}` is Claude
Code's `session_id` (a UUID). Timestamps are unix milliseconds UTC; a missing
or zero timestamp means "server now".

```
POST /api/agent/sessions/{id}/start
  { "started_at": 1730000000000, "source": "claude-code",
    "cwd": "...", "git_branch": "...", "model": "...", "project_id": "...",
    "tz_offset_min": 180 }
  -> upsert by id; a replay (--continue / --resume) refreshes metadata and
     counts as activity, it never duplicates sessions or entries

POST /api/agent/sessions/{id}/heartbeat
  { "at": 1730000600000, "activity": "tool_start|prompt|turn_end|compact",
    "cwd": "...", "git_branch": "...", "model": "...", "tz_offset_min": 180 }
  -> advances the watermark (monotonic, out-of-order heartbeats cannot rewind
     it); an unknown id is created implicitly, a closed session is revived and
     continues its own entry. Metadata is optional and only fills values a lost
     start never delivered.

POST /api/agent/sessions/{id}/stop
  { "ended_at": 1730003600000, "reason": "clear|logout|prompt_input_exit|other" }
  -> closes the session and its running entry; no-op if already closed (404 if
     the session never existed)
```

A signal older than the last billed moment (a spooled heartbeat delivered after
the stop) only refreshes metadata: it can neither revive the session nor open a
second entry.

The response is always the session state, including `time_entry_id`, `task_key`
and `tz_offset_min`.

## Server configuration

| Env var | Default | Meaning |
|---|---|---|
| `WORKTIME_AGENT_GRACE` | `10m` | Heartbeat silence after which reconciliation closes the session at the last heartbeat (`end_reason = stale_heartbeat`). |
| `WORKTIME_AGENT_IDLE` | `10m` | Largest gap between signals still billed as continuous work; a larger gap becomes `paused_ms` and is not billed. |
| `WORKTIME_AGENT_TOOL_MAX` | `30m` | How much of a gap that began with `activity=tool_start` is still billed. |
| `WORKTIME_AGENT_MAX_PAUSE` | `4h` | Pause after which the entry is cut in two even when the timezone is unknown. |
| `WORKTIME_AGENT_RECONCILE` | `1m` | How often the reconciliation job runs. |

All accept Go duration syntax (`90s`, `15m`). Grace should be a small multiple
of the heartbeat cadence; heartbeats fire on every prompt, tool use and turn
end, so during real work they are seconds apart.

## Behaviour in failure scenarios

- **Clean `/exit` or terminal close**: `SessionEnd` fires `stop`, the entry
  ends at the stop moment (or at the last heartbeat if the stop arrived after
  more than the idle threshold of silence - except that silence which began with
  a `tool_start` is billed up to `WORKTIME_AGENT_TOOL_MAX`, because a running
  tool is the one gap known to be work). `reason=resume` sends no stop: that
  session is being handed over to a resumed one with the same id.
- **`kill -9`, OOM, machine sleep, SSH drop**: no stop ever arrives;
  reconciliation closes the session within the grace period with
  `ended_at = last_heartbeat_at`. A trailing tool run is *not* extended here,
  unlike on the stop path: a stop carries the moment work ended, reconciliation
  only knows when the job happened to run, so billing from there would invent
  time. An interrupted tool run is therefore under-counted rather than over-counted.
- **Plan/usage limit mid-task**: same as above - whatever hook fires or not,
  the timer cannot stay open longer than the grace period after the last real
  activity.
- **worktime down or unreachable**: events queue on disk with their original
  timestamps and replay later; the server rebuilds the same timeline. The queue
  is flushed under a directory lock, so parallel hook processes never send the
  same spooled request twice.
- **User edits in the PWA**: see "Ownership of the entry" above.
- **`/clear`, compaction, fork**: Claude Code issues a new `session_id`, which
  is a new session and a new entry by design. They only converge in the UI,
  once both are attached to the same task.

## Subagents

Subagent transcripts carry the **parent's** `session_id` (plus `isSidechain`
and their own `agent_id`), so a subagent can never create a second session or a
duplicate entry - session id is the idempotency key of all three endpoints. The
problem subagents do cause is the opposite one: while the parent waits for a
long `Task`, no hook fires, which is what `tool_start` exists for.

If a future Claude Code release starts giving subagents their own `session_id`,
each of them will show up as its own session and its own row. Attach them to
the same task with `set_agent_task` and the Timer page will group them; the
wall-clock figure next to the sum is there precisely because parallel agents
make the two differ.

## Not implemented yet

- statusLine integration showing the running timer inside Claude Code.
- Cross-checking session counts against Claude Code OpenTelemetry metrics.
- `SubagentStart`/`SubagentStop` hooks (not confirmed to exist in the current
  release; `PreToolUse` covers the same gap).
