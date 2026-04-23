#!/usr/bin/env bash
# check-host-paths.sh — scan committed artifacts for host-local absolute paths
#
# Usage:
#   scripts/check-host-paths.sh [--staged] [--all] [FILE...]
#
# Modes:
#   --staged    scan only git-staged files (default when invoked from pre-commit)
#   --all       scan all files in scope (default when no mode specified)
#   FILE...     scan specific files
#
# Scope (which files are considered):
#   .htmlgraph/**   (excluding .htmlgraph/htmlgraph.db — binary)
#   .claude/**      (excluding .claude/settings.local.json — documented ephemeral state)
#   .claude/worktrees/ is also excluded (derived mirror, not source)
#
# What we scan for (host-local absolute path patterns):
#   /Users/<username>/
#   /home/<username>/          except /home/runner/ (GitHub Actions CI)
#   /workspaces/<username>/
#   /private/var/folders/
#
# Allowlisted files (never flagged):
#   .htmlgraph/bugs/bug-4b6d8369.html  — this bug's own HTML, cites paths as examples
#   scripts/host-paths-allowlist.txt   — optional per-project allowlist (one path per line)
#
# Exit codes:
#   0   clean (prints "OK — N files scanned")
#   1   violations found (prints "file:line: <path>" per hit)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

# ── Pattern ────────────────────────────────────────────────────────────────
# Extended regex for grep -E (no lookahead support).
# /home/runner/ is intentionally included in the ALLOWED set by using a
# negative match in the fallback: we scan with a broad pattern and then
# filter out /home/runner/ hits in post-processing.
#
# Broad pattern — catches all candidates:
BROAD_PATTERN='/Users/[^/[:space:]"'"'"'<>][^[:space:]"'"'"'<>]*/|/home/[^/[:space:]"'"'"'<>][^[:space:]"'"'"'<>]*/|/workspaces/[^/[:space:]"'"'"'<>][^[:space:]"'"'"'<>]*/|/private/var/folders/'

# ── Static allowlist (files never checked) ────────────────────────────────
STATIC_ALLOWLIST=(
    ".htmlgraph/bugs/bug-4b6d8369.html"
)

# Load optional per-project allowlist
ALLOWLIST_FILE="$REPO_ROOT/scripts/host-paths-allowlist.txt"
declare -a EXTRA_ALLOWLIST=()
if [[ -f "$ALLOWLIST_FILE" ]]; then
    while IFS= read -r line || [[ -n "$line" ]]; do
        # Skip blank lines and comments
        [[ -z "$line" || "$line" == \#* ]] && continue
        EXTRA_ALLOWLIST+=("$line")
    done < "$ALLOWLIST_FILE"
fi

is_allowlisted() {
    local rel_path="$1"
    for allow in "${STATIC_ALLOWLIST[@]}" "${EXTRA_ALLOWLIST[@]}"; do
        if [[ "$rel_path" == "$allow" ]]; then
            return 0
        fi
    done
    return 1
}

# ── File collection ────────────────────────────────────────────────────────
MODE="all"
EXPLICIT_FILES=()

for arg in "$@"; do
    case "$arg" in
        --staged) MODE="staged" ;;
        --all)    MODE="all" ;;
        *)        EXPLICIT_FILES+=("$arg") ;;
    esac
done

