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
   flushes the queue on the next event. Accepted queue records retain FIFO order,
   but new storage is deliberately capped at 1000 retained records per WorkTime
   origin: a further event is dropped and reported in `WORKTIME_HOOK_LOG` rather
   than filling the machine's disk. A larger backlog adopted from both old queue
   layouts is preserved, not truncated. A tracking failure never blocks the
   agent: the script always exits 0 and curl has a hard 3s timeout.

**The entry waits for activity.** A start says a process exists, which is not
the same as work: the agent binary is launched far more often than it is worked
in, and on the first machine to measure it 222 of 249 sessions in a week
reported no activity at all. So the entry is opened by the first heartbeat. If
that signal arrives within the idle threshold, the entry is back-dated to the
session start; after a longer gap it opens at the signal itself. A session that
never reports activity keeps its row in
`agent_sessions`, so the evidence of the launch survives, and produces no time
entry.

Nothing is billed before a late first signal. Writing a piece before the pause
would produce a start moment closed at that same start moment: a zero-length row,
which is the artefact deferred materialization exists to prevent.

The rows the old behaviour had already written are removed by migration 008, on
the deploy that brings it. The delete is soft, like every other delete here, so
the tombstones reach clients that had already pulled the rows. It takes an entry
only where the session never reported activity, the agent flow still owns the row
(`server_seq` is the one the session recorded) and the description is still that
session's own tag - so anything renamed, named after a tracker task or edited in
the PWA stays. The `agent_sessions` rows are kept either way: the launch did
happen, and a deleted session could no longer tell "was empty" from "went
missing".

**Short technical sessions are noise, not work.** A short-lived subagent can
emit one heartbeat and then stop before `set_agent_task` has a chance to name
it. That is real activity as far as the lifecycle is concerned, so the first
heartbeat correctly opens a row, but a duration below 30 seconds rounds to
`0m` in the feed. Idle splitting, stop and reconciliation soft-delete only
that exact class:
the task is still unset, the automatic description is unchanged, the agent
still owns the row, and its billable duration is below 30 seconds. The
tombstone gets its own `server_seq`, so every device removes a row it may have
already pulled. The first duration that renders as `1m` (30 seconds) stays.
Any outside edit is remembered permanently for the current entry, including a
project- or tags-only edit, so deliberately touched short entries are never
cleaned up; task-named and manually renamed entries are protected as well.
Migration 010 conservatively marks every entry already materialized by a v9
agent session as edited, because v9 did not retain enough history to distinguish
an untouched row from a project, tags or bounds edit that the agent had already
adopted. A few old technical `0m` rows may therefore remain; preserving possibly
edited time data takes priority over cleaning historical noise. New v10 sessions
start unmarked and follow the precise cleanup rule above.

A session owns one running entry at a time and may produce several finished
entries. Signals no farther apart than the idle threshold
(`WORKTIME_AGENT_IDLE`, default `10m`) count as continuous work. A larger gap
closes the current entry at the last billable activity and opens a new one at
the next signal. Every pause is therefore a row boundary: a session can stay
alive under the same `session_id`, while its time appears as separate continuous
segments in the PWA and reports. Trailing idle is trimmed on stop and by
reconciliation. Auto-compaction sends a heartbeat (`PreCompact`), not a stop,
so it does not create a false pause.

A project edit accepted through normal sync also updates the session's current
project in the same transaction. That includes clearing the project. A stale LWW
loser, an older segment, and a row owned by another user cannot change it, so the
next segment after an idle gap inherits exactly the last accepted project of the
current entry. The same accepted-write path durably marks any project, tags,
bounds, or description edit as user-owned before the next heartbeat. This matters
when stop or reconciliation closes the row immediately: a later resume must not
forget the edit and discard the same sub-minute row on its second stop. A manual
description also sets the dedicated user-named guard - anything but the session's
current automatic name or the session tag its rows carry before a task is set -
while lifecycle-internal writes never pass through this path.

Migration 009 removes the old `paused_ms` column. Historical rows store only the
total paused duration, not individual pause boundaries, so the migration
preserves their duration by compressing the interval: it moves the stop backward
for finished rows and the start forward for running rows. Every changed row gets
a fresh sync cursor so existing browsers receive the compacted boundary.

Browsers can be offline while that server migration runs. IndexedDB schema
version 2 therefore applies the same clamped boundary compaction atomically when
a v1 database is first opened, then removes the legacy property from each row.
Dirty markers, tombstones, IDs, and sync metadata remain unchanged, and the
schema version prevents a later reopen from compacting an interval twice.

