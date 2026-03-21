# Comparisons

Detailed comparisons with alternative approaches.

## vs Graph Databases

### Neo4j

**Neo4j Strengths:**
- Mature, battle-tested
- Advanced graph algorithms
- Cypher query language is expressive
- Enterprise support

**Neo4j Weaknesses:**
- Requires Docker + JVM
- Learning curve for Cypher
- Binary data format
- License costs for enterprise
- Complex deployment

**HtmlGraph Approach:**
- `pip install htmlgraph` with ~10 focused runtime dependencies
- No external infrastructure (no JVM, no database servers)
- SQLite SQL and Python SDK for queries
- Plain text HTML files for canonical state
- Free, MIT license
- Local files + local SQLite, no cloud required

**When to use Neo4j:** Large-scale production systems with complex graph queries, enterprise support requirements.

**When to use HtmlGraph:** Rapid prototyping, AI agent coordination, personal projects, Git-friendly workflows.

### Memgraph

Similar trade-offs to Neo4j, with emphasis on real-time analytics.

**Use Memgraph when:** You need streaming graph analytics, real-time pattern matching.

**Use HtmlGraph when:** You need simplicity, portability, human readability.

## vs Document Databases

### JSON Files

**JSON Strengths:**
- Simple format
- Widely supported
- Easy to parse

**JSON Weaknesses:**
- No native graph structure
- Manual reference management
- No built-in presentation
- Needs custom UI

**HtmlGraph Advantages:**
- Native hyperlinks (graph edges)
- Built-in rendering with CSS
- CSS selector queries
- Human-readable in browser

### YAML

Similar to JSON, with more readable syntax but same limitations for graph data.

## vs Note-Taking Tools

### Notion

**Notion Strengths:**
- Beautiful UI
- Collaboration features
- Mobile apps

**Notion Weaknesses:**
- Cloud-only
- Rate-limited API
- No version control
- Vendor lock-in
- Limited AI agent access

**HtmlGraph Advantages:**
- Fully offline
- Unlimited API access
- Git native
- Own your data
- Direct SDK access for agents

### Obsidian

**Obsidian Strengths:**
- Local-first
- Markdown files
- Plugin ecosystem

**Obsidian Weaknesses:**
- Backlinks not typed (no relationship types)
- Proprietary graph format
- Markdown limitations for structured data

**HtmlGraph Advantages:**
- Typed relationships (`data-relationship="blocks"`)
- Native web format
- Structured data with Pydantic
- AI agent-first design

### Roam Research

Similar to Notion but with better graph features. Still cloud-based with same limitations.

## vs AI Agent Memory Systems

### Beads

[Beads](https://github.com/steveyegge/beads) by Steve Yegge is a similar project focused on AI agent task management.

**Beads Strengths:**

- Hash-based IDs prevent conflicts
- Semantic memory decay
- Ready task detection
- Multiple frontends (TUI, web, VS Code)

**Beads Weaknesses:**

- Requires CLI daemon
- JSONL storage format
- Needs viewer tools

**HtmlGraph Approach:**

- Adopted hash-based IDs (inspired by Beads)
- ~10 focused runtime dependencies (pydantic, justhtml, watchdog, rich, jinja2, networkx, and others)
- Three-tier storage: HTML for canonical state, JSONL for event history, SQLite for operational queries
- No daemon process required
- HTML renders in any browser
- Web standards-based

**Shared Design Goals:**

Both projects aim to give AI agents persistent, structured memory beyond a single context window. HtmlGraph's hash-based ID system was directly inspired by Beads' approach to multi-agent collision resistance.

**When to use Beads:** CLI-first workflow, multiple frontends needed, semantic memory decay.

**When to use HtmlGraph:** Browser-first workflow, minimal infrastructure, web standards preference.

## vs AI Agent Frameworks

### LangChain/LangGraph

**LangChain Strengths:**
- Rich ecosystem
- Many integrations
- Active development

**LangChain Weaknesses:**
- Complex abstractions
- Framework lock-in
- Python/JS specific
- No built-in observability

**HtmlGraph Advantages:**
- Simple, web-standards based
- Language agnostic (any language can parse HTML)
- Built-in observability (view in browser)
- No framework lock-in

### AutoGPT/BabyAGI

**AutoGPT Strengths:**
- Autonomous operation
- Task decomposition

**AutoGPT Weaknesses:**
- State management in JSON
- Limited observability
- No multi-agent coordination

**HtmlGraph Advantages:**
- Graph-based state (HTML)
- Full observability (dashboard)
- Multi-agent coordination built-in

## Feature Comparison Matrix

| Feature | Neo4j | JSON | Notion | Obsidian | HtmlGraph |
|---------|-------|------|--------|----------|-----------|
| Setup complexity | High | Low | None (cloud) | Low | Low |
| Query language | Cypher | jq/custom | UI only | Search | SQLite SQL / SDK API |
| Version control | ❌ | ✅ | ❌ | ✅ | ✅ |
| Offline-first | ✅ | ✅ | ❌ | ✅ | ✅ (live dashboard optional) |
| AI agent API | REST | File I/O | Rate-limited | File I/O | SDK + File I/O |
| Human readable | ❌ | 🟡 | ✅ | ✅ | ✅ |
| Graph native | ✅ | ❌ | 🟡 | 🟡 | ✅ |
| Typed relationships | ✅ | ❌ | ❌ | ❌ | ✅ |
| Self-hosting | ✅ | ✅ | ❌ | ✅ | ✅ |
| Cost | $$$ | Free | $ | $ | Free |

## When to Use Each

### Use Neo4j when:
- Enterprise production system
- Complex graph algorithms needed
- Dedicated DBA available
- Budget for licensing

### Use JSON when:
- Simple key-value data
- No graph structure needed
- Minimal querying

### Use Notion when:
- Team collaboration is primary
- Cloud-first is acceptable
- AI agents are secondary

### Use Obsidian when:
- Personal knowledge base
- Markdown preference
- Plugin ecosystem needed

### Use HtmlGraph when:
- AI agent coordination
- Git-based workflows
- Offline-first required (HTML and JSONL files work without any server)
- Simplicity is priority
- Own your data
- No external database servers or cloud services required
- Live dashboard is optional (Docker needed only for the Phoenix LiveView dashboard)

## Next Steps

- [Design Decisions](decisions.md) - Why specific choices were made
- [Why HTML?](why-html.md) - Core philosophy
