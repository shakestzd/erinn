---
name: wipnote-tracker
description: ARCHIVED — Use wipnote skill instead. wipnote workflow combining session tracking, orchestration, and parallel coordination.
---

<!-- ARCHIVED: This skill has been superseded by the wipnote skill -->
<!-- Python SDK references removed — use Go CLI commands instead -->

# wipnote Tracker Skill (ARCHIVED)

> **This skill is archived.** Use `/wipnote:wipnote` for current workflow patterns.

---

## Core Workflow

```bash
# Session start
wipnote status
wipnote analytics summary

# Create and track work
wipnote feature create "Title"
wipnote feature start <feat-id>

# Mark complete
wipnote feature complete <feat-id>
```

---

## Work Item Commands

```bash
# Features
wipnote feature create "Title"
wipnote feature start <feat-id>
wipnote feature complete <feat-id>
wipnote find features --status todo
wipnote find features --status in-progress

# Bugs
wipnote bug create "Title" --track <trk-id>
wipnote bug start <bug-id>
wipnote bug complete <bug-id>

# Spikes (investigation)
wipnote spike create "Title"
wipnote spike start <spike-id>
wipnote spike complete <spike-id>

# Tracks (multi-feature initiatives)
wipnote track new "Title"
```

---

## Analytics

```bash
wipnote analytics summary
wipnote analytics summary
wipnote snapshot --summary
wipnote find features --status todo
```

---

## Parallel Orchestration

Dispatch independent tasks in a single message:

```python
# All in one message = parallel execution
use the gemini-operator workflow described here
use the sonnet-coder workflow described here
use the sonnet-coder workflow described here
```

See `/wipnote:orchestrator-directives-skill` for complete patterns.

---

## Work Type Classification

Work type is inferred from work item ID prefix:
- `feat-*` → feature-implementation
- `spike-*` → spike-investigation
- `bug-*` → bug-fix
- `chore-*` → maintenance
