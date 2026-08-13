// The setup prompts handed out by Settings -> Connect an agent. The text is an
// instruction to an agent, not documentation for a person: imperative, checkable
// steps, and a proof at the end that the tracking actually started.
//
// Four rules shape everything here, each of them paid for by a review that
// caught the opposite:
//   - the prompt never hardcodes what the server already knows. The hook script
//     and its wiring are fetched from this instance, so a fork or an
//     un-upgraded server can never install a hook that speaks a different
//     protocol than its own API;
//   - every command carries its own values. An agent usually runs each shell
//     call in a fresh process, so a command reading $WORKTIME_TOKEN sends an
//     empty header and every step "succeeds" with a 401;
//   - nothing is written over until its replacement has been checked. A server
//     older than the hook endpoint answers 200 with the SPA's index.html, so a
//     download straight onto the hook replaces a working install with HTML;
//   - "the command ran" is not proof, and neither is a check the setup cannot
//     pass yet. The probe delivers a start through the script and reads the
//     session back, and every criterion that only a restart can satisfy says so.

export type AgentClient = "claude-code" | "codex";

export interface SetupPromptOptions {
  /** Instance the agent will report to, e.g. https://wt.example.com. */
  origin: string;
  /** API token to embed. Plaintext exists only at creation, so Settings makes one per download. */
  token: string;
  /**
   * Session id the setup probe uses. Fixed inside one file so a re-run reuses
   * that row, but minted per download: agent_sessions has a global primary key,
   * so a constant would belong to whoever ran it first and every other user of
   * the instance would get "session belongs to another user" instead of a proof.
   */
  probeSession: string;
}

export function setupPromptFilename(client: AgentClient): string {
  return client === "codex" ? "worktime-setup-codex.md" : "worktime-setup-claude-code.md";
}

export function setupPrompt(client: AgentClient, options: SetupPromptOptions): string {
  const origin = options.origin.replace(/\/+$/, "");
  return client === "codex"
    ? codexPrompt(origin, options.token, options.probeSession)
    : claudeCodePrompt(origin, options.token, options.probeSession);
}


const preamble = (origin: string, token: string, client: AgentClient) => `    WORKTIME_URL   = ${origin}
    WORKTIME_TOKEN = ${token}

Every command below already carries those two values, so nothing has to be exported first: assume one shell call keeps none of the previous one's environment.

Rules for the whole run:

- the token belongs only in files under \`$HOME\`. Never write it into a repository file or a commit, and never print it in your replies or your report - the user pastes that report elsewhere;
- every shell command below is POSIX \`sh\`. On Windows run all of them in Git Bash (\`C:\\Program Files\\Git\\bin\\bash.exe\`), never \`cmd.exe\` or PowerShell: \`printf\`, \`tr\`, \`chmod\` and \`sh -n\` do not exist there.${
  client === "codex" ? " Codex does not default to Git Bash, so this is on you to arrange." : ""
} If Git Bash is missing, stop and report that Git for Windows has to be installed first - the hook is a POSIX script and has no PowerShell port;
- write config files with your file-editing tool, never with shell redirection. On Windows that produces a BOM and CRLF, which breaks both JSON parsing and \`/bin/sh\`;
- re-running this prompt must stay safe. Everything under \`~/.worktime\` is ours and is replaced outright; every other file - the client's config, \`CLAUDE.md\`, \`AGENTS.md\` - is merged into, never overwritten.

## 1. Preflight

    curl --version
    git --version
    curl -sS -o /dev/null -w '%{http_code}\\n' -H "Authorization: Bearer ${token}" ${origin}/api/me

\`curl\` must answer (on Windows, from Git Bash) and the last line must print exactly \`200\`. A missing \`git\` is not fatal - entries then carry no branch - so note it and go on. Anything else: stop, find the code in the table at the end, and report its "What to do" line verbatim as the reason.

## 2. Install the integration scripts

Download beside the real file, check it, and only then put it in place:

    mkdir -p ~/.worktime
    curl -fsSL -H "Authorization: Bearer ${token}" ${origin}/api/agent/hook.sh -o ~/.worktime/wt-hook.new
    head -n 1 ~/.worktime/wt-hook.new                      # exactly: #!/bin/sh
    grep -c 'api/agent/sessions' ~/.worktime/wt-hook.new   # at least 1
    sh -n ~/.worktime/wt-hook.new                          # prints nothing

Only when all three pass:

    tr -d '\\r' < ~/.worktime/wt-hook.new > ~/.worktime/wt-hook.sh
    rm ~/.worktime/wt-hook.new
    chmod +x ~/.worktime/wt-hook.sh

**Never download straight onto \`wt-hook.sh\`.** A server older than this endpoint answers \`200\` with the app's \`index.html\`, so \`curl -f\` does not stop it and a working hook would be replaced by HTML. If the checks fail, delete the \`.new\` file, leave any existing hook alone, and fall back to \`https://raw.githubusercontent.com/Siyet/worktime/main/integrations/claude-code/wt-hook.sh\` - then say in your report that the script came from GitHub and may be newer than this server.${
  client === "claude-code"
    ? `

