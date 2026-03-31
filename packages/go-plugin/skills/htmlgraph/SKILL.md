---
name: htmlgraph
description: HtmlGraph workflow skill combining session tracking, orchestration, and parallel coordination. Activated automatically at session start. Enforces delegation patterns, manages multi-agent workflows, ensures proper activity attribution, and maintains feature awareness. Use when working with HtmlGraph projects, spawning parallel agents, or coordinating complex work.
---

# HtmlGraph Skill

Use this skill when HtmlGraph is tracking the session to ensure proper activity attribution, documentation, and orchestration patterns. Activate this skill at session start via the SessionStart hook.

---

## 📚 REQUIRED READING

**→ READ [../../../AGENTS.md](../../../AGENTS.md) FOR COMPLETE SDK DOCUMENTATION**

The root AGENTS.md file contains:
- ✅ **Python SDK Quick Start** - Installation, initialization, basic operations
- ✅ **Deployment Instructions** - Using `deploy-all.sh` script
- ✅ **API & CLI Reference** - Alternative interfaces
- ✅ **Best Practices** - Patterns for AI agents
- ✅ **Complete Workflow Examples** - End-to-end scenarios

**This file (SKILL.md) contains Claude Code-specific instructions only.**

**For SDK usage, deployment, and general agent workflows → USE AGENTS.md**

---

## When to Activate This Skill

- At the start of every session when HtmlGraph plugin is enabled
- When the user asks about tracking, features, or session management
- When drift detection warnings appear
- When the user mentions htmlgraph, features, sessions, or activity tracking
- When discussing work attribution or documentation
- When planning multi-agent work or parallel execution
- When using Task tool to spawn subagents
- When coordinating concurrent feature implementation

**Trigger keywords:** htmlgraph, feature tracking, session tracking, drift detection, activity log, work attribution, feature status, session management, orchestrator, parallel, concurrent, delegation, Task tool, multi-agent, spawn agents

---

## Work Item Attribution (MANDATORY — before ALL other work)

Every session MUST have an active work item before any code changes:

1. **Check current state:** `htmlgraph status` — shows in-progress features
2. **Start a feature:** `htmlgraph feature start <id>` — attributes all activity to this feature
3. **Complete when done:** `htmlgraph feature complete <id>` — marks feature done
4. **Create if needed:** `htmlgraph feature create "Title"` — creates and returns an ID
5. **Subagents inherit attribution** only if the prompt includes `htmlgraph feature start <id>`

**The YOLO mode hook will BLOCK Write/Edit tools if no feature is active for the current session.**

Run `htmlgraph help` for the full command reference.

---

## Core Responsibilities

### 1. **MANDATORY DELEGATION RULES** (NON-NEGOTIABLE)

## FORBIDDEN: Direct Execution

I MUST NOT execute these operations directly. I MUST delegate ALL of these to subagents via Task():

- ❌ **FORBIDDEN**: Git commands (add, commit, push, pull, merge, branch, rebase, checkout)
- ❌ **FORBIDDEN**: Multi-file code changes (2+ files)
- ❌ **FORBIDDEN**: Single-file code changes (unless truly trivial <5 lines)
- ❌ **FORBIDDEN**: Research & exploration (codebase searches, grep, find)
- ❌ **FORBIDDEN**: Testing & validation (pytest, test suites, debugging)
- ❌ **FORBIDDEN**: Build & deployment (package publishing, docker)
- ❌ **FORBIDDEN**: Complex file operations (batch operations, migrations)
- ❌ **FORBIDDEN**: Any operation that could require error handling or retries

## CONSEQUENCE OF VIOLATION:
- Context pollution (5-10+ tool calls instead of 2)
- Cascading failures (hooks fail, conflicts occur)
- Lost strategic context
- Reduced parallel efficiency
- User frustration with non-compliance

## PERMITTED: Strategic Operations ONLY

I MAY ONLY execute these operations directly:
- ✅ **PERMITTED**: Task() - Delegation to subagents
- ✅ **PERMITTED**: AskUserQuestion() - Clarifying requirements with user
- ✅ **PERMITTED**: TodoWrite() - Tracking work items
- ✅ **PERMITTED**: CLI operations - Creating features, spikes, analytics (`htmlgraph feature create`, `htmlgraph spike create`)

**EVERYTHING ELSE → DELEGATE VIA Task()**

## ENFORCEMENT MECHANISM

### DEFAULT ACTION: DELEGATE

I MUST delegate by default. I MUST NOT rationalize direct execution.

### BLOCKING QUESTIONS (Halt Direct Execution)

Before executing ANY tool call, I MUST ask:

1. **Is this operation in the FORBIDDEN list?**
   → If YES: **HALT. Delegate via Task(). No exceptions.**

2. **Could this require error handling or retries?**
   → If YES: **HALT. Delegate via Task(). No exceptions.**

3. **Could this cascade into 2+ tool calls?**
   → If YES: **HALT. Delegate via Task(). No exceptions.**

4. **Am I thinking "this is simple enough to do directly"?**
   → If YES: **HALT. That's rationalization. Delegate via Task().**

