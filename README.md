# HtmlGraph

**Local-first observability and coordination platform for AI-assisted development.**

Local-first core — HTML files as nodes, hyperlinks as edges, SQLite for fast local queries. No Postgres, no Redis, no cloud sync required. An optional Phoenix LiveView dashboard provides real-time observability.

> **Design philosophy:** "HTML is All You Need" — work items are standard HTML files readable in any browser, diffable in git, and editable without tooling.

## Why HtmlGraph?

Modern AI agent systems are drowning in complexity:
- Neo4j/Memgraph → Docker, JVM, learn Cypher
- Redis/PostgreSQL → More infrastructure
- Custom protocols → More learning curves

**HtmlGraph uses what you already know:**
- ✅ HTML files = Graph nodes
- ✅ `<a href>` = Graph edges
- ✅ CSS selectors = Query language
- ✅ Any browser = Visual interface
- ✅ Git = Version control (diffs work!)

## Installation

```bash
pip install htmlgraph
```

## Quick Start

### CLI (recommended for new projects)

```bash
htmlgraph init --install-hooks
htmlgraph serve
```

This bootstraps:
- `index.html` dashboard at the project root
- `.htmlgraph/events/` append-only JSONL event stream (Git-friendly)
- `.htmlgraph/index.sqlite` analytics cache (rebuildable; gitignored via `.gitignore`)
- versioned hook scripts under `.htmlgraph/hooks/` (installed into `.git/hooks/` with `--install-hooks`)

### CLI Workflow

```bash
# Create and track features
htmlgraph feature create "User Authentication"
# Returns: feat-abc12345

# Start working on it
htmlgraph feature start feat-abc12345

# Query features by status
htmlgraph find features --status todo
htmlgraph find features --status in-progress

# Complete feature
htmlgraph feature complete feat-abc12345

# Create a track
htmlgraph track new "Q1 Security Initiative"

# Get project snapshot
htmlgraph snapshot --summary
```

### HTML File Format

HtmlGraph nodes are standard HTML files:

```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>User Authentication</title>
</head>
<body>
    <article id="feature-001"
             data-type="feature"
             data-status="in-progress"
             data-priority="high">

        <header>
            <h1>User Authentication</h1>
        </header>

        <nav data-graph-edges>
            <section data-edge-type="blocked_by">
                <h3>Blocked By:</h3>
                <ul>
                    <li><a href="feature-002.html">Database Schema</a></li>
                </ul>
            </section>
        </nav>

        <section data-steps>
            <h3>Steps</h3>
            <ol>
                <li data-completed="true">✅ Create auth routes</li>
                <li data-completed="false">⏳ Add middleware</li>
            </ol>
        </section>
    </article>
</body>
</html>
```

## Features

- **Purpose-built for Claude Code** — Purpose-built observability for AI-assisted development. Natively understands Claude Code sessions, hooks, features, spikes, and agent attribution — not a generic monitoring tool adapted for AI.
- **No external infrastructure** — 10 runtime deps (justhtml, pydantic, jinja2, rich, watchdog, pyyaml, tenacity, networkx, pydantic-settings, typing_extensions), no Postgres/Redis/cloud required
- **HTML canonical store** - work items are standard HTML files, git-diffable and browser-readable
- **SQLite operational layer** - fast local queries, dashboard analytics, rebuild from HTML source
- **Phoenix LiveView dashboard** - real-time activity feed and agent activity monitoring (optional, requires Elixir/Erlang)
- **Multi-AI agent support** - Claude, Gemini, Codex, Copilot coordination out of the box
- **Event-driven hook system** - Claude Code hooks record all tool calls and session events
- **CLI for programmatic access** - features, bugs, spikes, tracks via Go binary
- **Version control friendly** - git diff works perfectly on all artifacts
- **Graph algorithms** - BFS, shortest path, cycle detection, topological sort
- **Agent Handoff** - Context-preserving task transfers between agents
- **Deployment Automation** - One-command releases with version management

## Orchestrator Architecture: Flexible Multi-Agent Coordination

HtmlGraph implements an orchestrator pattern that coordinates multiple AI agents in parallel, preserving context efficiency while maintaining complete flexibility in model selection. Instead of rigid rules, the pattern uses **capability-first thinking** to choose the right tool (and model) for each task.