Claude Code can also show the same running timer in its status line. Install that read-only client with the same checked replacement:

    curl -fsSL -H "Authorization: Bearer ${token}" ${origin}/api/agent/statusline.sh -o ~/.worktime/wt-statusline.new
    head -n 1 ~/.worktime/wt-statusline.new                  # exactly: #!/bin/sh
    grep -c 'status-line' ~/.worktime/wt-statusline.new      # at least 1
    sh -n ~/.worktime/wt-statusline.new                      # prints nothing

Only when all three pass:

    tr -d '\\r' < ~/.worktime/wt-statusline.new > ~/.worktime/wt-statusline.sh
    rm ~/.worktime/wt-statusline.new
    chmod +x ~/.worktime/wt-statusline.sh

Apply the same no-overwrite rule and GitHub fallback as for the hook, using \`integrations/claude-code/wt-statusline.sh\`. The status line only reads the timer; it never sends a heartbeat, so its refreshes cannot turn idle time into tracked work.`
    : ""
}
`;

const failureLadder = (origin: string, client: AgentClient) => `## If something fails

| What you see | What it means | What to do |
|---|---|---|
| \`401\` on any call | the token is wrong, revoked, or was copied with a trailing newline | ask the user for a fresh token from Settings -> API tokens |
| \`200\` and the body is HTML | this server predates the endpoint, or the URL is wrong | the GitHub fallback in step 2; never leave the HTML in place |
| \`400 ... is not a UUID\` | the payload was parsed wrong | print the JSON you fed the script; its top-level \`session_id\` must be a UUID |
| \`404\` on \`/stop\` **in step 7** | the start never reached the server | if \`~/.worktime/queue\` holds \`.req\` files for this instance, drain it and repeat 7.1; if it is empty, this is a failure - re-run 7.1 with the payload printed |
| \`404\` on \`/stop\` in daily use | a start that was lost | harmless; the next heartbeat recreates the session |
| \`/mcp\` answers HTML | wrong base URL, or a proxy stripping the path | \`curl -i -X POST -H 'Content-Type: application/json' ${origin}/mcp\` must answer JSON-RPC, 400, 401 or 415 - anything but HTML |
| \`Forbidden: invalid Host header\` | the server predates the reverse-proxy fix | the instance needs upgrading; tracking still works, naming does not |${
  client === "claude-code"
    ? `\n| MCP fails while the hooks work | \`\${WORKTIME_TOKEN}\` was expanded when the server was added | \`~/.claude.json\` must still hold the literal \`\${WORKTIME_TOKEN}\`; re-add the server with single quotes |`
    : `\n| MCP fails while the hooks work | the header never reached the server | \`~/.codex/config.toml\` must hold the \`http_headers\` line from step 5 |`
}
| the hook log stays empty | the hook saw no credentials, or it rejected the session id before it could log | check the log path exists and re-run 7.1 with the payload printed |
| \`\\r: command not found\` | the script was saved with CRLF | redo the \`tr -d '\\r'\` step |
| \`~/.worktime/queue\` keeps growing | the server is unreachable | nothing is lost: events replay with their original timestamps once it is back |

## Report back

For each step say PASS, FAIL or ALREADY IN PLACE. Then: where the hook came from, the \`time_entry_id\` the probe returned, the exact name of the probe entry so the user can delete it, and everything still left for the user to do. Do not print the token.

Last, if these instructions were saved to a file, delete it: it carries a live token, and a stray \`worktime-setup-*.md\` is exactly the kind of file that ends up committed.
`;

