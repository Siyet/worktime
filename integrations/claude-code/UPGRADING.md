# Upgrading the Claude Code integration

## The short way

**Settings -> Connect an agent** downloads a prompt that does all of this, for
Claude Code or for Codex, and then proves the result by sending a signal through
the hook and reading it back. The rest of this file is what that prompt does.

## One queue per instance, and a name for the client

The hook now spools into `~/.worktime/queue/<instance>` instead of one flat
directory. A request spooled for one instance could otherwise be replayed with
another instance's token - and the 401 that answers it counts as a permanent
rejection, so the event was dropped as well. Requests left by the old flat layout
are adopted by whichever instance they were addressed to on the next flush;
nothing is lost and there is nothing to do by hand.

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

4. **Tell the agent to name its work.** Entries are called
   `Claude Code #ab12cd34` until the session is attached to a tracker task with
   the `set_agent_task` MCP tool. Adding a line to the project's `CLAUDE.md`
   ("call set_agent_task with the task number as soon as you know it") is what
   makes the tracked time group by task instead of by session.

No server-side action is needed: migrations run at startup, and existing rows
keep working (`paused_ms` starts at zero, so nothing changes retroactively).