### ONLY PROCEED WITH DIRECT EXECUTION IF:
- Operation is explicitly in PERMITTED list
- AND I am 100% certain it's a single tool call
- AND It is strategic (decisions), not tactical (execution)

### WHEN IN DOUBT: DELEGATE
**If uncertain whether to delegate, I MUST delegate. No exceptions.**

## ABSOLUTE PROHIBITION: Git Commands

I MUST NOT execute git commands directly under ANY circumstances.

**FORBIDDEN COMMANDS:**
- git add, git commit, git push, git pull
- git merge, git rebase, git cherry-pick
- git branch, git checkout, git switch
- git tag, git stash, git reset

**REQUIRED ACTION:**
I MUST delegate ALL git operations via Task().

**ENFORCEMENT:**
- If I consider direct git execution → **HALT**
- If I think "this is just one commit" → **HALT**
- If I think "git is simpler than delegating" → **HALT**
- **ALWAYS delegate git. No rationalization. No exceptions.**

**WHY THIS IS ABSOLUTE:**
Git operations cascade unpredictably:
- Commit hooks may fail → Fix code → Retry commit
- Push may fail → Pull → Merge conflicts → Retry push
- Tests may fail in hooks → Debug → Fix → Retry

Context cost: Direct execution = 7-15 tool calls vs Delegation = 2 tool calls

**Delegation Pattern:**
```python
# Delegate git operations to copilot-operator
Task(
    subagent_type="htmlgraph:copilot-operator",
    description="Commit and push changes",
    prompt="Commit files CLAUDE.md, SKILL.md with message 'docs: consolidate skills'. Push to origin main. Handle any errors."
)
```

## FORBIDDEN PATTERNS - NEVER USE THESE

### ❌ FORBIDDEN PATTERN 1: Direct Git Execution

**NEVER DO THIS:**
```python
# FORBIDDEN - Direct git commands
Bash(command="git add .")
Bash(command="git commit -m 'fix bug'")
Bash(command="git push origin main")
```

**WHY FORBIDDEN:**
- Pre-commit hooks may fail → Cascades into 5+ tool calls
- Push may fail → Pull conflicts → Another 3+ tool calls
- Context pollution from error handling

**✅ CORRECT APPROACH:**
```python
Task(
    prompt="""
    Commit and push changes:
    - Files: All modified files
    - Message: 'fix bug'
    - Handle errors (hooks, conflicts, push failures)
    """,
    subagent_type="general-purpose"
)
```

### ❌ FORBIDDEN PATTERN 2: Direct Code Implementation

**NEVER DO THIS:**
```python
# FORBIDDEN - Direct multi-file changes
Edit(file_path="src/auth.py", ...)
Edit(file_path="src/middleware.py", ...)
Bash(command="pytest tests/test_auth.py")
```

**WHY FORBIDDEN:**
- Multi-file changes consume context
- Tests may fail → Debug → Fix → Retest cascade
- No parallel potential

**✅ CORRECT APPROACH:**
```python
Task(
    prompt="""
    Implement authentication:
    1. Edit src/auth.py (add JWT validation)
    2. Edit src/middleware.py (add auth middleware)
    3. Write tests in tests/test_auth.py
    4. Run pytest until all pass

    Report: What you implemented and test results
    """,
    subagent_type="general-purpose"
)
```

### ❌ FORBIDDEN PATTERN 3: Direct Research/Exploration

**NEVER DO THIS:**
```python
# FORBIDDEN - Direct codebase search
Grep(pattern="authenticate", path="src/")
Read(file_path="src/auth.py")
Grep(pattern="JWT", path="src/")
Read(file_path="src/middleware.py")
```

**WHY FORBIDDEN:**
- Consumes context with file contents
- Unpredictable number of reads
- No strategic value in details

**✅ CORRECT APPROACH:**
```python
Task(
    prompt="""
    Find all authentication code:
    - Search for: authenticate, JWT, token validation
    - Scope: src/ directory
    - Report: Which files handle auth and what each does
    """,
    subagent_type="Explore"
)
```

### ✅ RECOGNITION TEST

**Before ANY tool execution, ask:**
- Does this match a FORBIDDEN pattern? → If YES: **DELEGATE**
- Is this in the PERMITTED list? → If NO: **DELEGATE**
- Am I rationalizing ("it's just one...")? → If YES: **DELEGATE**

**See:** `packages/claude-plugin/rules/orchestration.md` for complete orchestrator directives and delegation patterns.

#### Parallel Workflow (6-Phase Process)

When coordinating multiple agents with Task tool, follow this structured workflow:

```
1. ANALYZE   → Check dependencies, assess parallelizability
2. PREPARE   → Cache shared context, partition files
3. DISPATCH  → Generate prompts via SDK, spawn agents in ONE message
4. MONITOR   → Track health metrics per agent
5. AGGREGATE → Collect results, detect conflicts
6. VALIDATE  → Verify outputs, run tests
```

**Quick Start - Parallel Execution:**
```bash
# Check what's ready (no dependencies = safe to parallelize)
htmlgraph analytics summary
htmlgraph find features --status todo
```

