#!/bin/bash
# HtmlGraph Status Line for Claude Code + Oh My Posh
# Fast: uses sqlite3 directly (~5ms vs ~1500ms for uv run python)

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

# Fast active work item query via sqlite3 (~5ms)
DB_PATH="$(pwd)/.htmlgraph/htmlgraph.db"
if [ -f "$DB_PATH" ]; then
    # Primary: session-scoped active item (uses sessions.active_feature_id set by SDK)
    # SESSION_ID is extracted from the JSON input above (line 15 background block).
    # Re-extract here synchronously for immediate use.
    SESSION_ID_SYNC=$(echo "$INPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('session_id',''))" 2>/dev/null)
    ACTIVE_WORK=""
    if [ -n "$SESSION_ID_SYNC" ]; then
        ACTIVE_WORK=$(sqlite3 "$DB_PATH" "
            SELECT COALESCE(substr(f.title, 1, 25), s.active_feature_id)
            FROM sessions s
            LEFT JOIN features f ON f.id = s.active_feature_id
            WHERE s.session_id = '$(echo "$SESSION_ID_SYNC" | tr -d "'\"")'
              AND s.active_feature_id IS NOT NULL
            LIMIT 1
        " 2>/dev/null)
    fi
    # Fallback: global in-progress query when no session-scoped item found
    if [ -z "$ACTIVE_WORK" ]; then
        ACTIVE_WORK=$(sqlite3 "$DB_PATH" "
            SELECT substr(title, 1, 25) FROM features
            WHERE status = 'in-progress'
            ORDER BY CASE type
                WHEN 'bug' THEN 0
                WHEN 'feature' THEN 1
                ELSE 2
            END, rowid DESC
            LIMIT 1
        " 2>/dev/null)
    fi
else
    ACTIVE_WORK=""
fi

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
