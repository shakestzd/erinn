---
name: diagnose
description: "Diagnose bugs, errors, and issues with root cause analysis. Use when asked to diagnose, debug, investigate, or find root cause of any problem — whether a wipnote bug ID, error message, unexpected behavior, or delegation audit."
user_invocable: true
---

# /wipnote:diagnose

General-purpose diagnostic skill for investigating bugs, errors, and unexpected behavior.

## Usage

```
/wipnote:diagnose <bug-id>              # Investigate a specific bug
/wipnote:diagnose <error or symptom>    # Investigate an error or behavior
/wipnote:diagnose --delegation          # Audit delegation compliance (legacy mode)
```

## When to Activate

Trigger on:
- "diagnose", "debug", "investigate", "root cause", "why is this broken"
- A bug ID like `bug-5126e3cf`
- An error message or symptom description
- "why isn't X working", "what's wrong with", "figure out why"
- "delegation audit", "delegation score" (routes to delegation mode)

## Work Item Attribution

All diagnostic work must be attributed:
- Bug investigation: `wipnote bug start <bug-id>` before investigating
- New errors: `wipnote bug create "Error: description" --track <trk-id>` then start it
- Run `wipnote help` for available commands

## Instructions for Claude

### Route by Input

**If given a bug ID** (matches `bug-*`):
1. Start attribution: `wipnote bug start <bug-id>`
2. Fetch bug details: `wipnote bug show <bug-id>`
3. Dispatch the debugger agent with the bug context
4. Present findings and suggested fix

**If given an error message or symptom**:
1. Create and start a bug: `wipnote bug create "<summary>" --track <trk-id>` then `wipnote bug start <id>`
2. Dispatch the debugger agent with the error context
3. Present findings and suggested fix

**If `--delegation` flag**:
1. Run the delegation audit (see Delegation Mode below)

**If no arguments**:
1. Check project health: `wipnote recommend --top 3`
2. Identify bottlenecks, stale items, or anomalies
3. Suggest what to investigate

### Bug Investigation (Primary Mode)

Dispatch `use @researcher` with a structured prompt:

```text
## Bug: <bug-id> — <title>

### Symptom
<description from bug or user>

### Investigation Steps
1. Search codebase for relevant code paths
2. Read the source files involved
3. Identify the root cause with file paths and line numbers
4. Check if the issue affects other cases beyond the reported one
5. Query SQLite or run CLI commands to verify current state
6. Propose a fix (describe what to change, don't implement)

### Report Format
- **Root cause**: What's wrong and where (file:line)
- **Blast radius**: Does this affect other cases?
- **Suggested fix**: What code change resolves it
- **Verification**: How to confirm the fix works
```

After the agent reports back, present findings to the user in this format:

```markdown
## Diagnosis: <bug-id>

**Root cause:** <one-line summary>
**Location:** `<file>:<line>`
**Blast radius:** <scope of impact>

### Details
<agent's detailed findings>

### Suggested Fix
<what to change>

### Next Steps
- [ ] Implement fix
- [ ] Run tests: `go test ./...`
- [ ] Verify: <specific verification command>
```

### Delegation Audit Mode (--delegation)

When `--delegation` is specified, audit the current session's delegation compliance:

1. **Collect data**:
```bash
wipnote status
sqlite3 .wipnote/wipnote.db "
SELECT tool_name, COUNT(*) as count
FROM agent_events
WHERE session_id = (SELECT session_id FROM agent_events ORDER BY timestamp DESC LIMIT 1)
GROUP BY tool_name ORDER BY count DESC;
"
```

2. **Compute score**: `delegations / (delegations + direct_impl + git_writes) * 100`
   - Delegations = Task + Agent calls
   - Direct impl = Edit + Write calls
   - Git writes = Bash calls containing git commit/push/merge

3. **Present report**:
```markdown
## Delegation Diagnostic

### Score: X% (N/M actions delegated)

### Gaps Found
| Time | Tool | Action | Should Use |
|------|------|--------|------------|
| ... | ... | ... | ... |

### Recommendations
1. ...
```