```python
# DISPATCH - Spawn all agents in ONE message (critical!)
Task(subagent_type="htmlgraph:codex-operator", description="Feature A", prompt="Implement feature A...")
Task(subagent_type="htmlgraph:sonnet-coder", description="Feature B", prompt="Implement feature B...")
Task(subagent_type="htmlgraph:haiku-coder", description="Feature C", prompt="Implement feature C (simple)...")
```

**When to Parallelize:**
- Multiple independent tasks can run simultaneously
- Work can be partitioned without file conflicts
- `htmlgraph find features --status todo` shows 2+ independent tasks

**When NOT to Parallelize:**
- Shared dependencies or file conflicts
- Tasks < 1 minute (overhead not worth it)
- Complex coordination required

**Anti-Patterns to Avoid:**
- ❌ Sequential Task calls (send all in ONE message for true parallelism)
- ❌ Overlapping file edits (partition work to avoid conflicts)
- ❌ No shared context caching (read shared files once, not per-agent)

---

### 2. **Use CLI, Not Direct File Operations** (CRITICAL)

**ABSOLUTE RULE: DO NOT use Read, Write, or Edit tools on `.htmlgraph/` HTML files.**

Use the Go CLI to ensure all HTML is validated and the SQLite index stays in sync.

❌ **FORBIDDEN:**
```python
# NEVER DO THIS
Write('/path/to/.htmlgraph/features/feature-123.html', ...)
Edit('/path/to/.htmlgraph/sessions/session-456.html', ...)
```

✅ **REQUIRED - Use CLI:**
```bash
# Create work items
htmlgraph feature create "Title"
htmlgraph bug create "Bug description"
htmlgraph spike create "Investigation title"

# Update status
htmlgraph feature start <id>
htmlgraph feature complete <id>

# Query
htmlgraph find features --status todo
htmlgraph find bugs --status open
htmlgraph feature show <id>

# Analytics
htmlgraph snapshot --summary
htmlgraph analytics summary
htmlgraph analytics summary
```

**Why SDK is best:**
- ✅ 3-16x faster than CLI (no process startup)
- ✅ Type-safe with auto-complete
- ✅ Context managers (auto-save)
- ✅ Vectorized batch operations
- ✅ Works offline (no server needed)
- ✅ Supports ALL collections (features, bugs, chores, spikes, epics, etc.)

**Why this matters:**
- Direct file edits bypass HTML validation
- Break the SQLite index sync
- Skip event logging and activity tracking
- Can corrupt graph structure and relationships

**NO EXCEPTIONS: NEVER read, write, or edit `.htmlgraph/` files directly.**

Use the CLI for inspection:

```bash
# ✅ CORRECT - Inspect sessions/events via CLI
htmlgraph session list
htmlgraph status
htmlgraph snapshot --summary
```

❌ **FORBIDDEN - Reading files directly:**
```bash
# NEVER DO THIS
cat .htmlgraph/features/feature-123.html
tail -10 .htmlgraph/events/session-123.jsonl
```

### 2. Feature Awareness (MANDATORY)
Always know which feature(s) are currently in progress:
- Check active features at session start: `htmlgraph status`
- Reference the current feature when discussing work
- Alert immediately if work drifts from the assigned feature

### 3. Continuous Tracking (CRITICAL)

**ABSOLUTE REQUIREMENT: Track ALL work in HtmlGraph.**

**Update HtmlGraph immediately after completing each piece of work:**
- Start features before working: `htmlgraph feature start <id>`
- Complete features when done: `htmlgraph feature complete <id>`
- Create spikes to document findings: `htmlgraph spike create "title"`
- Report bugs: `htmlgraph bug create "title"`

### 5. Activity Attribution
HtmlGraph automatically tracks tool usage. Action items:
- Use descriptive summaries in Bash `description` parameter
- Reference feature IDs in commit messages
- Mention the feature context when starting new tasks

### 6. Documentation Habits
For every significant piece of work:
- Summarize what was done and why
- Note any decisions made and alternatives considered
- Record blockers or dependencies discovered

## Working with Tracks, Specs, and Plans

### What Are Tracks?

**Tracks are high-level containers for multi-feature work** (conductor-style planning):
- **Track** = Overall initiative with multiple related features
- **Spec** = Detailed specification with requirements and acceptance criteria
- **Plan** = Implementation plan with phases and estimated tasks
- **Features** = Individual work items linked to the track

**When to create a track:**
- Work involves 3+ related features
- Need high-level planning before implementation
- Multi-phase implementation
- Coordination across multiple sessions or agents

**When to skip tracks:**
- Single feature work
- Quick fixes or enhancements
- Direct implementation without planning phase

---

### Creating Tracks

```bash
# Create a track
htmlgraph track new "User Authentication System"
htmlgraph track list

# Link features to a track when creating them
htmlgraph feature create "OAuth Integration"
# Then associate via: htmlgraph feature show <id> (note the track-id field)
```

---

### Track Workflow Example