**Long tool calls.** `PreToolUse` sends a heartbeat with `activity=tool_start`.
Without it a twenty minute `Bash` or `Task` is indistinguishable from an empty
chair: `PostToolUse` and `SubagentStop` only fire once the gap has already
happened. After a `tool_start` the first `WORKTIME_AGENT_TOOL_MAX` (default
`30m`) of the gap are billed and any remainder becomes a pause between two
entries - a ceiling, not a switch, so a hung tool cannot bill forever and a tool
one minute over the limit does not lose everything.

Entries land in `time_entries` through the same `server_seq` allocation as
`/api/sync` writes, so they reach every client over the normal pull path.

## Naming an entry: the task, not the directory

An agent entry is named after the tracker task it belongs to. Until the task is
known its stored description carries a short session tag - `Claude Code
#ab12cd34`, the first eight hex characters of the session id - which keeps two
concurrent sessions as distinct rows. The Timer feed hides that technical
suffix and exposes the full identifier in the entry editor. Nothing is guessed from the branch,
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
- It renames **every** entry of the session, not only the current one - every
  segment belongs to the same task and must carry the same description.
- Entries the user renamed by hand are left alone.
- An explicit `session_id` is authoritative. Otherwise a supplied `cwd` is a
  constraint, not a fallback hint: after lexical, case-insensitive path
  normalization it must match exactly one active session. Zero matches and
  multiple matches are errors that list the relevant active sessions and ask
  for `session_id`; neither case changes a session or entry. Only when both
  selectors are omitted may the sole active session be selected automatically.
  Path matching understands POSIX paths and Windows drive/UNC paths without
  consulting the server filesystem, which may belong to a different OS.
- Calling it again with the same key changes nothing; with a different key it
  renames (a task can be corrected).

`list_running_timers` shows `session_tag` and `task_key` for agent rows, so an
agent can tell whether the call is still needed.

Two sessions attached to the same task stay two rows - different sessions are
different data - but the Timer page groups them into one line with a count badge
that unfolds back into individual rows repeating the task name. Their technical
identifiers remain available in each row's full editor.

The group's separate edit button opens one dialog for its description, project
and tags; dates, times and session identifiers remain per-entry. Membership is
captured when the dialog opens, and the local write is all-or-none. A Stop or
boundary correction arriving while it is open is preserved, while a concurrent
change to grouping metadata asks the user to reopen instead of overwriting it.
Server delivery can still settle individual rows through LWW or validation, so
the UI reports the exact accepted/rejected/conflicted count and retries rejected
members only. A project accepted for each currently owned agent row reaches that
row's session in the same sync transaction. A genuinely different description
sets the existing exact user-named guard; a no-op keeps normalised spelling
variants byte-for-byte and does not invent user intent.

## Filing the session under a project

The hook sends no project, so an agent session starts under none, and the report
files it as `(no project)`. Either side can fix that: the user edits the entry in
the app, or the agent calls the MCP tool

```
update_time_entry(project: "WorkTime", entry_id?: "...", description?: "...")
  -> { entry_id, description, project, started_at, elapsed, session_tag, task_key }
```

- With no `entry_id` it edits the timer that is running - the agent's own row in
  a normal session. Concurrent timers are a feature here, so two running rows
  make the call an error naming the candidates rather than a guess.
- The project is named, not identified: use `list_projects` or `create_project`
  first. An unknown name is an error, never a new project. An empty string
  detaches the entry.
- When the row is the one an agent session currently owns, the project is
  written to the session too, so the entry it opens after the next pause starts
  on the same project instead of falling back to none.
- It edits a finished entry too, by `entry_id` - `list_running_timers` only
  reports the running ones, so that id has to come from the app or from the
  earlier answer of this tool.
- A `description` set this way counts as chosen by the user, exactly like a
  rename in the app: the session stops fixing that row's name, and
  `set_agent_task` leaves it alone. Changing only the project keeps the
  automatic name and later renames still apply.

## Ownership of the entry

The server remembers the `server_seq` it last wrote to the entry. When the row
comes back with a different one, it was changed outside the agent flow, and the
session decides by the row's state:

| Row state | What happens |
|---|---|
| Deleted | The session lets it go and opens a new entry; the tombstone is never resurrected. |
| Stopped by the user | The session lets it go and opens a new entry - otherwise the rest of the session would be tracked nowhere. |
| Alive, but edited (name, project, tags, boundaries) | The session adopts it and keeps writing. If the description is neither the automatic one nor the session tag a device that missed a task rename may still carry, it is treated as chosen by the user and never rewritten; the old tag is renamed to the task name. |

