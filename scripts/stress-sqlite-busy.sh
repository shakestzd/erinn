#!/usr/bin/env bash
# SQLITE_BUSY multi-harness contention validation for bug-74a7bda7.
#
# Reproduces the original failure scenario: the dashboard read pool + indexer
# contending with concurrent wipnote CLI writers (including the completion path)
# across parallel AI/CLI sessions. Run ONE instance per session/terminal.
#
# Usage:
#   scripts/stress-sqlite-busy.sh <claude|codex|term> [iterations]
#
# Run it the truest way (no AI needed) in two terminals at once:
#   scripts/stress-sqlite-busy.sh claude   &
#   scripts/stress-sqlite-busy.sh codex    &
# ...or paste "run scripts/stress-sqlite-busy.sh <name> and report the RESULT
# block" into each wipnote claude/codex session.
#
# The dashboard is auto-started by any wipnote launcher (ensureServeForDashboard);
# in a devcontainer it binds 0.0.0.0:8088. Override with WIPNOTE_STRESS_BASE.
# It does NOT need TMPDIR=... — that is only for go build/test/check-gate.
#
# PASS  = first_party_busy == 0  AND  lock_blocked_completes == 0
# Self-cleaning: every spike is created -> exercised -> deleted, plus a tag sweep.
# Note: only "database is locked"/SQLITE_BUSY counts as failure. A provenance/
# gate refusal on `complete` (if any) is NOT a contention failure; the
# create/start/add-step/delete writes still exercise heavy lock contention.

set -u

HARNESS="${1:-}"
ITERS="${2:-25}"
if [ -z "$HARNESS" ]; then
  echo "usage: $0 <claude|codex|term> [iterations]" >&2
  exit 2
fi

BASE="${WIPNOTE_STRESS_BASE:-http://127.0.0.1:8088}"
TAG="STRESS-${HARNESS}-$$-${RANDOM}"

command -v wipnote >/dev/null 2>&1 || { echo "wipnote not on PATH" >&2; exit 1; }
curl -fsS "$BASE/api/status" >/dev/null 2>&1 || curl -fsS "$BASE/" >/dev/null 2>&1 || {
  echo "dashboard not reachable at $BASE — start a wipnote launcher (auto-starts serve) or set WIPNOTE_STRESS_BASE" >&2
  exit 1
}

# wall-clock barrier so independently-started instances overlap maximally
sleepfor=$(( 60 - $(date +%S) ))
if [ "$sleepfor" -ge 1 ] 2>/dev/null; then
  echo "[$TAG] barrier: sleeping ${sleepfor}s"
  sleep "$sleepfor"
fi
echo "[$TAG] GO $(date -Ins 2>/dev/null || date)"

# background dashboard read-pool pressure (the shared-lock starvation source)
(
  for _ in $(seq 1 600); do
    curl -fsS "$BASE/api/sessions"        >/dev/null 2>&1
    curl -fsS "$BASE/api/events?limit=50" >/dev/null 2>&1
    curl -fsS "$BASE/api/stats"           >/dev/null 2>&1
  done
) &
RPID=$!

busy=0; ops=0; complete_fail=0; samples=""
scan() {
  if printf '%s' "$1" | grep -Eqi 'database is locked|SQLITE_BUSY|disk image is malformed'; then
    busy=$((busy + 1))
    samples="${samples}
[$2] $(printf '%s' "$1" | tr '\n' ' ' | cut -c1-200)"
  fi
}

n=0
while [ "$n" -lt "$ITERS" ]; do
  n=$((n + 1))
  o=$(wipnote spike create "[$TAG] iter $n" --description "bug-74a7bda7 sqlite-busy contention validation" 2>&1)
  ec=$?
  ops=$((ops + 1)); scan "$o" "create"
  id=$(printf '%s' "$o" | grep -oE 'spk-[0-9a-f]+' | head -1)
  if [ -z "$id" ]; then
    [ "$ec" -ne 0 ] && scan "$o" "create-exit"
    continue
  fi
  for c in "start $id" "add-step $id step-a" "add-step $id step-b" "add-step $id step-c" "complete $id"; do
    o=$(wipnote spike $c 2>&1)
    ec=$?
    ops=$((ops + 1)); scan "$o" "$c"
    case "$c" in
      complete*)
        if [ "$ec" -ne 0 ] && printf '%s' "$o" | grep -Eqi 'locked|busy'; then
          complete_fail=$((complete_fail + 1))
        fi
        ;;
    esac
  done
  wipnote spike delete "$id" >/dev/null 2>&1
done

kill "$RPID" 2>/dev/null
wait "$RPID" 2>/dev/null

# sweep any stragglers created by this run
for sid in $(wipnote find "$TAG" 2>/dev/null | grep -oE 'spk-[0-9a-f]+'); do
  wipnote spike delete "$sid" >/dev/null 2>&1
done

echo "==== RESULT [$TAG] ===="
echo "harness=$HARNESS ops=$ops first_party_busy=$busy lock_blocked_completes=$complete_fail"
[ -n "$samples" ] && printf 'busy samples:%s\n' "$samples"
if [ "$busy" -eq 0 ] && [ "$complete_fail" -eq 0 ]; then
  echo "VERDICT: PASS"
  exit 0
else
  echo "VERDICT: FAIL"
  exit 1
fi