```bash
# 1. Create track
htmlgraph track new "API Rate Limiting"

# 2. Create features for the track
htmlgraph feature create "Core Implementation: token bucket + Redis"
htmlgraph feature create "API Integration: middleware + error handling"
htmlgraph feature create "Testing & Validation: unit + load tests"

# 3. Work on features
htmlgraph feature start <feat-id>
# ... do the work ...
htmlgraph feature complete <feat-id>

# 4. Track progress
htmlgraph track list
htmlgraph snapshot --summary
```
- Complete workflow: `docs/TRACK_WORKFLOW.md`
- Full proposal: `docs/AGENT_FRIENDLY_SDK.md`

---

## Pre-Work Validation Hook

**NEW:** HtmlGraph enforces the workflow via a PreToolUse validation hook that ensures code changes are always tracked.

### How Validation Works

The validation hook runs BEFORE every tool execution and makes decisions based on your current work item:

**VALIDATION RULES:**

| Scenario | Tool | Action | Reason |
|----------|------|--------|--------|
| **Active Feature** | Read | ✅ Allow | Exploration is always allowed |
| **Active Feature** | Write/Edit/Delete | ✅ Allow | Code changes match active feature |
| **Active Spike** | Read | ✅ Allow | Spikes permit exploration |
| **Active Spike** | Write/Edit/Delete | ⚠️ Warn + Allow | Planning spike, code changes not tracked |
| **Auto-Spike** (session-init) | All | ✅ Allow | Planning phase, don't block |
| **No Active Work** | Read | ✅ Allow | Exploration without feature is OK |
| **No Active Work** | Write/Edit/Delete (1 file) | ⚠️ Warn + Allow | Single-file changes often trivial |
| **No Active Work** | Write/Edit/Delete (3+ files) | ❌ Deny | Requires explicit feature creation |
| **SDK Operations** | All | ✅ Allow | Creating work items always allowed |

### When Validation BLOCKS (Deny)

Validation **DENIES** code changes (Write/Edit/Delete) when ALL of these are true:

1. ❌ No active feature, bug, or chore (no work item)
2. ❌ Changes affect 3 or more files
3. ❌ Not an auto-spike (session-init or transition)
4. ❌ Not an SDK operation (e.g., creating features)

**What you see:**
```
PreToolUse Validation: Cannot proceed without active work item
- Reason: Multi-file changes (5 files) without tracked work item
- Action: Create a feature first with htmlgraph feature create
```

**Resolution:** Create a feature using the feature decision framework, then try again.

### When Validation WARNS (Allow with Warning)

Validation **WARNS BUT ALLOWS** when:

