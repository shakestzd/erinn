---
id: orchestrator-directives
name: Orchestrator Directives Skill
description: >-
  wipnote orchestration patterns for AI-assisted development. Use when working on code in an
  wipnote project — provides delegation patterns, model selection, quality gates, and work
  tracking guidance. Activate when planning work, delegating to agents, debugging, building
  features, or managing tasks.
trigger: "when user asks about delegation, orchestration, or cost optimization"
visibility: "always"
tags: ["delegation", "orchestration", "cost-optimization", "multi-ai", "spawners"]
---

# Orchestrator Directives Skill

Use this skill for delegation patterns and decision frameworks in orchestrator mode.

**Trigger keywords:** orchestrator, delegation, subagent, task coordination, parallel execution, cost-first, spawner

---

## Quick Start - What is Orchestration?

Delegate tactical work to specialized subagents while you focus on strategic decisions. Save Claude Code context (expensive) by using FREE/CHEAP AIs for appropriate tasks.

**Basic pattern:**
```python
# Exploration/research: try the free agy research sidecar first
Bash('agy -p "Search codebase for authentication patterns..." --dangerously-skip-permissions 2>&1')
# fallback → use @patch-coder
```

**When to use:** ALWAYS use for complex tasks requiring research, code generation, git operations, or any work that could fail and require retries.

**For complete guidance:** See sections below or run `/multi-ai-orchestration` for model selection details.

---

## Harness-Aware Research Delegation Contract

In orchestrator mode, web/docs research and multi-file codebase exploration are tactical work. They **MUST run in a sidecar context**, not in the main orchestrator context, whenever a sidecar dispatch surface is available. Research-first does not mean the orchestrator personally performs broad research; it means the orchestrator dispatches a researcher, codebase reader, or external CLI sidecar before committing to an implementation path.

Before starting research, inspect the available dispatch surface in the active harness:

- **Claude Code** — use `Task` / subagent dispatch when exposed.
- **Codex** — when `multi_agent_v1` or native `wipnote-*` custom agents are exposed, prefer native subagents such as `wipnote-researcher`, `wipnote-patch-coder`, `wipnote-feature-coder`, and `wipnote-test-runner`. Use nested `codex exec` only when those native subagents are unavailable.
- **Antigravity** — use harness-native multi-agent or custom-agent spawn when exposed.
- **External CLI sidecar** — use `agy`, `codex exec`, or another documented sidecar CLI when native or harness-local dispatch is unavailable and the sidecar is appropriate.
- **No dispatch surface available** — stop and report `delegation unavailable in this harness/session`; do not silently convert broad research into main-context work.

Do not use main-context `web.search_query`, `web.open`, or broad local glob/search loops for docs gathering or repository exploration when a researcher sidecar is expected. Narrow exceptions are allowed only when:

1. The user explicitly asks the orchestrator to browse or verify one fact directly.
2. A higher-priority instruction requires latest/high-stakes fact verification and no sidecar is available.
3. A sidecar failed or is unavailable, and the orchestrator performs a one-shot confirmation before reporting the limitation.

When using an exception, keep the main-context research minimal and record why sidecar delegation was unavailable or skipped.

---

## Batching wipnote CLI Calls (IMPERATIVE)

Each Bash tool call spends one agent turn from the user's quota. **Chain wipnote bookkeeping commands with `&&` into a single Bash invocation whenever possible.** wipnote exists to reduce agent overhead — do not add it back by issuing one Bash call per `wipnote link add`.

**Do this (1 tool call):**
```bash
wipnote bug create "A" --track trk-xxx --description "..." && \
wipnote bug create "B" --track trk-xxx --description "..." && \
wipnote link add feat-aaa feat-bbb --rel blocks && \
wipnote link add feat-ccc feat-ddd --rel relates_to
```

**Never 4 separate Bash calls for the same thing.**

**When NOT to chain:** only when a downstream command must parse the ID printed by an earlier command. Chain the creators into one call, then chain the dependents into a second call. Two calls, not eight.

Applies to `feature/bug/spike/track/plan create|start|complete|add-step`, `link add|remove`, `feature edit`, and any other wipnote bookkeeping.

---

## Step Tracking via Task Tool (Orchestrator Only)

The active work item's step checklist is updated automatically when the orchestrator calls `TaskCreate` and `TaskUpdate`. wipnote's `TaskCreated` hook (`internal/hooks/task_tracking.go`) shells out to `wipnote feature add-step` on every TaskCreate; the `TaskCompleted` hook increments the step counter on TaskUpdate(status="completed").

**Important: subagents do NOT have `TaskCreate`/`TaskUpdate` in their tools allowlist.** These are MCP/agent-teams tools available only to the orchestrator. Telling a subagent "use the Task tool" in its prompt does nothing — the tool isn't there.

**Use TaskCreate when:**
- Dispatching subagent work that maps to a step on the active work item
- Starting any multi-step task with 3+ distinct sub-steps you can name in advance
- About to spawn multiple parallel subagents — one TaskCreate per dispatched subagent

**Use TaskUpdate(status="completed") when:**
- A subagent returns with the step done
- You verify quality gates pass for that step
- The unit of work matches a slice in the work item's design

**Skip TaskCreate when:**
- The work is a single trivial action (one Bash call, one file read)
- You're in conversation/clarification mode, not execution mode
- The user's request is purely informational

**Concrete pattern:**

```
TaskCreate(subject="Strip skills frontmatter from 5 agent files",
           description="Edit plugin/agents/{patch,feature,architect,researcher,test-runner}.md")
→ subject + task ID become a step on the active feature
→ dispatch the subagent
→ on return, TaskUpdate(taskId, status="completed")
→ wipnote step counter increments
```

Verify via `wipnote feature show <id>` — `Steps: N/M complete` should reflect the TaskUpdate calls. Steps remain unchecked if you skipped TaskCreate.

---

## CRITICAL: Cost-First Delegation (IMPERATIVE)

**Claude Code is EXPENSIVE. You MUST delegate to FREE/CHEAP AIs first.**

<details>
<summary><strong>Cost Comparison & Pre-Delegation Checklist</strong></summary>

