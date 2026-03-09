# Core Concepts

HtmlGraph is a local-first observability and coordination platform for AI-assisted development. This guide explains the core architecture and how its components work together.

## Architecture Overview

HtmlGraph is a hybrid runtime with three storage layers, each canonical for its purpose:

```
Work Items (HTML files)    ← canonical for document CRUD, human-readable artifacts
Event Log (JSONL)          ← canonical for append-only history
Runtime Indexes (SQLite)   ← canonical for operational querying, dashboard, analytics, search
```

These layers are kept in sync automatically. The HTML files are durable artifacts you can open in any browser; SQLite provides fast indexed access for the dashboard and SDK queries; the JSONL log is the immutable audit trail.

## Storage Layers

### Work Items (HTML Artifacts)

Work items -- features, bugs, spikes, tracks -- are stored as HTML files in the `.htmlgraph/` directory. Each file is a self-contained document with structured metadata in `data-*` attributes and human-readable content in standard HTML.

**Why HTML?**
- Open in any browser -- no special tooling required
- Git-native -- plain text diffs, merge-friendly
- Durable -- the file format will outlast any database engine
- Self-describing -- metadata and presentation in one file

```html
<article id="feat-a1b2c3d4"
         data-type="feature"
         data-status="in-progress"
         data-priority="high">
    <h1>User Authentication</h1>
    <section data-steps>
        <ol>
            <li data-completed="true">Create auth routes</li>
            <li data-completed="false">Add middleware</li>
        </ol>
    </section>
</article>
```

**File locations:**
- `.htmlgraph/features/feat-{hash}.html`
- `.htmlgraph/bugs/bug-{hash}.html`
- `.htmlgraph/spikes/spk-{hash}.html`
- `.htmlgraph/tracks/trk-{hash}.html`
- `.htmlgraph/sessions/sess-{hash}.html`

### Event Log (JSONL)

Every agent action is recorded as an append-only JSON line. The event log is the immutable history of what happened, when, and by whom.

- **Append-only** -- events are never modified or deleted
- **Git-friendly** -- JSONL diffs show exactly which events were added
- **Rebuildable** -- SQLite indexes can be rebuilt from the event log

**File location:** `.htmlgraph/events/{session-id}.jsonl`

Each event contains:
- **Timestamp** -- when the event occurred
- **Event type** -- `ToolUse`, `UserPrompt`, `SessionStart`, etc.
- **Session ID** -- which session generated the event
- **Feature ID** -- which work item receives attribution
- **Payload** -- event-specific data

### Runtime Indexes (SQLite)

SQLite is the canonical store for all operational queries. The dashboard, analytics, search, sync state, and cursor tracking all read from SQLite.

- **Fast queries** -- indexed access to work items, sessions, events
- **Dashboard views** -- Kanban boards, timelines, graphs all query SQLite
- **Analytics** -- bottleneck detection, velocity tracking, work recommendations
- **Full-text search** -- FTS5 index across all work items
- **Rebuildable** -- can be reconstructed from HTML files and JSONL events

**File location:** `.htmlgraph/htmlgraph.db`

## Key Components

### Features

**Features** are the atomic units of work. Each feature is an HTML file with:

- **Status**: `todo`, `in-progress`, `blocked`, `done`
- **Priority**: `low`, `medium`, `high`, `critical`
- **Steps**: Checklist of implementation tasks
- **Properties**: Custom metadata (`effort`, `completion`, etc.)
- **Edges**: Links to related features (blocks, blocked_by, related)

```python
from htmlgraph import SDK

sdk = SDK(agent="claude")

feature = sdk.features.create(
    title="User Authentication",
    status="todo",
    priority="high",
    steps=["Create endpoint", "Add middleware", "Write tests"]
)
```

### Tracks

**Tracks** are multi-feature projects that bundle related work with specs and plans. Each track is a directory containing:

- **index.html**: Track overview and dashboard
- **spec.html**: Requirements and success criteria
- **plan.html**: Phased implementation plan with time estimates

```python
track = sdk.tracks.builder() \
    .title("OAuth Integration") \
    .with_spec(
        overview="Add OAuth 2.0 support",
        requirements=[("Google OAuth", "must-have")]
    ) \
    .with_plan_phases([
        ("Phase 1", ["Configure OAuth (2h)", "Setup endpoints (1h)"])
    ]) \
    .create()
```

