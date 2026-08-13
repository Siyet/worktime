#!/bin/sh
# Tests for wt-statusline.sh. The terminal decoration must be read-only, fast,
# and silent when it cannot identify or reach the current session.

set -u

STATUSLINE=$(cd "$(dirname "$0")" && pwd)/wt-statusline.sh
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
FAILURES=0

mkdir -p "$WORK/bin"
cat > "$WORK/bin/curl" <<'STUB'
#!/bin/sh
argv="$*"
url=""
config=""
while [ $# -gt 0 ]; do
    case "$1" in
        -m) shift 2 ;;
        --config) [ "$2" = "-" ] && config=$(cat); shift 2 ;;
        -f|-s|-S|-fsS) shift ;;
        http*) url="$1"; shift ;;
        *) shift ;;
    esac
done
printf '%s\n' "$argv" >> "$WT_STATUS_ARGV_LOG"
printf '%s\n' "$config" >> "$WT_STATUS_CONFIG_LOG"
printf '%s\n' "$url" >> "$WT_STATUS_URL_LOG"
printf '%s' 'WorkTime 0:12:34 · WORKTIME test'
STUB
chmod +x "$WORK/bin/curl"

PATH="$WORK/bin:$PATH"
export PATH
export WORKTIME_URL="http://worktime.test/"
export WORKTIME_TOKEN="wt_status_secret"
export WT_STATUS_ARGV_LOG="$WORK/argv.log"
export WT_STATUS_CONFIG_LOG="$WORK/config.log"
export WT_STATUS_URL_LOG="$WORK/url.log"
: > "$WT_STATUS_ARGV_LOG"
: > "$WT_STATUS_CONFIG_LOG"
: > "$WT_STATUS_URL_LOG"

SESSION="11111111-2222-3333-4444-555555555555"

check() {
    if [ "$2" = "$3" ]; then
        printf 'ok   %s\n' "$1"
    else
        printf 'FAIL %s\n     want: %s\n     got:  %s\n' "$1" "$3" "$2"
        FAILURES=$((FAILURES + 1))
    fi
}

output=$(printf '{"session_id":"%s","cwd":"/repo","tool_input":{"session_id":"bad"}}' "$SESSION" | sh "$STATUSLINE")
check "prints the server-provided timer" "$output" "WorkTime 0:12:34 · WORKTIME test"
check "reads the top-level session endpoint" "$(cat "$WT_STATUS_URL_LOG")" \
    "http://worktime.test/api/agent/sessions/$SESSION/status-line"
check "keeps the token out of argv" "$(grep -c wt_status_secret "$WT_STATUS_ARGV_LOG" || true)" 0
check "passes the token through curl config" "$(cat "$WT_STATUS_CONFIG_LOG")" \
    'header = "Authorization: Bearer wt_status_secret"'

before=$(wc -l < "$WT_STATUS_URL_LOG" | tr -d ' ')
printf '{"session_id":"../../etc/passwd"}' | sh "$STATUSLINE" >/dev/null
printf '{}' | sh "$STATUSLINE" >/dev/null
after=$(wc -l < "$WT_STATUS_URL_LOG" | tr -d ' ')
check "invalid or absent session ids make no request" "$after" "$before"

if [ "$FAILURES" -ne 0 ]; then
    printf '\n%d failure(s)\n' "$FAILURES"
    exit 1
fi
printf '\nall status line tests passed\n'
