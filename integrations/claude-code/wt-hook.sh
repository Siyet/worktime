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
#   - Transient failures are spooled to $WORKTIME_QUEUE_DIR (default
#     ~/.worktime/queue) with original timestamps and flushed on the next event.
#     The FIFO is bounded at 1000 files; a cap/write drop is visible in the
#     optional log. The server is idempotent, replays are safe.

set -u
# Queue files contain hook payloads (including cwd and git branch). Apply the
# private mask before the first possible log, directory or temporary-file write;
# chmod after creation would still leave a world-readable race window.
umask 077

EVENT="${1:-heartbeat}"
BASE_URL="${WORKTIME_URL:-}"
TOKEN="${WORKTIME_TOKEN:-}"
QUEUE_ROOT="${WORKTIME_QUEUE_DIR:-$HOME/.worktime/queue}"
FLUSH_LIMIT=20
QUEUE_LIMIT=1000

[ -n "$BASE_URL" ] && [ -n "$TOKEN" ] || exit 0

# The v1 directory replaced every punctuation character with `_`, so distinct
# origins such as http://host:8080 and http://host/8080 collided. v2 hashes the
# exact URL. sha256sum is present on GNU/Linux and Git Bash; macOS ships shasum.
# The dependency-free fallback hex-encodes every byte losslessly and splits the
# path into 64-character components, well below filesystem name limits.
origin_queue_dir() {
    digest=""
    if command -v sha256sum >/dev/null 2>&1; then
        digest=$(printf '%s' "$BASE_URL" | sha256sum 2>/dev/null | awk '{print $1}')
    elif command -v shasum >/dev/null 2>&1; then
        digest=$(printf '%s' "$BASE_URL" | shasum -a 256 2>/dev/null | awk '{print $1}')
    fi
    case "$digest" in
        *[!0-9a-fA-F]*|'') digest="" ;;
    esac
    if [ "${#digest}" -eq 64 ]; then
        printf '%s/v2/sha256/%s' "$QUEUE_ROOT" "$digest"
        return
    fi

    encoded=$(printf '%s' "$BASE_URL" | od -An -tx1 2>/dev/null | tr -d ' \n')
    case "$encoded" in
        *[!0-9a-fA-F]*|'') printf '%s/v2/unavailable' "$QUEUE_ROOT"; return ;;
    esac
    path="$QUEUE_ROOT/v2/hex"
    while [ -n "$encoded" ]; do
        component=$(printf '%.64s' "$encoded")
        path="$path/$component"
        encoded=${encoded#"$component"}
    done
    printf '%s' "$path"
}

QUEUE_DIR=$(origin_queue_dir)
ORIGIN_FILE="$QUEUE_DIR/.origin"
# Both layouts shipped before v2 are adopted by exact request URL below.
LEGACY_QUEUE_DIR="$QUEUE_ROOT/$(printf '%s' "$BASE_URL" | tr -c 'A-Za-z0-9' '_')"
LOCK_DIR="$QUEUE_DIR/.lock"
SPOOL_LOCK_DIR="$QUEUE_DIR/.spool-lock"
QUEUE_READY=0
OWNS_QUEUE_LOCK=0
OWNS_SPOOL_LOCK=0

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

# Diagnostic values are useful only as small correlation labels. Keeping a
# conservative alphabet prevents a hook payload from forging another log line.
sanitize_log_value() {
    value=$(printf '%s' "$1" | tr -c 'A-Za-z0-9._:-' '_')
    printf '%.64s' "$value"
}

SESSION_ID=$(json_string session_id)
case "$SESSION_ID" in
    *[!0-9a-fA-F-]*|'') exit 0 ;;
esac

# Debug records deliberately contain no URL, token or request body. The full
# session UUID was already part of the old event log and is the correlation key
# needed to investigate missing time.
log_record() {
    [ -z "${WORKTIME_HOOK_LOG:-}" ] || {
        printf '%s session=%s event=%s state=%s detail=%s\n' \
            "$(now_ms)" "$SESSION_ID" "$EVENT" "$1" "$2" >> "$WORKTIME_HOOK_LOG" 2>/dev/null || true
        chmod 600 "$WORKTIME_HOOK_LOG" 2>/dev/null || true
    }
}

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
            # The leading zero is stripped by hand rather than with bash's 10# prefix:
            # this script runs under whatever /bin/sh is, and in dash 10# is a syntax
            # error, while a bare 08 or 09 would be read as invalid octal.
            hours=$(printf '%s' "$zone" | cut -c2-3 | sed 's/^0//')
            minutes=$(printf '%s' "$zone" | cut -c4-5 | sed 's/^0//')
            total=$(( ${hours:-0} * 60 + ${minutes:-0} ))
            [ "$sign" = "-" ] && total=$((-total))
            printf '%s' "$total"
            ;;
    esac
}

