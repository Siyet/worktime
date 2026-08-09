#!/bin/sh
# Tests for wt-hook.sh. Run it directly: sh wt-hook_test.sh
#
# The hook is the one piece of this project that cannot be covered by Go or
# Playwright, runs on every tool call of every session, and must never fail the
# agent - so its parsing, its "do not send" rules and its queue are pinned here.
# curl is replaced by a stub on PATH that records every request instead of
# making it; WT_STUB_CODE decides what the fake server answers.

set -u

HOOK=$(cd "$(dirname "$0")" && pwd)/wt-hook.sh
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

FAILURES=0

mkdir -p "$WORK/bin"
cat > "$WORK/bin/curl" <<'STUB'
#!/bin/sh
# Records "URL<TAB>BODY" and answers with $WT_STUB_CODE (default 200).
url=""
body=""
while [ $# -gt 0 ]; do
    case "$1" in
        -X) shift 2; continue ;;
        -d) body="$2"; shift 2; continue ;;
        -H) shift 2; continue ;;
        -m) shift 2; continue ;;
        -w|-o) shift 2; continue ;;
        -sS|-s|-S) shift; continue ;;
        http*) url="$1"; shift; continue ;;
        *) shift ;;
    esac
done
[ -z "${WT_STUB_DELAY:-}" ] || sleep "$WT_STUB_DELAY"
printf '%s\t%s\n' "$url" "$body" >> "$WT_STUB_LOG"
printf '%s' "${WT_STUB_CODE:-200}"
STUB
chmod +x "$WORK/bin/curl"

PATH="$WORK/bin:$PATH"
export PATH
export WORKTIME_URL="http://worktime.test"
export WORKTIME_TOKEN="wt_test"
export WT_STUB_CODE=200
export WT_STUB_DELAY=""

SESSION="11111111-2222-3333-4444-555555555555"

check() {
    if [ "$2" = "$3" ]; then
        printf 'ok   %s\n' "$1"
    else
        printf 'FAIL %s\n     want: %s\n     got:  %s\n' "$1" "$3" "$2"
        FAILURES=$((FAILURES + 1))
    fi
}

run_hook() {
    # run_hook EVENT PAYLOAD
    printf '%s' "$2" | WORKTIME_QUEUE_DIR="$WORK/queue" sh "$HOOK" "$1"
}

reset_log() {
    WT_STUB_LOG="$WORK/calls-$1.log"
    export WT_STUB_LOG
    : > "$WT_STUB_LOG"
}

# --- top-level fields are not shadowed by nested ones ---------------------------
reset_log parse
rm -rf "$WORK/queue"
unset CLAUDE_PROJECT_DIR 2>/dev/null || true
run_hook start "{\"session_id\":\"$SESSION\",\"cwd\":\"C:\\\\Users\\\\dev\\\\WorkTime\",\"tool_input\":{\"cwd\":\"C:\\\\tmp\\\\other\"}}"
body=$(cut -f2 "$WT_STUB_LOG")
case "$body" in
    *'"cwd":"C:\\Users\\dev\\WorkTime"'*) result=top ;;
    *'"cwd":"C:\\tmp\\other"'*) result=nested ;;
    *) result="unexpected: $body" ;;
esac
check "start sends the top-level cwd, not tool_input.cwd" "$result" top
check "start hits the start endpoint" "$(cut -f1 "$WT_STUB_LOG")" \
    "http://worktime.test/api/agent/sessions/$SESSION/start"

# --- CLAUDE_PROJECT_DIR wins and is escaped -------------------------------------
reset_log projectdir
rm -rf "$WORK/queue"
CLAUDE_PROJECT_DIR='C:\Users\dev\Projects\WorkTime' run_hook start "{\"session_id\":\"$SESSION\",\"cwd\":\"/tmp/elsewhere\"}"
body=$(cut -f2 "$WT_STUB_LOG")
case "$body" in
    *'"cwd":"C:\\Users\\dev\\Projects\\WorkTime"'*) result=escaped ;;
    *) result="unexpected: $body" ;;
esac
check "the project dir is escaped for JSON" "$result" escaped

