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
# --config - means the real curl reads options from stdin, which is where the
# Authorization header is passed so the token never appears in argv; the stub reads it
# the same way and records it separately, so the test can prove that is still true.
argv="$*"
url=""
body=""
config=""
while [ $# -gt 0 ]; do
    case "$1" in
        -X) shift 2; continue ;;
        -d) body="$2"; shift 2; continue ;;
        -H) shift 2; continue ;;
        -m) shift 2; continue ;;
        -w|-o) shift 2; continue ;;
        --config) [ "$2" = "-" ] && config=$(cat); shift 2; continue ;;
        -sS|-s|-S) shift; continue ;;
        http*) url="$1"; shift; continue ;;
        *) shift ;;
    esac
done
[ -z "${WT_STUB_DELAY:-}" ] || sleep "$WT_STUB_DELAY"
[ -z "${WT_STUB_ARGV_LOG:-}" ] || printf '%s\n' "$argv" >> "$WT_STUB_ARGV_LOG"
[ -z "${WT_STUB_CONFIG_LOG:-}" ] || printf '%s\n' "$config" >> "$WT_STUB_CONFIG_LOG"
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

stat_mode() {
    stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1" 2>/dev/null || true
}

run_hook() (
    # run_hook EVENT PAYLOAD [QUEUE_DIR]
    queue_dir=${3:-$WORK/queue}
    printf '%s' "$2" | WORKTIME_QUEUE_DIR="$queue_dir" sh "$HOOK" "$1"
)

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

# --- the token never reaches the process arguments ------------------------------
# argv is world-readable through ps and /proc/<pid>/cmdline, and this runs on every
# tool call, so the credential goes to curl through a config on stdin instead.
reset_log token
rm -rf "$WORK/queue"
WT_STUB_ARGV_LOG="$WORK/argv.log"
WT_STUB_CONFIG_LOG="$WORK/config.log"
export WT_STUB_ARGV_LOG WT_STUB_CONFIG_LOG
: > "$WT_STUB_ARGV_LOG"
: > "$WT_STUB_CONFIG_LOG"
run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
check "the token is absent from the command line" "$(grep -c "$WORKTIME_TOKEN" "$WT_STUB_ARGV_LOG" || true)" 0
check "the token is delivered through the config on stdin"     "$(grep -c "Authorization: Bearer $WORKTIME_TOKEN" "$WT_STUB_CONFIG_LOG" || true)" 1
unset WT_STUB_ARGV_LOG WT_STUB_CONFIG_LOG

# --- an unreachable server spools, the next event flushes in order --------------
reset_log queue
rm -rf "$WORK/queue"
WT_STUB_CODE=000
run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
WT_STUB_CODE=200
check "unreachable requests are spooled" "$(find "$WORK/queue" -name '*.req' | wc -l | tr -d ' ')" 2
# The spooled bodies are read before the flush, so the ordering check below compares
# actual timestamps. Grepping the first line for a digit would pass in either order:
# every current epoch-ms value starts with the same digit.
spooled_first=$(tail -n +2 "$(find "$WORK/queue" -name '*.req' | sort | head -n 1)")
reset_log flush
run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
check "the queue is flushed with the next event" "$(wc -l < "$WT_STUB_LOG" | tr -d ' ')" 3
check "nothing is left in the queue" "$(find "$WORK/queue" -name '*.req' | wc -l | tr -d ' ')" 0
# The backlog has to land before the live event. The server ignores any signal at or
# before its watermark, so the other order lets the live heartbeat book the outage as a
# pause and then drops the spooled proof that it was work.
check "the backlog is delivered before the live event" \
    "$(head -n 1 "$WT_STUB_LOG" | cut -f2)" "$spooled_first"

# --- a stop replays the spooled start first -------------------------------------
# stop on a session the server has never seen is a 404, which the hook classifies as
# permanent and drops. Flushing first means the start has created the session by then.
reset_log stopflush
rm -rf "$WORK/queue"
WT_STUB_CODE=000
run_hook start "{\"session_id\":\"$SESSION\",\"cwd\":\"/tmp/project\"}"
WT_STUB_CODE=200
reset_log stopflush2
run_hook stop "{\"session_id\":\"$SESSION\",\"reason\":\"clear\"}"
check "the spooled start goes out before the stop" "$(head -n 1 "$WT_STUB_LOG" | cut -f1)" \
    "http://worktime.test/api/agent/sessions/$SESSION/start"
