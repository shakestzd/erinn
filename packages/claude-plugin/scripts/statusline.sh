#!/bin/bash
# HtmlGraph Status Line for Claude Code + Oh My Posh
# Active work item: reads HTML files via SDK (source of truth, 2s timeout)

# Read input from stdin (Claude Code sends JSON with model info)
INPUT=$(cat)

# Extract and cache model info in background (non-blocking)
if [ -n "$INPUT" ]; then
    (
        DB_PATH="$(pwd)/.htmlgraph/htmlgraph.db"
        if [ -f "$DB_PATH" ]; then
            MODEL_NAME=$(echo "$INPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('model',{}).get('display_name','Claude'))" 2>/dev/null)
            MODEL_ID=$(echo "$INPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('model',{}).get('id','unknown'))" 2>/dev/null)
            SESSION_ID=$(echo "$INPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('session_id',''))" 2>/dev/null)
            sqlite3 "$DB_PATH" "
                CREATE TABLE IF NOT EXISTS model_cache (
                    id INTEGER PRIMARY KEY CHECK (id = 1),
                    model TEXT NOT NULL, model_id TEXT, session_id TEXT,
                    updated_at TEXT DEFAULT CURRENT_TIMESTAMP
                );
                INSERT OR REPLACE INTO model_cache (id, model, model_id, session_id, updated_at)
                VALUES (1, '$MODEL_NAME', '$MODEL_ID', '$SESSION_ID', datetime('now'));
            " 2>/dev/null
        fi
    ) &
fi

# Get active work item from HTML (source of truth)
ACTIVE_WORK=$(timeout 2 uv run python -c "
from htmlgraph import SDK
w = SDK().get_active_work_item()
print(w.get('title', '')[:25] if w else '')
" 2>/dev/null || echo "")

export HTMLGRAPH_ACTIVE="$ACTIVE_WORK"

# Check for Oh My Posh config
OMP_CONFIG="${HTMLGRAPH_OMP_CONFIG:-$HOME/.claude.omp.json}"
OMP_BIN="${HTMLGRAPH_OMP_BIN:-/opt/homebrew/bin/oh-my-posh}"

if [ -x "$OMP_BIN" ] && [ -f "$OMP_CONFIG" ]; then
    echo "$INPUT" | "$OMP_BIN" claude --config "$OMP_CONFIG"
else
    # Fallback: simple text status line (no Oh My Posh)
    if [ -n "$ACTIVE_WORK" ]; then
        echo "🔧 $ACTIVE_WORK"
    fi
fi