1. ⚠️ Single-file changes without active work item (likely trivial)
2. ⚠️ Active spike (planning-only, code changes won't be fully tracked)
3. ⚠️ Auto-spike (session initialization, inherent planning phase)

**What you see:**
```
PreToolUse Validation: Warning - activity may not be tracked
- File: src/config.py (1 file)
- Reason: Single-file change without active feature
- Option: Create feature if this is significant work
```

**You can continue** - but consider if the work deserves a feature.

### Auto-Spike Integration

**Auto-spikes are automatic planning spikes created during session initialization.**

When the validation hook detects the start of a new session:
- ✅ Creates an automatic spike (e.g., `spike-session-init-abc123`)
- ✅ Marks it as planning-only (code changes permitted but not deeply tracked)
- ✅ Does NOT block any operations
- ✅ Allows exploration without forcing feature creation

**Why auto-spikes?**
- Captures early exploration work that doesn't fit a feature yet
- Avoids false positives from investigation activities
- Enables "think out loud" without rigid workflow
- Transitions to feature when scope becomes clear

**Example auto-spike lifecycle:**
```
Session Start
  ↓
Auto-spike created: spike-session-init-20251225
  ↓
Investigation/exploration work
  ↓
"This needs to be a feature" → Create feature, link to spike
  ↓
Feature takes primary attribution
  ↓
Spike marked as resolved
```

### Decision Framework for Code Changes

**Use this framework to decide if you need a feature before making code changes:**

```
User request or idea
  ├─ Single file, <30 min? → DIRECT CHANGE (validation warns, allows)
  ├─ 3+ files? → CREATE FEATURE (validation denies without feature)
  ├─ New tests needed? → CREATE FEATURE (validation blocks)
  ├─ Multi-component impact? → CREATE FEATURE (validation blocks)
  ├─ Hard to revert? → CREATE FEATURE (validation blocks)
  ├─ Needs documentation? → CREATE FEATURE (validation blocks)
  └─ Otherwise → DIRECT CHANGE (validation warns, allows)
```

**Key insight:** Validation's deny threshold (3+ files) aligns with the feature decision threshold in CLAUDE.md.

---

## Validation Scenarios (Examples)

### Scenario 1: Working with Auto-Spike (Session Start)

**Situation:** You just started a new session. No features are active.

```python
# Session starts → auto-spike created automatically
# spike-session-init-20251225 is now active (auto-created)

# All of these work WITHOUT creating a feature:
- Read code files (exploration)
- Write to a single file (validation warns but allows)
- Create a feature (SDK operation, always allowed)
- Ask the user what to work on
```

**Flow:**
1. ✅ Session starts
2. ✅ Validation creates auto-spike for this session
3. ✅ You explore and read code (no restrictions)
4. ✅ You ask user what to work on
5. ✅ User says: "Implement user authentication"
6. ✅ You create feature: `htmlgraph feature create "User Authentication"`
7. ✅ Feature becomes primary (replaces auto-spike attribution)
8. ✅ You can now code freely

**Result:** Work is properly attributed to the feature, not the throwaway auto-spike.

---

### Scenario 2: Multi-File Feature Implementation

**Situation:** User says "Build a user authentication system"

**WITHOUT feature:**
```bash
# Try to edit 5 files without creating a feature
htmlgraph something that touches 5 files

# Validation DENIES:
# ❌ PreToolUse Validation: Cannot proceed without active work item
#    Reason: Multi-file changes (5 files) without tracked work item
#    Action: Create a feature first
```

**WITH feature:**
```bash
# Create the feature first
htmlgraph feature create "User Authentication"
# → feat-abc123 created and marked in-progress

# Now implement - all 5 files allowed
# Edit src/auth.py
# Edit src/middleware.py
# Edit src/models.py
# Write tests/test_auth.py
# Update docs/authentication.md

# Validation ALLOWS:
# ✅ All changes attributed to feat-abc123
# ✅ Session shows feature context
# ✅ Work is trackable
```

**Result:** Multi-file feature work is tracked and attributed.

---

### Scenario 3: Single-File Quick Fix (No Feature)

**Situation:** You notice a typo in a docstring.

```bash
# Edit a single file without creating a feature
# Edit src/utils.py (fix typo)

# Validation WARNS BUT ALLOWS:
# ⚠️  PreToolUse Validation: Warning - activity may not be tracked
#    File: src/utils.py (1 file)
#    Reason: Single-file change without active feature
#    Option: Create feature if this is significant work

# You can choose:
# - Continue (typo is trivial, doesn't need feature)
# - Cancel and create feature (if it's a bigger fix)
```

**Result:** Small fixes don't require features, but validation tracks the decision.

---

## Working with HtmlGraph

Use the Go CLI for all HtmlGraph operations.

```bash
# Check Current Status
htmlgraph status
htmlgraph feature list

# Start Working on a Feature
htmlgraph feature start <feature-id>

# Set Primary Feature (when multiple are active)
htmlgraph feature start <feature-id>

# Complete a Feature
htmlgraph feature complete <feature-id>

# Create work items
htmlgraph feature create "Title"
htmlgraph bug create "Bug description"
htmlgraph spike create "Investigation title"

# Query
htmlgraph find features --status todo
htmlgraph find features --status in-progress
htmlgraph find bugs --status open
htmlgraph feature show <id>

# Analytics
htmlgraph snapshot --summary
htmlgraph analytics summary
htmlgraph analytics summary
```

---

## Strategic Planning & Dependency Analytics

**NEW:** HtmlGraph now provides intelligent analytics to help you make smart decisions about what to work on next.

### Quick Start: Get Recommendations

```bash
# Get smart recommendations on what to work on
htmlgraph analytics summary
```

### Available Strategic Planning Features

#### 1. Find Bottlenecks

Identify tasks blocking the most downstream work:

```bash
htmlgraph analytics summary
```

#### 2. Get Parallel Work

Find tasks that can be worked on simultaneously:

```bash
htmlgraph find features --status todo
```

#### 3. Recommend Next Work

Get smart recommendations considering priority, dependencies, and impact:

```bash
htmlgraph analytics summary
```

#### 4. Assess Risks ⚠️

Check for dependency-related risks:

```bash
htmlgraph snapshot --summary
```

This provides a project health overview including blocked tasks and dependency issues.

#### 5. Analyze Impact

See what work is available and what's blocking progress:

```bash
htmlgraph analytics summary
htmlgraph analytics summary
```

### Recommended Decision Flow

At the start of each work session:

```bash
# 1. Check for bottlenecks
htmlgraph analytics summary

# 2. Get recommendations
htmlgraph analytics summary

# 3. Check parallel opportunities
htmlgraph find features --status todo

# 4. Project health snapshot
htmlgraph snapshot --summary
```

### When to Use Each Command

- **`htmlgraph analytics summary`**: At session start, during sprint planning
- **`htmlgraph analytics summary`**: When deciding what task to pick up
- **`htmlgraph find features --status todo`**: When coordinating multiple agents
- **`htmlgraph snapshot --summary`**: During project health checks, before milestones

**See also**: `docs/AGENT_STRATEGIC_PLANNING.md` for complete guide

---

## Orchestrator Workflow (Multi-Agent Delegation)

**CRITICAL: When spawning subagents with Task tool, follow the orchestrator workflow.**

### When to Use Orchestration

Use orchestration (spawn subagents) when:
- Multiple independent tasks can run in parallel
- Work can be partitioned without conflicts
- Speedup factor > 1.5x
- `htmlgraph find features --status todo` shows 2+ independent tasks

### 6-Phase Parallel Workflow

```
1. ANALYZE   → Check dependencies with htmlgraph analytics
2. PREPARE   → Cache shared context, partition files
3. DISPATCH  → Spawn agents in ONE message (parallel)
4. MONITOR   → Track health metrics per agent
5. AGGREGATE → Collect results, detect conflicts
6. VALIDATE  → Verify outputs, run tests
```

### Task() Delegation Pattern

Dispatch all independent tasks in a single message for true parallelism:

```python
# 1. ANALYZE - Check what's available
# Run: htmlgraph analytics summary
# Run: htmlgraph find features --status todo

# 2. DISPATCH - Spawn all agents in ONE message (parallel)
Task(
    subagent_type="htmlgraph:gemini-operator",
    description="Research: Find API endpoints",
    prompt="Research the codebase and find all API endpoints in src/api/. Document patterns."
)
Task(
    subagent_type="htmlgraph:sonnet-coder",
    description="Implement feat-123",
    prompt="Implement feat-123. Context: [paste research findings]. Test command: uv run pytest"
)

# 3. VALIDATE - After agents complete, run quality gates
# Run: uv run pytest && uv run ruff check
```

### Parallel vs Sequential

| Scenario | Pattern |
|----------|---------|
| Independent tasks | Dispatch ALL in one message |
| Task B depends on Task A | Run A first, then B |
| Research before implementation | Sequential (research blocks coding) |
| Tests + docs update | Parallel (independent) |

### Anti-Patterns to Avoid

❌ **DON'T:** Send Task calls in separate messages (sequential)
```python
# BAD - agents run one at a time
result1 = Task(...)  # Wait
result2 = Task(...)  # Then next
```

✅ **DO:** Send all Task calls in ONE message (parallel)
```python
# GOOD - true parallelism, all dispatched at once
Task(prompt="Update docs...", subagent_type="htmlgraph:gemini-operator")
Task(prompt="Update tests...", subagent_type="htmlgraph:sonnet-coder")
Task(prompt="Create migration guide...", subagent_type="htmlgraph:gemini-operator")
```

**See also**: `/htmlgraph:orchestrator-directives-skill` for detailed delegation patterns

---

## Work Type Classification (Phase 1)

**NEW: HtmlGraph now automatically categorizes all work by type to differentiate exploratory work from implementation.**

### Work Type Categories

All events are automatically tagged with a work type based on the active feature:

- **feature-implementation** - Building new functionality (feat-*)
- **spike-investigation** - Research and exploration (spike-*)
- **bug-fix** - Correcting defects (bug-*)
- **maintenance** - Refactoring and tech debt (chore-*)
- **documentation** - Writing docs (doc-*)
- **planning** - Design decisions (plan-*)
- **review** - Code review
- **admin** - Administrative tasks

### Creating Spikes (Investigation Work)

Use spikes for timeboxed investigation:

```bash
# Create a spike for research
htmlgraph spike create "Investigate OAuth providers"

# Start working on it
htmlgraph spike start <spike-id>

# Mark complete when done
htmlgraph spike complete <spike-id>
```

**When to create a spike:**
- Investigating technical implementation options
- Researching system design decisions
- Identifying and assessing project risks
- Uncategorized investigation work

### Creating Chores (Maintenance Work)

Use chores for maintenance tasks:

```bash
# Create a chore
htmlgraph chore create "Refactor authentication module"

# Start working on it
htmlgraph chore start <chore-id>
```

**When to create a chore:**
- Fixing defects and errors (corrective)
- Adapting to environment changes (adaptive)
- Improving performance/maintainability (perfective)
- Preventing future problems via refactoring (preventive)

### Session Work Type Analytics

```bash
# View current project status
htmlgraph status

# View snapshot with work distribution
htmlgraph snapshot --summary

# Find in-progress work by type
htmlgraph find features --status in-progress
htmlgraph find bugs --status open
```

### Automatic Work Type Inference

Work type is automatically inferred from work item ID prefix:

- `feat-*` → feature-implementation
- `spike-*` → spike-investigation
- `bug-*` → bug-fix
- `chore-*` → maintenance

**No manual tagging required!** The system automatically categorizes your work based on what you're working on.

### Why This Matters

Work type classification enables you to:

1. **Differentiate exploration from implementation** - "How much time was spent researching vs building?"
2. **Track technical debt** - "What % of work is maintenance vs new features?"
3. **Measure innovation** - "What's our spike-to-feature ratio?"
4. **Session context** - "Was this primarily an exploratory session or implementation?"

## Research Checkpoint - MANDATORY Before Implementation

**CRITICAL: Always research BEFORE implementing solutions. Never guess.**

HtmlGraph enforces a research-first philosophy. This emerged from dogfooding where we repeatedly made trial-and-error attempts before researching documentation.

**Complete debugging guide:** See [DEBUGGING.md](../../../DEBUGGING.md)

### When to Research (Before ANY Implementation)

**STOP and research if:**
- ❓ You encounter unfamiliar errors or behaviors
- ❓ You're working with Claude Code hooks, plugins, or configuration
- ❓ You're implementing a solution based on assumptions
- ❓ Multiple attempted fixes have failed
- ❓ You're debugging without understanding root cause
- ❓ You're about to "try something" to see if it works

### Research-First Workflow

**REQUIRED PATTERN:**
```
1. RESEARCH     → Use documentation, claude-code-guide, GitHub issues
2. UNDERSTAND   → Identify root cause through evidence
3. IMPLEMENT    → Apply fix based on understanding
4. VALIDATE     → Test to confirm fix works
5. DOCUMENT     → Capture learning in HtmlGraph spike
```

**❌ NEVER do this:**
```
1. Try Fix A    → Doesn't work
2. Try Fix B    → Doesn't work
3. Try Fix C    → Doesn't work
4. Research     → Find actual root cause
5. Apply fix    → Finally works
```

### Available Research Tools

**Debugging Agents (use these!):**
- **Researcher Agent** - Research documentation before implementing
  - Activate via: `.claude/agents/researcher.md`
  - Use for: Documentation research, pattern identification

- **Debugger Agent** - Systematically analyze errors
  - Activate via: `.claude/agents/debugger.md`
  - Use for: Error analysis, hypothesis testing

- **Test Runner Agent** - Enforce quality gates
  - Activate via: `.claude/agents/test-runner.md`
  - Use for: Pre-commit validation, test execution

**Claude Code Tools:**
```bash
# Built-in debug commands
claude --debug <command>        # Verbose output
/hooks                          # List active hooks
/hooks PreToolUse              # Show specific hook
/doctor                         # System diagnostics
claude --verbose               # Detailed logging
```

**Documentation Resources:**
- Claude Code docs: https://code.claude.com/docs
- Hook documentation: https://code.claude.com/docs/en/hooks.md
- GitHub issues: https://github.com/anthropics/claude-code/issues

### Research Checkpoint Questions

**Before implementing ANY fix, ask yourself:**
- [ ] Did I research the documentation for this issue?
- [ ] Have I used the researcher agent or claude-code-guide?
- [ ] Is this approach based on evidence or assumptions?
- [ ] Have I checked GitHub issues for similar problems?
- [ ] What debug tools can provide more information?
- [ ] Am I making an informed decision or guessing?

### Example: Correct Research-First Pattern

**Scenario**: Hooks are duplicating

**✅ CORRECT (Research First):**
```
1. STOP - Don't remove files yet
2. RESEARCH - Read Claude Code hook loading documentation
3. Use /hooks command to inspect active hooks
4. Check GitHub issues for "duplicate hooks"
5. UNDERSTAND - Hooks from multiple sources MERGE
6. IMPLEMENT - Remove duplicates from correct source
7. VALIDATE - Verify fix with /hooks command
8. DOCUMENT - Create spike with findings
```

**❌ WRONG (Trial and Error):**
```
1. Remove .claude/hooks/hooks.json - Still broken
2. Clear plugin cache - Still broken
3. Remove old plugin versions - Still broken
4. Remove marketplaces symlink - Still broken
5. Finally research documentation
6. Find root cause: Hook merging behavior
```

### Documenting Research Findings

**REQUIRED: Capture all research in HtmlGraph spike:**

```bash
htmlgraph spike create "Research: [Problem] — Root cause: [finding]. Sources: [docs/issues]. Solution: [what was chosen and why]."
```

### Integration with Pre-Work Validation

The validation hook already prevents multi-file changes without a feature. Research checkpoints add another layer:

1. **Pre-Work Validation** - Ensures work is tracked
2. **Research Checkpoint** - Ensures decisions are evidence-based

Both work together to maintain quality and prevent wasted effort.

---

## Feature Creation Decision Framework

**CRITICAL**: Use this framework to decide when to create a feature vs implementing directly.

### Quick Decision Rule

Create a **FEATURE** if ANY apply:
- Estimated >30 minutes of work
- Involves 3+ files
- Requires new automated tests
- Affects multiple components
- Hard to revert (schema, API changes)
- Needs user/API documentation

Implement **DIRECTLY** if ALL apply:
- Single file, obvious change
- <30 minutes work
- No cross-system impact
- Easy to revert
- No tests needed
- Internal/trivial change

### Decision Tree (Quick Reference)

```
User request received
  ├─ Bug in existing feature? → See Bug Fix Workflow in WORKFLOW.md
  ├─ >30 minutes? → CREATE FEATURE
  ├─ 3+ files? → CREATE FEATURE
  ├─ New tests needed? → CREATE FEATURE
  ├─ Multi-component impact? → CREATE FEATURE
  ├─ Hard to revert? → CREATE FEATURE
  └─ Otherwise → IMPLEMENT DIRECTLY
```

### Examples

**✅ CREATE FEATURE:**
- "Add user authentication" (multi-file, tests, docs)
- "Implement session comparison view" (new UI, Playwright tests)
- "Fix attribution drift algorithm" (complex, backend tests)

**❌ IMPLEMENT DIRECTLY:**
- "Fix typo in README" (single file, trivial)
- "Update CSS color" (single file, quick, reversible)
- "Add missing import" (obvious fix, no impact)

### Default Rule

**When in doubt, CREATE A FEATURE.** Over-tracking is better than losing attribution.

See `docs/WORKFLOW.md` for the complete decision framework with detailed criteria, thresholds, and edge cases.

## Session Workflow Checklist

**MANDATORY: Follow this checklist for EVERY session. No exceptions.**

### Session Start (DO THESE FIRST)
1. ✅ Activate this skill (done automatically)
2. ✅ **AUTO-SPIKE CREATED:** Validation hook automatically creates an auto-spike for session exploration (see "Auto-Spike Integration" section)
3. ✅ **RUN:** `htmlgraph status` - Get comprehensive session context (optimized, 1 call)
   - Replaces: status + feature list + session list + git log + analytics
   - Reduces context usage from 30% to <5%
4. ✅ Review active features and decide if you need to create a new one
5. ✅ Greet user with brief status update
6. ✅ **RESEARCH CHECKPOINT:** Before implementing ANY solution:
   - Did I research documentation first?
   - Am I using evidence or assumptions?
   - Should I activate researcher/debugger agent?
7. ✅ **DECIDE:** Create feature or implement directly? (use decision framework)
8. ✅ **If creating feature:** Run `htmlgraph feature start <id>`

### During Work (DO CONTINUOUSLY)
1. ✅ Feature MUST be marked "in-progress" before you write any code
   - ⚠️ **VALIDATION NOTE:** Validation will warn or deny multi-file changes without active feature (see "Pre-Work Validation" section)
   - Single-file changes are allowed with warning
   - 3+ file changes require active feature to proceed
2. ✅ **CRITICAL:** Mark each step complete IMMEDIATELY after finishing it (use CLI)
3. ✅ Document ALL decisions as you make them
4. ✅ Test incrementally - don't wait until the end
5. ✅ Watch for drift warnings and act on them immediately

#### How to Mark Steps Complete

**IMPORTANT:** After finishing each step, mark it complete using the CLI:

```bash
# Mark a step complete
htmlgraph feature step-complete <feature-id> <step-number>
```

**Step numbering is 0-based** (first step = 0, second step = 1, etc.)

**When to mark complete:**
- ✅ IMMEDIATELY after finishing a step
- ✅ Even if you continue working on the feature
- ✅ Before moving to the next step
- ❌ NOT at the end when all steps are done (too late!)

**Example workflow:**
1. Start feature: `htmlgraph feature start feat-123`
2. Work on step 0 (e.g., "Design models")
3. **MARK STEP 0 COMPLETE** → `htmlgraph feature step-complete feat-123 0`
4. Work on step 1 (e.g., "Create templates")
5. **MARK STEP 1 COMPLETE** → `htmlgraph feature step-complete feat-123 1`
6. Continue until all steps done
7. Complete feature: `htmlgraph feature complete feat-123`

### Session End (MUST DO BEFORE MARKING COMPLETE)
1. ✅ **RUN TESTS:** `uv run pytest` - All tests MUST pass
2. ✅ **VERIFY ATTRIBUTION:** Check that activities are linked to correct feature
3. ✅ **CHECK STEPS:** ALL feature steps MUST be marked complete
4. ✅ **CLEAN CODE:** Remove all debug code, console.logs, TODOs
5. ✅ **COMMIT WORK:** Git commit your changes IMMEDIATELY (allows user rollback)
   - Do this BEFORE marking the feature complete
   - Include the feature ID in the commit message
6. ✅ **COMPLETE FEATURE:** Run `htmlgraph feature complete <id>`
7. ✅ **UPDATE EPIC:** If part of epic, mark epic step complete

**REMINDER:** Completing a feature without doing all of the above means incomplete work. Don't skip steps.

## Handling Drift Warnings

When you see a drift warning like:
> Drift detected (0.74): Activity may not align with feature-self-tracking

Consider:
1. **Is this expected?** Sometimes work naturally spans multiple features
2. **Should you switch features?** Use `htmlgraph feature start <id>` to change attribution
3. **Is the feature scope wrong?** The feature's file patterns or keywords may need updating

## Session Continuity

At the start of each session:
1. Review previous session summary (if provided)
2. Note current feature progress
3. Identify what remains to be done
4. Ask the user what they'd like to work on

At the end of each session:
1. The SessionEnd hook will generate a summary
2. All activities are preserved in `.htmlgraph/sessions/`
3. Feature progress is updated automatically

## Best Practices

### Commit Messages
Include feature context:
```
feat(feature-id): Description of the change

- Details about what was done
- Why this approach was chosen

🤖 Generated with Claude Code
```

### Task Descriptions
When using Bash tool, always provide a description:
```bash
# Good - descriptive
Bash(description="Install dependencies for auth feature")

# Bad - no context
Bash(command="npm install")
```

### Decision Documentation
When making architectural decisions:

1. Track with `htmlgraph track "Decision" "Chose X over Y because Z"`
2. Or note in the feature's HTML file under activity log

## Dashboard Access

View progress visually:
```bash
htmlgraph serve
# Open http://localhost:8080
```

The dashboard shows:
- Kanban board with feature status
- Session history with activity logs
- Graph visualization of dependencies

## Key Files

- `.htmlgraph/features/` - Feature HTML files (the graph nodes)
- `.htmlgraph/sessions/` - Session HTML files with activity logs
- `index.html` - Dashboard (open in browser)

## Integration Points

HtmlGraph hooks track:
- **SessionStart**: Creates session, provides feature context
- **PostToolUse**: Logs every tool call with attribution
- **UserPromptSubmit**: Logs user queries
- **SessionEnd**: Finalizes session with summary

All data is stored as HTML files - human-readable, git-friendly, browser-viewable.
