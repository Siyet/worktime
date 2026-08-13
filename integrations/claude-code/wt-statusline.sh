#!/bin/sh
# Claude Code statusLine for the WorkTime session timer. Claude supplies the
# current session as JSON on stdin; this reads its already-tracked entry without
# sending a heartbeat or otherwise changing billable time.

set -u

BASE_URL=${WORKTIME_URL:-}
TOKEN=${WORKTIME_TOKEN:-}
INPUT=$(cat 2>/dev/null || true)

[ -n "$BASE_URL" ] && [ -n "$TOKEN" ] || exit 0

json_string() {
    printf '%s' "$INPUT" |
        grep -o "\"$1\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" |
        head -n 1 |
        sed 's/^"[^"]*"[[:space:]]*:[[:space:]]*"//; s/"$//'
}

SESSION_ID=$(json_string session_id)
case "$SESSION_ID" in
    *[!0-9a-fA-F-]*|'') exit 0 ;;
esac

# Keep the token out of argv, where ps and /proc could expose it. A status line
# is decoration: an unavailable server produces no output and never delays the
# terminal for more than one second.
printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" |
    curl -fsS -m 1 --config - \
        "${BASE_URL%/}/api/agent/sessions/$SESSION_ID/status-line" 2>/dev/null || true

exit 0
