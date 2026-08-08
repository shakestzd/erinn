#!/usr/bin/env bash
# scripts/go-test-streaming.sh - Run `go test` with per-event streaming
# output, so a hard process death mid-suite still names the last test that
# started (bug-61973a05).
#
# WHY THIS EXISTS: plain `go test [-v] ./...` buffers output PER PACKAGE —
# nothing is printed until that package's entire run finishes (documented in
# plugin/skills/code-quality-skill/SKILL.md's "Go suite timing" note; cmd/wipnote
# alone is ~2-3 minutes). If the test binary dies mid-package — a hard
# os.Exit, an unrecovered panic in a goroutine, an OOM kill — nothing for
# that package was ever flushed, and the run just stops with no name
# attached. A truncated suite and a complete suite that happened to find
# nothing wrong look identical from the outside, so lost coverage is
# invisible and any real failure later in the run is masked by it.
#
# `go test -json` does NOT have this problem: it emits one JSON object per
# event (a test starting, a line of its output, it passing/failing) and
# each one is written to stdout as it happens, not batched per package
# (verified for this repo: a synthetic test calling os.Exit() mid-run still
# left a `{"Action":"run","Test":"<the crasher>"}` record on disk with
# nothing after it — see bug-61973a05 for the induced-crash proof this
# script's behavior is based on). This script is the "make it the default
# so nobody has to remember a special flag" delivery for that property:
# pipe -json through jq to reconstruct the exact human-readable `-v` text
# for live viewing, while also keeping the raw JSONL log so a truncated run
# can still be inspected for the last test that started.
#
# USAGE:
#   scripts/go-test-streaming.sh [go test args, e.g. ./... or -run Pattern]
#
# Examples:
#   scripts/go-test-streaming.sh ./...
#   scripts/go-test-streaming.sh ./cmd/wipnote/...
#   scripts/go-test-streaming.sh ./cmd/wipnote/... -run TestWritableDBOpenBoundary
#
# Defaults to ./... when no package pattern is given. All arguments are
# passed straight through to `go test -json`.
#
# OUTPUT: human-readable test output streams to stdout as it happens (same
# text `go test -v` would print, reconstructed from the JSON `Output`
# field). The raw JSONL is preserved at the path printed at the end — if
# the run is truncated, find the last test that started with:
#   jq -r 'select(.Action=="run") | .Test' <log> | tail -1
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null)"; then
    :
else
    REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
fi
cd "$REPO_ROOT"

if ! command -v jq >/dev/null 2>&1; then
    echo "go-test-streaming: jq is required (reconstructs human-readable text from -json events)" >&2
    exit 1
fi

ARGS=("$@")
if [ ${#ARGS[@]} -eq 0 ]; then
    ARGS=("./...")
fi

LOG_DIR="${TMPDIR:-/tmp}/wipnote-go-test-streaming"
mkdir -p "$LOG_DIR"
LOG_FILE="${LOG_DIR}/$(date +%Y%m%dT%H%M%S).jsonl"

echo "go-test-streaming: go test -json ${ARGS[*]}"
echo "go-test-streaming: raw event log -> ${LOG_FILE}"
echo

set -o pipefail
go test -json "${ARGS[@]}" 2>&1 | tee "$LOG_FILE" | jq -r '.Output // empty'
STATUS=${PIPESTATUS[0]}

echo
if [ "$STATUS" -ne 0 ]; then
    LAST_RUN=$(jq -r 'select(.Action=="run") | .Test' "$LOG_FILE" 2>/dev/null | tail -1)
    if [ -n "$LAST_RUN" ]; then
        LAST_PACKAGE=$(jq -r --arg t "$LAST_RUN" 'select(.Action=="run" and .Test==$t) | .Package' "$LOG_FILE" 2>/dev/null | tail -1)
        LAST_CONCLUDED=$(jq -r --arg t "$LAST_RUN" 'select(.Test==$t and (.Action=="pass" or .Action=="fail" or .Action=="skip")) | .Action' "$LOG_FILE" 2>/dev/null | tail -1)
        if [ -z "$LAST_CONCLUDED" ]; then
            # Only the "never concluded" case is the anonymous-death signature
            # this script exists to catch — a concluded last test just means
            # a normal --- FAIL/PASS already printed above named its own
            # failure, and adding a second message here would only muddy it.
            echo "go-test-streaming: exit ${STATUS}. Last test to start was ${LAST_RUN} (${LAST_PACKAGE}) and it never concluded — the process most likely died inside it (hard exit, panic, or was killed)."
        fi
    else
        echo "go-test-streaming: exit ${STATUS}. No test ever started running — check build/compile output above."
    fi
    echo "go-test-streaming: full event log preserved at ${LOG_FILE}"
fi

exit "$STATUS"
