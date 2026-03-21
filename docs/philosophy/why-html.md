# Why HTML?

The philosophy behind "HTML is All You Need"

## The Problem

Modern AI agent systems are drowning in complexity:

- **Neo4j/Memgraph**: Requires Docker, JVM, learning Cypher query language
- **Redis**: Adds caching and state management overhead
- **PostgreSQL**: Heavy relational database setup and maintenance
- **Custom Protocols**: Proprietary agent coordination systems
- **JSON/YAML**: Manual reference management, no native graph structure
- **Separate UIs**: Additional observability tools and dashboards

Each component adds:

- Installation friction
- Learning curve
- Runtime dependencies
- Maintenance burden
- Integration complexity

## The Insight

**The web is already a giant graph database.**

Every HTML document has:

- **Nodes**: HTML files
- **Edges**: Hyperlinks (`<a href>`)
- **Properties**: `data-*` attributes
- **Query language**: SQLite SQL (via SDK) — HTML structure provides the schema
- **Presentation**: Built-in rendering with CSS
- **Portability**: Works everywhere
- **Version control**: Git-friendly text format

## Core Principles

### 1. Standards Over Invention

Use existing web standards instead of creating new ones:

- **HTML** for structure and content
- **CSS** for styling and presentation
- **JavaScript** for interactivity
- **HTTP** for serving
- **SQLite** for fast local queries (via the SDK)

These standards are:

- Well-documented
- Universally supported
- Battle-tested
- Familiar to everyone

### 2. Human-Readable First

Optimizing for human readability has unexpected benefits:

- **Debugging**: View source in any browser
- **Version control**: Meaningful git diffs
- **Onboarding**: No special tools to learn
- **Trust**: See exactly what's stored
- **Portability**: Works in any environment

### 3. Minimal Infrastructure

**The HTML files themselves** work with just:

- A file system
- A web browser

**The SDK** has 10 runtime dependencies:

- `pydantic`, `pydantic-settings` - Data validation and models
- `justhtml` - HTML parsing
- `watchdog` - File watching for live updates
- `rich` - Terminal output formatting
- `jinja2` - Template rendering
- `pyyaml` - YAML configuration
- `tenacity` - Retry logic
- `networkx` - Graph algorithms
- `sqlite3` - Operational queries (Python standard library)

**What you don't need:**

- External database servers (Neo4j, Redis, PostgreSQL)
- Build tools or compilation
- Cloud services or API keys
- Daemon processes
- Docker (optional: only needed for the live Phoenix dashboard)

### 4. Offline First

HtmlGraph's core storage is fully offline:

- HTML and JSONL files require no network, no authentication, and no external services
- The Python SDK works entirely on local files
- Copy the `.htmlgraph/` directory anywhere and it just works

The live dashboard (`htmlgraph serve`) requires running a local server process. The Phoenix LiveView dashboard uses Docker for the Elixir runtime, but this is optional — the HTML files themselves are always readable in any browser without it.

### 5. Git Native

HTML is plain text, which means:

- **Diffs show real changes**: See exactly what changed
- **Merge conflicts are readable**: Resolve conflicts easily
- **History is meaningful**: Understand evolution over time
- **Branches work naturally**: Experiment safely

### 6. AI Agent Friendly

HTML is ideal for AI agents:

- **Structured but flexible**: Easy to parse and generate
- **Self-documenting**: Content and metadata together
- **Hyperlinks are native**: Relationships are first-class
- **Python SDK**: Typed, fluent API for querying and mutating the graph

## Benefits

### For Developers

- **Fast setup**: `pip install htmlgraph`, done
- **No configuration**: Works out of the box
- **View in browser**: Open any file to see it styled
- **Standard tools**: Git, text editors, browsers

### For AI Agents

- **Simple API**: SDK or direct HTML manipulation
- **Context-efficient**: Lightweight node representation
- **Clear attribution**: Session tracking built-in
- **Deterministic**: TrackBuilder for repeatable workflows

### For Teams

- **No infrastructure**: No databases to maintain
- **Easy sharing**: Commit to git, done
- **Transparent**: Everyone can view the graph
- **Accessible**: No special permissions or access

### For Projects

- **Low overhead**: Files on disk, that's it
- **Scalable**: Millions of nodes possible
- **Portable**: Move projects easily
- **Archivable**: HTML will outlive most databases

## Trade-offs

### What You Gain

- Simplicity
- Portability
- Human readability
- No external database servers or cloud services
- Git integration
- Universal compatibility

### What You Give Up

- Sub-millisecond queries at very large scale (SQLite handles most workloads well)
- Complex graph algorithms out of the box (networkx is included; extend as needed)
- Concurrent writes without coordination (use the SDK's session model)
- Database GUI tools (use the HTML dashboard or `htmlgraph serve` instead)

**The trade-off is worth it** for most use cases. HtmlGraph uses a three-tier storage model — HTML files for canonical, human-readable state; JSONL for append-only event history; SQLite for fast operational queries and dashboard state — giving you the benefits of each without the infrastructure overhead of a standalone database server.

## Philosophy in Practice

### Start Simple

```python
# Just create a feature
feature = sdk.features.create("Add login")

# It's an HTML file
# Open it in a browser
# That's it
```

### Query and Analyze When Needed

```python
# Query with SQLite (built-in, always available)
in_progress = sdk.features.where(status="in-progress")

# Complex graph analysis via networkx
path = sdk.graph.shortest_path(start, end)
```

### Trust Web Standards

Don't reinvent what already exists:

- **Styling**: Use CSS, not custom renderers
- **Storage**: Use HTML files, not custom binary formats
- **Serving**: Use HTTP, not custom protocols
- **Queries**: Use SQLite SQL for programmatic access, the Python SDK for typed queries

## Comparisons

### vs Neo4j

| Feature | Neo4j | HtmlGraph |
|---------|-------|-----------|
| Setup | Docker + JVM + Cypher | `pip install` |
| Query | Learn Cypher | SQLite SQL / Python SDK |
| View data | Neo4j Browser | Any web browser |
| Version control | Binary exports | Git diff |
| Portability | Requires runtime | Just files |

### vs JSON/YAML

| Feature | JSON/YAML | HtmlGraph |
|---------|-----------|-----------|
| Structure | Manual references | Native hyperlinks |
| Presentation | Needs separate UI | Built-in rendering |
| Querying | jq or custom | SQLite SQL / Python SDK |
| Validation | JSON Schema | HTML + Pydantic |

### vs Notion/Roam

| Feature | Notion/Roam | HtmlGraph |
|---------|-------------|-----------|
| Ownership | Cloud-hosted | Your filesystem |
| API | Rate-limited | Direct file access |
| Offline | Limited | Full functionality |
| Version control | Not supported | Git native |
| Agent access | API tokens | Direct SDK |

## The Future

HTML isn't going anywhere. By building on web standards, HtmlGraph will work:

- In 10 years
- On any platform
- With any tools
- For any use case

**HTML is all you need.**

## Next Steps

- [Comparisons](comparisons.md) - Detailed comparisons with alternatives
- [Design Decisions](decisions.md) - Why specific choices were made
- [Getting Started](../getting-started/installation.md) - Try it yourself
