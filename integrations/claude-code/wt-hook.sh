#!/bin/sh
# WorkTime hook for Claude Code: reports session activity to a worktime server
# so agent working time is tracked automatically and survives crashes.
#
# Wired via .claude/settings.json (see settings.json.example next to this file):
#   wt-hook.sh start       <- SessionStart (startup|resume|clear|compact|fork)
#   wt-hook.sh heartbeat   <- UserPromptSubmit / PostToolUse / Stop / PreCompact
#   wt-hook.sh tool_start  <- PreToolUse
#   wt-hook.sh stop        <- SessionEnd
#
# Requires two environment variables:
#   WORKTIME_URL    e.g. https://wt.example.com (no trailing slash)
#   WORKTIME_TOKEN  a wt_... API token created in WorkTime settings
# Optional:
#   WORKTIME_HOOK_LOG  path to a debug log of every event seen by this hook
#
# Design constraints (do not "fix" these):
#   - Always exits 0: a tracking failure must never block the agent.
#   - curl runs with a hard 3s timeout.
#   - Failed calls are spooled to $WORKTIME_QUEUE_DIR (default ~/.worktime/queue)
#     with their original timestamps and flushed on the next event, so worktime
#     being unreachable loses nothing. The server is idempotent, replays are safe.

set -u

EVENT="${1:-heartbeat}"
BASE_URL="${WORKTIME_URL:-}"
TOKEN="${WORKTIME_TOKEN:-}"
QUEUE_DIR="${WORKTIME_QUEUE_DIR:-$HOME/.worktime/queue}"
LOCK_DIR="$QUEUE_DIR/.lock"
FLUSH_LIMIT=20

[ -n "$BASE_URL" ] && [ -n "$TOKEN" ] || exit 0

INPUT=$(cat 2>/dev/null || true)

# Extracts a top-level string field from the hook JSON on stdin. The value is
# returned still JSON-escaped, which makes it safe to embed back into a JSON
# body as-is (important for Windows paths with backslashes).
#
# The payload arrives as a single line and nests objects (tool_input carries its
# own "cwd"), so the match has to be the FIRST occurrence of the key and must
# not span other fields - a leading .* in sed silently returned the innermost
# value instead.
json_string() {
    printf '%s' "$INPUT" |
        grep -o "\"$1\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" |
        head -n 1 |
        sed 's/^"[^"]*"[[:space:]]*:[[:space:]]*"//; s/"$//'
}

# Escapes a raw (not yet JSON) value for embedding into a body.
json_escape() {
    printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

SESSION_ID=$(json_string session_id)
case "$SESSION_ID" in
    *[!0-9a-fA-F-]*|'') exit 0 ;;
esac

now_ms() {
    ms=$(date +%s%3N 2>/dev/null || true)
    case "$ms" in
        ''|*[!0-9]*) printf '%s000' "$(date +%s)" ;;
        *) printf '%s' "$ms" ;;
    esac
}

# CWD_JSON is JSON-ready, CWD_PATH is the raw path for git. CLAUDE_PROJECT_DIR
# wins over the payload's cwd: it is the project root, which is what the session
# is actually about, while cwd follows whatever directory the agent stepped into.
collect_context() {
    if [ -n "${CLAUDE_PROJECT_DIR:-}" ]; then
        CWD_PATH="$CLAUDE_PROJECT_DIR"
        CWD_JSON=$(json_escape "$CWD_PATH")
    else
        CWD_JSON=$(json_string cwd)
        # Unescape only for git; the JSON body reuses the escaped form verbatim.
        CWD_PATH=$(printf '%s' "$CWD_JSON" | sed 's/\\\\/\\/g')
    fi
    BRANCH=""
    [ -n "$CWD_PATH" ] && BRANCH=$(git -C "$CWD_PATH" rev-parse --abbrev-ref HEAD 2>/dev/null || true)
    BRANCH=$(json_escape "$BRANCH")
}

# UTC offset in minutes. Only a known offset lets the server tell whether a pause
# crossed the local midnight, and 0 has to mean UTC rather than "unknown", so an
# unparsable value is left out of the body entirely.
tz_offset_min() {
    zone=$(date +%z 2>/dev/null || true)
    case "$zone" in
        [+-][0-9][0-9][0-9][0-9])
            sign=$(printf '%s' "$zone" | cut -c1)
            hours=$(printf '%s' "$zone" | cut -c2-3)
            minutes=$(printf '%s' "$zone" | cut -c4-5)
            total=$((10#$hours * 60 + 10#$minutes))
            [ "$sign" = "-" ] && total=$((-total))
            printf '%s' "$total"
            ;;
    esac
}

# send URL BODY -> 0 delivered, 1 rejected (permanent, drop), 2 unreachable (retry)
send() {
    code=$(curl -sS -o /dev/null -w '%{http_code}' -m 3 -X POST "$1" \
        -H "Authorization: Bearer $TOKEN" \
        -H "Content-Type: application/json" \
        -d "$2" 2>/dev/null) || code=000
    case "$code" in
        2*) return 0 ;;
        000|408|429|5*) return 2 ;;
        *) return 1 ;;
    esac
}

