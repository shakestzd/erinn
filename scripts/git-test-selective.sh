#!/usr/bin/env bash
# scripts/git-test-selective.sh - Selective testing for Go packages in quality/completion gates.
set -euo pipefail

REPO_ROOT="$(pwd)"

# Helper to find the nearest directory containing go.mod
find_module_root() {
    local dir="$1"
    while [ "$dir" != "$REPO_ROOT" ] && [ "$dir" != "/" ] && [ "$dir" != "." ]; do
        if [ -f "$dir/go.mod" ]; then
            echo "$dir"
            return 0
        fi
        dir=$(dirname "$dir")
    done
    echo "$REPO_ROOT"
}

# 1. Resolve files touched by the current work item (if specified)
FILES=()

if [ -n "${WIPNOTE_WORKITEM_ID:-}" ]; then
    echo "selective-test: tracing files for work item ${WIPNOTE_WORKITEM_ID}..."
    # Get JSON output from wipnote trace and extract files
    # Output is a list of absolute paths
    if TRACE_OUT=$(wipnote trace "${WIPNOTE_WORKITEM_ID}" --json 2>/dev/null); then
        # Parse files array from JSON
        # Example JSON: { "files": ["/path/to/file1.go", ...] }
        while read -r file; do
            if [ -n "$file" ]; then
                FILES+=("$file")
            fi
        done < <(echo "$TRACE_OUT" | jq -r '.files[]?' 2>/dev/null || true)
    fi
fi

# 2. If no work item files found, fall back to uncommitted files in git status/diff
if [ ${#FILES[@]} -eq 0 ]; then
    echo "selective-test: no work item files traced, checking git diff/status..."
    # Include both staged, unstaged, and untracked Go files
    while read -r file; do
        if [ -n "$file" ]; then
            # Resolve to absolute path
            FILES+=("${REPO_ROOT}/$file")
        fi
    done < <(git diff --name-only; git diff --cached --name-only; git status --porcelain | grep '??' | awk '{print $2}')
fi

# 3. Filter list to Go files
GO_FILES=()
for file in "${FILES[@]}"; do
    if [[ "$file" == *.go ]]; then
        GO_FILES+=("$file")
    fi
done

# 4. If no Go files were modified, exit 0 instantly
if [ ${#GO_FILES[@]} -eq 0 ]; then
    echo "selective-test: no Go files modified. Skipping tests."
    exit 0
fi

# 5. Resolve module roots and package relative paths
TMP_LIST=$(mktemp)
trap 'rm -f "$TMP_LIST"' EXIT

for file in "${GO_FILES[@]}"; do
    dir=$(dirname "$file")
    if [ -d "$dir" ]; then
        mod_root=$(find_module_root "$dir")
        # Convert absolute path to package path relative to module root
        rel_pkg="./$(realpath --relative-to="$mod_root" "$dir")"
        echo "$mod_root|$rel_pkg" >> "$TMP_LIST"
    fi
done

if [ ! -s "$TMP_LIST" ]; then
    echo "selective-test: no valid Go package directories found. Skipping tests."
    exit 0
fi

# 6. Group and execute tests per module root
sort -u "$TMP_LIST" -o "$TMP_LIST"

# Extract unique module roots
MOD_ROOTS=($(awk -F'|' '{print $1}' "$TMP_LIST" | sort -u))

for mod in "${MOD_ROOTS[@]}"; do
    # Extract packages for this module
    pkgs=($(grep "^${mod}|" "$TMP_LIST" | awk -F'|' '{print $2}'))
    
    mod_name=$(basename "$mod")
    if [ "$mod" = "$REPO_ROOT" ]; then
        mod_name="root"
    fi
    
    echo "selective-test: running tests in module [${mod_name}] for packages: ${pkgs[*]}"
    cd "$mod"
    go test -short "${pkgs[@]}"
    cd "$REPO_ROOT"
done