check "the stop follows it" "$(tail -n 1 "$WT_STUB_LOG" | cut -f1)" \
    "http://worktime.test/api/agent/sessions/$SESSION/stop"

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
check "the spooled backlog is drained" "$(find "$WORK/queue" -name '00[0-4]-spooled.req' | wc -l | tr -d ' ')" 0
check "the lock directory is released" "$(find "$WORK/queue" -name '.lock' | wc -l | tr -d ' ')" 0

# An event raised while a backlog is still queued joins the queue instead of jumping
# it: overtaking would push the server's watermark past everything still waiting, and
# the rest of the backlog would then be discarded as stale. One more hook, with nothing
# left ahead of it, drains what remains and goes out live.
reset_log drain
run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
check "a deferred event is delivered by the next hook" \
    "$(find "$WORK/queue" -name '*.req' | wc -l | tr -d ' ')" 0

# --- the client name travels with the start -------------------------------------
# The same script serves Codex, whose hook payloads are identical. The server names
# an untitled entry after this field, so without it Codex work is filed as Claude's.
reset_log source
rm -rf "$WORK/queue"
run_hook start "{\"session_id\":\"$SESSION\",\"cwd\":\"/tmp/project\"}"
case "$(cut -f2 "$WT_STUB_LOG")" in
    *'"source":"claude-code"'*) result=default ;;
    *) result="unexpected: $(cut -f2 "$WT_STUB_LOG")" ;;
esac
check "start defaults to claude-code" "$result" default

reset_log source_codex
rm -rf "$WORK/queue"
WORKTIME_AGENT_SOURCE=codex run_hook start "{\"session_id\":\"$SESSION\",\"cwd\":\"/tmp/project\"}"
case "$(cut -f2 "$WT_STUB_LOG")" in
    *'"source":"codex"'*) result=codex ;;
    *) result="unexpected: $(cut -f2 "$WT_STUB_LOG")" ;;
esac
check "WORKTIME_AGENT_SOURCE names the client" "$result" codex
WORKTIME_AGENT_SOURCE=claude-code

# --- one queue per instance -----------------------------------------------------
# The token in this process belongs to one instance. Replaying someone else's request
# would hand their server this token, and the 401 it answers with counts as a
# permanent rejection - so their event would be dropped as well. Their backlog must
# also not hold this instance's events hostage, nor eat the shared cap: a dead
# instance's queue would otherwise grow until this one could no longer spool at all.
reset_log foreign
rm -rf "$WORK/queue"
mkdir -p "$WORK/queue"
# Spooled by an older version, which kept one flat directory for the machine.
printf '%s\n%s' "http://other-instance.test/api/agent/sessions/$SESSION/heartbeat" '{"at":1}' \
    > "$WORK/queue/000-foreign.req"
printf '%s\n%s' "http://worktime.test/api/agent/sessions/$SESSION/heartbeat" '{"at":2}' \
    > "$WORK/queue/001-legacy.req"
run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
check "a foreign request is never replayed with this token" \
    "$(grep -c 'other-instance.test' "$WT_STUB_LOG" || true)" 0
check "a request left by an older flat queue is adopted and delivered" \
    "$(grep -c '"at":2' "$WT_STUB_LOG" || true)" 1
check "the adopted request goes out before the live event" \
    "$(head -n 1 "$WT_STUB_LOG" | cut -f2)" '{"at":2}'
check "the foreign request is left where its own hook will find it" \
    "$(find "$WORK/queue" -maxdepth 1 -name '000-foreign.req' | wc -l | tr -d ' ')" 1

# A foreign backlog at the cap must not stop this instance from spooling: the two
# queues are separate directories, so the cap is per instance.
foreign_index=1
while [ "$foreign_index" -lt 1000 ]; do
    printf '%s\n%s' "http://other-instance.test/api/agent/sessions/$SESSION/heartbeat" "{\"at\":$foreign_index}" \
        > "$WORK/queue/foreign-$foreign_index.req"
    foreign_index=$((foreign_index + 1))
done
reset_log foreigncap
WT_STUB_CODE=000
run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
WT_STUB_CODE=200
check "this instance spools into its own directory" \
    "$(find "$WORK/queue" -mindepth 2 -name '*.req' | wc -l | tr -d ' ')" 1