### PRE-DELEGATION CHECKLIST (MUST EXECUTE BEFORE EVERY TASK())

Ask these questions IN ORDER:

1. **Can agy do this?** → Exploration, research, batch ops, file analysis
   - YES = MUST try `Bash("agy ...")` first (FREE), fallback to patch-coder

2. **Is this code work?** → Implementation, fixes, tests, refactoring
   - YES = In Codex, prefer native `wipnote-patch-coder` / `wipnote-feature-coder` / `wipnote-test-runner` when available; otherwise try `Bash("codex ...")` first (70% cheaper than Claude), fallback to feature-coder

3. **Is this git/GitHub?** → Commits, PRs, issues, branches
   - YES = MUST try `Bash("copilot ...")` first (60% cheaper, GitHub-native), fallback to patch-coder

4. **Does this need deep reasoning?** → Architecture, complex planning
   - YES = Use Claude Opus (expensive, but strategically needed)

5. **Is this coordination?** → Multi-agent work
   - YES = Use Claude Sonnet (mid-tier)

6. **ONLY if above fail** → Haiku (fallback)

### Cost Comparison Examples

| Task | WRONG (Cost) | CORRECT (Cost) | Savings |
|------|-------------|----------------|---------|
| Search 100 files | use the appropriate Gemini agent invocation ($15-25) | agy sidecar (FREE) | 100% |
| Generate code | use the appropriate Gemini agent invocation ($10) | Codex spawner ($3) | 70% |
| Git commit | use the appropriate Gemini agent invocation ($5) | Copilot spawner ($2) | 60% |
| Strategic decision | Direct task ($20) | Claude Opus ($50) | Must pay for quality |

### WRONG vs CORRECT Examples

```
WRONG (wastes Claude quota):
- Code implementation → use the appropriate Gemini agent invocation               # USE Bash("codex ..."), fallback feature-coder
- Git commits → use the appropriate Gemini agent invocation                       # USE Bash("copilot ..."), fallback patch-coder
- File search → use the appropriate Gemini agent invocation                       # USE Bash("agy ...") (FREE!)
- Research → use the appropriate Gemini agent invocation                          # USE Bash("agy ...") (FREE!)

CORRECT (cost-optimized):
- Code implementation → Codex native `wipnote-feature-coder` when available; else Bash("codex ...")
- Git commits → Bash("copilot ...")               # Cheap, GitHub-native; fallback patch-coder
- File search → Bash("agy ...")                # FREE!; fallback patch-coder
- Research → Bash("agy ...")                   # FREE!; fallback patch-coder
- Strategic decisions → Claude Opus               # Expensive, but needed
- Coder agents → Primary in Codex native multi-agent sessions; fallback elsewhere when CLI tools fail or aren't installed
```

</details>

---

## Core Concepts

<details>
<summary><strong>Orchestrator vs Executor Roles</strong></summary>

**Orchestrator (You):**
- Makes strategic decisions
- Delegates tactical work
- Tracks progress with SDK
- Coordinates parallel subagents
- Only executes: use the appropriate Gemini agent invocation, AskUserQuestion(), TodoWrite(), SDK operations

**Executor (Subagent):**
- Handles tactical implementation
- Researches specific problems
- Fixes issues with retries
- Reports findings back
- Consumes resources independently (saves your context)

**Why separation matters:**
- Context preservation (MUST prevent failures from compounding in your context)
- Parallel efficiency (MUST run multiple subagents simultaneously)
- Cost optimization (ALWAYS use cheaper subagents than Claude Code)
- Error isolation (MUST keep failures in subagent context)

</details>

<details>
<summary><strong>Why Delegation Matters: Context Cost Model</strong></summary>

**What looks like "one bash call" becomes many:**
- Initial command fails → need to retry
- Test hooks break → need to fix code → retry
- Push conflicts → need to pull/merge → retry
- Each retry consumes tokens

**Context cost comparison:**
```
Direct execution (fails):
  bash call 1 → fails
  bash call 2 → fails
  bash call 3 → fix code
  bash call 4 → bash call 1 retry
  bash call 5 → bash call 2 retry
  = 5+ tool calls, context consumed

Delegation (cascades isolated):
  use the appropriate Gemini agent invocation → 1 tool call
  Read result → 1 tool call
  = 2 tool calls, clean context
```

**Token savings:**
- Each failed retry: 2,000-5,000 tokens wasted
- Cascading failures: 10,000+ tokens wasted
- Subagent isolation: None of that pollution in orchestrator context

</details>

<details>
<summary><strong>Decision Framework: When to Delegate vs Execute</strong></summary>

Ask yourself these questions:

1. **Will this likely be ONE tool call?**
   - Uncertain → DELEGATE
   - Certain → MAY do directly (single file read, quick check)

2. **Does this require error handling?**
   - If yes → DELEGATE (subagent handles retries)

3. **Could this cascade into multiple operations?**
   - If yes → DELEGATE

4. **Is this strategic or tactical?**
   - Strategic (decisions) → Do directly
   - Tactical (execution) → DELEGATE

**Rule of thumb:** When in doubt, ALWAYS DELEGATE. Cascading failures are expensive.

### Data File Reads — Direct Read Tool Permitted

The orchestrator MAY call the `Read` tool directly, without delegating to `researcher` or `reader`, when ALL of the following hold:

1. The file is a **data or config file**: YAML, JSON, TOML, Markdown (non-source), `.wipnote/**/*.yaml`, `.wipnote/**/*.html`, log files, or plain text output
2. It is a **single-file read** — not a glob-then-read pattern, not multiple files
3. The task is **retrieval only** — you need the content to compose a subsequent delegation or user response, not to modify code

**Anti-pattern this replaces:** Delegating a 30 KB YAML read to `researcher` pays ~60 s of skill-injection overhead for work that takes <100 ms inline. Do not delegate single data-file reads.

**Source code and writes still MUST delegate:**
- `.go`, `.ts`, `.py`, and other source files → delegate to researcher or coder
- Any `Edit` or `Write` operation → delegate to appropriate coder agent
- Multi-file reads or glob patterns → use `reader` (zero-skill agent)