# The queue is shared by every hook process of every session, and hooks run
# concurrently, so an unlocked flush sends the same file twice. mkdir is atomic
# everywhere, which makes it the lock. A hook killed mid-flush would leave the
# lock behind forever and silently freeze the queue, hence both the trap and the
# age-based takeover.
lock_queue() {
    mkdir -p "$QUEUE_DIR" 2>/dev/null || return 1
    if [ -d "$LOCK_DIR" ] && [ -n "$(find "$LOCK_DIR" -maxdepth 0 -mmin +5 2>/dev/null)" ]; then
        rmdir "$LOCK_DIR" 2>/dev/null || true
    fi
    mkdir "$LOCK_DIR" 2>/dev/null || return 1
    trap 'rmdir "$LOCK_DIR" 2>/dev/null; exit 0' EXIT INT TERM
    return 0
}

unlock_queue() {
    trap - EXIT INT TERM
    rmdir "$LOCK_DIR" 2>/dev/null || true
}

flush_queue() {
    [ -d "$QUEUE_DIR" ] || return 0
    lock_queue || return 0
    flushed=0
    for spooled in "$QUEUE_DIR"/*.req; do
        [ -f "$spooled" ] || continue
        [ "$flushed" -lt "$FLUSH_LIMIT" ] || break
        url=$(head -n 1 "$spooled")
        body=$(tail -n +2 "$spooled")
        send "$url" "$body"
        case $? in
            2) break ;;             # still unreachable, keep the rest in order
            *) rm -f "$spooled" ;;  # delivered, or permanently rejected
        esac
        flushed=$((flushed + 1))
    done
    unlock_queue
}

deliver() {
    send "$1" "$2"
    [ $? -eq 2 ] || return 0    # delivered, or rejected for good - nothing to retry
    mkdir -p "$QUEUE_DIR" 2>/dev/null || return 0
    # Cap the queue so a long outage cannot fill the disk. Only spooled requests
    # count; the lock directory must not push the queue over the cap.
    count=$(find "$QUEUE_DIR" -maxdepth 1 -name '*.req' 2>/dev/null | wc -l)
    [ "$count" -lt 1000 ] || return 0
    spool="$QUEUE_DIR/$(now_ms)-$$.req"
    { printf '%s\n' "$1"; printf '%s' "$2"; } > "$spool" 2>/dev/null || true
}

SESSION_URL="$BASE_URL/api/agent/sessions/$SESSION_ID"
REASON=$(json_string reason)
SOURCE=$(json_string source)

[ -z "${WORKTIME_HOOK_LOG:-}" ] || printf '%s %s %s %s %s\n' \
    "$(now_ms)" "$EVENT" "$(json_string hook_event_name)" \
    "$SOURCE$REASON" "$SESSION_ID" >> "$WORKTIME_HOOK_LOG" 2>/dev/null || true

# The backlog goes out before the current event. Delivering the live one first defeats
# the queue twice over: the server ignores any signal at or before its watermark, so a
# live heartbeat after an outage books the whole gap as a pause and the spooled
# heartbeats that prove it was work are then discarded as stale; and a stop for a
# session whose start is still spooled hits a session that does not exist yet, which is
# a 404 - permanent, dropped, never retried.
#
# SessionStart is the exception: it is the one synchronous hook (5 second budget), and
# nothing can be queued for a session that has not started. It flushes afterwards.
# Draining an empty queue costs nothing, and an unreachable server stops the drain on
# the first attempt.
[ "$EVENT" = "start" ] || flush_queue

# tz_offset_min rides along on every event. A session first seen from a heartbeat -
# because its start was lost - would otherwise keep tz_offset_min NULL forever, and
# crossesLocalMidnight can then never cut a night break in two. cwd and git_branch stay
# on start only: collect_context shells out to git, and this runs on every tool call.
TZ_FIELD=""
OFFSET=$(tz_offset_min)
[ -z "$OFFSET" ] || TZ_FIELD=",\"tz_offset_min\":$OFFSET"

case "$EVENT" in
    start)
        collect_context
        deliver "$SESSION_URL/start" \
            "{\"started_at\":$(now_ms),\"source\":\"claude-code\",\"cwd\":\"$CWD_JSON\",\"git_branch\":\"$BRANCH\"$TZ_FIELD}"
        ;;
    heartbeat)
        deliver "$SESSION_URL/heartbeat" "{\"at\":$(now_ms)$TZ_FIELD}"
        ;;
    tool_start)
        # PreToolUse, i.e. *before* the tool runs. Without it a twenty minute Bash
        # or Task call is indistinguishable from an empty chair: every other hook
        # fires only once the gap has already happened.
        deliver "$SESSION_URL/heartbeat" "{\"at\":$(now_ms),\"activity\":\"tool_start\"$TZ_FIELD}"
        ;;
    stop)
        # SessionEnd fires with reason=resume when the session is handed over to a
        # resumed one that keeps the same id: stopping here would close a session
        # that is still working, and the next heartbeat would have to revive it.
        if [ "$REASON" != "resume" ]; then
            [ -n "$REASON" ] || REASON=other
            deliver "$SESSION_URL/stop" "{\"ended_at\":$(now_ms),\"reason\":\"$REASON\"$TZ_FIELD}"
        fi
        ;;
esac

# SessionStart delivers first, then drains whatever is left of its budget.
[ "$EVENT" != "start" ] || flush_queue

exit 0
