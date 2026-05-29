# Local-first architecture research for wipnote

Date: 2026-05-28

This note captures research and planning context for applying proven local-first
architecture patterns to wipnote's dashboard, CLI, SQLite read index, and
agent-plugin hooks.

## Prompt context

The research was prompted by recurring `SQLITE_BUSY` issues and a broader
question: whether local-first web architecture, WebAssembly, and proven sync
patterns from other projects can improve how the wipnote dashboard works with
the wipnote CLI and the Claude/Codex/Gemini plugin hooks.

## Sources reviewed

- Smashing Magazine: "The Architecture of Local-First Web Development"
  - https://www.smashingmagazine.com/2026/05/architecture-local-first-web-development/
- DuckDB: "The Quack Remote Protocol"
  - https://duckdb.org/2026/05/12/quack-remote-protocol
- Local-first software directory/community
  - https://lofi.so
- PowerSync docs
  - https://docs.powersync.com/
- ElectricSQL docs
  - https://electric-sql.com/docs
- TinyBase synchronization docs
  - https://tinybase.org/guides/synchronization/
- Automerge Repo docs
  - https://automerge.org/docs/reference/repositories/
- Yjs offline editing docs
  - https://docs.yjs.dev/getting-started/allowing-offline-editing
- Epicenter architecture/code
  - https://github.com/EpicenterHQ/epicenter
- Any-Sync protocol
  - https://github.com/anyproto/any-sync

## Takeaways

### 1. Keep canonical local files separate from derived indexes

The strongest pattern for wipnote is "canonical log/document first, derived DB
second." Epicenter is especially relevant: it uses local-readable project data
plus SQLite/materialized views for fast queries. wipnote already has this shape:
`.wipnote/*.html` and session/event artifacts are canonical, while SQLite is a
per-user read index that should be rebuildable.

Planning implication: make the rebuild/staleness contract explicit. Every
SQLite table used by the dashboard should be either clearly rebuildable from
canonical files/events or explicitly labeled as non-rebuildable state.

### 2. Treat SQLite contention as a topology problem

The DuckDB Quack post validates the direction of putting a single process in
charge of writes. Quack is DuckDB-specific and not an immediate replacement for
wipnote's SQLite layer, but the architecture is relevant: multi-process
embedded DB mutation becomes more reliable when writes are serialized through a
local owner.

wipnote already moved in this direction with the dashboard read-only handle,
`RetryOnBusy`, and the single-writer queue used by the OTel/indexer path.

Planning implication: graduate the existing single-writer pattern into a local
`wipnote daemon` or coordinator that owns SQLite writes for dashboard, hooks,
CLI commands, and agent sessions. Direct SQLite writes from short-lived hooks
should become fallback behavior, not the primary path.

### 3. Use reactive dashboard state instead of repeated broad fetches

PowerSync, TinyBase, and ElectricSQL all emphasize local reactive state:
clients query a local projection and update incrementally as changes arrive.
For wipnote, this maps to dashboard panels that subscribe to specific streams:
active work, session events, lineage neighborhoods, busy counters, and plan
feedback.

Planning implication: introduce typed server-sent event channels or snapshot +
delta APIs so the dashboard does not repeatedly re-query broad project state.
This should reduce backend read pressure and make the UI feel more live.

### 4. Use "shapes" or scoped projections for dashboard views

ElectricSQL's shape concept and PowerSync's sync streams are useful mental
models even without adopting either tool. Each dashboard view should request the
minimal projection it needs.

Planning implication: define stable projections such as:

- `active-work`
- `session-events:<session-id>`
- `lineage:<work-item-id>`
- `collector-status`
- `plan:<plan-id>`
- `agent-activity`

These can be served from SQLite plus canonical files and pushed over SSE.

### 5. WebAssembly is useful for shared read-only logic, not host mutation

WASM can help reduce drift between CLI and dashboard by reusing selected Go
logic in the browser: parsers, validators, lineage traversal, status
classification, or hook-policy simulation. It cannot replace host plugin hooks
or local process management.

Planning implication: evaluate a small WASM package boundary for read-only
interpretation and dashboard-side simulations. Avoid compiling the entire CLI
to WASM. Keep mutation in the Go process/daemon.

### 6. CRDTs should be surgical

Automerge, Yjs, Loro, and Any-Sync are proven for collaborative documents and
object graphs. wipnote does not appear to need a CRDT rewrite. Most wipnote data
is better represented as append-only events plus deterministic reducers.

Planning implication: consider CRDTs only for future collaborative editing of
rich plans/specs/notes. Do not use CRDTs for hook events, status transitions, or
the core SQLite read index.

### 7. Conflicts should become first-class objects

Local-first systems expect conflicts. wipnote currently experiences conflicts
as races, lock errors, failed hooks, duplicate work items, or confusing agent
state. A better model is to accept conflicting observations where possible and
surface them as resolvable objects.

Planning implication: add explicit conflict/violation records for agent
collisions, stale sessions, duplicate work item creation, failed hook writes,
and cache/index divergence.

## Candidate plan themes

1. Local coordinator / daemon
   - Single owner for SQLite writes.
   - Hooks and CLI submit events/commands to the coordinator.
   - Direct write mode remains an offline/fallback path.

2. Rebuildable read-index contract
   - Inventory SQLite tables.
   - Mark each as canonical-derived, ephemeral, or non-rebuildable.
   - Add dashboard-visible staleness and reindex health.

3. Reactive dashboard projections
   - Snapshot + delta API.
   - Typed SSE channels.
   - Panel-specific projections instead of broad reloads.

4. Browser-side shared logic
   - Investigate a small Go WASM package for read-only plan/work-item parsing,
     lineage traversal, hook-policy simulation, or validation.
   - Keep file/process/SQLite mutation in Go on the host.

5. Conflict and recovery UX
   - First-class conflict records.
   - Dashboard panels for SQLite contention, stale cache, duplicate item risk,
     hook write failures, and orphaned sessions.

6. Local-first test harness
   - Simulate multiple agents, hooks, dashboard reads, daemon restarts, and
     cache deletion.
   - Assert replay convergence and no lost canonical events.

## Recommended planning posture

Classify this as complex. The plan should not be a single large rewrite. It
should be a staged architecture plan that preserves current behavior while
moving toward a daemon/coordinator and a more reactive dashboard.

Non-goals for the first plan:

- Do not replace SQLite with DuckDB.
- Do not introduce a required cloud sync backend.
- Do not rewrite canonical `.wipnote` storage as CRDTs.
- Do not make WASM responsible for local filesystem, process, or DB mutation.
- Do not require the dashboard to be open for hooks or CLI commands to work.