This is why stopping an agent's row by hand - including `stop_all_timers` while
an agent is working - splits its work in two rows rather than losing it.

**Undo and restarts.** The Timer page's Undo writes a stopped row back with
`stopped_at` cleared - several rows in one push when stops made in a quick
burst are undone together - and an Undo of a delete clears `deleted_at`. A
device that was offline can also push a row it still shows running after the
server closed or split the session, or write a row the server has removed in the
meantime. By then the session may have let the row go, and reconciliation only
walks sessions, so a running row no session points at would run forever. The
same sync transaction therefore settles every row the push brings back after the
server had ended it. This runs once per row, after every pushed row is written,
on the state the whole push left, so the order of rows inside a push never
changes the outcome; several rows of one session are settled in `started_at`
order, each seeing what the earlier ones decided.

The server remembers what it did to a row in three server-only columns
(migration 012; no DTO carries them, so the wire format is unchanged). They are
compared by value, so a rename by anyone keeps them, while a hand-edited
`stopped_at` makes that stop the user's:

- `agent_end` - the `stopped_at` the agent flow wrote when it closed the row: an
  idle split, the stop hook, reconciliation, or one of the closes below.
- `agent_deleted_at` - the `deleted_at` it wrote when it removed the row: a
  zero-minute technical row, or a replacement an Undo made redundant.
- `agent_paused_from` - set on a row the session opened after an idle pause: the
  last billable moment before that pause, which is where an idle split closes
  the row that ran until then.

Nothing recorded who ended a row before migration 012, so the migration counts
every agent row already stopped or deleted as ended by the server and copies
its stop into `agent_end` and its tombstone into `agent_deleted_at`. That errs
on the safe side: a server end taken for the user's would let a stale device
reopen the row and bill the gap the server cut out, while a user's end taken for
the server's costs only an Undo still in flight across the upgrade, which then
keeps the stop. The backfill touches server-only columns, so clients pull
nothing because of it.

**A tombstone the server wrote stays.** Whatever a device pushes for such a row -
a rename, a Stop, the row running again - the tombstone is put back, a row
pushed running also gets its previous stop back, and the pushed name, project
and tags are kept. A replacement the server removed is never billed a second
time next to the row that covers it.

**A row pushed running again** whose stored version was stopped, or deleted by
the user, is settled by who ended it and what the session did after that.
"Later rows" are the session's live rows other than this one that start at or
after it (rows of one session do not overlap); the "current row" is the one the
session points at. The first matching line of the table applies.

