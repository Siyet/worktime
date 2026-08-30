#!/usr/bin/env bash
set -euo pipefail

deadline_seconds="${1:-300}"
retry_seconds="${2:-10}"

if ! [[ "$deadline_seconds" =~ ^[1-9][0-9]*$ ]] || ! [[ "$retry_seconds" =~ ^[1-9][0-9]*$ ]]; then
  echo "attestation deadline and retry interval must be positive integers" >&2
  exit 2
fi
if ! [[ "${VERSION:-}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "VERSION must be a stable SemVer tag" >&2
  exit 2
fi
if ! [[ "${SOURCE_SHA:-}" =~ ^[0-9a-f]{40}$ ]]; then
  echo "SOURCE_SHA must be a lowercase 40-character Git revision" >&2
  exit 2
fi
if [[ "${GITHUB_REPOSITORY:-}" != "Siyet/worktime" ]]; then
  echo "release attestation polling is restricted to Siyet/worktime" >&2
  exit 2
fi
if ! command -v timeout >/dev/null 2>&1; then
  echo "GNU timeout is required for bounded attestation polling" >&2
  exit 2
fi

started_ns="$(node -e 'process.stdout.write(process.hrtime.bigint().toString())')"

remaining_seconds() {
  node -e '
    const started = BigInt(process.argv[1]);
    const budget = BigInt(process.argv[2]) * 1000000000n;
    const remaining = budget - (process.hrtime.bigint() - started);
    process.stdout.write(remaining <= 0n ? "0" : String(remaining / 1000000000n));
  ' "$started_ns" "$deadline_seconds"
}

pending_tag="no attestations for tag $VERSION (sha1:$SOURCE_SHA)"
pending_release="no attestations found for release $VERSION in worktime"

while true; do
  remaining="$(remaining_seconds)"
  if [[ "$remaining" -le 1 ]]; then
    echo "GitHub release attestation was not available before the ${deadline_seconds}s deadline" >&2
    exit 1
  fi
  call_timeout=$((remaining - 1))

  set +e
  verification_output="$(timeout --signal=TERM --kill-after=1s "${call_timeout}s" gh release verify "$VERSION" 2>&1)"
  verification_status=$?
  set -e
  if [[ "$verification_status" -eq 0 ]]; then
    printf '%s\n' "$verification_output"
    exit 0
  fi
  if [[ "$verification_status" -eq 124 || "$verification_status" -eq 137 ]]; then
    echo "GitHub release attestation verification exceeded its deadline" >&2
    exit 1
  fi
  if [[ "$verification_output" != "$pending_tag" && "$verification_output" != "$pending_release" ]]; then
    printf '%s\n' "$verification_output" >&2
    exit 1
  fi

  remaining="$(remaining_seconds)"
  if [[ "$remaining" -le 0 ]]; then
    echo "GitHub release attestation was not available before the ${deadline_seconds}s deadline" >&2
    exit 1
  fi
  sleep_for="$retry_seconds"
  if [[ "$sleep_for" -gt "$remaining" ]]; then
    sleep_for="$remaining"
  fi
  sleep "$sleep_for"
done