</details>

<details>
<summary><strong>Three Allowed Direct Operations</strong></summary>

Only these can be executed directly by orchestrator:

1. **use the appropriate Gemini agent invocation** - Delegation itself
   - Use spawner subagent types when possible
   - Example: `use @feature-coder`

2. **AskUserQuestion()** - Clarifying requirements
   - Get user input before delegating
   - Example: `AskUserQuestion("Should we use Redis or PostgreSQL?")`

3. **TodoWrite()** - Tracking work items
   - Create/update todo lists
   - Example: `TodoWrite(todos=[...])`

**wipnote CLI operations** (create features and bugs):
- `wipnote feature create "title" --track <trk-id>`
- `wipnote bug create "title" --track <trk-id>`

**Track and Lineage Search (MANDATORY before creating ANY work item):**

Before creating ANY feature, bug, or spike:
1. Run `wipnote relevant <topic>` — it searches ALL items including completed tracks, plans, and features (`wipnote find` also returns all statuses without `--status`). The CIGS roster shows only open items; an empty roster does NOT mean no lineage exists.
2. If the first result is generic, ambiguous, or only loosely related, inspect provenance before creating/attaching anything: run `wipnote lineage <candidate>`, `wipnote trace <candidate>`, and/or `wipnote history <candidate>` on the best plan/feature/track candidates until you can name the closest causal parent. Prefer a precise edge to that feature or plan: use `spawned_from` if this work exists because another item's investigation surfaced it, or `caused_by` if genuine defect causality. Avoid broad `part_of` edges to catch-all tracks.
3. Match the new work against existing tracks, plans, and completed features — attach to the best fitting existing lineage rather than creating a new standalone item.
4. Only create a new track if NO existing track covers the scope; run `wipnote track list` to enumerate all tracks.
5. When in doubt, ask the user which track or plan to use.
6. `--standalone <reason>` requires justification naming what was searched (e.g., `"searched: wipnote relevant <topic> — no existing track/plan covers this scope"`).

This applies equally to features, bugs, and spikes with `--track` or `--plan`:
- Search completed lineage first; a completed track is still valid lineage for new work in the same scope.
- Do not stop at a generic track when a nearby feature/plan is visible. First prove no closer causal node exists by checking lineage/trace/history for the relevant candidates.
- Only create a new track as a last resort.

Everything else MUST be delegated.

</details>

---

## Model Selection & Spawner Guide

<details>
<summary><strong>Spawner Selection Decision Tree</strong></summary>

**Decision tree (check each in order):**

1. **Is this exploration/research/analysis?**
   - Files search: YES → agy sidecar (FREE)
   - Pattern analysis: YES → agy sidecar (FREE)
   - Documentation reading: YES → agy sidecar (FREE)
   - Learning unfamiliar system: YES → agy sidecar (FREE)

2. **Is this code implementation/testing?**
   - Generate code: YES → Codex spawner (70% cheaper)
   - Fix bugs: YES → Codex spawner
   - Write tests: YES → Codex spawner
   - Refactor code: YES → Codex spawner

3. **Is this git/GitHub operation?**
   - Commit changes: YES → Copilot spawner (60% cheaper, GitHub-native)
   - Create PR: YES → Copilot spawner
   - Manage branches: YES → Copilot spawner
   - Review code: YES → Copilot spawner

4. **Does this need deep reasoning?**
   - Architecture decisions: YES → Claude Opus (expensive, but needed)
   - Complex design: YES → Claude Opus
   - Strategic planning: YES → Claude Opus

5. **Is this multi-agent coordination?**
   - Coordinate multiple spawners: YES → Claude Sonnet (mid-tier)
   - Complex workflows: YES → Claude Sonnet

6. **All else fails** → use the appropriate Gemini agent invocation with Haiku (fallback)

**Delegation Pattern:**
- `Bash("agy ...")` - FREE, exploration & research → fallback: patch-coder
- `Bash("codex ...")` - Cheap code specialist, implementation & testing → fallback: feature-coder
- `Bash("copilot ...")` - Cheap git specialist, GitHub integration → fallback: patch-coder
- Coder agents (`patch-coder`, `feature-coder`) - Fallback only when CLI tools fail

</details>

<details>
<summary><strong>Spawner Details & Configuration</strong></summary>

### Antigravity CLI — agy (FREE - Exploration)
```bash
agy -p "Analyze codebase for:
- All authentication patterns
- OAuth implementations
- Session management
- JWT usage" --dangerously-skip-permissions 2>&1
```

**If agy fails/unavailable → fallback to patch-coder**

**Best for:**
- File searching (FREE!)
- Pattern analysis (FREE!)
- Documentation research (FREE!)
- Understanding unfamiliar systems (FREE!)

### Codex CLI (Cheap - Code)
```bash
codex exec "Implement OAuth authentication:
- Add JWT token generation
- Include error handling
- Write unit tests" --full-auto --json -m gpt-4.1-mini -C . 2>&1
```

**In Codex, prefer native `wipnote-feature-coder` / `wipnote-patch-coder` / `wipnote-test-runner` first. If native subagents are unavailable or fail → use `codex exec`, then fallback to feature-coder.**

**Best for:**
- Code generation
- Bug fixes
- Test writing
- Refactoring
- Sandboxed execution

### Copilot CLI (Cheap - Git)
```bash
copilot -p "Commit changes:
- Message: 'feat: add OAuth authentication'
- Files: src/auth/*.py, tests/test_auth.py
- Do NOT push" --allow-all-tools --no-color --add-dir . 2>&1
```

**If copilot fails/unavailable → fallback to patch-coder**

**Best for:**
- Git commits (60% cheaper than Task)
- PR creation
- Branch management
- GitHub integration
- Resolving conflicts

### use the appropriate Gemini agent invocation with feature-coder/architect-coder (Strategic)
```python
Use Gemini agent invocation with:
    message="Design authentication architecture...",
    workflow="feature-coder"  # or "architect-coder" for deep reasoning
```

**feature-coder (Mid-tier):**
- Coordinate complex workflows
- Multi-agent orchestration
- Fallback when spawners fail