### Sessions

**Sessions** track all activity during an agent's work session. Each session is an HTML file with:

- **Events**: Log of all tool calls and interactions
- **Features worked on**: Which features received attribution
- **Timestamps**: Start and end times
- **Agent**: Which agent did the work

Sessions are automatically created and managed by HtmlGraph hooks.

### Spikes

**Spikes** are time-boxed investigation tasks. Use them to research a question, prototype an approach, or document findings before committing to a feature.

## Graph Structure

### Nodes

Every HTML file in HtmlGraph is a graph node. Nodes have:

- **ID**: Unique, collision-resistant identifier (e.g., `feat-a1b2c3d4`)
- **Type**: `feature`, `track`, `session`, `bug`, `spike`, or custom
- **Properties**: Stored in `data-*` attributes
- **Content**: Human-readable description in HTML

#### Hash-Based IDs

HtmlGraph uses hash-based IDs for multi-agent collaboration:

| Type | Prefix | Example |
|------|--------|---------|
| Feature | `feat-` | `feat-a1b2c3d4` |
| Bug | `bug-` | `bug-12345678` |
| Track | `trk-` | `trk-abcdef12` |
| Session | `sess-` | `sess-7890abcd` |
| Spike | `spk-` | `spk-87654321` |

These IDs are collision-resistant -- multiple agents can create nodes simultaneously without conflicts.

### Edges

Edges are created using standard HTML hyperlinks. The relationship type is specified using `data-relationship` attributes:

```html
<a href="feat-005.html"
   data-relationship="blocks">Database Schema</a>
```

Common relationship types: `blocks`, `blocked_by`, `related`, `implements`, `part_of`.

## Data Flow

```
┌──────────────────────────────────────────────────────────┐
│ 1. Agent creates/updates work items via SDK or CLI       │
└────────────────┬─────────────────────────────────────────┘
                 │
                 ▼
┌──────────────────────────────────────────────────────────┐
│ 2. Pydantic models validate data and generate HTML       │
└────────────────┬─────────────────────────────────────────┘
                 │
                 ▼
┌──────────────────────────────────────────────────────────┐
│ 3. HTML files written to .htmlgraph/ directory           │
└────────────────┬─────────────────────────────────────────┘
                 │
                 ▼
┌──────────────────────────────────────────────────────────┐
│ 4. Hooks log events to JSONL append-only log             │
└────────────────┬─────────────────────────────────────────┘
                 │
                 ▼
┌──────────────────────────────────────────────────────────┐
│ 5. SQLite indexes updated for fast queries and search    │
└────────────────┬─────────────────────────────────────────┘
                 │
                 ▼
┌──────────────────────────────────────────────────────────┐
│ 6. FastAPI/HTMX dashboard reflects changes in real time  │
└──────────────────────────────────────────────────────────┘
```

## Design History

HtmlGraph began with the philosophy "HTML is All You Need" -- the idea that web standards (HTML files, hyperlinks, CSS selectors) could serve as a lightweight graph database. That origin still shows in the architecture: work items remain HTML files, and the graph structure uses hyperlinks as edges. Over time, the project evolved into a hybrid runtime where HTML provides durable human-readable artifacts, SQLite provides fast operational access, and JSONL provides immutable history. The "HTML is All You Need" framing is best understood as a design influence, not a literal architecture claim.

## SDK vs CLI vs Dashboard

### SDK (Python)

For programmatic access and agent integration:

```python
from htmlgraph import SDK
sdk = SDK(agent="claude")
feature = sdk.features.create("Task")
```

### CLI (Bash)

For command-line workflows:

```bash
htmlgraph feature create "Task"
htmlgraph feature start feat-a1b2c3d4
htmlgraph serve
```

### Dashboard (Browser)

For visual exploration -- Kanban board view, graph visualization, timeline view, session history. Run `htmlgraph serve` or open `index.html` in any browser.

## Next Steps

- [Features & Tracks Guide](../guide/features-tracks.md) - Detailed feature and track workflows
- [TrackBuilder Guide](../guide/track-builder.md) - Master the TrackBuilder API
- [Sessions Guide](../guide/sessions.md) - Understanding session tracking
- [API Reference](../api/index.md) - Complete SDK documentation
