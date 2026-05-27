#!/usr/bin/env bash
# JS complexity ratchet for the dashboard front-end.
#
# oxlint has no git-baseline mode (golangci-lint's --new-from-merge-base), so
# we ratchet on a committed violation COUNT instead: the gate fails when the
# dashboard JS carries MORE complexity violations than the recorded baseline.
# Pre-existing debt is tolerated; newly-introduced complexity is blocked —
# the same "tolerate existing, block new" stance as the Go gate (.golangci.yml).
#
# When you reduce violations, lower the number in .oxlint-baseline in the same
# change to lock in the gain (the gate prints a reminder when the count drops).
#
# Limitation: a count is coarser than golangci-lint's per-line diffing — it
# can't tell a removed violation from an added one, so swapping one for another
# nets zero. It is a forcing function against accumulation, not a proof.
set -euo pipefail

OXLINT_VERSION="1.67.0"
DASH_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../cmd/wipnote/dashboard" && pwd)"
BASELINE_FILE="$DASH_DIR/.oxlint-baseline"
RULE_RE='error eslint\((complexity|max-depth|max-nested-callbacks)\)'

baseline=$(tr -dc '0-9' < "$BASELINE_FILE" 2>/dev/null || true)
baseline=${baseline:-0}

output=$(cd "$DASH_DIR" && npx --yes "oxlint@${OXLINT_VERSION}" --config .oxlintrc.json js components 2>&1 || true)
count=$(printf '%s\n' "$output" | grep -cE "$RULE_RE" || true)

printf '%s\n' "$output" | grep -E "$RULE_RE" || true
echo "JS complexity violations: ${count} (baseline: ${baseline})"

if [ "$count" -gt "$baseline" ]; then
  echo "FAIL: new JS complexity introduced (${count} > ${baseline})."
  echo "      Refactor the offending function(s) below the thresholds in cmd/wipnote/dashboard/.oxlintrc.json."
  exit 1
fi
if [ "$count" -lt "$baseline" ]; then
  echo "Violations dropped — set .oxlint-baseline to ${count} in this change to lock in the improvement."
fi
echo "OK"
