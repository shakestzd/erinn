# Orchestrator Architecture: Flexible Multi-Agent Coordination

**"Coordination, Not Control" - Flexible Model Selection Based on Task Needs**

Use this document to understand HtmlGraph's orchestrator pattern. MUST coordinate multiple AI agents in parallel. MUST preserve context efficiency while maximizing model flexibility.

For coordination across multiple terminal sessions, worktrees, and parallel subagents, see [Multi-Session Coordination Spec](./multi-session-coordination-spec.md).

---

## Core Principle: Flexibility Over Rigidity

Use the orchestrator pattern as **flexible coordination**, NOT a rigid hierarchy with fixed rules:

- ✅ **Flexible model selection** - Choose any model for any work based on task fit and cost
- ✅ **Dynamic spawner composition** - Mix and match spawner types within the same workflow
- ✅ **Cost optimization** - MUST use cheaper models for exploratory work, pay for expensive reasoning only when needed
- ✅ **Capability-first thinking** - Identify needed capability, then select best model/CLI

**Anti-Pattern: Rigid Rules**
```python
# ❌ NEVER enforce: "Gemini must do exploration"
# ❌ NEVER enforce: "Claude must do reasoning"
# ❌ NEVER enforce: "Copilot must do git operations"
```

**Pattern: Capability-Based Selection**
```python
# ✅ ALWAYS use capability matching:
# ✅ "This task needs fast, cheap exploration → MUST use Gemini spawner"
# ✅ "This task needs deep reasoning → Use Claude Opus"
# ✅ "This task needs GitHub integration → MUST use Copilot spawner"
```

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                        Orchestrator (Haiku)                     │
│              Cheap, Strategic, Context-Preserving               │
│                                                                 │
│  Role: Coordinate parallel work, make high-level decisions    │
│  Context: Stays clean (delegates heavy lifting)               │
│  Cost: Minimal (mostly Task() calls)                           │
└──────────────────────┬──────────────────────────────────────────┘
                       │
        ┌──────────────┼──────────────┬─────────────────┐
        │              │              │                 │
        ▼              ▼              ▼                 ▼
   ┌─────────┐   ┌─────────┐   ┌─────────┐   ┌──────────────┐
   │ Gemini  │   │ Copilot │   │ Codex   │   │ Claude (any) │
   │ Spawner │   │ Spawner │   │ Spawner │   │   Spawner    │
   └────┬────┘   └────┬────┘   └────┬────┘   └──────┬───────┘
        │             │             │                │
        │ FREE        │ GitHub      │ Codex          │ Any task
        │ exploratory │ integrated  │ integrated     │ (reasoning,
        │ research    │ workflows   │ workflows      │  analysis)
        │ batch ops   │ git ops     │ coding         │
        │             │ GitHub API  │ completions    │
        ▼             ▼             ▼                ▼
     ┌────────────────────────────────────────────────┐
     │         Parallel Subagent Execution            │
     │    (Each runs independently with full tool     │
     │     access within their spawner's scope)       │
     └────────────────────────────────────────────────┘
```

---

## Using Spawner Agents via Task()

Invoke spawner agents directly through Task() using simple names:

**Syntax:**
```python
# Invoke Gemini spawner (FREE - 2M tokens/min)
Task(subagent_type="gemini", prompt="Search codebase for auth patterns")

# Invoke Codex spawner (Paid - OpenAI)
Task(subagent_type="codex", prompt="Implement user authentication")

# Invoke Copilot spawner (Subscription - GitHub)
Task(subagent_type="copilot", prompt="Review and approve PR")
```

**Simple Names (No Namespace):**
- `"gemini"` - Google Gemini 2.0-Flash (FREE tier, 2M tokens/min, best for exploration)
- `"codex"` - OpenAI Codex/GPT-4 (code generation, 128K context)
- `"copilot"` - GitHub Copilot (GitHub integration, git operations)

**Transparent Failures - Orchestrator Decides:**

If a spawner CLI is not installed, Task() fails explicitly:

```python
# This fails with clear error (CLI not installed)
Task(subagent_type="gemini", prompt="Analyze codebase")
# Error: Spawner 'gemini' requires 'gemini' CLI.
# Install: https://ai.google.dev/gemini-api/docs/cli
# Orchestrator can now decide: install, use different spawner, or fallback to Claude

# Orchestrator handles fallback explicitly
try:
    result = Task(subagent_type="gemini", prompt="Analyze code")
except CLINotFound:
    # Try alternative spawner
    result = Task(subagent_type="codex", prompt="Analyze code")

# OR
if gemini_cli_available():
    Task(subagent_type="gemini", ...)
else:
    Task(subagent_type="haiku", ...)  # Fallback to Claude
```

**Attribution & Cost:**

Every spawner execution shows clear attribution:

```json
{
  "response": "Analysis complete: 15 auth patterns found",
  "agent": "gemini-2.0-flash",      // Actual AI (not wrapper)
  "tokens": 245000,
  "cost": 0.0,                        // FREE for Gemini
  "duration": 8.3
}
```

The dashboard distinguishes spawner activities from direct Claude actions:

```
claude-code → gemini-2.0-flash   (Gemini spawner - FREE)
claude-code → gpt-4              (Codex spawner - Paid)
claude-code → github-copilot     (Copilot spawner - Subscription)
```

**Error Handling Pattern:**

```python
# Explicit failure - orchestrator makes decision
Task(subagent_type="gemini", prompt="...", description="Search with Gemini")
# If fails: Shows CLI requirement + options
# Orchestrator receives error and chooses:
# 1. Install the CLI and retry
# 2. Use different spawner
# 3. Fallback to Claude (haiku, sonnet, opus)
# 4. Cancel operation
```

**Model Flexibility:**

While each spawner has a preferred model, all can use alternative Claude models:

```python
# Spawner agents default to their preferred model but can call different models
Task(subagent_type="codex", prompt="...", model="gpt-4-turbo")  # Override model
Task(subagent_type="copilot", prompt="...", allow_tools=["shell", "git"])
```

---

## The Four Spawner Types

### 1. Gemini Spawner

**Best for:** Exploratory research, batch analysis, multimodal tasks, cost-sensitive workflows

```python
from htmlgraph.orchestration import HeadlessSpawner

spawner = HeadlessSpawner()
result = spawner.spawn_gemini(
    prompt="Analyze codebase and find all authentication patterns",
    model="gemini-2.0-flash",  # FREE tier with 2M tokens/min
    include_directories=["src/auth", "src/security"],
    output_format="json"
)

if result.success:
    patterns = json.loads(result.response)
    print(f"Found {len(patterns)} patterns (Cost: FREE)")
else:
    # Automatic fallback to Haiku
    Task(prompt="Find auth patterns", subagent_type="general-purpose")
```

**Characteristics:**
- Model: Google Gemini 2.0-Flash
- Cost: FREE tier (2M tokens/minute)
- Best use: Large-scale exploration, batch file analysis
- Fallback: Automatic to Haiku if CLI fails
- Tools: Read, Bash, Grep, Glob, WebSearch, WebFetch

**When to choose:**
- Need to analyze large codebases (exploratory)
- Budget-conscious (FREE tier)
- Batch processing many files
- Don't require Claude's deep reasoning
- Want fast iteration (2M tokens/min throughput)

---

### 2. Copilot Spawner

**Best for:** GitHub-integrated workflows, git operations, repository management

```python
from htmlgraph.orchestration import HeadlessSpawner

spawner = HeadlessSpawner()

# GitHub operations only
result = spawner.spawn_copilot(
    prompt="Create GitHub issue and link to PR #123",
    allow_tools=["github(*)"]  # Fine-grained tool control
)

# Git operations only
result = spawner.spawn_copilot(
    prompt="Create feature branch and commit changes",
    allow_tools=["shell(git)"]
)

# Mixed permissions
result = spawner.spawn_copilot(
    prompt="GitHub workflow with restrictions",
    allow_tools=["github(*)", "shell(git)"],
    deny_tools=["shell(rm)", "write(/etc/*)"]
)

if result.success:
    print(f"GitHub operation complete: {result.response}")
else:
    # Automatic fallback to Sonnet
    Task(prompt="Use gh CLI for GitHub operations", subagent_type="sonnet")
```

**Characteristics:**
- Model: GitHub Copilot CLI
- Cost: Depends on Copilot subscription
- Best use: GitHub-native operations, git workflows
- Fallback: Automatic to Claude Sonnet if CLI fails
- Tools: Read, Bash, Grep, Glob (with fine-grained permissions)

**Tool Permission Controls:**
```python
# Allow specific tools only
allow_tools=["github(*)", "shell(git)"]

# Block dangerous operations
deny_tools=["shell(rm)", "write(/etc/*)", "shell(sudo)"]

# Hybrid approach
allow_tools=["github(*)", "shell(git)"]
deny_tools=["shell(rm)"]
```

**When to choose:**
- GitHub-centric workflows (issues, PRs, actions)
- Need GitHub API access
- Want tool-restricted execution for safety
- Git operations integrated with GitHub
- Testing Copilot's code generation capabilities

---

### 3. Codex Spawner

**Best for:** Code generation, Codex platform integration, coding completions

```python
from htmlgraph.orchestration import HeadlessSpawner

spawner = HeadlessSpawner()
result = spawner.spawn_codex(
    prompt="Generate unit tests for src/auth/login.py",
    include_directories=["src/auth"],
    model="codex"  # Or specific model available on Codex
)

if result.success:
    tests = result.response
    print(f"Generated tests:\n{tests}")
else:
    # Automatic fallback to Claude
    Task(prompt="Generate unit tests for auth module")
```

**Characteristics:**
- Model: Codex platform default
- Cost: Platform-dependent
- Best use: Code generation, coding tasks
- Fallback: Automatic to Claude if CLI fails
- Tools: Read, Bash, Grep, Glob

**When to choose:**
- Code generation and completions needed
- Codex platform integration required
- Prefer Codex's code-specific capabilities
- Testing different models for code tasks
- Codex-specific features (e.g., execution environment)

---

### 4. Claude Spawner (Flexible)

**Best for:** Reasoning, analysis, strategic planning, any complex task

```python
from htmlgraph.orchestration import HeadlessSpawner

spawner = HeadlessSpawner()

# Use any Claude model via Task()
result = spawner.spawn_claude(
    prompt="Analyze architecture and recommend refactoring",
    model="claude-opus-4-5",  # Or haiku, sonnet, etc.
    reasoning="extensive"  # For complex reasoning tasks
)

# Or simpler: just use Task() directly
Task(
    subagent_type="general-purpose",
    prompt="Analyze architecture and recommend refactoring"
)
```

**Characteristics:**
- Model: Any Claude model (Haiku, Sonnet, Opus, etc.)
- Cost: Model-dependent
- Best use: Reasoning, analysis, strategic decisions
- Flexibility: Can use alternative models within spawner
- Tools: Full access (Read, Bash, Grep, Glob, Edit, Write)

**Model selection within Claude spawner:**
```python
# Use Haiku for cheap, fast tasks
Task(subagent_type="general-purpose",
     prompt="Run tests and report failures")

# Use Sonnet for balanced tasks
result = spawner.spawn_claude(
    model="claude-3-5-sonnet-20241022",
    prompt="Code refactoring task"
)

# Use Opus for deep reasoning
result = spawner.spawn_claude(
    model="claude-opus-4-5",
    prompt="Complex architectural analysis"
)
```

**When to choose:**
- Reasoning and analysis required
- Strategic planning and decision-making
- Complex multi-step problems
- When other spawners don't fit
- Need Claude's specific capabilities

---

## Decision Framework: Which Spawner to Use?

```
Task starts here
      │
      ▼
Is it exploratory research?
├─ YES → Use subagent_type="gemini" (FREE, fast, 2M tokens/min)
└─ NO → Continue...
        │
        ▼
Does it need GitHub integration?
├─ YES → Use subagent_type="copilot" (GitHub API, git ops)
└─ NO → Continue...
        │
        ▼
Is it a code generation task?
├─ YES → Use subagent_type="codex" (code completions)
└─ NO → Continue...
        │
        ▼
Does it need reasoning/analysis?
├─ YES → Use subagent_type="sonnet" or "opus" (Claude models)
└─ NO → Default to subagent_type="haiku"
```

**Remember:** These are guidelines, not rigid rules. MUST mix and match based on actual task needs:

```python
# ✅ Flexible approach - use best tool for each subtask
Task(subagent_type="gemini",
     prompt="Explore codebase and list all API endpoints")

Task(subagent_type="copilot",
     prompt="Create GitHub issue for findings")

Task(subagent_type="sonnet",
     prompt="Analyze endpoints for security issues")
```

---

## Pattern Examples

### Pattern 1: Parallel Multi-Tool Exploration

Launch multiple spawners to explore different aspects simultaneously:

```python
from htmlgraph import SDK

sdk = SDK(agent="orchestrator")

# Parallel exploration - all run at same time
gemini_task = Task(
    subagent_type="gemini",
    prompt="Find all authentication patterns in src/auth/. Return JSON with pattern names, file locations, and brief descriptions."
)

codex_task = Task(
    subagent_type="codex",
    prompt="Generate unit tests for src/auth/login.py based on common auth patterns."
)

claude_task = Task(
    subagent_type="sonnet",
    prompt="Analyze auth patterns for security vulnerabilities. Focus on: token handling, session management, input validation."
)

# Orchestrator waits for all to complete
# Each runs in parallel - total time = slowest task
print("Exploration complete!")
```

**Benefits:**
- MUST run all work in parallel (NEVER sequential)
- MUST use best tool for each agent's task
- MUST keep orchestrator focused (only coordinates)
- Total time = slowest subagent (NEVER sum of all)

---

### Pattern 2: Cost-Optimized Workflow

Use cheaper models for heavy lifting, expensive models only for reasoning:

```python
# Step 1: Cheap exploration (Gemini FREE)
gemini_result = Task(
    subagent_type="gemini",
    prompt="Analyze 500+ Python files for security issues. Return structured list of potential issues found."
)

# Step 2: Expensive analysis (Claude Opus - only on findings)
analysis_result = Task(
    subagent_type="opus",  # Uses expensive model
    prompt=f"""Given these potential security issues found by exploration:

{gemini_result.findings}

Perform deep security analysis:
1. Verify each issue is real (not false positive)
2. Estimate severity (critical/high/medium/low)
3. Recommend fixes
4. Create remediation plan

Output: Prioritized security report with fixes"""
)

print(f"Cheap exploration: FREE (Gemini)")
print(f"Deep analysis: 1,000 tokens (Opus)")
print(f"Total cost: Minimal")
```

**Cost Breakdown:**
- Exploration: FREE (Gemini spawner)
- Analysis: Only on real findings (Opus)
- Orchestrator: Minimal (just coordination)

---

### Pattern 3: Model Fallback Chain

Use preferred model with automatic fallback:

```python
# Primary: Gemini (FREE)
# Fallback: Haiku (if Gemini CLI fails)
result = spawner.spawn_gemini(
    prompt="Explore codebase",
    model="gemini-2.0-flash"
)

if not result.success:
    print("Gemini failed, falling back to Haiku...")
    # Automatic fallback in spawner
    # OR explicit fallback
    Task(prompt="Explore codebase", subagent_type="general-purpose")
```

**Fallback chains:**
- Gemini spawner → Haiku if CLI fails
- Copilot spawner → Sonnet if CLI fails
- Codex spawner → Claude if CLI fails
- Claude spawner → Any other Claude model if needed

---

### Pattern 4: Mixed Spawner Workflow

Combine different spawners for optimal cost and capability:

```python
# Part 1: Cheap, parallel exploration (multiple spawners)
exploration_results = {
    "auth": Task(
        subagent_type="gemini",
        prompt="Explore src/auth/ security"
    ),
    "api": Task(
        subagent_type="gemini",
        prompt="Explore src/api/ endpoints"
    ),
    "github": Task(
        subagent_type="copilot",
        prompt="Check GitHub issues and PRs",
        allow_tools=["github(*)"]
    )
}

# Part 2: Consolidation (expensive reasoning, once only)
consolidation = Task(
    subagent_type="sonnet",
    prompt=f"""Based on exploration findings:

Auth findings: {exploration_results['auth'].response}
API findings: {exploration_results['api'].response}
GitHub status: {exploration_results['github'].response}

Create a unified plan for next steps."""
)

# Cost profile:
# - Exploration: FREE (Gemini) + GitHub API (Copilot)
# - Consolidation: One expensive Claude call
# - Total: Optimized cost with thorough analysis
```

---

## Model Flexibility Within Spawners

**Key insight:** Spawners are NOT limited to their primary model. Use alternative models within spawners:

```python
# ✅ Gemini spawner usually uses Gemini 2.0-Flash
Task(subagent_type="gemini",
     prompt="Explore codebase")

# ✅ But it can also use Haiku for cost optimization
Task(subagent_type="haiku",
     prompt="Explore codebase (faster alternative)")

# ✅ Copilot spawner usually uses Copilot
Task(subagent_type="copilot",
     prompt="GitHub workflow",
     allow_tools=["github(*)"])

# ✅ But fallback uses Sonnet (if Copilot unavailable)
Task(subagent_type="sonnet",
     prompt="GitHub workflow (fallback)")

# ✅ Claude spawner can use ANY Claude model
Task(subagent_type="haiku", prompt="...")   # Cheap
Task(subagent_type="sonnet", prompt="...")  # Balanced
Task(subagent_type="opus", prompt="...")    # Expensive
```

**Principle:** ALWAYS treat models as tools, NEVER as rigid assignments.

---

## Cost Optimization Strategy

### 1. Know Your Task Requirements

```python
# Task type → Best spawner → Expected cost
{
    "exploratory": ("gemini-spawner", "FREE"),
    "batch-analysis": ("gemini-spawner", "FREE"),
    "github-ops": ("copilot-spawner", "GitHub API"),
    "code-generation": ("codex-spawner", "Platform-dependent"),
    "reasoning": ("claude-spawner", "Model-dependent"),
    "mixed-task": ("multiple spawners", "Optimized"),
}
```

### 2. Parallel Over Sequential

```python
# ❌ Sequential (expensive orchestrator fills context)
result1 = bash("search for auth patterns")
result2 = bash("search for api patterns")
result3 = bash("search for db patterns")
# Cost: 3 × expensive orchestrator tool calls

# ✅ Parallel (cheap orchestrator, parallel subagents)
Task(prompt="search for auth patterns")  # Subagent 1
Task(prompt="search for api patterns")   # Subagent 2
Task(prompt="search for db patterns")    # Subagent 3
# Cost: 3 × cheap subagent calls, runs in parallel
```

### 3. Use Cheap Models for Heavy Lifting

```python
# Pattern: Gemini for exploration, Claude for reasoning
Task(subagent_type="gemini-spawner",
     prompt="Find all database tables and relationships")

Task(subagent_type="claude-spawner",
     prompt="Analyze schema design and recommend improvements")

# Cost breakdown:
# - Exploration: FREE (Gemini)
# - Analysis: Expensive (Claude) but only on structured findings
```

### 4. Progressive Disclosure

```python
# Start cheap, escalate only if needed
Task(subagent_type="gemini-spawner",
     prompt="Does this code have security issues?")

if security_issues_found:
    # Only now use expensive Opus for deep analysis
    Task(subagent_type="claude-spawner",
         prompt="Deep security analysis and fixes",
         model="claude-opus-4-5")
```

---

## Orchestrator Benefits Over Direct Execution

### Context Preservation

```
DIRECT (fills context):
Orchestrator reads file 1 → context: 20 lines
Orchestrator reads file 2 → context: 40 lines
Orchestrator reads file 3 → context: 60 lines
... total context pollution: HIGH

DELEGATED (preserves context):
Orchestrator Task("Read files and summarize")
Subagent reads all 3 files
Orchestrator gets: 1 summary line
... total context pollution: MINIMAL
```

### Parallel Execution

```
SEQUENTIAL (slow):
Subagent 1: 10 seconds
Subagent 2: 10 seconds
Subagent 3: 10 seconds
Total time: 30 seconds

PARALLEL (fast):
Subagent 1: 10 seconds  ↓
Subagent 2: 10 seconds  ↓ Run at same time
Subagent 3: 10 seconds  ↓
Total time: 10 seconds (3x faster!)
```

### Cost Optimization

```
DIRECT (expensive):
Orchestrator (Opus): 5 tool calls, full context
Cost: 5 × expensive operations

DELEGATED (cheap):
Orchestrator (Haiku): 5 Task() calls
Subagents (Haiku/Gemini): Do the actual work
Cost: 5 × cheap orchestration calls
```

---

## Advanced: Subagent Capabilities

Each spawner type has different tool access:

| Spawner | Tools | Best For |
|---------|-------|----------|
| **Gemini** | Read, Bash, Grep, Glob, WebSearch, WebFetch | Exploration, research, multimodal |
| **Copilot** | Read, Bash, Grep, Glob (+ GitHub API) | GitHub workflows, git operations |
| **Codex** | Read, Bash, Grep, Glob | Code generation, completions |
| **Claude** | All tools (Read, Bash, Edit, Write, Grep, Glob) | General purpose, complex tasks |

---

## Troubleshooting

### Spawner Fails to Connect

```python
# If spawner CLI fails, automatic fallback occurs
result = spawner.spawn_gemini(prompt="...")

if not result.success:
    print(f"Spawner failed: {result.error}")
    # Fallback to Task() with general-purpose subagent
    Task(prompt=original_prompt)
```

### Subagent Exceeded Time Limit

```python
# Make prompt more specific, tighter boundaries
Task(
    prompt="In src/auth/ only, find login patterns. Stop after 5 minutes."
)
```

### Results Not Structured Enough

```python
# Request explicit output format
Task(
    prompt="""Find API endpoints and return as JSON:
    {
        "endpoints": [
            {"path": "/api/users", "method": "GET", "file": "src/api/users.py"}
        ]
    }"""
)
```

---

## Best Practices

1. **Start with Orchestrator Mode (Guidance)**
   - Learn patterns before strict enforcement
   - Use warnings to identify optimization opportunities

2. **Delegate Early**
   - NEVER fill orchestrator context before delegating
   - ALWAYS delegate exploratory work immediately

3. **Mix Spawners Strategically**
   - ALWAYS use cheap models for exploration
   - ONLY use expensive models for reasoning
   - ALWAYS use GitHub spawner for GitHub ops

4. **Use Parallel Execution**
   - ALWAYS run multiple Task() calls in parallel
   - Total time = slowest task, NEVER sum of all

5. **Request Structured Output**
   - ALWAYS ask for JSON, markdown tables, or organized text
   - Make consolidation easier

6. **Monitor Results**
   - ALWAYS check subagent status via session tracking
   - MUST review child sessions for debugging

---

## Related Documentation

- [Delegation Guide](./guide/delegation.md) - Detailed delegation patterns
- [AGENTS.md](../AGENTS.md) - SDK and workflow examples
- [CLAUDE.md](../CLAUDE.md) - Project-specific guidance
- [examples/](../examples/) - Real-world usage examples

---

## Summary

Use the orchestrator pattern as **flexible coordination, NOT rigid control**:

- ✅ **MUST choose models based on task needs** (NEVER fixed rules)
- ✅ **MUST mix spawner types within workflows** (pick the right tool)
- ✅ **MUST optimize cost strategically** (cheap for exploration, expensive for reasoning)
- ✅ **MUST preserve orchestrator context** (delegate heavy lifting)
- ✅ **MUST maximize parallel execution** (run independent tasks simultaneously)

**Key insight:** The orchestrator is a **coordinator, NOT a controller**. MUST:
1. Break work into parallel subtasks
2. Delegate to best-fit spawner agents
3. Wait for results
4. Consolidate findings
5. Make high-level decisions

ALWAYS run everything else in subagents, keeping the orchestrator cheap and focused.