**architect-coder (Expensive):**
- Deep reasoning
- Architecture decisions
- Strategic planning
- When quality matters more than cost

</details>

---

## Delegation Patterns & Examples

<details>
<summary><strong>Basic Delegation Pattern</strong></summary>

**Simple exploration (try CLI first):**
```bash
agy -p "Search codebase for authentication patterns and summarize findings" \
  --dangerously-skip-permissions 2>&1
# fallback → use @patch-coder
```

**Code implementation (try CLI first):**
```bash
codex exec "Implement OAuth authentication endpoint with JWT support" \
  --full-auto --json -m gpt-4.1-mini -C . 2>&1
# fallback → use @feature-coder
```

**Code implementation (Codex-native preferred when available):**
```text
Spawn the native `wipnote-feature-coder` subagent for implementation work.
Use nested `codex exec` only when the native Codex subagent surface is unavailable.
```

**Git operations (try CLI first):**
```bash
copilot -p "Commit changes with message: 'feat: add OAuth authentication'. Do NOT push." \
  --allow-all-tools --no-color --add-dir . 2>&1
# fallback → use @patch-coder
```

</details>

<details>
<summary><strong>Git/Code Operations (Bash-first, patch-coder fallback)</strong></summary>

**Try the Copilot CLI directly via Bash first, then delegate to patch-coder if unavailable.**

```bash
# Priority 1: Bash-copilot (preferred)
copilot -p "Stage files: <list>. Commit with message: '<message>'. Do NOT push." \
  --allow-all-tools --no-color --add-dir . 2>&1
```

```python
# Priority 2: patch-coder fallback (if copilot fails or not installed)
Use Gemini agent invocation with:
    agent="@patch-coder",
    description="Commit and push changes",
    message="Stage files: <list>. Commit with message: 'feat: add X'. Do NOT push.",
```

**Pattern:** orchestrator tries the CLI directly, falls back to a coder agent.

</details>

<details>
<summary><strong>Code Generation (Bash-first, feature-coder fallback)</strong></summary>

**For implementation, refactoring, and structured output tasks:**

```bash
# Priority 1 outside Codex-native sessions: Bash-codex
codex exec "TASK_DESCRIPTION" --full-auto --json -m gpt-4.1-mini -C . 2>&1
```

```python
# Priority 1 in Codex-native sessions, or Priority 2 elsewhere
Use Gemini agent invocation with:
    agent="@feature-coder",
    description="Implement feature X",
    message="Add OAuth authentication to the login endpoint.",
```

**Pattern:** in Codex native multi-agent sessions, use `wipnote-feature-coder` first; otherwise try the CLI directly, then fall back to a coder agent.
Always use `-m gpt-4.1-mini` for nested `codex exec` (never expensive gpt-5.4 default).

</details>

<details>
<summary><strong>Research & Analysis (Bash-first, patch-coder fallback)</strong></summary>

**For codebase exploration, documentation research, and large-context analysis:**

```bash
# Priority 1: Bash-agy (preferred — FREE)
agy -p "TASK_DESCRIPTION" --dangerously-skip-permissions 2>&1
```

```python
# Priority 2: patch-coder fallback (if agy fails or not installed)
Use Gemini agent invocation with:
    agent="@patch-coder",
    description="Research auth patterns",
    message="Analyze all authentication patterns in this codebase. Find security gaps.",
```

**Pattern:** orchestrator tries the CLI directly, falls back to a coder agent.

</details>

<details>
<summary><strong>Parallel Delegation (Multiple Independent Tasks)</strong></summary>

**MANDATORY: Always analyze parallelizability when 2+ tasks are identified.**

Before presenting recommendations or starting multi-task work, ALWAYS:
1. Check dependency graph — do any tasks depend on outputs of others?
2. Check file overlap — do tasks touch the same files/modules?
3. If independent → propose parallel worktree execution as the DEFAULT
4. If dependent → identify the critical path and parallelize what you can

**Decision matrix:**

| Dependency? | File Overlap? | Action |
|-------------|---------------|--------|
| No | No | Parallel worktrees (DEFAULT) |
| No | Yes | Sequential (same files = merge conflicts) |
| Yes | No | Pipeline (parallel where deps allow) |
| Yes | Yes | Sequential |

**Pattern: Spawn all at once in isolated worktrees**

```python
# Launch parallel agents in worktrees — one per feature
Use Gemini agent invocation with:
    agent="@feature-coder",
    description="Feature A",
    message="Implement feature A...",
    isolation="worktree",
    run_in_background=True,

Use Gemini agent invocation with:
    agent="@feature-coder",
    description="Feature B",
    message="Implement feature B...",
    isolation="worktree",
    run_in_background=True,

Use Gemini agent invocation with:
    agent="@patch-coder",
    description="Feature C (simple)",
    message="Implement feature C...",
    isolation="worktree",
    run_in_background=True,
```

**Benefits:**
- 3 tasks in parallel: time = max(T1, T2, T3) instead of T1+T2+T3
- Cost optimization: Uses cheapest model for each task
- Worktree isolation: No merge conflicts during execution
- Independent results: Each task tracked separately

**After completion:** Merge worktree branches to main, run quality gates, clean up.

</details>

<details>
<summary><strong>Sequential Delegation with Dependencies</strong></summary>

**Pattern: Chain dependent tasks in sequence**

```python
# 1. Research existing patterns (free agy research sidecar)
Bash('agy -p "Find all OAuth implementations in codebase..." --dangerously-skip-permissions 2>&1')
# fallback → use @patch-coder

# 2. Wait for research, then implement
# (In next message after reading result)
research_findings = "..."  # Read from previous task result

Use Gemini agent invocation with:
    workflow="codex",
    description="Implement OAuth based on research",
    message=f"""
    Implement OAuth using discovered patterns:
    {research_findings}
    """

# 3. Wait for implementation, then commit
Use Gemini agent invocation with:
    workflow="copilot",
    description="Commit implementation",
    message="Commit OAuth implementation..."
```

**When to use:** When later tasks depend on earlier results

</details>

<details>
<summary><strong>wipnote Result Retrieval</strong></summary>