// The proof, identical for both clients apart from what is invoked. It rests on
// one asymmetry in the API: start and heartbeat create a session for an unknown
// id, stop does not. So a 200 from stop can only mean the script delivered
// something, and the cwd and timezone in that response can only have come from
// the script - this probe never sends either.
const probe = (origin: string, token: string, client: AgentClient, session: string, invocation: string) => {
  const tag = `${client === "codex" ? "Codex" : "Claude Code"} #${session.replace(/-/g, "").slice(0, 8)}`;
  return `## 7. Prove the whole chain, end to end

Probe id \`${session}\`, fixed so a re-run of this file reuses one row instead of leaving a new one behind.

7.1 Deliver a start *through the script*, exactly as a hook would:

    ls ~/.worktime/queue/*.req 2>/dev/null | wc -l     # note this number
    printf '{"session_id":"${session}","cwd":"%s","hook_event_name":"SessionStart","source":"startup"}' "$PWD" \\
      | ${invocation} start
    ls ~/.worktime/queue/*.req 2>/dev/null | wc -l     # must not have grown
    tail -n 1 ~/.worktime/hook.log                     # one new line, ending in the probe id

If the first count was not \`0\`, the script deliberately spools this event behind the backlog - order is what makes the replay correct - and the queue drains at most twenty per run. Flush it and repeat 7.1 until the count reads \`0\` or stops dropping - whatever stays belongs to another instance, and this hook will never touch it:

    printf '{"session_id":"${session}"}' \\
      | ${invocation} heartbeat

Only a count that grew from \`0\` means the server is unreachable. If \`$PWD\` holds a backslash or a quote, \`cd ~\` first - the payload above is assembled by hand and neither is escaped.

7.2 Close the probe and read the proof:

    curl -sS -X POST -H "Authorization: Bearer ${token}" -H 'Content-Type: application/json' \\
      -d '{"reason":"other"}' ${origin}/api/agent/sessions/${session}/stop
    date +%s%3N

Expect \`"status":"closed"\`, a non-empty \`"time_entry_id"\`, a non-empty \`"cwd"\`, a non-null \`"tz_offset_min"\` - and **\`"last_heartbeat_at"\` within the last two minutes of the timestamp you just printed**.

That last one is the criterion. A stop on a session that is already closed only reads the row back and writes nothing, so the first four can all be left over from an earlier run of this file: \`last_heartbeat_at\` is the only field that just now, in 7.1, can have moved. \`cwd\` and \`tz_offset_min\` still matter - neither call here sends them, so they can only have come from the script.

A \`404 session not found\` means the start never reached the server: stop is the one signal that never creates a session. Check the queue before calling it a failure.

7.3 Cross the MCP half. If the worktime tools are already in your tool list, call \`set_agent_task(task_key: "WORKTIME", task_title: "setup check", session_id: "${session}")\` and expect \`"task_key":"WORKTIME"\` back - \`renamed_entries\` is 1 the first time and 0 on a re-run, when the name is already right.

If those tools are not listed, that is expected rather than a failure - a server added in step 5 is not loaded until the client restarts. Ask the server directly instead:

    curl -sS -X POST -H "Authorization: Bearer ${token}" -H 'Content-Type: application/json' \\
      -H 'Accept: application/json, text/event-stream' \\
      -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' ${origin}/mcp

The response arrives as an event stream (\`event: message\` then \`data: {...}\`), so read the JSON out of the \`data:\` line rather than parsing the whole body. It must list \`set_agent_task\`. That proves the server's MCP half; whether this client picked up the config is what the restart settles. Report which of the two paths you used.

A short probe entry stays on the Timer page as evidence: named \`WORKTIME setup check\` if 7.3 renamed it, otherwise \`${tag}\`. Tell the user its exact name and that it is safe to delete.
`;
};