# send URL BODY -> 0 delivered, 1 rejected (permanent, drop), 2 unreachable (retry).
# SEND_CODE is kept only for a body-free diagnostic record.
#
# The token goes to curl through a config on stdin rather than as an argument: argv is
# world-readable through ps and /proc/<pid>/cmdline, and this runs on every tool call.
# stdin is free by now - the hook payload was read in full at startup.
send() {
    code=$(printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" |
        curl -sS -o /dev/null -w '%{http_code}' -m 3 -X POST "$1" \
        --config - \
        -H "Content-Type: application/json" \
        -d "$2" 2>/dev/null) || code=000
    SEND_CODE=$code
    case "$code" in
        2*) return 0 ;;
        000|408|429|5*) return 2 ;;
        *) return 1 ;;
    esac
}

# Creates and binds the v2 queue to this exact origin. Noclobber makes the
# binding an atomic claim when several hooks start together. A mismatched
# binding is a fail-closed security error: do not send, delete or inspect files
# in a directory that belongs to another origin.
ensure_queue() {
    mkdir -p "$QUEUE_DIR" 2>/dev/null || return 1
    chmod 700 "$QUEUE_DIR" 2>/dev/null || true
    if [ ! -e "$ORIGIN_FILE" ]; then
        (set -C; printf '%s\n' "$BASE_URL" > "$ORIGIN_FILE") 2>/dev/null || true
    fi
    owner=$(cat "$ORIGIN_FILE" 2>/dev/null || true)
    if [ "$owner" != "$BASE_URL" ]; then
        log_record binding_mismatch fail_closed
        return 2
    fi
    chmod 600 "$ORIGIN_FILE" 2>/dev/null || true
    QUEUE_READY=1
    return 0
}

# The queue is shared by every hook process of every session, and hooks run
# concurrently, so an unlocked flush sends the same file twice. mkdir is atomic
# everywhere, which makes it the lock. A hook killed mid-flush would leave the
# lock behind forever and silently freeze the queue, hence both the trap and the
# age-based takeover.
cleanup_owned_locks() {
    if [ "$OWNS_QUEUE_LOCK" -eq 1 ]; then
        rmdir "$LOCK_DIR" 2>/dev/null || true
        OWNS_QUEUE_LOCK=0
    fi
    if [ "$OWNS_SPOOL_LOCK" -eq 1 ]; then
        rmdir "$SPOOL_LOCK_DIR" 2>/dev/null || true
        OWNS_SPOOL_LOCK=0
    fi
}

install_lock_trap() {
    trap 'cleanup_owned_locks; exit 0' EXIT INT TERM
}

refresh_lock_trap() {
    if [ "$OWNS_QUEUE_LOCK" -eq 1 ] || [ "$OWNS_SPOOL_LOCK" -eq 1 ]; then
        install_lock_trap
    else
        trap - EXIT INT TERM
    fi
}

lock_queue() {
    [ "$QUEUE_READY" -eq 1 ] || return 1
    if [ -d "$LOCK_DIR" ] && [ -n "$(find "$LOCK_DIR" -maxdepth 0 -mmin +5 2>/dev/null)" ]; then
        rmdir "$LOCK_DIR" 2>/dev/null || true
    fi
    mkdir "$LOCK_DIR" 2>/dev/null || return 1
    OWNS_QUEUE_LOCK=1
    install_lock_trap
    return 0
}

unlock_queue() {
    if [ "$OWNS_QUEUE_LOCK" -eq 1 ]; then
        rmdir "$LOCK_DIR" 2>/dev/null || true
        OWNS_QUEUE_LOCK=0
    fi
    refresh_lock_trap
}