**Subagents report findings automatically:**

When a use the appropriate Gemini agent invocation completes, findings are available via CLI:
```bash
# Check recent spikes
wipnote spike list

# View specific spike
wipnote spike show <id>
```

**Pattern: Read findings after Task completes**

```bash
# 1. Delegate exploration (try the agy CLI first)
agy -p "Find all authentication patterns..." --dangerously-skip-permissions 2>&1
# fallback → use @patch-coder
```

```bash
# 2. The subagent creates a spike with findings
# Read findings via: wipnote spike list (then spike show <id>)

# 3. Use findings in next delegation
# In Codex native sessions: spawn `wipnote-feature-coder`
# Otherwise: try codex CLI first, then fallback → use @feature-coder
```

</details>

<details>
<summary><strong>Debugging Delegation Order (Third-Party Libraries)</strong></summary>

## Debugging Delegation Order

When debugging third-party library issues, enforce this order:

1. **Reproduce the failure** — run Bash commands to confirm the error message
2. **Delegate doc search to researcher** — WebSearch for official docs (FREE via agy or researcher agent)
3. **Delegate GitHub issues search to researcher** — check for known issues or recent changes
4. **Only THEN delegate source code reading** — last resort if docs and issues didn't resolve it

Do NOT delegate source code reading as the first debugging step.

**Pattern:**
```bash
# Step 1: Reproduce (direct Bash)
Bash("run command that triggers the error")

# Step 2 & 3: Delegate research (try the agy CLI first — FREE)
agy -p "Search official docs and GitHub issues for: <library> <error message>" \
  --dangerously-skip-permissions 2>&1
# fallback → researcher agent with WebSearch
```

</details>

<details>
<summary><strong>Error Handling & Retries</strong></summary>

**Let subagents handle retries:**

```python
# WRONG - Don't retry directly as orchestrator
bash_result = Bash(command="git commit -m 'feat: new'")
if failed:
    # Retry directly (context pollution)
    Bash(command="git pull && git commit")  # More context used

# CORRECT - Subagent handles retries
Use Gemini agent invocation with:
    workflow="copilot",
    description="Commit changes with retry",
    message="""
    Commit changes:
    Message: "feat: new feature"

    If commit fails:
    1. Pull latest changes
    2. Resolve conflicts if any
    3. Retry commit
    4. Handle pre-commit hooks

    Report final status: success or failure
    """
```

**Benefits:**
- Subagent context handles retries (not your context)
- Cleaner error reporting
- Automatic recovery attempts
- You get clean success/failure

</details>

---

## Subagent Budget-Pause Handling

<details>
<summary><strong>Pattern A: Auto-Resume Budget-Paused Subagents</strong></summary>

In some harness environments (notably VS Code devcontainer / agent-teams runtime), a delegated subagent may pause at a low tool budget and return an INTERMEDIATE, non-final message with NO completion. This is harness/runtime behavior, not a task failure. The message may be mid-sentence, lack a final report, or trail with "let me now…" — clear signs the work is not actually finished.

**Detection & Recovery:**
1. **Detect non-final return:** Message ends mid-step, no completion summary, no final SHA or deliverable list, trailing incomplete sentence
2. **DO NOT treat as done:** This is NOT a task failure; it's a pause condition
3. **DO NOT re-dispatch a fresh agent:** Re-dispatch loses all prior context and forces the agent to restart from scratch
4. **MUST resume the SAME agent via your harness's agent-resume mechanism** with a restated, explicit finish-line. Do NOT re-dispatch (that loses context). The exact primitive depends on your harness:
   
   **Claude Code:** Use `SendMessage` to send a continuation message to the paused agent by its `agentId` (available in the original task result):
   ```
   SendMessage({ to: <agentId> }, "Continue and finish the work. You paused mid-task. Complete the remaining steps: <restate exact deliverables>. Report final status with commit SHA or summary.")
   ```
   
   **Codex CLI:** Use Codex's subagent continuation mechanism (check your Codex version's documentation for re-engaging the same spawned agent instance to continue work without re-dispatch).
   
   **Gemini CLI:** Use Gemini's agent-messaging API to continue the paused agent (refer to your Gemini CLI docs for message-passing or agent-resume mechanisms).

5. **Expect multiple resume cycles:** May require 2-3 additional messages/resumes before a genuine final report is returned

**Why this matters:**
- Harness tool budgets are per-session — temporary, not permanent
- Resuming the same agent keeps context and avoids restarting
- Multiple resumes are normal and expected in this condition

**Pattern (harness-agnostic pseudocode):**
```
use the ... workflow described here
  → returns intermediate result, no completion
  
→ Resume(same_agent, "Continue and finish: <deliverables>")
  → returns partial progress
  
→ Resume(same_agent, "Still not done. Complete: <deliverables>. Report final SHA/summary.")
  → finally returns complete result
```

</details>

<details>
<summary><strong>Pattern B: Completion Gate for Subagent-Delegated Code</strong></summary>

When code for a work item was written by a delegated subagent, completing the item (`wipnote <type> complete <id>`) is DOUBLE-GATED. The orchestrator MUST perform both:

**(a) Run its OWN session-scoped quality gate:**
```bash
wipnote check --gate
```
A subagent's gate record does NOT count toward work-item completion — gate records are session-bound. You (the orchestrator in the main session) must run the gate yourself.

**(b) Pass explicit completion rationale with committed SHAs:**
```bash
wipnote feature complete <feat-id> --accepted-advisory "Subagent implementation verified. Commits: <SHA1>, <SHA2> (cite real SHAs from git log, no host paths)."
```
Subagent commits are not auto-linked to the work item in the current schema. You must cite the real commit SHAs in the completion advisory so reviewers can trace implementation back to the work.

**Why double-gating is necessary:**

The underlying defect is tracked in **bug-3718b630**: the harness/hook system currently lacks:
- Auto-linking of subagent commits to the work item they implement
- Orchestrator-visible gate records (subagent gates are session-local, not visible to orchestrator)