| Previous end, and what the session did after it | What happens |
|---|---|
| Stopped, then deleted - by anyone | Both are put back and the pushed name, project and tags kept; the session and every other row are left alone. No Undo produces a running copy of such a row - an Undo reverses one step, and deleting a stopped row leaves it stopped - so the copy comes from a device that saw neither write. |
| The stop is the server's (it equals `agent_end`) | The stop stands. The pushed name, project and tags are kept and the stop is put back; the session and every other row are left alone. A stale device cannot reopen a row across a pause the server cut. |
| The user's; the session is active and still on this row | Nothing; the next signal adopts the edit as usual. |
| The user's; among the rows the session opened after this one - removed ones included - one opened after an idle pause before any live row did | The Undo says the row kept running, and a running row would have been split at that pause. Whether there was a pause is what `agent_paused_from` recorded when the session opened the row, by the session's own rules: a gap a running tool call covered is work, not a pause. So it is closed where the pause began, or at the user's end if that came first, and everything after is left alone. The same rows result whether the heartbeat that ended the pause reached the server before the Undo or after it, and whether the rows after the stop survived or were zero-minute noise the server removed. |
| The user's; no later rows | The session takes the row back. While the session is active the row keeps running, and the session's own idle split, stop and reconciliation close it at its last activity, so idle time is not billed. When the session has ended the row is closed at the later of the user's end and the session's end - never before the user's own stop - and a resume continues it. |
| The user's; the only later row is the current row, nobody edited it and this push did not write it | With no pause before it, that replacement continued the restored row's work and only duplicates its time: it is removed (a server tombstone) and the session takes the row back as above. |
| The same, but the replacement was touched - renamed, moved to a project (the agent's own `update_time_entry` included) or retagged, or written by this very push | The replacement stays as it is and the session stays on it. The restored row is closed where the replacement begins, never before the user's end: no overlap and no hole between them. |
| The user's; anything else - several later rows, a later row the session no longer points at, or a session still running a row that began before this one | What came after is work of its own: the row is closed again at the user's end, keeping the pushed edits. |

When the session takes a row back the row counts as edited by the user, its
project becomes the session's project for later segments, and it counts as
user-named only when its description is neither the session's current
automatic name nor the session tag every row carries until a task is set. A
restored row that arrives with that old tag - an Undo from a device that had not
pulled `set_agent_task`'s rename yet - is renamed to the task name by the
server, so later task renames keep reaching it. The server keeps no history of
task names, though: after two `set_agent_task` calls, a device that pulled only
the first rename writes back a name the session no longer gives, which counts as
the user's, and later task renames skip that row. Every close written here is
recorded in `agent_end`, so replaying or re-pushing the same restore changes
nothing, and every server-side write gets a fresh `server_seq` and an
`updated_at` past the stored one, so other devices pull it and the pushing
device's copy loses to it. The rule never measures gaps itself: every pause it
honours is one the session's idle split decided and recorded, so `Sync` needs
no idle threshold of its own.

**Order matters in one case.** The user stops a row after the agent's last
heartbeat and undoes the stop while the session is silent. If the Undo lands
first, the session is still on the row and takes it back running, and
reconciliation then closes it at the last heartbeat. If reconciliation lands
first, the session has ended, and the Undo closes the row at the later of the
user's stop and the session's end - the user's stop. The two outcomes differ by
the time between the last heartbeat and the user's stop: time the user watched
the row run, but the agent did nothing in. That stretch is bounded by
`WORKTIME_AGENT_GRACE` plus one reconciliation interval, because once
reconciliation has run the row is no longer running for the user to stop.

A Stop from a stale device is not a restore and is not settled: when the device
stops a row the server already closed, its stop replaces the server's by
last-write-wins and bills the idle tail the server had cut. Telling that apart
from a deliberate edit of the end would need the version the device edited, which
the wire format does not carry.

## Setup

The short way: **Settings -> Connect an agent** downloads a setup prompt for
Claude Code or for Codex. Paste it into a fresh agent session and the agent does
everything below itself, then proves the tracking works by sending a signal
through the hook and reading it back. Each download issues its own API token and
writes it into the file, so treat the file as a secret.

By hand:

1. Create an API token in WorkTime (Settings -> API tokens).
2. Copy `integrations/claude-code/wt-hook.sh` and
   `integrations/claude-code/wt-statusline.sh` to `~/.worktime/` and make both
   executable (`chmod +x`). On Windows the scripts run under Git Bash, which
   Claude Code uses for hooks and status-line commands.
3. Export the environment variables where Claude Code runs (shell profile):

   ```sh
   export WORKTIME_URL="https://wt.example.com"
   export WORKTIME_TOKEN="wt_..."   # never commit this
   ```

4. Merge `integrations/claude-code/settings.json.example` into
   `~/.claude/settings.json` (user-wide) or a project's
   `.claude/settings.local.json` (gitignored). **Merging it again after an
   upgrade is not optional**: the `SessionStart` matcher, `PreToolUse` hook and
   `statusLine` command live in your settings file, and an old copy silently
   keeps the old behaviour. Claude Code supports one status line; preserve an
   existing non-WorkTime command rather than overwriting it.
5. Connect the MCP server, or the agent has no `set_agent_task` to call and
   every entry stays under its session tag:

   ```sh
   claude mcp add --scope user --transport http worktime "$WORKTIME_URL/mcp" \
       --header 'Authorization: Bearer ${WORKTIME_TOKEN}'
   ```

   The `${WORKTIME_TOKEN}` is expanded when the server is contacted, so the
   token stays in the environment instead of being copied into
   `~/.claude.json`. `claude mcp list` must show the server as connected: the
   hooks and the MCP server are two independent halves, and tracked time with
   an unreachable `/mcp` looks exactly like a working setup until you notice
   nothing is ever named.

Set `WORKTIME_HOOK_LOG=~/wt-hook.log` to record every event and delivery outcome
the hook sees; it is the fastest way to tell whether a hook fires at all. The
log records only a timestamp, session id, event, state and HTTP status class -
never the token, origin or request body. The hook applies `umask 077` before its
first write, so new log and queue files are owner-only. Queue requests may
contain the working directory and Git branch and should still be treated as
private data.

`WORKTIME_AGENT_SOURCE` names the client in the `start` signal (default
`claude-code`). Codex delivers the same payload shape on the same events, so the
same script serves it - set the variable to `codex` and its untitled entries read
`Codex #ab12cd34` instead of being filed under Claude Code's name.

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
  -> upsert by id; opens no time entry on its own - the first heartbeat does,
     back at this moment when it arrives inside the idle threshold. A replay
     (--continue / --resume) refreshes metadata and counts as activity; after
     a pause it starts a new entry without duplicating the agent session

POST /api/agent/sessions/{id}/heartbeat
  { "at": 1730000600000, "activity": "tool_start|prompt|turn_end|compact",
    "cwd": "...", "git_branch": "...", "model": "...", "tz_offset_min": 180 }
  -> advances the watermark (monotonic, out-of-order heartbeats cannot rewind
     it); an unknown id is created implicitly, and a closed session is revived.
     It continues the current entry inside the idle threshold and starts a new
     one after a pause. Metadata is optional and only fills values a lost start
     never delivered.

POST /api/agent/sessions/{id}/stop
  { "ended_at": 1730003600000, "reason": "clear|logout|prompt_input_exit|other" }
  -> closes the session and its running entry; no-op if already closed (404 if
     the session never existed)

GET /api/agent/sessions/{id}/status-line
  -> one plain-text line with the running entry's billable duration and name;
     204 after the entry stops. Polling is read-only: it never advances a
     heartbeat or changes tracked time.
```

```
GET /api/agent/hook.sh
GET /api/agent/statusline.sh
GET /api/agent/hook-settings.json
  -> the hook script, status-line script and settings fragment this binary was
     built with; same Bearer auth as everything else under /api
```

The three assets are served by the instance rather than linked from GitHub on
purpose: the hook and the endpoints it posts to are one protocol, and a fork or
a server that has not been upgraded yet would otherwise install a hook that
speaks a different version than its own server. `hook-settings.json` is a
fragment to merge into the user's settings, never a file to copy over.

## Status line and accuracy validation

Claude Code passes the current `session_id` to `wt-statusline.sh` on stdin. The
script reads `/status-line` with a one-second timeout, while
`refreshInterval: 5` keeps the displayed `WorkTime H:MM:SS · task` moving
between model turns. A missing token, unknown session or unreachable server
prints nothing and never blocks Claude Code. The read is deliberately not a
heartbeat: otherwise leaving a terminal open would manufacture working time.

OpenTelemetry is an optional independent check, not another source of truth for
WorkTime. Enable Claude Code telemetry and export metrics as described in the
[Claude Code monitoring guide](https://code.claude.com/docs/en/monitoring-usage),
then compare the weekly sum of WorkTime agent-entry billable durations with
the **increase** in the cumulative `claude_code.active_time.total` counter for
the same user and period. Sum its `type=user` and `type=cli` series exactly once;
do not sum duplicated exporter replicas. That duration metric is the useful
accuracy check; `claude_code.session.count` only checks that session starts are
present. Investigate a difference above roughly 5% by session id and the hook
log before changing idle thresholds. WorkTime does not ingest or require an OTel
collector, so tracking continues unchanged when telemetry is disabled.

The 5% figure is an operational acceptance target, not a result established by
the deterministic test suite. A real comparison needs a complete production
week. Record the evidence in an issue or release note with this reproducible
template before declaring that target met:

```text
Period (UTC): <inclusive start> .. <exclusive end>
User / WorkTime instance: <pseudonymous identifier>
WorkTime agent billable seconds: <sum only rows with agent_session_id, clipping every interval to [start,end)>
OTel counter start: <sum type=user + type=cli once, excluding duplicate replicas>
OTel counter end: <same series/aggregation at exclusive end>
claude_code.active_time.total delta seconds: <end - start, with counter resets handled by the backend>
Absolute difference seconds: abs(WorkTime - OTel)
Relative difference: abs(WorkTime - OTel) / OTel * 100
Concurrent sessions: <confirm both sides use additive per-session semantics>
Known exclusions / outages / queue_drop records: <details>
Result: PASS only when OTel > 0, periods match, and difference <= 5%
```

Lifecycle, crash and queue behavior are covered in CI; no synthetic run is
presented as a substitute for that weekly production observation.

A signal older than the last billed moment (a spooled heartbeat delivered after
the stop) only refreshes metadata: it can neither revive the session nor open a
second entry.

Every start, heartbeat and stop response is the session state, including
`time_entry_id`, `task_key` and `tz_offset_min`.

## Server configuration

| Env var | Default | Meaning |
|---|---|---|
| `WORKTIME_AGENT_GRACE` | `10m` | Heartbeat silence after which reconciliation closes the session at the last heartbeat (`end_reason = stale_heartbeat`). |
| `WORKTIME_AGENT_IDLE` | `10m` | Largest gap between signals still billed as continuous work; a larger gap starts a new entry. |
| `WORKTIME_AGENT_TOOL_MAX` | `30m` | How much of a gap that began with `activity=tool_start` is still billed. |
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
  timestamps and replay later; the server rebuilds the same timeline for events
  accepted into the spool. Each exact `WORKTIME_URL` has a cryptographically
  named directory plus an exact `.origin` binding, so another instance's files
  are never sent with this instance's token. The old flat and lossy per-instance
  layouts are adopted by exact request origin without deleting collided foreign
  records. The queue is flushed under a directory lock, so parallel hook
  processes never send the same spooled request twice. At 1000 retained records,
  or when local storage is unwritable, later events cannot be recovered and the
  hook logs `queue_drop` when logging is enabled.
- **User edits in the PWA**: see "Ownership of the entry" above.
- **Compaction and session replacement**: `PreCompact` is a heartbeat. A
  following same-id `SessionStart(compact)` is an idempotent continuation and
  neither closes nor duplicates the row. Only when the client actually supplies
  a different `session_id` (for example some `/clear` or fork flows) is it a new
  session and a new row by design. Rows converge in the UI once attached to the
  same task.

## Acceptance evidence

| Behaviour | Automated evidence | Status |
|---|---|---|
| Idempotent start/heartbeat/stop, monotonic queued signals, revive rules | Store and API agent suites | Verified in CI |
| First-activity materialisation, idle/tool segmentation, one row per pause | Store tests and browser agent-tracking suite | Verified in CI |
| Lost stop (`kill -9` equivalent) closes at the exact watermark | Periodic reconcile browser regression | Verified in CI |
| Server-down orphan closes on the first restart pass, before the long periodic tick | Restart-with-same-DB browser regression | Verified in CI |
| Shipped shell, real curl, minted Bearer and live embedded binary agree on the protocol | Black-box browser fixture | Verified in CI on POSIX runners |
| Queue ordering, concurrency, origin collision/isolation, legacy adoption, cap and POSIX privacy | `make test-hook` | Verified in CI |
| Status line is read-only, one line, one-second timeout and uncached assets match the binary | API/status-line/hook asset suites | Verified in CI |
| One real production week differs from OTel active time by at most 5% | Operational template above | **Pending real-world evidence** |

## Subagents and tool ownership

Only the top-level/main agent for the user's chat owns WorkTime attribution.
A child, subagent, reviewer, Task worker, background agent or delegated agent
must not call `set_agent_task` or any other WorkTime MCP tool. It returns task
and project findings to the parent instead; the parent applies them to the
sessions that belong to the chat. This rule is part of both generated managed
blocks (`~/.claude/CLAUDE.md` and `~/.codex/AGENTS.md`) and the MCP server's
model-facing initialization instructions.

Hook session identifiers are a client protocol detail and can vary between
client versions and execution modes. WorkTime makes only the narrower guarantee
it can enforce: every received `session_id` is an idempotency key. A `start`
with a previously unseen id records a metadata-only session; it creates no Timer
row until that id sends a heartbeat. It is therefore possible to see a
start-only session in diagnostics without seeing a duplicate timer.

The MCP connection is stateless and authenticated by a user-wide Bearer token.
Its request has no authenticated main/subagent identity, so the server cannot
authorize by agent role. `set_agent_task` instead fails closed on the facts it
does know: an explicit `session_id` is authoritative and user-scoped; otherwise
a supplied cwd must match exactly one active session; without either selector
there must be exactly one active session. Independent concurrent top-level
sessions remain supported and must use an exact selector when the implicit
choice is ambiguous. cwd, source, activity and the presence of a Timer row are
not reliable evidence that the MCP caller is the main agent.

While a parent waits for a long `Task`, no ordinary post-tool hook fires. That
accounting gap is what the pre-tool `tool_start` signal covers; it is separate
from who is allowed to name or file the resulting work.

## Not implemented yet

- `SubagentStart`/`SubagentStop` hooks (not confirmed to exist in the current
  release; `PreToolUse` covers the same gap).