# Spooling is independent of flushing because records appear through an atomic
# rename, but the cap check and that rename must be serialized with other
# writers or two simultaneous hooks can both observe 999 and grow past 1000.
lock_spool() {
    [ "$QUEUE_READY" -eq 1 ] || return 1
    if [ -d "$SPOOL_LOCK_DIR" ] && [ -n "$(find "$SPOOL_LOCK_DIR" -maxdepth 0 -mmin +5 2>/dev/null)" ]; then
        rmdir "$SPOOL_LOCK_DIR" 2>/dev/null || true
    fi
    attempts=0
    while ! mkdir "$SPOOL_LOCK_DIR" 2>/dev/null; do
        attempts=$((attempts + 1))
        [ "$attempts" -lt 100 ] || return 1
        sleep 0.01
    done
    OWNS_SPOOL_LOCK=1
    install_lock_trap
    return 0
}

unlock_spool() {
    if [ "$OWNS_SPOOL_LOCK" -eq 1 ]; then
        rmdir "$SPOOL_LOCK_DIR" 2>/dev/null || true
        OWNS_SPOOL_LOCK=0
    fi
    refresh_lock_trap
}

# Moves one exact-origin legacy request without overwriting a request already
# carrying the same old filename. Keeping the timestamp prefix preserves FIFO
# order even when two old layouts happened to use the same process id.
adopt_legacy_request() {
    spooled=$1
    legacy_request_belongs "$spooled" || return 0
    name=${spooled##*/}
    target="$QUEUE_DIR/$name"
    suffix=0
    while [ -e "$target" ]; do
        suffix=$((suffix + 1))
        target="$QUEUE_DIR/${name%.req}-legacy-$suffix.req"
    done
    chmod 600 "$spooled" 2>/dev/null || true
    if mv "$spooled" "$target" 2>/dev/null; then
        chmod 600 "$target" 2>/dev/null || true
        log_record legacy_adopted accepted
    else
        log_record queue_drop legacy_move
    fi
}

legacy_request_belongs() {
    [ -f "$1" ] || return 1
    legacy_url=$(head -n 1 "$1" 2>/dev/null)
    [ "${legacy_url#"$BASE_URL"/}" != "$legacy_url" ]
}

legacy_queue_pending() {
    for legacy_pending_file in "$QUEUE_ROOT"/*.req; do
        legacy_request_belongs "$legacy_pending_file" && return 0
    done
    if [ -d "$LEGACY_QUEUE_DIR" ]; then
        for legacy_pending_file in "$LEGACY_QUEUE_DIR"/*.req; do
            legacy_request_belongs "$legacy_pending_file" && return 0
        done
    fi
    return 1
}

# Requests may exist in the original flat root or the shipped v1 lossy origin
# directory. A collided v1 directory can contain several origins; only requests
# whose first-line URL begins with this exact BASE_URL plus `/` are claimed.
adopt_legacy_queue() {
    lock_spool || {
        log_record queue_drop legacy_lock
        return 1
    }
    for spooled in "$QUEUE_ROOT"/*.req; do
        adopt_legacy_request "$spooled"
    done
    if [ "$LEGACY_QUEUE_DIR" != "$QUEUE_DIR" ] && [ -d "$LEGACY_QUEUE_DIR" ]; then
        for spooled in "$LEGACY_QUEUE_DIR"/*.req; do
            adopt_legacy_request "$spooled"
        done
    fi
    unlock_spool
    return 0
}

prepare_queue() {
    ensure_queue
    status=$?
    [ "$status" -ne 2 ] || return 2
    if [ "$status" -ne 0 ]; then
        if legacy_queue_pending; then
            log_record queue_drop legacy_unavailable
            return 3
        fi
        return 1
    fi
    if ! lock_queue; then
        if legacy_queue_pending; then
            log_record queue_drop legacy_lock
            return 3
        fi
        return 0
    fi
    if ! adopt_legacy_queue; then
        unlock_queue
        if legacy_queue_pending; then
            log_record queue_drop legacy_lock
            return 3
        fi
        return 0
    fi
    unlock_queue
    return 0
}

flush_queue() {
    [ "$QUEUE_READY" -eq 1 ] || return 0
    lock_queue || return 0
    adopt_legacy_queue
    flushed=0
    for spooled in "$QUEUE_DIR"/*.req; do
        [ -f "$spooled" ] || continue
        [ "$flushed" -lt "$FLUSH_LIMIT" ] || break
        url=$(head -n 1 "$spooled")
        body=$(tail -n +2 "$spooled")
        send "$url" "$body"
        case $? in
            0)
                rm -f "$spooled"
                log_record flush_delivered "http_$SEND_CODE"
                ;;
            1)
                rm -f "$spooled"
                log_record flush_rejected "http_$SEND_CODE"
                ;;
            2)
                log_record flush_retry "http_$SEND_CODE"
                break
                ;;
        esac
        flushed=$((flushed + 1))
    done
    unlock_queue
}

spool_request() {
    [ "$QUEUE_READY" -eq 1 ] || {
        log_record queue_drop unavailable
        return 1
    }
    lock_spool || {
        log_record queue_drop spool_lock
        return 1
    }
    # Cap the queue so a long outage cannot fill the disk. Only spooled requests
    # count; the lock directory must not push the queue over the cap.
    count=$(find "$QUEUE_DIR" -maxdepth 1 -name '*.req' 2>/dev/null | wc -l)
    if [ "$count" -ge "$QUEUE_LIMIT" ]; then
        unlock_spool
        log_record queue_drop cap
        return 1
    fi
    spool="$QUEUE_DIR/$(now_ms)-$$.req"
    # Written aside and moved into place: a reader that catches the file between
    # creation and its first line would see an empty queue and let the live event
    # overtake the backlog, which is the one thing the queue exists to prevent.
    if { printf '%s\n' "$1"; printf '%s' "$2"; } > "$spool.tmp" 2>/dev/null &&
        mv "$spool.tmp" "$spool" 2>/dev/null; then
        chmod 600 "$spool" 2>/dev/null || true
        unlock_spool
        log_record queued transient
        return 0
    fi
    rm -f "$spool.tmp" 2>/dev/null || true
    unlock_spool
    log_record queue_drop write
    return 1
}

queue_pending() {
    [ -d "$QUEUE_DIR" ] || return 1
    [ -n "$(find "$QUEUE_DIR" -maxdepth 1 -name '*.req' 2>/dev/null | head -n 1)" ]
}

deliver() {
    # A backlog the flush could not finish - it drains at most FLUSH_LIMIT per hook -
    # means this event must not overtake it. The server ignores anything at or before
    # its watermark, so sending the live event now would move the watermark past
    # everything still queued and the next flush would deliver it into a no-op, with
    # the outage recorded as idle time. Spooling keeps the order; the next hook
    # continues the drain.
    if queue_pending; then
        spool_request "$1" "$2"
        return 0
    fi
    send "$1" "$2"
    case $? in
        0) log_record delivered "http_$SEND_CODE" ;;
        1) log_record rejected "http_$SEND_CODE" ;;
        2) spool_request "$1" "$2" || true ;;
    esac
    return 0
}

SESSION_URL="$BASE_URL/api/agent/sessions/$SESSION_ID"
REASON=$(json_string reason)
HOOK_NAME=$(sanitize_log_value "$(json_string hook_event_name)")
HOOK_SOURCE=$(sanitize_log_value "$(json_string source)")
HOOK_REASON=$(sanitize_log_value "$REASON")
log_record seen "name_${HOOK_NAME:-none}_source_${HOOK_SOURCE:-none}_reason_${HOOK_REASON:-none}"

# Adopt old flat/v1 requests before deciding whether this event may go live.
# A binding mismatch is the one queue failure that also blocks live delivery:
# sending from a directory claimed by another origin could expose its requests
# to this origin's token. Ordinary unwritable storage still allows a live call.
prepare_queue
prepare_status=$?
case "$prepare_status" in
    2|3) exit 0 ;;
esac

# The backlog goes out before the current event. Delivering the live one first defeats
# the queue twice over: the server ignores any signal at or before its watermark, so a
# live heartbeat after an outage books the whole gap as a pause and the spooled
# heartbeats that prove it was work are then discarded as stale; and a stop for a
# session whose start is still spooled hits a session that does not exist yet, which is
# a 404 - permanent, dropped, never retried.
#
# SessionStart is the exception: it is the one synchronous hook (5 second budget), so
# it flushes afterwards. prepare_queue has already adopted legacy requests; if any
# exist, deliver() spools this start behind them instead of letting it overtake.
# Draining an empty queue costs nothing, and an unreachable server stops on the first.
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
        # The source names the client, and the server puts it in the entry name until
        # a task is set. The same script runs under Codex, whose hook events carry the
        # same field names - without this every Codex session would be filed as
        # "Claude Code #ab12cd34".
        AGENT_SOURCE=$(json_escape "${WORKTIME_AGENT_SOURCE:-claude-code}")
        deliver "$SESSION_URL/start" \
            "{\"started_at\":$(now_ms),\"source\":\"$AGENT_SOURCE\",\"cwd\":\"$CWD_JSON\",\"git_branch\":\"$BRANCH\"$TZ_FIELD}"
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