const standingInstruction = (file: string, tag: string) => `## 6. Standing instruction

Append this block to \`${file}\`, creating the file if it is missing. If a block with the same markers is already there, replace its contents instead of adding a second one:

    <!-- worktime:begin -->
    - WorkTime: call the MCP tool set_agent_task(task_key, task_title) as soon as the
      task number is known, looking the title up in whatever tracker connection is
      available. WorkTime never reads a tracker itself. Until it is called, the work is
      filed under a technical tag like "${tag}".
    - If asked to book this work to a project, call update_time_entry(project: "<name>")
      with no entry_id - it edits the running session and takes a project name from
      list_projects, never an id.
    <!-- worktime:end -->

Nothing else about tracking belongs there: the hooks do the tracking, these two calls only name the work and file it.
`;

function claudeCodePrompt(origin: string, token: string, probeSession: string): string {
  return `# Set up WorkTime time tracking (Claude Code)

Configure this machine so every Claude Code session is tracked in WorkTime automatically, then prove it works. "The command ran" is not proof.

${preamble(origin, token, "claude-code")}
## 3. Wire everything into ~/.claude/settings.json

The hooks, status line and MCP client all read their credentials from the \`env\` block of this file - no shell profile is involved, and one mechanism covers all three operating systems.

Fetch the canonical wiring rather than typing it out, and back the file up before touching it:

    mkdir -p ~/.claude
    curl -fsSL -H "Authorization: Bearer ${token}" ${origin}/api/agent/hook-settings.json
    echo "$HOME"
    [ -f ~/.claude/settings.json.wtbak ] || cp ~/.claude/settings.json ~/.claude/settings.json.wtbak 2>/dev/null || true

Then edit \`~/.claude/settings.json\` - a missing file starts as \`{}\` - so that:

- \`env\` contains \`"WORKTIME_URL": "${origin}"\`, \`"WORKTIME_TOKEN": "${token}"\` and \`"WORKTIME_HOOK_LOG"\` set to the \`.worktime/hook.log\` path under the home directory you just printed, **written out in full**. Values in that block are not shell-expanded, so a literal \`$HOME\` silently disables the one log that says whether a hook fired at all. Leave every other variable there untouched;
- every event from the fetched wiring is present. If an event already has an entry whose command mentions \`wt-hook.sh\`, replace that entry in place - do not append a second one, or every signal is sent twice;
- merge the fetched \`statusLine\` when the file has none. If its current command already mentions \`wt-statusline.sh\`, replace it with the fetched object. If another status line is configured, preserve it exactly and report that WorkTime's line was not installed - Claude Code supports only one, and this setup never takes somebody else's terminal UI away;
- the rest of the file is edited, not re-serialised: do not reformat or reorder what you are not changing.

Then read it back and confirm it parses as JSON. If it does not, restore \`~/.claude/settings.json.wtbak\`, stop, and report it: a broken settings file disables every hook the user has, not only these. On macOS and Linux finish with \`chmod 600 ~/.claude/settings.json\` - it now holds a token.

Clean up while you are in there: delete any handler for an event that is **not** in the fetched wiring but whose command mentions \`wt-hook\`. An older install wired different events, and a leftover keeps sending signals nobody reads.

\`PreToolUse\` is load-bearing - it is the only signal sent *before* a tool runs, so without it a twenty minute Bash or Task call is subtracted from the entry as idle.

## 4. Check the wiring took

    grep -o wt-hook ~/.claude/settings.json | wc -l        # one per event in the fetched wiring
    grep -c '"WORKTIME_TOKEN"' ~/.claude/settings.json     # exactly 1
    grep -c '"WORKTIME_HOOK_LOG"' ~/.claude/settings.json  # exactly 1
    grep -c wt-statusline ~/.claude/settings.json          # 1, or 0 only when preserving another status line

Any other number is a duplicate or a missed event. Nothing later in this prompt reads that file - the probe carries its own credentials - so this is the only place a typo in it is caught.

## 5. Connect the MCP server

    claude mcp list
    # only if worktime is missing or points somewhere else:
    claude mcp remove worktime --scope user 2>/dev/null
    claude mcp add --scope user --transport http worktime "${origin}/mcp" --header 'Authorization: Bearer \${WORKTIME_TOKEN}'

The single quotes matter: \`\${WORKTIME_TOKEN}\` must reach Claude Code unexpanded, so the token stays in \`settings.json\` instead of being copied into \`~/.claude.json\`.

The criterion is that \`claude mcp list\` **lists** \`worktime\` - not that it connects. The placeholder resolves from the \`env\` block written in step 3, which this process read at startup, so on a machine set up for the first time the entry legitimately reads "failed to connect" until Claude Code restarts. Report which of the two you saw. If \`worktime\` is not listed at all, the add failed: report FAIL with the command's output and stop - do not retry with another transport or scope. And never swap the placeholder for the literal token to make the line go green.

Hooks and MCP are independent halves: time can be tracked perfectly while every naming call fails, and the only symptom is that entries keep a technical tag forever.

${standingInstruction("~/.claude/CLAUDE.md", "Claude Code #ab12cd34")}
${probe(origin, token, "claude-code", probeSession, `WORKTIME_URL="${origin}" WORKTIME_TOKEN="${token}" WORKTIME_HOOK_LOG="$HOME/.worktime/hook.log" sh ~/.worktime/wt-hook.sh`)}
## 8. Hand over to the user

Hook wiring is picked up by the file watcher, but the \`env\` block, status line and MCP server list are read when the process starts. If this session began before step 3, nothing is tracked until Claude Code is restarted - say so plainly.

Then give them two checks for the next session: the terminal status line must contain \`WorkTime H:MM:SS\`, and \`list_running_timers\` must return a row named \`Claude Code #<8 hex>\` with a growing elapsed. Both read the same entry. Skip the first check only when an existing non-WorkTime status line was deliberately preserved.

If it never appears, \`tail ~/.worktime/hook.log\`: a line per prompt and per tool call means the hooks fire and the problem is elsewhere; an empty log means they do not.

${failureLadder(origin, "claude-code")}`;
}

