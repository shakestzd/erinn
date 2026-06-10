---
name: parallel-orchestrator
description: ARCHIVED — Use orchestrator-directives-skill instead. Orchestrate parallel agent workflows using Task tool. Activate when planning multi-agent work or coordinating concurrent feature implementation.
---

<!-- ARCHIVED: This skill has been superseded by orchestrator-directives-skill -->
<!-- SDK references removed — use Go CLI commands instead -->

# Parallel Orchestrator Skill (ARCHIVED)

> **This skill is archived.** Use `/wipnote:orchestrator-directives-skill` for current orchestration patterns.

## Core Principle: 6-Phase Parallel Workflow

```
1. ANALYZE   → wipnote analytics summary + wipnote find features --status todo
2. PREPARE   → Cache shared context, isolate tasks
3. DISPATCH  → Spawn agents in ONE message (parallel)
4. MONITOR   → Track health metrics per agent
5. AGGREGATE → Collect results, detect conflicts
6. VALIDATE  → uv run pytest && uv run ruff check
```

---

## Phase 1: Pre-Flight Analysis

```bash
# Check what can be parallelized
wipnote analytics summary
wipnote find features --status todo
wipnote analytics summary
```

### Decision Criteria

| Condition | Action |
|-----------|--------|
| 2+ independent todo features | Can parallelize |
| Shared file edits | Partition or sequence |
| All features blocked | Resolve bottlenecks first |

---

## Phase 2: Context Preparation

Identify files all agents need and share that context in each agent's prompt. Partition file ownership to avoid conflicts.

---

## Phase 3: Dispatch with Task Tool

**CRITICAL: Send ALL Task calls in a SINGLE message for true parallelism!**

```python
# CORRECT: All in one message (parallel)
use the gemini-operator workflow described here
use the sonnet-coder workflow described here
use the sonnet-coder workflow described here

# WRONG: Sequential messages (not parallel)
# result1 = use the appropriate Gemini agent invocation  # Wait for completion
# result2 = use the appropriate Gemini agent invocation  # Then next one
```

---

## Phase 4: Monitor (During Execution)

Agents track their own health via transcript analytics.

### Healthy Patterns

| Pattern | Why |
|---------|-----|
| `Grep → Read` | Search before reading |
| `Read → Edit → Bash` | Read, modify, test |
| `Glob → Read` | Find files first |

---

## Phase 5: Aggregate Results

After all agents complete, collect their results and check for conflicts via git diff / test suite.

```bash
uv run pytest
uv run ruff check --fix
git diff --stat
```

---

## Phase 6: Validate

```bash
# Verify all tests pass
uv run pytest

# Commit unified changes
wipnote feature complete <feat-id>
```

---

## When NOT to Parallelize

| Situation | Reason | Alternative |
|-----------|--------|-------------|
| Shared dependencies | Conflicts | Sequential + handoff |
| Tasks < 1 minute | Overhead not worth it | Sequential |
| Overlapping files | Merge conflicts | Partition files |

---

## Troubleshooting

### "Not enough independent tasks"
```bash
# Check what's available
wipnote analytics summary
wipnote find features --status todo
```

### File conflicts detected
- Improve task isolation in Phase 2
- Consider sequential execution for overlapping work
