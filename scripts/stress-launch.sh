#!/usr/bin/env bash
# stress-launch.sh — Smoke-test parallel terminal session launch against a
# running `htmlgraph serve` instance.
#
# Usage:  bash scripts/stress-launch.sh
# Exits 0 on full pass, 1 on any criterion failure, 2 if serve is not running.
#
# Criteria tested:
#   1. POST /api/terminal/start x4 (concurrent) — all succeed with distinct id + port
#   2. GET  /api/terminal/sessions — returns 4 entries
#   3. POST /api/terminal/stop-all — succeeds (HTTP 200)
#   4. GET  /api/terminal/sessions — returns 0 non-exited entries (or empty)
set -euo pipefail

BASE_URL="${HTMLGRAPH_URL:-http://localhost:8080}"
PASS=0
FAIL=0

pass() { echo "PASS: $*"; ((PASS++)) || true; }
fail() { echo "FAIL: $*"; ((FAIL++)) || true; }

# ── Prerequisite: serve must be reachable ────────────────────────────────────
if ! curl -sf --max-time 3 "${BASE_URL}/" >/dev/null 2>&1; then
  echo "ERROR: htmlgraph serve is not reachable at ${BASE_URL}" >&2
  echo "       Start it with: htmlgraph serve" >&2
  exit 2
fi

# ── Clean slate: stop any existing sessions ──────────────────────────────────
curl -sf -X POST "${BASE_URL}/api/terminal/stop-all" >/dev/null 2>&1 || true
sleep 0.5

# ── Criterion 1: POST /api/terminal/start x4 concurrently ───────────────────
TMPDIR_STRESS="$(mktemp -d)"
trap 'rm -rf "$TMPDIR_STRESS"' EXIT

for i in 1 2 3 4; do
  curl -sf -X POST \
    -H 'Content-Type: application/json' \
    -d '{}' \
    "${BASE_URL}/api/terminal/start" \
    -o "${TMPDIR_STRESS}/resp${i}.json" &
done
wait

# Verify all 4 responses exist and contain id + port.
all_ok=true
declare -A seen_ids
declare -A seen_ports

for i in 1 2 3 4; do
  resp="${TMPDIR_STRESS}/resp${i}.json"
  if [[ ! -f "$resp" ]]; then
    fail "start request ${i}: response file missing"
    all_ok=false
    continue
  fi

  id="$(python3 -c "import sys,json; d=json.load(open('$resp')); print(d.get('id',''))" 2>/dev/null || true)"
  port="$(python3 -c "import sys,json; d=json.load(open('$resp')); print(d.get('port','0'))" 2>/dev/null || true)"

  if [[ -z "$id" ]]; then
    fail "start request ${i}: missing 'id' in response $(cat "$resp")"
    all_ok=false
    continue
  fi
  if [[ -z "$port" || "$port" == "0" ]]; then
    fail "start request ${i}: missing or zero 'port' in response $(cat "$resp")"
    all_ok=false
    continue
  fi
  if [[ -n "${seen_ids[$id]+_}" ]]; then
    fail "start request ${i}: duplicate id '$id'"
    all_ok=false
    continue
  fi
  if [[ -n "${seen_ports[$port]+_}" ]]; then
    fail "start request ${i}: duplicate port '$port'"
    all_ok=false
    continue
  fi
  seen_ids["$id"]=1
  seen_ports["$port"]=1
done

if $all_ok; then
  pass "4 concurrent starts returned distinct id+port pairs"
else
  fail "one or more start requests returned invalid/duplicate id or port"
fi

# ── Criterion 2: GET /api/terminal/sessions — expect 4 entries ──────────────
sleep 1  # brief settle time for state transitions
sessions_json="$(curl -sf "${BASE_URL}/api/terminal/sessions" 2>/dev/null || echo '[]')"
session_count="$(python3 -c "import sys,json; print(len(json.loads('''${sessions_json}''')))" 2>/dev/null || echo 0)"

if [[ "$session_count" -eq 4 ]]; then
  pass "GET /api/terminal/sessions returned 4 entries"
else
  fail "GET /api/terminal/sessions returned ${session_count} entries (expected 4)"
fi

# ── Criterion 3: POST /api/terminal/stop-all ────────────────────────────────
stop_http="$(curl -sf -o /dev/null -w '%{http_code}' -X POST "${BASE_URL}/api/terminal/stop-all" 2>/dev/null || echo 000)"
if [[ "$stop_http" == "200" ]]; then
  pass "POST /api/terminal/stop-all returned HTTP 200"
else
  fail "POST /api/terminal/stop-all returned HTTP ${stop_http} (expected 200)"
fi

# ── Criterion 4: GET /api/terminal/sessions — expect 0 non-exited ───────────
sleep 0.5
sessions_after="$(curl -sf "${BASE_URL}/api/terminal/sessions" 2>/dev/null || echo '[]')"
non_exited="$(python3 -c "
import json
sessions = json.loads('''${sessions_after}''')
print(sum(1 for s in sessions if s.get('state') != 'exited'))
" 2>/dev/null || echo 0)"

if [[ "$non_exited" -eq 0 ]]; then
  pass "After stop-all: 0 non-exited sessions"
else
  fail "After stop-all: ${non_exited} non-exited sessions remain"
fi

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "Results: ${PASS} passed, ${FAIL} failed"
if [[ "$FAIL" -gt 0 ]]; then
  exit 1
fi
exit 0