Both are durable fixes that belong in the binary and hooks, not in per-user guidance. Until those fixes land, completion is gated twice: (a) verifies code quality in orchestrator context, (b) documents the subagent's commits for future traceability.

**Example:**
```bash
# Subagent finishes: feat-abc
wipnote feature show feat-abc  # check commit history
git log --oneline --grep="feat-abc" | head -3
# Output: a1b2c3d feat: implementation detail
#         x9y8z7w docs: added guide

# Orchestrator runs quality gate
wipnote check --gate
# ✓ build, vet, tests pass

# Orchestrator completes with rationale
wipnote feature complete feat-abc --accepted-advisory \
  "Subagent implementation validated. Commits: a1b2c3d, x9y8z7w. Quality gate passed."
```

</details>

---

## Known Issues / Environment

**Devcontainer subagent budget-pause behavior:** In the VS Code devcontainer runtime, delegated subagents may pause at low tool budgets and return intermediate (non-final) results. This is harness/runtime behavior, not a code error. **See Pattern A (Auto-Resume) above** for detection and recovery steps.

**Subagent commit linkage gap (bug-3718b630):** Subagent commits are not auto-linked to the work item they implement, and subagent quality-gate records are session-local and invisible to the orchestrator. This blocks full automation of completion gates. **See Pattern B (Completion Gate) above** for the interim workaround (double-gating + explicit SHAs in advisory). The durable fix belongs in the binary and hooks.

**Codex exec sandbox failures in devcontainers (bwrap/bubblewrap):** In VS Code devcontainers and GitHub Codespaces, `codex exec` may fail immediately because the container lacks bubblewrap (bwrap) privileges — the nested session cannot run even `pwd`. Failure signatures: "bwrap", "bubblewrap", "Operation not permitted", "cannot create namespace". On ANY of these in `codex exec` output, treat the environment as permanently incompatible with nested Codex execution: skip `codex exec` for the rest of the session and delegate directly to in-harness agents (e.g. `feature-coder`). Do not retry.

**Full Go suite is silent ~6 min by design:** `go test ./...` buffers per-package output; `cmd/wipnote` (~320s from cold) prints first, so the run produces no output until it completes. Do not treat silence as a stall and do not kill the run — budget ≥10 min. Need progress? `go test -json ./...` or split: `go test ./internal/... && go test ./cmd/...`. (Cached runs finish in seconds.)

---

## Advanced: Post-Compact Persistence

<details>
<summary><strong>Orchestrator Activation After Compact</strong></summary>

**How it works:**

1. Before compact, SDK sets environment variable: `CLAUDE_ORCHESTRATOR_ACTIVE=true`
2. SessionStart hook detects post-compact state
3. Orchestrator Directives Skill auto-activates
4. This skill section appears automatically (first time post-compact)

**Why:** Preserve orchestration discipline after context compact

**What you see:**
- Skill automatically activates (no manual invocation needed)
- Quick start section visible by default
- Expand detailed sections as needed
- Full guidance available without re-reading docs

**To manually trigger:**
```
/orchestrator-directives
```

**Environment variable:**
```bash
CLAUDE_ORCHESTRATOR_ACTIVE=true  # Set by SDK
```

</details>

<details>
<summary><strong>Session Continuity Across Compacts</strong></summary>

**Features preserved across compact:**
- Work items in wipnote
- Feature/spike tracking
- Delegation patterns
- Model selection guidance
- This skill's guidance