check "a foreign cap-sized backlog remains untouched" \
    "$(find "$WORK/queue" -maxdepth 1 -name '*foreign*.req' | wc -l | tr -d ' ')" 1000
check "no half-written spool file is left behind" \
    "$(find "$WORK/queue" -name '*.tmp' | wc -l | tr -d ' ')" 0
rm -rf "$WORK/queue"

# --- v2 origin binding separates colliding v1 directories ----------------------
reset_log collision_one
rm -rf "$WORK/queue"
COLLISION_ONE='http://host:8080'
COLLISION_TWO='http://host/8080'
legacy_one=$(printf '%s' "$COLLISION_ONE" | tr -c 'A-Za-z0-9' '_')
legacy_two=$(printf '%s' "$COLLISION_TWO" | tr -c 'A-Za-z0-9' '_')
check "the regression origins really collide under the v1 mapping" "$legacy_one" "$legacy_two"
mkdir -p "$WORK/queue/$legacy_one"
printf '%s\n%s' "$COLLISION_ONE/api/agent/sessions/$SESSION/heartbeat" '{"at":1}' \
    > "$WORK/queue/$legacy_one/001-one.req"
printf '%s\n%s' "$COLLISION_TWO/api/agent/sessions/$SESSION/heartbeat" '{"at":2}' \
    > "$WORK/queue/$legacy_one/002-two.req"

WORKTIME_URL="$COLLISION_ONE" WORKTIME_TOKEN=wt_one run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
check "the first origin adopts its own collided legacy request" \
    "$(grep -c '\"at\":1}' "$WT_STUB_LOG" || true)" 1
check "the first origin never replays the second origin with its token" \
    "$(grep -c "$COLLISION_TWO" "$WT_STUB_LOG" || true)" 0
check "the second origin's collided request is not deleted" \
    "$(find "$WORK/queue/$legacy_one" -name '002-two.req' | wc -l | tr -d ' ')" 1

reset_log collision_two
WORKTIME_URL="$COLLISION_TWO" WORKTIME_TOKEN=wt_two run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
check "the second origin later adopts and delivers its own request" \
    "$(grep -c "$COLLISION_TWO.*\"at\":2" "$WT_STUB_LOG" || true)" 1
bindings=$(find "$WORK/queue/v2" -name .origin -type f -exec cat {} \; 2>/dev/null | sort)
check "the colliding v1 origins have distinct exact v2 bindings" "$bindings" \
    "$(printf '%s\n%s' "$COLLISION_ONE" "$COLLISION_TWO" | sort)"

# If another process holds the adoption lock, a live event must not overtake an
# exact-origin legacy record. The later call drains the old record first.
WORKTIME_URL=http://worktime.test
WORKTIME_TOKEN=wt_test
WORKTIME_HOOK_LOG=
reset_log adoption_busy
rm -rf "$WORK/adoption-busy"
run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/adoption-busy"
busy_dir=$(find "$WORK/adoption-busy/v2" -name .origin -type f | head -n 1 | sed 's|/.origin$||')
mkdir "$busy_dir/.lock"
WT_STUB_CODE=000 run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/adoption-busy"
WT_STUB_CODE=200
check "spooling never releases another process's queue lock" \
    "$(find "$busy_dir" -maxdepth 1 -name '.lock' -type d | wc -l | tr -d ' ')" 1
printf '%s\n%s' "http://worktime.test/api/agent/sessions/$SESSION/heartbeat" '{"at":17}' \
    > "$WORK/adoption-busy/017-legacy.req"
reset_log adoption_blocked
WORKTIME_HOOK_LOG="$WORK/adoption-busy.log" \
    run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/adoption-busy"
check "a busy adoption lock prevents the live event overtaking legacy work" \
    "$(wc -l < "$WT_STUB_LOG" | tr -d ' ')" 0
check "a deferred legacy record is not deleted" \
    "$(find "$WORK/adoption-busy" -maxdepth 1 -name '017-legacy.req' | wc -l | tr -d ' ')" 1
check "the deferred event is explicit in the log" \
    "$(grep -c 'state=queue_drop detail=legacy_lock' "$WORK/adoption-busy.log" || true)" 1
rmdir "$busy_dir/.lock"
reset_log adoption_recovered
run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/adoption-busy"
check "the legacy record is delivered before work resumes" \
    "$(head -n 1 "$WT_STUB_LOG" | cut -f2)" '{"at":17}'

