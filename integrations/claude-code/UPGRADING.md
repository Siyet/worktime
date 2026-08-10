# Upgrading the Claude Code integration

The hook script and the hook wiring live outside this repository - in
`~/.worktime/wt-hook.sh` and `~/.claude/settings.json` - so a server upgrade
never updates them. After pulling a WorkTime release that changes agent
tracking, redo both steps or the old behaviour silently stays.

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
