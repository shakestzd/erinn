---
name: strategic-planning
description: Use HtmlGraph analytics to make smart work prioritization decisions. Activate when recommending work, finding bottlenecks, assessing risks, or analyzing project impact.
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

**CLI reference:** Run `htmlgraph help` for available commands. Key commands:
- `htmlgraph status` — project overview
- `htmlgraph find features --status in-progress` — active work
- `htmlgraph recommend` — AI-recommended next work

---

## Core Principle: Data-Driven Decisions

HtmlGraph provides analytics that consider:
- **Dependencies** - What blocks/enables other work
- **Priority** - Business importance
- **Impact** - How many tasks are unlocked
- **Risk** - Circular deps, complexity
- **Parallelism** - What can run concurrently

---

## Quick Decision Framework

```bash
# 1. What should I work on? (recommendations)
htmlgraph analytics summary

# 2. What's blocking progress?
htmlgraph analytics summary

# 3. Project snapshot (status + WIP)
htmlgraph snapshot --summary

# 4. Find in-progress work
htmlgraph find features --status in-progress
```

---

## CLI Reference

### `htmlgraph analytics summary`

Find tasks that block the most downstream work.

```bash
htmlgraph analytics summary
```

**Use when:**
- Progress feels slow
- Many tasks are "blocked"
- Planning sprint priorities

---

### `htmlgraph analytics summary`

Get scored recommendations considering all factors.

```bash
htmlgraph analytics summary
```

**Scoring factors:**
- Priority weight (critical=100, high=75, medium=50, low=25)
- Blocks count (×10 per blocked task)
- No dependencies bonus (+20)
- Bottleneck bonus (+30)

---

### `htmlgraph find features --status todo`

Find tasks that can run concurrently (no dependencies).

```bash
# All todo features
htmlgraph find features --status todo

# All in-progress
htmlgraph find features --status in-progress
```

**Use when:**
- Multiple agents available
- Want to speed up delivery
- Planning parallel sprints

---

### `htmlgraph snapshot --summary`

Project health and status overview.

```bash
htmlgraph snapshot --summary
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
htmlgraph status
htmlgraph snapshot --summary
htmlgraph analytics summary
```

---

### Pattern 2: Something Is Blocked

```bash
# Find what's causing the block
htmlgraph analytics summary
htmlgraph find features --status blocked
```

---

### Pattern 3: Planning Parallel Work

```bash
# Check what's ready (no dependencies)
htmlgraph analytics summary
htmlgraph find features --status todo
```

---

### Pattern 4: Review All Work

```bash
# See everything by status
htmlgraph find features --status in-progress
htmlgraph find features --status todo
htmlgraph find bugs --status open
```

---

## Integration with Planning

Use CLI analytics to inform planning decisions:

```bash
# Get full picture before planning
htmlgraph analytics summary
htmlgraph analytics summary
htmlgraph snapshot --summary

# Then create a spike to document the plan
htmlgraph spike create "Plan: Real-time collaboration — [analysis findings]"
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
htmlgraph analytics summary

# What should I do?
htmlgraph analytics summary

# Project snapshot
htmlgraph snapshot --summary

# Check status
htmlgraph status

# Find in-progress work
htmlgraph find features --status in-progress

# Find todo work (parallelizable candidates)
htmlgraph find features --status todo
```