# A tampered or cryptographically colliding v2 binding fails closed. Neither a
# queued file nor a live event may be sent or deleted with the wrong token.
bound_dir=$(find "$WORK/queue/v2" -name .origin -type f -exec grep -lFx "$COLLISION_ONE" {} \; | head -n 1 | sed 's|/.origin$||')
printf '%s' "$COLLISION_TWO" > "$bound_dir/.origin"
printf '%s\n%s' "$COLLISION_ONE/api/agent/sessions/$SESSION/heartbeat" '{"at":3}' > "$bound_dir/003-bound.req"
reset_log binding_mismatch
WORKTIME_HOOK_LOG="$WORK/binding.log" \
WORKTIME_URL="$COLLISION_ONE" WORKTIME_TOKEN=wt_one run_hook heartbeat "{\"session_id\":\"$SESSION\"}"
check "a mismatched v2 binding sends nothing" "$(wc -l < "$WT_STUB_LOG" | tr -d ' ')" 0
check "a mismatched v2 binding deletes nothing" "$(find "$bound_dir" -name '003-bound.req' | wc -l | tr -d ' ')" 1
check "a mismatched v2 binding is observable" "$(grep -c 'state=binding_mismatch' "$WORK/binding.log" || true)" 1
WORKTIME_URL=http://worktime.test
WORKTIME_TOKEN=wt_test
WORKTIME_HOOK_LOG=

