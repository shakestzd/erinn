#!/usr/bin/env bash
# scripts/git-test-selective.sh - Selective testing for Go packages in quality/completion gates.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null)"; then
    :
else
    REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
fi
cd "$REPO_ROOT"

collect_git_files() {
    git diff --name-only
    git diff --cached --name-only
    git status --porcelain | awk "\$1 == \"??\" {print \$2}"
}

append_file() {
    local file="$1"
    if [ -z "$file" ]; then
        return 0
    fi
    if [[ "$file" = /* ]]; then
        FILES+=("$file")
    else
        FILES+=("${REPO_ROOT}/$file")
    fi
}

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

FILES=()

if [ -n "${WIPNOTE_WORKITEM_ID:-}" ]; then
    echo "selective-test: tracing files for work item ${WIPNOTE_WORKITEM_ID}..."
    if ! command -v jq >/dev/null 2>&1; then
        echo "selective-test: jq is required to parse wipnote trace JSON" >&2
        exit 1
    fi
    if ! TRACE_OUT=$(wipnote trace "${WIPNOTE_WORKITEM_ID}" --json 2>/dev/null); then
        echo "selective-test: failed to trace work item ${WIPNOTE_WORKITEM_ID}" >&2
        exit 1
    fi
    if ! TRACE_FILES=$(jq -r ".files[]?" <<<"$TRACE_OUT"); then
        echo "selective-test: failed to parse wipnote trace JSON" >&2
        exit 1
    fi
    while read -r file; do
        append_file "$file"
    done <<<"$TRACE_FILES"
fi

if [ ${#FILES[@]} -eq 0 ]; then
    echo "selective-test: no work item files traced, checking git diff/status..."
    while read -r file; do
        append_file "$file"
    done < <(collect_git_files)
fi

GO_FILES=()
for file in "${FILES[@]}"; do
    if [[ "$file" == *.go ]]; then
        GO_FILES+=("$file")
    fi
done

if [ ${#GO_FILES[@]} -eq 0 ] && [ -n "${WIPNOTE_WORKITEM_ID:-}" ]; then
    echo "selective-test: traced files contain no Go files, checking git diff/status..."
    while read -r file; do
        if [[ "$file" == *.go ]]; then
            append_file "$file"
            GO_FILES+=("${REPO_ROOT}/$file")
        fi
    done < <(collect_git_files)
fi

if [ ${#GO_FILES[@]} -eq 0 ]; then
    echo "selective-test: no Go files modified. Skipping tests."
    exit 0
fi

TMP_LIST=$(mktemp)
trap "rm -f \"$TMP_LIST\"" EXIT

for file in "${GO_FILES[@]}"; do
    dir=$(dirname "$file")
    if [ -d "$dir" ]; then
        mod_root=$(find_module_root "$dir")
        rel_pkg="./$(realpath --relative-to="$mod_root" "$dir")"
        echo "$mod_root|$rel_pkg" >> "$TMP_LIST"
    fi
done

if [ ! -s "$TMP_LIST" ]; then
    echo "selective-test: no valid Go package directories found. Failing closed." >&2
    exit 1
fi

sort -u "$TMP_LIST" -o "$TMP_LIST"
mapfile -t MOD_ROOTS < <(cut -d"|" -f1 "$TMP_LIST" | sort -u)

for mod in "${MOD_ROOTS[@]}"; do
    mapfile -t changed_pkgs < <(awk -F"|" -v mod="$mod" "\$1 == mod {print \$2}" "$TMP_LIST")

    mod_name=$(basename "$mod")
    if [ "$mod" = "$REPO_ROOT" ]; then
        mod_name="root"
    fi

    cd "$mod"
    mapfile -t changed_imports < <(go list -f "{{.ImportPath}}" "${changed_pkgs[@]}")
    pkgs=("${changed_pkgs[@]}")

    while IFS='|' read -r pkg target deps; do
        if [ -z "$target" ]; then
            target="$pkg"
        fi
        if [[ "$target" == *.test ]]; then
            continue
        fi
        for changed in "${changed_imports[@]}"; do
            if [[ "$target" == "$changed" || " $deps " == *" $changed "* ]]; then
                pkgs+=("$target")
                break
            fi
        done
    done < <(go list -test -f "{{.ImportPath}}|{{if .ForTest}}{{.ForTest}}{{else}}{{.ImportPath}}{{end}}|{{join .Deps \" \"}}" ./...)

    mapfile -t pkgs < <(printf "%s\n" "${pkgs[@]}" | sort -u)
    echo "selective-test: running tests in module [${mod_name}] for packages: ${pkgs[*]}"
    go test -short "${pkgs[@]}"
    cd "$REPO_ROOT"
done
