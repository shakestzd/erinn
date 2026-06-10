# Architectural Memory Measurement Protocol

## Baseline (2026-06-10)

Observed research-phase duration on serve-area tasks without arch cards: **15–25 minutes**.

"Research phase" is defined as: time from subagent start to the subagent's first `Edit` or
`Write` tool call in the session transcript. This window captures all context-gathering work
(reading code, grepping, exploring directories) before the subagent begins mutating files.

## Metric Definition

**Research phase** = elapsed time from the subagent's first `tool_decision` event in the
session transcript to the first `tool_decision` event where `tool_name` is `Edit` or `Write`.

## Session Transcript Fields

Events are recorded in `.wipnote/sessions/<session-id>/events.ndjson`. Each event is a JSON
object. Relevant fields:

```
{
  "ts":        "<RFC3339 timestamp>",        // wall time of the event
  "canonical": "tool_decision",              // look for this kind
  "tool_name": "Edit" | "Write" | "Bash",   // the tool the agent called
  "session_id": "<uuid>"                     // matches the subagent's session
}
```

To measure research phase for a session:

```bash
SESSION_ID="<subagent-session-id>"
SESSION_FILE=".wipnote/sessions/${SESSION_ID}/events.ndjson"

# First Edit or Write tool call timestamp
python3 - <<'EOF'
import json, sys
from datetime import datetime

events = []
with open("$SESSION_FILE") as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        e = json.loads(line)
        if e.get('canonical') == 'tool_decision':
            events.append(e)

if not events:
    print("No tool_decision events found")
    sys.exit(1)

start_ts = datetime.fromisoformat(events[0]['ts'].replace('Z', '+00:00'))
edit_ts = None
for e in events:
    if e.get('tool_name') in ('Edit', 'Write'):
        edit_ts = datetime.fromisoformat(e['ts'].replace('Z', '+00:00'))
        break

if edit_ts:
    delta = edit_ts - start_ts
    print(f"Research phase: {delta.total_seconds() / 60:.1f} minutes")
else:
    print("No Edit/Write tool call found in transcript")
EOF
```

## How to Run the Comparison

1. **With arch cards (new):** Dispatch a serve-area task to a coder subagent. The
   `wipnote:agent-context` skill instructs the agent to run `wipnote arch resolve --for
   <work-item-id>` at start; matched cards land in context automatically. Record the subagent
   session ID from `wipnote wip show` or the TaskCreate log. Run the script above.

2. **Without cards (baseline):** Run the same task dispatch from a checkout before
   `feat-359312ab` (or temporarily deprecate all serve-* cards with
   `wipnote arch deprecate <slug>`). Record the session ID and run the script.

3. **Compare:** A reduction from the 15–25 min baseline toward under 5 min is the signal
   that arch context injection is working.

## Cards Authored (dogfood, 2026-06-10)

Authored as part of `feat-359312ab`. Covers the `wipnote serve` parent+child process model,
dashboard SSE streaming, writequeue hazard, proxy invariant, and child lifecycle.

| Slug | Kind | Globs |
|------|------|-------|
| `serve-hub-architecture` | subsystem-map | `cmd/wipnote/serve*.go`, `cmd/wipnote/dashboard/**`, `cmd/wipnote/dashboard.go` |
| `serve-writequeue-hazard` | hazard | `cmd/wipnote/serve_child.go`, `cmd/wipnote/serve_global.go` |
| `serve-parent-proxy-invariant` | invariant | `cmd/wipnote/serve_parent.go` |
| `serve-child-lifecycle` | subsystem-map | `cmd/wipnote/serve_child.go`, `internal/childproc/**` |
| `serve-sse-dashboard` | subsystem-map | `cmd/wipnote/dashboard/**`, `cmd/wipnote/serve.go` |

All cards verified at HEAD `e56c49a13549da970df6ea78c4dd4453b1ece6e9`.

Run `wipnote arch list` and `wipnote arch validate` to confirm card state.

## No New Telemetry Required

The `events.ndjson` machinery already records `tool_decision` events with `ts` (wall clock
timestamp) and `tool_name` fields. No new instrumentation is needed — the measurement is a
post-hoc query on existing session data. The `canonical: "tool_decision"` kind is set by the
Claude Code hook pipeline and is present in all sessions collected since the session ingestion
was wired in the `wipnote serve` child process.