# A broken hash utility must not collapse every instance into one directory.
# The fallback encodes every URL byte and keeps every path component below the
# common 255-byte filesystem limit.
mkdir -p "$WORK/bad-hash-bin"
printf '#!/bin/sh\nprintf %s\\n not-a-hash\n' > "$WORK/bad-hash-bin/sha256sum"
chmod +x "$WORK/bad-hash-bin/sha256sum"
saved_path=$PATH
PATH="$WORK/bad-hash-bin:$PATH" run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/fallback"
PATH=$saved_path
fallback_origin=$(find "$WORK/fallback/v2/hex" -name .origin -type f | head -n 1)
check "an invalid hash result uses the lossless fallback" "$(cat "$fallback_origin")" "http://worktime.test"
fallback_components_ok=1
relative_fallback=${fallback_origin#"$WORK/fallback/v2/hex/"}
old_ifs=$IFS
IFS=/
for component in $relative_fallback; do
    [ "${#component}" -le 64 ] || fallback_components_ok=0
done
IFS=$old_ifs
check "fallback queue components are at most 64 characters" "$fallback_components_ok" 1

# --- queue and log files are private, outcomes are honest -----------------------
reset_log private
rm -rf "$WORK/private-queue" "$WORK/private.log"
WT_STUB_CODE=000
WORKTIME_HOOK_LOG="$WORK/private.log" \
    run_hook start "{\"session_id\":\"$SESSION\",\"cwd\":\"/secret/project\",\"hook_event_name\":\"SessionStart\",\"source\":\"startup\"}" "$WORK/private-queue"
WT_STUB_CODE=200
private_req=$(find "$WORK/private-queue" -name '*.req' | head -n 1)
private_dir=${private_req%/*}
check "a new queue request is mode 600" "$(stat_mode "$private_req")" 600
check "an origin queue directory is mode 700" "$(stat_mode "$private_dir")" 700
check "an origin binding is mode 600" "$(stat_mode "$private_dir/.origin")" 600
check "the hook log is mode 600" "$(stat_mode "$WORK/private.log")" 600
check "the log records transient queueing" "$(grep -c 'state=queued' "$WORK/private.log" || true)" 1
check "the log keeps sanitized hook provenance" \
    "$(grep -c 'state=seen detail=name_SessionStart_source_startup_reason_none' "$WORK/private.log" || true)" 1
check "the token is absent from the hook log" "$(grep -c 'wt_test' "$WORK/private.log" || true)" 0
check "request bodies are absent from the hook log" "$(grep -c '/secret/project' "$WORK/private.log" || true)" 0
check "origins are absent from the hook log" "$(grep -c 'worktime.test' "$WORK/private.log" || true)" 0

for transient in 408 429 503; do
    reset_log "transient-$transient"
    rm -rf "$WORK/transient-$transient"
    WT_STUB_CODE=$transient \
        run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/transient-$transient"
    check "HTTP $transient is retained for retry" \
        "$(find "$WORK/transient-$transient" -name '*.req' | wc -l | tr -d ' ')" 1
done
WT_STUB_CODE=200

reset_log permanent
rm -rf "$WORK/permanent" "$WORK/permanent.log"
WT_STUB_CODE=401 WORKTIME_HOOK_LOG="$WORK/permanent.log" \
    run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/permanent"
WT_STUB_CODE=200
check "a permanent 4xx is not queued forever" "$(find "$WORK/permanent" -name '*.req' | wc -l | tr -d ' ')" 0
check "a permanent rejection is visible" "$(grep -c 'state=rejected.*http_401' "$WORK/permanent.log" || true)" 1

# Queue creation can succeed while the final atomic rename still fails. Stub
# only mv so the regression reaches that distinct write outcome.
reset_log write_failure
rm -rf "$WORK/write-failure" "$WORK/write-failure.log" "$WORK/bad-mv-bin"
run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/write-failure"
mkdir -p "$WORK/bad-mv-bin"
printf '#!/bin/sh\nexit 1\n' > "$WORK/bad-mv-bin/mv"
chmod +x "$WORK/bad-mv-bin/mv"
saved_path=$PATH
WT_STUB_CODE=000 WORKTIME_HOOK_LOG="$WORK/write-failure.log" PATH="$WORK/bad-mv-bin:$PATH" \
    run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/write-failure"
PATH=$saved_path
WT_STUB_CODE=200
check "an atomic rename failure is reported as a write drop" \
    "$(grep -c 'state=queue_drop detail=write' "$WORK/write-failure.log" || true)" 1
check "a failed atomic rename leaves no partial request" \
    "$(find "$WORK/write-failure" -name '*.tmp' -o -name '*.req' | wc -l | tr -d ' ')" 0

# The FIFO is intentionally bounded. Older accepted requests stay in order; the
# next event is dropped visibly once 1000 files are retained.
reset_log cap
rm -rf "$WORK/cap" "$WORK/cap.log"
WT_STUB_CODE=000 WORKTIME_HOOK_LOG="$WORK/cap.log" \
    run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/cap"
cap_dir=$(find "$WORK/cap/v2" -name .origin -type f | head -n 1 | sed 's|/.origin$||')
cap_index=1
while [ "$cap_index" -lt 1000 ]; do
    printf '%s\n%s' "http://worktime.test/api/agent/sessions/$SESSION/heartbeat" "{\"at\":$cap_index}" \
        > "$cap_dir/1000000000000-$cap_index.req"
    cap_index=$((cap_index + 1))
done
WT_STUB_CODE=000 WORKTIME_HOOK_LOG="$WORK/cap.log" \
    run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/cap"
WT_STUB_CODE=200
check "the queue stays at its 1000-file bound" "$(find "$cap_dir" -name '*.req' | wc -l | tr -d ' ')" 1000
check "a cap drop is explicit in the log" "$(grep -c 'state=queue_drop detail=cap' "$WORK/cap.log" || true)" 1
rm -f "$(find "$cap_dir" -name '*.req' | head -n 1)"
: > "$WORK/cap.log"
WT_STUB_CODE=000 WORKTIME_HOOK_LOG="$WORK/cap.log" \
    run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/cap" &
WT_STUB_CODE=000 WORKTIME_HOOK_LOG="$WORK/cap.log" \
    run_hook heartbeat "{\"session_id\":\"$SESSION\"}" "$WORK/cap" &
wait
WT_STUB_CODE=200
check "concurrent writers cannot exceed the queue bound" \
    "$(find "$cap_dir" -name '*.req' | wc -l | tr -d ' ')" 1000
check "exactly one of two writers is dropped at 999" \
    "$(grep -c 'state=queue_drop detail=cap' "$WORK/cap.log" || true)" 1
check "the spool lock is released" "$(find "$cap_dir" -name '.spool-lock' | wc -l | tr -d ' ')" 0

# Failure to create local storage is non-fatal and visible; a tracking hook may
# never block the editor even when both the server and its queue are unavailable.
reset_log unavailable
rm -f "$WORK/unavailable.log"
WT_STUB_CODE=000 WORKTIME_HOOK_LOG="$WORK/unavailable.log" \
    run_hook heartbeat "{\"session_id\":\"$SESSION\"}" /dev/null
WT_STUB_CODE=200
check "unavailable queue storage is explicit in the log" \
    "$(grep -c 'state=queue_drop detail=unavailable' "$WORK/unavailable.log" || true)" 1

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