**Key Principles:**
- ✅ **Flexible model selection** - Any model can do any work; choose based on task fit and cost
- ✅ **Dynamic spawner composition** - Mix and match spawner types (Gemini, Copilot, Codex, Claude) within the same workflow
- ✅ **Cost optimization** - Use cheaper models for exploratory work, expensive models only for reasoning
- ✅ **Parallel execution** - Independent tasks run simultaneously, reducing total time

**Example: Parallel Exploration with Multiple Spawners**

```python
# All run in parallel - each uses the best tool for the job
Task(subagent_type="gemini-spawner",    # FREE exploration
     prompt="Find all authentication patterns in src/auth/")

Task(subagent_type="copilot-spawner",   # GitHub integration
     prompt="Check GitHub issues related to auth",
     allow_tools=["github(*)"])

Task(subagent_type="claude-spawner",    # Deep reasoning
     prompt="Analyze auth patterns for security issues")

# Orchestrator coordinates, subagents work in parallel
# Total time = slowest task (not sum of all)
# Cost = optimized (cheap exploration + expensive reasoning only)
```

**Spawner Types:**
- **Gemini Spawner** - FREE exploratory research, batch analysis (2M tokens/min)
- **Copilot Spawner** - GitHub-integrated workflows, git operations
- **Codex Spawner** - Code generation, coding completions
- **Claude Spawner** - Deep reasoning, analysis, strategic planning (any Claude model)

→ [Complete Orchestrator Architecture Guide](docs/architecture/orchestrator-architecture.md) - Detailed patterns, cost optimization, decision framework, and advanced examples
→ [Multi-Session Coordination Spec](docs/architecture/multi-session-coordination-spec.md) - Roles, claim leasing, handoffs, and integration authority across branches and worktrees

## Comparison

| Feature | Neo4j | JSON | HtmlGraph |
|---------|-------|------|-----------|
| Setup | Docker + JVM | None | None |
| Query Language | Cypher | jq | CSS selectors |
| Human Readable | ❌ Browser needed | 🟡 Text editor | ✅ Any browser |
| Version Control | ❌ Binary | ✅ JSON diff | ✅ HTML diff |
| Visual UI | ❌ Separate tool | ❌ Build it | ✅ Built-in |
| Graph Native | ✅ | ❌ | ✅ |

## Use Cases

1. **AI Agent Coordination** - Task tracking, dependencies, progress
2. **Knowledge Bases** - Linked notes with visual navigation
3. **Documentation** - Interconnected docs with search
4. **Task Management** - Todo lists with dependencies

## Contributing

HtmlGraph is developed using HtmlGraph itself (dogfooding). This means:

- ✅ Every development action is replicable by users through the package
- ✅ We use the SDK, CLI, and plugins - not custom scripts
- ✅ Our development workflow IS the documentation

**See [`docs/archive/DEVELOPMENT.md`](docs/archive/DEVELOPMENT.md) for:**
- Dogfooding principles
- Replicable workflows
- Environment setup (PyPI tokens, etc.)
- Development best practices

**Quick start for contributors:**
```bash
# Clone and setup
git clone https://github.com/shakestzd/htmlgraph
cd htmlgraph
uv sync

# Start tracking your work (dogfooding!)
uv run htmlgraph init --install-hooks
uv run htmlgraph serve  # View dashboard

# Use CLI for development
uv run htmlgraph feature list
uv run htmlgraph find features --status todo
```

## License

MIT

## System Prompt & Delegation Documentation

For Claude Code users and teams using HtmlGraph for AI agent coordination:

- **[System Prompt Quick Start](docs/archive/system-prompts/SYSTEM_PROMPT_QUICK_START.md)** - Setup your system prompt in 5 minutes (start here!)
- **[System Prompt Architecture](docs/architecture/system-prompt-architecture.md)** - Technical deep dive + troubleshooting
- **[Delegation Enforcement Admin Guide](docs/contributing/DELEGATION_ENFORCEMENT_ADMIN_GUIDE.md)** - Setup cost-optimal delegation for your team
- **[System Prompt Developer Guide](docs/archive/system-prompts/SYSTEM_PROMPT_DEVELOPER_GUIDE.md)** - Extend with custom layers, hooks, and skills

## Links

- [GitHub](https://github.com/shakestzd/htmlgraph)
- [CLI Reference](docs/API_REFERENCE.md) - Complete CLI documentation
- [Documentation](docs/) - CLI guide, workflows, development principles
- [Examples](examples/) - Real-world usage examples
- [PyPI](https://pypi.org/project/htmlgraph/)