# --- a bad session id sends nothing ---------------------------------------------
reset_log badid
rm -rf "$WORK/queue"
run_hook heartbeat '{"session_id":"../../etc/passwd"}'
check "an invalid session id makes no requests" "$(wc -l < "$WT_STUB_LOG" | tr -d ' ')" 0
run_hook heartbeat '{}'
check "a missing session id makes no requests" "$(wc -l < "$WT_STUB_LOG" | tr -d ' ')" 0

# --- SessionEnd reason=resume must not stop the session -------------------------
reset_log resume
rm -rf "$WORK/queue"
run_hook stop "{\"session_id\":\"$SESSION\",\"reason\":\"resume\"}"
check "reason=resume sends no stop" "$(wc -l < "$WT_STUB_LOG" | tr -d ' ')" 0
run_hook stop "{\"session_id\":\"$SESSION\",\"reason\":\"clear\"}"
check "reason=clear sends the stop" "$(wc -l < "$WT_STUB_LOG" | tr -d ' ')" 1
case "$(cut -f2 "$WT_STUB_LOG")" in
    *'"reason":"clear"'*) result=clear ;;
    *) result="unexpected: $(cut -f2 "$WT_STUB_LOG")" ;;
esac
check "the stop carries the reason" "$result" clear

# --- an unreachable server spools, the next event flushes in order --------------
reset_log queue
rm -rf "$WORK/queue"
WT_STUB_CODE=000
run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
WT_STUB_CODE=200
check "unreachable requests are spooled" "$(find "$WORK/queue" -name '*.req' | wc -l | tr -d ' ')" 2
reset_log flush
run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
check "the queue is flushed with the next event" "$(wc -l < "$WT_STUB_LOG" | tr -d ' ')" 3
check "nothing is left in the queue" "$(find "$WORK/queue" -name '*.req' | wc -l | tr -d ' ')" 0

# --- concurrent hooks never send a spooled request twice ------------------------
reset_log parallel
rm -rf "$WORK/queue"
mkdir -p "$WORK/queue"
index=0
while [ "$index" -lt 5 ]; do
    printf '%s\n%s' "http://worktime.test/api/agent/sessions/$SESSION/heartbeat" "{\"at\":$index}" \
        > "$WORK/queue/00$index-spooled.req"
    index=$((index + 1))
done
WT_STUB_DELAY=0.05
run_hook heartbeat "{\"session_id\":\"$SESSION\"}" &
run_hook heartbeat "{\"session_id\":\"$SESSION\"}" &
wait
WT_STUB_DELAY=""
spooled=$(grep -c 'heartbeat.*"at":[0-4]}' "$WT_STUB_LOG" || true)
check "each spooled request is delivered exactly once" "$spooled" 5
check "the queue is drained" "$(find "$WORK/queue" -name '*.req' | wc -l | tr -d ' ')" 0
check "the lock directory is released" "$(find "$WORK/queue" -name '.lock' | wc -l | tr -d ' ')" 0

# --- the tool_start signal and the timezone -------------------------------------
reset_log toolstart
rm -rf "$WORK/queue"
run_hook tool_start "{\"session_id\":\"$SESSION\"}"
case "$(cut -f2 "$WT_STUB_LOG")" in
    *'"activity":"tool_start"'*) result=marked ;;
    *) result="unexpected: $(cut -f2 "$WT_STUB_LOG")" ;;
esac
check "PreToolUse marks the signal as a tool start" "$result" marked
check "the tool start is a heartbeat" "$(cut -f1 "$WT_STUB_LOG")" \
    "http://worktime.test/api/agent/sessions/$SESSION/heartbeat"

reset_log tz
rm -rf "$WORK/queue"
run_hook start "{\"session_id\":\"$SESSION\",\"cwd\":\"/tmp/project\"}"
case "$(cut -f2 "$WT_STUB_LOG")" in
    *'"tz_offset_min":'[-0-9]*) result=sent ;;
    *) result="missing: $(cut -f2 "$WT_STUB_LOG")" ;;
esac
check "start carries the local UTC offset in minutes" "$result" sent

# --- the hook always exits 0 ----------------------------------------------------
reset_log exitcode
rm -rf "$WORK/queue"
WT_STUB_CODE=500
run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
status=$?
WT_STUB_CODE=200
check "a failing server still exits 0" "$status" 0

if [ "$FAILURES" -gt 0 ]; then
    printf '\n%s test(s) failed\n' "$FAILURES"
    exit 1
fi
printf '\nall hook tests passed\n'
