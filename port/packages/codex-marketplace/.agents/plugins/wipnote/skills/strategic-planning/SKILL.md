---
name: strategic-planning
description: Use wipnote analytics to make smart work prioritization decisions. Activate when recommending work, finding bottlenecks, assessing risks, or analyzing project impact.
---

# Strategic Planning Skill

## When to Activate This Skill

**Trigger keywords:**
- "what should I work on", "recommend", "prioritize"
- "bottleneck", "blocking", "stuck"
- "risk", "impact", "dependencies"
- "strategic", "roadmap", "plan"

**Trigger situations:**
- Starting a new session (what to work on?)
- Multiple tasks available (which is most important?)
- Progress seems slow (what's blocking us?)
- Planning major changes (what's the impact?)

**CLI reference:** Run `wipnote help` for available commands. Key commands:
- `wipnote status` — project overview
- `wipnote find features --status in-progress` — active work
- `wipnote recommend` — AI-recommended next work

---

## Core Principle: Data-Driven Decisions

wipnote provides analytics that consider:
- **Dependencies** - What blocks/enables other work
- **Priority** - Business importance
- **Impact** - How many tasks are unlocked
- **Risk** - Circular deps, complexity
- **Parallelism** - What can run concurrently

---

## Quick Decision Framework

```bash
# 1. What should I work on? (recommendations)
wipnote analytics summary

# 2. What's blocking progress?
wipnote analytics summary

# 3. Project snapshot (status + WIP)
wipnote snapshot --summary

# 4. Find in-progress work
wipnote find features --status in-progress
```

---

## CLI Reference

### `wipnote analytics summary`

Find tasks that block the most downstream work.

```bash
wipnote analytics summary
```

**Use when:**
- Progress feels slow
- Many tasks are "blocked"
- Planning sprint priorities

---

### `wipnote analytics summary`

Get scored recommendations considering all factors.

```bash
wipnote analytics summary
```

**Scoring factors:**
- Priority weight (critical=100, high=75, medium=50, low=25)
- Blocks count (×10 per blocked task)
- No dependencies bonus (+20)
- Bottleneck bonus (+30)

---

### `wipnote find features --status todo`

Find tasks that can run concurrently (no dependencies).

```bash
# All todo features
wipnote find features --status todo

# All in-progress
wipnote find features --status in-progress
```

**Use when:**
- Multiple agents available
- Want to speed up delivery
- Planning parallel sprints

---

### `wipnote snapshot --summary`

Project health and status overview.

```bash
wipnote snapshot --summary
```

**Use when:**
- Before major releases
- Sprint planning
- Health checks

---

## Decision Patterns

### Pattern 1: Start of Session

```bash
# Get project status overview
wipnote status
wipnote snapshot --summary
wipnote analytics summary
```

---

### Pattern 2: Something Is Blocked

```bash
# Find what's causing the block
wipnote analytics summary
wipnote find features --status blocked
```

---

### Pattern 3: Planning Parallel Work

```bash
# Check what's ready (no dependencies)
wipnote analytics summary
wipnote find features --status todo
```

---

### Pattern 4: Review All Work

```bash
# See everything by status
wipnote find features --status in-progress
wipnote find features --status todo
wipnote find bugs --status open
```

---

## Integration with Planning

Use CLI analytics to inform planning decisions:

```bash
# Get full picture before planning
wipnote analytics summary
wipnote snapshot --summary

# Use wipnote plan generate to create formal plans
wipnote plan generate <track-id>
```

---

## Best Practices

### DO

1. **Check bottlenecks first** - High-leverage work
2. **Use recommendations** - Considers all factors
3. **Assess risks before big changes** - Avoid surprises
4. **Analyze impact** - Understand consequences
5. **Check parallel capacity** - Optimize throughput

### DON'T

1. **Ignore blocked tasks** - They signal bottlenecks
2. **Skip risk assessment** - Before major releases
3. **Parallelize without analysis** - May cause conflicts
4. **Work on low-impact tasks** - When bottlenecks exist

---

## Quick Reference

```bash
# What's blocking us?
wipnote analytics summary

# What should I do?
wipnote analytics summary

# Project snapshot
wipnote snapshot --summary

# Check status
wipnote status

# Find in-progress work
wipnote find features --status in-progress

# Find todo work (parallelizable candidates)
wipnote find features --status todo
```

---

## Recommend Workflow

Run `wipnote recommend [--top N]` (default N=5) for scored recommendations:

```bash
wipnote recommend --top 5
```

After presenting output, analyze whether the top recommendations can run in parallel:

1. Check dependencies — do any recommended items block each other?
2. Check file overlap — do they modify the same files or modules?
3. If independent → propose `/wipnote:execute` for parallel dispatch
4. If dependent → identify critical path, propose sequential order

**Propose next action:**
- 2+ independent items → `/wipnote:execute` (parallel)
- Blocked items → start with the blocker using `/wipnote:plan <id>`
- Single item → delegate to appropriate agent tier (haiku/sonnet/opus)