**What's lost:**
- Your context (that's why compact happens)
- Intermediate tool outputs
- Local variables

**Re-activation pattern:**

```
Before compact:
- Work on features, track in wipnote
- Delegate with clear prompts
- Use SDK to save progress

After compact:
- Orchestrator Skill auto-activates
- Re-read recent spikes for context
- Continue delegations
- Use Task IDs for parallel coordination
```

</details>

---

## Core Development Principles (Enforce in ALL Delegations)

When delegating to ANY coder agent, include these requirements in the prompt:

### Research First
- **Web research is a default phase, not a debugging fallback.** Route web research into planning and pre-implementation by default: latest docs, OSS alternatives, and provider plugin/hook ecosystems should be checked before design decisions are locked, not only when debugging.
- Search for existing OSS packages/libraries/tools before approving custom implementations — if a maintained package covers the requirement, adopt it. Record the outcome (adopt or build-custom with rationale) in the work item.
- When work touches agent harnesses (Claude Code, Codex CLI, Antigravity), check Anthropic/OpenAI/Google CLI docs for existing plugins, skills, subagents, or hooks that may already cover the requirement before building new ones.
- Check `go.mod` before adding new dependencies
- Prefer well-maintained packages over custom implementations

### Capability Delivery & Context Economy
When delivering a wipnote capability, choose the cheapest context tier that works:
**CLI via Bash (≈zero resident cost) > Skill (progressive disclosure) > deferred MCP tool (names-only until used) > eager MCP tool (full schema always resident — avoid).**
Never expose wipnote's own command surface as eager MCP tools. MCP is for external, user-chosen services only, with deferred/tool-search loading. CLI is the most cross-harness-portable tier.

### Plugin / Project Boundary
wipnote must **never author, generate, or overwrite a project's own instruction files (AGENTS.md, CLAUDE.md, GEMINI.md)**. Those are user-owned and describe the host project. wipnote agents READ them; at most they OFFER an opt-in, user-reviewed snippet — never silent ownership. Cross-harness portability of wipnote *behavior* comes from the single-source manifest → generated per-harness trees, NOT from AGENTS.md.

### Code Design
- **DRY** — Extract shared logic; check `internal/` for existing utilities before writing new ones
- **Single Responsibility** — One clear purpose per module, function, and struct
- **KISS** — Simplest solution that satisfies current requirements
- **YAGNI** — Only implement what is needed now, not speculative future needs

### Module Size Limits (from code-hygiene rules)
- Functions: <50 lines | Structs: <300 lines | Files: <500 lines
- If a file would exceed limits, split it as part of the work — do not defer refactoring

### Before Committing
```bash
go build ./... && go vet ./... && go test ./...
```
Never commit with unresolved build errors, vet warnings, or test failures.

### Environment Hazards (auto-injected via arch memory)

Operational hazards for this repo (TMPDIR setup for go test, banned git stash, TestExtractArchive
flake, gpg/401 signing in sandboxed Bash) are stored as `kind=hazard` arch cards and injected
automatically by `wipnote arch resolve` at dispatch time. Do NOT repeat them verbatim in dispatch
prompts — the arch injection delivers them with fresher, deduplicated text. Include the arch resolve
output under `## Architectural context` in every subagent prompt instead.

---

## Core Philosophy

<details>
<summary><strong>Core Principles Summary</strong></summary>

**Principle 1: Delegation > Direct Execution**
- Cascading failures consume exponentially more context than structured delegation
- One failed bash call becomes 3-5 calls with retries
- Delegation isolates failures to subagent context

**Principle 2: Cost-First > Capability-First**
- Use FREE/cheap AIs (Antigravity/agy, Codex, Copilot) before expensive Claude Code
- agy: FREE (exploration)
- Codex: 70% cheaper (code)
- Copilot: 60% cheaper (git)
- Claude: Expensive (strategic only)

**Principle 3: You Don't Know the Outcome**
- What looks like "one tool call" often becomes many
- Unexpected failures, conflicts, retries consume context
- Delegation removes unpredictability from orchestrator context

**Principle 4: Parallel > Sequential**
- Multiple subagents can work simultaneously
- Much faster than sequential execution
- Orchestrator stays available for decisions

**Principle 5: Track Everything**
- Use wipnote CLI to track delegations
- Features, spikes, bugs created for all work
- Clear record of who did what

</details>

---

## Core Philosophy

**Delegation > Direct Execution.** Cascading failures consume exponentially more context than structured delegation.

**Cost-First > Capability-First.** Use FREE/cheap AIs before expensive Claude models.

---

## Quick Reference Table

<details>
<summary><strong>Operation Type → Correct Delegation</strong></summary>

| Operation | MUST Use | Cost | Fallback |
|-----------|----------|------|----------|
| Search files | `Bash("agy ...")` | FREE | patch-coder |
| Pattern analysis | `Bash("agy ...")` | FREE | patch-coder |
| Documentation research | `Bash("agy ...")` | FREE | patch-coder |
| Code generation | `Bash("codex ...")` | $ (70% off) | feature-coder |
| Bug fixes | `Bash("codex ...")` | $ (70% off) | patch-coder |
| Write tests | `Bash("codex ...")` | $ (70% off) | patch-coder |
| Git commits | `Bash("copilot ...")` | $ (60% off) | patch-coder |
| Create PRs | `Bash("copilot ...")` | $ (60% off) | patch-coder |
| Architecture | Claude Opus | $$$$ | Sonnet |
| Strategic decisions | Claude Opus | $$$$ | use the appropriate Gemini agent invocation |

**Key:** FREE = No cost | $ = Cheap | $$$$ = Expensive (but necessary)

</details>

---

---

## Prefer the structured wrappers

When delegating, remind the subagent (in the prompt itself, not just by relying on agent definitions):
- Use `wipnote search '<pattern>'` for structural code search, not `grep`.
- Use `wipnote sh "<command>"` for any command likely to produce verbose output.

These reduce per-turn output volume and let tighter `maxTurns` caps actually hold.

---

## Multi-agent Git Isolation

When multiple agents or CLIs work on the same repository concurrently, follow this operating model to avoid Git index contention and interleaved commits:

**Source edits** must happen in **per-agent worktrees or isolated clones**, never in the shared main checkout. Create an isolated worktree for each agent:
```bash
wipnote yolo --feature <feat-id>   # creates a managed linked worktree
# or manually: git worktree add .claude/worktrees/<id> -b <branch>
```

**Metadata commits** (wipnote HTML artifacts, session data) are automatically serialized via the repo-scoped advisory lock (`runGitMutation` in feat-3f66d83f). This lock is safe from any worktree because it lives in the per-user cache directory, not in `.git/` or `.wipnote/`.

**Git lock file cleanup** is opt-in only — never automatic. Use `wipnote launcher git-lock --fix` (requires an age threshold and a no-live-writer check) to clean stale lock files. Do NOT remove `.git/index.lock` manually unless you have confirmed no process is writing.

**Diagnosis:** Run `wipnote launcher doctor` to check whether you're in the primary worktree (warns if so) or a properly isolated linked worktree.

---

## Pre-Work Validation (YOLO Mode Hook)

The PreToolUse hook enforces attribution before code changes. Behavior by scenario:

| Active Work Item | Tool | Action |
|-----------------|------|--------|
| Feature | Read | Allow |
| Feature | Write/Edit/Delete | Allow |
| Spike | Read | Allow |
| Spike | Write/Edit/Delete | Warn + Allow |
| None | Read | Allow |
| None | Write/Edit (1 file) | Warn + Allow |
| None | Write/Edit (3+ files) | **Deny** |

**When denied:** Create a work item first, then retry.

```bash
wipnote feature create "Title" --track <trk-id>   # creates + returns feat-id
wipnote feature start <feat-id>                   # sets attribution for this session
```

**Decision rule for code changes:**
- Single file, <30 min → direct change (warns, allows)
- 3+ files, or new tests, or multi-component → create feature first

---

## Architectural Memory

wipnote maintains a queryable store of architectural facts in `.wipnote/arch/` (cards with
kinds: `hazard`, `invariant`, `subsystem-map`, `decision`). The facts are relevance-filtered
into subagent prompts under a hard word budget. Using this store saves each coder agent
the 15-25 min research tax of re-deriving the same facts from code.

### Dispatch-Time Ritual (MANDATORY for every subagent dispatch)

Before composing a subagent prompt, run:

```bash
wipnote arch resolve --for <work-item-id>
# or for path-based queries:
wipnote arch resolve --for "cmd/wipnote/arch_cmds.go,internal/arch/"
```

Paste the output verbatim into the subagent's prompt under a heading like
`## Architectural context`. The output is already budget-capped (~450 words) and
annotated with UNVERIFIED drift markers. The subagent must treat UNVERIFIED cards
as advisory only and verify assumptions in code.

If no cards match, the command prints "No arch cards matched." — skip the heading entirely.

### Post-Completion Distillation (AFTER every work item completes)

When a subagent returns or you complete a work item yourself, distill durable learnings
into arch cards using one of two paths:

**Path A — Completion-time (recommended, single step):**
```bash
wipnote feature complete <feat-id> --learning "Body text: max 120 words." \
  --learning-kind invariant   # or: hazard, decision, subsystem-map
```
The `--learning` flag validates the body BEFORE marking done. A failed validation
aborts the completion with a clear error — the learning is never silently lost.

**Path B — Manual add (for learnings discovered outside completion):**
```bash
wipnote arch add <slug> --kind invariant --body "Body text." \
  --paths "cmd/wipnote/**" --links <work-item-id> --created-by "agent"
```

### Post-Completion Nudge

After a successful completion, wipnote prints drift-suspect arch cards whose globs
overlap the item's touched paths. Act on the nudge:

```bash
wipnote arch verify <slug>      # re-pins verified_at to HEAD; card is trustworthy again
wipnote arch edit <slug> --body "Updated body."   # update stale content then verify
```

### Trust Model

- Active cards (no drift marker): authoritative — include in prompt without caveat.
- UNVERIFIED cards (drift marker or empty verified_at): advisory — include but tell the
  subagent to verify assumptions in code.
- Retired/superseded cards: excluded from resolve output by default.

---

## Related Skills

- **[/multi-ai-orchestration](/multi-ai-orchestration)** - Comprehensive model selection guide with detailed decision matrix
- **[/code-quality](/code-quality)** - Quality gates and pre-commit workflows
- **[/strategic-planning](/strategic-planning)** - wipnote analytics for smart prioritization

## Reference Documentation

- **Complete Rules:** See [orchestration.md](../../rules/orchestration.md)
- **Advanced Patterns:** See [reference.md](./reference.md)
- **wipnote CLI:** `wipnote --help`

---

## Quick Summary

**Cost-First Orchestration:**
1. `Bash("agy ...")` (FREE) → exploration, research, analysis → fallback: patch-coder
2. `Bash("codex ...")` (70% off) → code implementation, fixes, tests → fallback: feature-coder
3. `Bash("copilot ...")` (60% off) → git operations, PRs → fallback: patch-coder
4. Claude Opus → deep reasoning, strategy only

**Orchestrator Rule:**
Only execute: use the appropriate Gemini agent invocation, AskUserQuestion(), TodoWrite(), SDK operations

**Everything else → Delegate to appropriate spawner**

**When in doubt → DELEGATE**

---

## Agent Teams vs Subagents

Claude Code v2.1.32+ ships an experimental **agent teams** feature where independent Claude instances self-claim work from a shared task list and message each other directly. This section helps you decide when to use teams vs traditional subagent delegation.

### Decision Criteria

| Dimension | Agent Teams | Subagents |
|-----------|-------------|-----------|
| **Ownership** | Parallel — each teammate claims tasks independently | Sequential — orchestrator dispatches one-at-a-time |
| **Communication** | Teammates message each other directly | Subagents report back to orchestrator only |
| **Best for** | Competing-hypothesis debugging, multi-lens review, feature ownership splitting | Sequential task chains, research→implement, isolated single-task work |
| **wipnote tracking** | Automatic — TeammateIdle/TaskCreated/TaskCompleted hooks fire per teammate | Manual — orchestrator attributes via `wipnote feature start/complete` |
| **Context isolation** | Each teammate has its own context window | Subagents inherit orchestrator's context model |
| **Cost model** | N teammates × full session cost | Orchestrator + N smaller subagent calls |

### Opt-In Requirements

Agent teams require explicit opt-in:

1. **Environment variable:** `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`
2. **Minimum version:** Claude Code **2.1.32** or later
3. The wipnote plugin works with or without teams enabled — hooks gracefully no-op when no team is active

### How to Spawn a Team

There is no SDK API for teams. Spawn via natural language:

```
Create an agent team to <describe the work and how to divide it>
```

Claude Code will create teammates, assign them work from a shared task list, and let them coordinate directly.

### Caveats

- **`skills:` and `mcpServers:` frontmatter are NOT applied to teammates** — do not rely on skill injection or MCP servers in agent definitions used as teammates. Teammates run with base capabilities only.
- **No session resume** — teammates exit via the `exit-code-2` block-and-return contract; Claude Code's `/resume` is not currently wired through this path. If a teammate is blocked (e.g., by a quality gate), the teammate is stranded. Always provide manual recovery instructions in stderr.
- **One team per session** — you cannot spawn multiple teams in a single Claude Code session.
- **No nested teams** — a teammate cannot create its own team.
- **`/wipnote:execute` is unchanged** — the parallel dispatch skill continues to use subagents with worktree isolation. This plan does not convert it to use teams.

### Example Prompts

**1. Multi-lens PR review:**
```
Create an agent team: one teammate reviews for correctness,
one for performance, one for security. Each writes findings
to a shared review.md under their section heading.
```

**2. Competing-hypothesis debugging:**
```
Create an agent team to debug the flaky test in internal/hooks/.
One teammate investigates timing issues, one investigates state
pollution, one investigates resource contention. First to find
root cause messages the others.
```

**3. Feature ownership splitting:**
```
Create an agent team for track trk-XXXX. Each teammate claims
one unblocked feature and works it to completion. Use
wipnote feature start/complete for attribution.
```

### What wipnote Captures

When agent teams are active, wipnote automatically records:

- **Teammate identity** — every TeammateIdle, TaskCreated, and TaskCompleted event includes `teammate_name`
- **Step attribution** — feature steps are prefixed with `[teammate-name]` so `wipnote snapshot` shows who did what
- **Optional quality gate** — TaskCompleted can run build/test gates before allowing task completion. Opt-in via `.wipnote/config.json`:

```json
{
  "block_task_completion_on_quality_failure": true
}
```

> **WARNING:** Enabling the quality gate can strand teammates. Blocked teammates cannot be `/resume`d. When blocking occurs, stderr includes a manual recovery command: `wipnote feature complete <feature-id>`.