function codexPrompt(origin: string, token: string, probeSession: string): string {
  return `# Set up WorkTime time tracking (Codex CLI)

Configure this machine so every Codex session is tracked in WorkTime automatically, then prove it works. "The command ran" is not proof.

## 0. Does this build have hooks?

    command -v codex || echo MISSING
    codex --version
    codex --help | grep -c 'dangerously-bypass-hook-trust'

\`MISSING\` means Codex is not on this shell's PATH: find it before going on rather than reading the count, which is \`0\` for a missing command and for an old build alike. A non-zero count means the hooks engine is there. If it prints \`0\`, **skip steps 3 and 4**, do 1, 2, 5, 6, 7 and 8, and everywhere step 7 invokes \`sh ~/.worktime/wt-codex.sh\` - which step 3 would have written - call the hook directly instead:

    ... | WORKTIME_URL="${origin}" WORKTIME_TOKEN="${token}" WORKTIME_AGENT_SOURCE=codex \\
          WORKTIME_HOOK_LOG="$HOME/.worktime/hook.log" sh ~/.worktime/wt-hook.sh start

Then tell the user that until they upgrade Codex nothing is tracked automatically: they have to ask you for \`start_timer\` at the beginning of a session and \`stop_timer\` before they leave.

Codex lifecycle hooks deliver the same stdin JSON as Claude Code's - \`session_id\`, \`cwd\`, \`hook_event_name\`, \`source\`, \`reason\` - so the same script serves both. Check \`codex mcp add --help\` and the hooks section of \`codex --help\` before assuming any flag or file name below: trust the tool over this prompt. If \`codex\` is not on the PATH of the shell you are using - on Windows it is often a \`.cmd\` shim invisible from Git Bash - find it before going on, or steps 0 and 5 will look like an unsupported build.

${preamble(origin, token, "codex")}
## 3. Wrap the script with this machine's credentials

Codex hooks inherit only the environment Codex itself was started with, which no setup step can change for a terminal that is already running. And Codex parses \`async\` on a hook without implementing it, so a hook that talks to the network adds its latency to whatever fired it. A wrapper solves both and leaves the downloaded script byte-identical, so it can be replaced at any time.

Write exactly these lines to \`~/.worktime/wt-codex.sh\`, overwriting any earlier copy - the file is ours:

    #!/bin/sh
    export WORKTIME_URL="${origin}"
    export WORKTIME_TOKEN="${token}"
    export WORKTIME_AGENT_SOURCE=codex
    export WORKTIME_HOOK_LOG="$HOME/.worktime/hook.log"
    event=\${1:-heartbeat}
    payload=$(cat 2>/dev/null || true)
    case "$event" in
        start|stop) printf '%s' "$payload" | "$HOME/.worktime/wt-hook.sh" "$event" >/dev/null 2>&1 ;;
        *)          printf '%s' "$payload" | "$HOME/.worktime/wt-hook.sh" "$event" >/dev/null 2>&1 & ;;
    esac
    exit 0

Then run these - they are commands, not part of the file:

    chmod 700 ~/.worktime/wt-codex.sh
    sh -n ~/.worktime/wt-codex.sh

Reading stdin to the end before branching releases Codex's pipe while the detached child keeps the payload. \`start\` and \`stop\` stay in the foreground on purpose: a detached child has no guarantee of outliving the process that spawned it, and those two are the signals that open and close the entry. \`WORKTIME_AGENT_SOURCE\` is what makes entries read \`Codex #ab12cd34\` rather than \`Claude Code #ab12cd34\` - the server names an untitled entry after the client that reported it.

## 4. Wire the hooks

\`mkdir -p ~/.codex\` first - on a machine where Codex has never run, the directory does not exist.

Prefer \`~/.codex/hooks.json\` over an inline \`[hooks]\` table in \`config.toml\`: Codex loads both and warns when one layer carries both, and a JSON file is far easier to rewrite correctly. If the user already wires hooks in \`config.toml\`, merge there instead and keep one file.

Print \`echo "$HOME"\` and write the absolute path into every command string below - neither \`~\` nor \`$HOME\` is promised expansion, because the command is not promised a shell. Copy the shape verbatim, including the escaped quotes: the value is a JSON string containing a quoted shell path, and an unquoted path breaks on a home directory with a space in it.

    {
      "hooks": {
        "SessionStart":     [{"matcher": "startup|resume|clear|compact|fork",
                              "hooks": [{"type": "command", "command": "\\"/absolute/home/.worktime/wt-codex.sh\\" start", "timeout": 10}]}],
        "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "\\"/absolute/home/.worktime/wt-codex.sh\\" heartbeat", "timeout": 10}]}],
        "PreToolUse":       [{"hooks": [{"type": "command", "command": "\\"/absolute/home/.worktime/wt-codex.sh\\" tool_start", "timeout": 10}]}],
        "PostToolUse":      [{"hooks": [{"type": "command", "command": "\\"/absolute/home/.worktime/wt-codex.sh\\" heartbeat", "timeout": 10}]}],
        "Stop":             [{"hooks": [{"type": "command", "command": "\\"/absolute/home/.worktime/wt-codex.sh\\" heartbeat", "timeout": 10}]}],
        "PreCompact":       [{"hooks": [{"type": "command", "command": "\\"/absolute/home/.worktime/wt-codex.sh\\" heartbeat", "timeout": 10}]}],
        "SessionEnd":       [{"hooks": [{"type": "command", "command": "\\"/absolute/home/.worktime/wt-codex.sh\\" stop", "timeout": 3}]}]
      }
    }

Load-bearing details, do not simplify them away:

- \`PreToolUse\` deliberately carries no matcher: it must fire for every tool. It is the only signal sent *before* a tool runs.
- \`SessionEnd\` gets \`timeout: 3\` because Codex caps that event there. If the stop is cut short anyway nothing is lost: the server closes a silent session at its last heartbeat.
- On Windows add \`"commandWindows"\` next to each \`"command"\`, with the absolute Git Bash path, for example \`"C:\\\\Program Files\\\\Git\\\\bin\\\\bash.exe" "%USERPROFILE%/.worktime/wt-codex.sh" start\`. The plain \`command\` goes through \`cmd.exe /C\` there, where \`$HOME\` does not expand and a \`.sh\` file is not executable. A bare \`bash\` often resolves to WSL, which has a different home directory.

Merge, never overwrite. The removal pass is what makes a re-run and an upgrade behave the same:

1. Back up \`~/.codex/hooks.json\` if it exists; otherwise create it containing \`{}\`.
2. Delete every handler whose command mentions \`wt-hook\` or \`wt-codex\`, then drop matcher groups and event keys left empty.
3. Add the wiring above, appending to each event's existing array rather than replacing it.
4. Check \`~/.codex/config.toml\` for stale inline \`[[hooks.*]]\` blocks referencing our scripts and delete them - both layers are merged, so a leftover keeps firing.
5. Confirm the file still parses; if it does not, restore the backup and stop.

Then verify the wiring and the command string itself. The payload must carry a session id: without one the script exits before it does anything, and the wrapper hides its child's errors, so an empty payload returns 0 even when no hook is installed at all.

    grep -o wt-codex ~/.codex/hooks.json | wc -l    # one per event above
    printf '{"session_id":"${probeSession}"}' | sh "$HOME/.worktime/wt-codex.sh" heartbeat
    tail -n 1 ~/.worktime/hook.log                  # a new line naming that id

## 5. Connect the MCP server

\`codex mcp add\` has no flag for a literal header, so drop any old entry with the CLI and write the table by hand:

    codex mcp remove worktime 2>/dev/null || true

Back the file up first, without overwriting an older backup (\`[ -f ~/.codex/config.toml.wtbak ] || cp ~/.codex/config.toml ~/.codex/config.toml.wtbak\`). If a \`[mcp_servers.worktime]\` table is already there, replace it rather than appending a second one - two tables of the same name make the whole file unparseable and Codex loses every setting in it, not only ours. Otherwise append:

    [mcp_servers.worktime]
    url = "${origin}/mcp"
    http_headers = { Authorization = "Bearer ${token}" }
    startup_timeout_sec = 20

A literal header rather than \`bearer_token_env_var\`: that alternative reads the environment Codex was launched with, which is empty unless the user started it from a terminal that sourced a profile. Unlike Claude Code, Codex expands nothing in this file, so the literal token here is the correct choice, not a shortcut. Then confirm the file still parses (\`codex mcp list\` reading it back is proof enough), restore the backup if it does not, and \`chmod 600 ~/.codex/config.toml\` on macOS and Linux.

Hooks and MCP are independent halves: time can be tracked perfectly while every naming call fails, and the only symptom is that entries keep a technical tag forever.

${standingInstruction("~/.codex/AGENTS.md", "Codex #ab12cd34")}
${probe(origin, token, "codex", probeSession, "sh ~/.worktime/wt-codex.sh")}
## 8. Hand over to the user

Two things you cannot do yourself, and both look exactly like a broken setup when they are skipped:

- **the hooks have to be trusted.** Codex will not run a hook until the user reviews it with \`/hooks\`; an untrusted hook fails silently.
- **Codex has to be restarted.** Hooks and MCP servers are loaded at startup.

After that, ask them to have you call \`list_running_timers\` in the new session: it must return a row named \`Codex #<8 hex>\` with a growing elapsed, which is that session's own entry. A row named \`Claude Code #<8 hex>\` instead means \`SessionStart\` never fired but a later hook did - the client name rides only on \`start\` - so re-check that it is wired and trusted.

If it never appears, \`tail ~/.worktime/hook.log\`: a line per prompt and per tool call means the hooks fire and the problem is elsewhere; an empty log means they do not.

${failureLadder(origin, "codex")}`;
}