collect_files() {
    if [[ ${#EXPLICIT_FILES[@]} -gt 0 ]]; then
        printf '%s\n' "${EXPLICIT_FILES[@]}"
        return
    fi

    if [[ "$MODE" == "staged" ]]; then
        # Only staged files that fall under .htmlgraph/ or .claude/
        git -C "$REPO_ROOT" diff --cached --name-only | grep -E '^(\.htmlgraph/|\.claude/)' || true
    else
        # All files under .htmlgraph/ and .claude/
        find "$REPO_ROOT/.htmlgraph" "$REPO_ROOT/.claude" \
            -type f 2>/dev/null | \
            sed "s|^$REPO_ROOT/||"
    fi
}

# ── Exclusions ─────────────────────────────────────────────────────────────
is_excluded() {
    local rel_path="$1"
    # Binary DB
    [[ "$rel_path" == ".htmlgraph/htmlgraph.db" ]] && return 0
    # Ephemeral host-local settings (the point of that file is to store local paths)
    [[ "$rel_path" == ".claude/settings.local.json" ]] && return 0
    # .claude worktrees mirror (derived, not source)
    [[ "$rel_path" == .claude/worktrees/* ]] && return 0
    return 1
}

# ── CI runner allowance ────────────────────────────────────────────────────
# A grep hit is acceptable if it only references /home/runner/ (GitHub Actions).
is_ci_runner_only_hit() {
    local line="$1"
    # Strip everything that looks like /home/runner/...  and see if any
    # /home/ path remains. If not, it's a CI-only hit.
    local stripped
    stripped=$(echo "$line" | sed 's|/home/runner/[^[:space:]"'"'"'<>]*||g')
    # If /home/ still appears, there's a non-runner /home path
    if echo "$stripped" | grep -qE '/home/[^/[:space:]"'"'"'<>][^[:space:]"'"'"'<>]*/'; then
        return 1
    fi
    # Check for other flagged patterns (Users, workspaces, private/var)
    if echo "$stripped" | grep -qE '/Users/[^/[:space:]"'"'"'<>][^[:space:]"'"'"'<>]*/|/workspaces/[^/[:space:]"'"'"'<>][^[:space:]"'"'"'<>]*/|/private/var/folders/'; then
        return 1
    fi
    return 0
}

# ── Scan ──────────────────────────────────────────────────────────────────
VIOLATIONS=0
SCANNED=0

while IFS= read -r rel_path; do
    [[ -z "$rel_path" ]] && continue

    # When explicit files are given they may be absolute or CWD-relative paths.
    if [[ "$rel_path" == /* ]]; then
        abs_path="$rel_path"
    elif [[ ${#EXPLICIT_FILES[@]} -gt 0 ]]; then
        # Explicit file argument: resolve relative to CWD, not REPO_ROOT.
        abs_path="$(cd "$(pwd)" && realpath -e "$rel_path" 2>/dev/null || echo "$PWD/$rel_path")"
    else
        abs_path="$REPO_ROOT/$rel_path"
    fi

    [[ -f "$abs_path" ]] || continue

    if is_excluded "$rel_path"; then
        continue
    fi

    if is_allowlisted "$rel_path"; then
        continue
    fi

    SCANNED=$((SCANNED + 1))

    # Use grep -nE with broad pattern; filter /home/runner/ in post-processing
    hits=$(grep -nE "$BROAD_PATTERN" "$abs_path" 2>/dev/null || true)

    if [[ -n "$hits" ]]; then
        while IFS= read -r hit; do
            [[ -z "$hit" ]] && continue
            lineno="${hit%%:*}"
            content="${hit#*:}"

            # Skip lines that contain only CI runner paths
            if is_ci_runner_only_hit "$content"; then
                continue
            fi

            echo "$rel_path:$lineno: $content"
            VIOLATIONS=$((VIOLATIONS + 1))
        done <<< "$hits"
    fi
done < <(collect_files)

# ── Result ────────────────────────────────────────────────────────────────
if [[ $VIOLATIONS -gt 0 ]]; then
    echo "" >&2
    echo "FAIL: $VIOLATIONS host-local path hit(s) found in $SCANNED file(s)." >&2
    echo "      Remove or replace these paths before committing." >&2
    echo "      To permanently allowlist a file, add its repo-relative path to:" >&2
    echo "      scripts/host-paths-allowlist.txt" >&2
    exit 1
fi

echo "OK — $SCANNED files scanned, no host-local paths found."
