# Upgrading the Claude Code integration

## The short way

**Settings -> Connect an agent** downloads a prompt that does all of this, for
Claude Code or for Codex, and then proves the result by sending a signal through
the hook and reading it back. The rest of this file is what that prompt does.

## Collision-safe private queue, and a name for the client

The hook now binds every spool directory to the exact `WORKTIME_URL`. The
previous `<instance>` name replaced punctuation with `_`; distinct URLs such as
`http://host:8080` and `http://host/8080` therefore collided. A request for one
instance could be replayed with another instance's token - and the 401 response
then deleted that foreign event. The new v2 path uses a validated SHA-256 (or a
lossless, bounded-component hex fallback) and stores the exact origin in a
private `.origin` file. A mismatched binding fails closed: no request in that
directory is sent or deleted.

Requests left by both older layouts - the original flat root and the lossy
per-instance directory - are inspected and adopted only when their request URL
matches the exact current origin. Mixed collided directories are split over
time without overwriting or reordering either instance's records; no manual
migration is required. Existing events already deleted by an old wrong-origin
401 cannot be reconstructed.

The hook applies `umask 077` before its first write because queued start events
may contain a working directory and Git branch. New queue directories are mode
700 and queue, binding and optional log files are mode 600 on POSIX systems.
The FIFO accepts new records only while fewer than 1000 are retained per exact
origin; a larger backlog adopted from both legacy layouts is preserved rather
than truncated. Accepted records replay in order; events beyond the cap, local
write failures, transient retry and permanent HTTP rejection are visible in
`WORKTIME_HOOK_LOG`. The hook still always exits 0 so tracking cannot block the
agent.

`WORKTIME_AGENT_SOURCE` names the client in the `start` signal (default
`claude-code`). Codex fires the same events with the same payload, so the same
script serves it - set the variable to `codex` there and its entries read
`Codex #ab12cd34` instead of being filed under Claude Code's name.

Copy the script again to pick both up: `integrations/claude-code/wt-hook.sh` ->
`~/.worktime/wt-hook.sh`.

The hook script and the hook wiring live outside this repository - in
`~/.worktime/wt-hook.sh` and `~/.claude/settings.json` - so a server upgrade
never updates them. After pulling a WorkTime release that changes agent
tracking, redo both steps or the old behaviour silently stays.

## Running timer in the Claude Code status line

Copy `integrations/claude-code/wt-statusline.sh` to
`~/.worktime/wt-statusline.sh`, make it executable, and merge the `statusLine`
object from `settings.json.example`. The script shares `WORKTIME_URL` and
`WORKTIME_TOKEN` with the hook, but only reads the current session's timer; its
five-second refresh never advances a heartbeat. If `settings.json` already has
another status-line command, leave it in place: Claude Code supports one, and a
WorkTime upgrade must not replace the user's terminal UI.

## One entry per session, named after the task

1. **Copy the script again**: `integrations/claude-code/wt-hook.sh` ->
   `~/.worktime/wt-hook.sh` (keep it executable). New in this release: a
   `tool_start` event, the local UTC offset on `start`, a locked offline queue,
   no stop on `SessionEnd.reason=resume`, and top-level JSON parsing that no
   longer picks `tool_input.cwd` instead of `cwd`.

2. **Merge `settings.json.example` into `~/.claude/settings.json`** (or the
   project's `.claude/settings.local.json`). Two entries matter:

   - `SessionStart` matcher is now `startup|resume|clear|compact|fork`. With the
     old `startup|resume` a session created by `/clear` never sends `start`, so
     the server never learns its working directory or timezone.
   - `PreToolUse` -> `wt-hook.sh tool_start` is **new and load-bearing**: it is
     the only signal sent *before* a tool runs, so without it a twenty minute
     `Bash` or `Task` is subtracted from the entry as idle time.

3. **Check the MCP server is actually connected**: `claude mcp list` must show
   `worktime` as connected, and `claude mcp add --scope user --transport http
   worktime "$WORKTIME_URL/mcp" --header 'Authorization: Bearer
   ${WORKTIME_TOKEN}'` adds it if it is missing. Hooks and MCP are independent:
   time can be tracked perfectly while every `set_agent_task` call fails, and
   the only symptom is that entries keep their session tag.

4. **Upgrade the managed agent instruction.** Entries are called
   `Claude Code #ab12cd34` until the top-level/main agent attaches its session
   to a tracker task with `set_agent_task`. The old instruction said every agent
   should call the tool; because child agents inherit `CLAUDE.md`, that lets a
   subagent rename its parent's work.

   The safest upgrade is to download a fresh Claude Code setup prompt from
   Settings and run it again. It keeps the stable `<!-- worktime:begin -->` and
   `<!-- worktime:end -->` markers: replace everything between the existing
   pair with the freshly generated managed block, preserve both markers and all
   text outside them, and never append a second block. The new block says that
   only the top-level/main agent uses WorkTime MCP tools; children, subagents,
   reviewers, Task workers, background and delegated agents call none of them
   and return task or project findings to the parent instead.

   Codex installations use the same managed block in `~/.codex/AGENTS.md` and
   are upgraded by re-running the Codex setup prompt from Settings. Local files
   are not rewritten automatically when the WorkTime server is upgraded.

No manual server-side data change is needed: database migrations run at startup.
