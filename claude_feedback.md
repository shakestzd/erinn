# What HtmlGraph Is Leaving on the Table

This is a fundamental architectural question. You've built a graph system that stores data in the format a graph engine (the browser) natively understands — **but then you bypass that engine entirely** and rebuild the graph processing in Python.

---

## The Core Gap

| What HtmlGraph Does | What the Browser Already Does |
|---------------------|-------------------------------|
| Parses HTML with `justhtml` (Python) | Parses HTML natively (C++ engine) |
| Builds graph in `networkx` (Python) | Builds DOM tree automatically |
| Queries with Python SDK | Queries with `querySelectorAll` (CSS selectors) |
| Indexes in SQLite | Could index in IndexedDB (browser-native DB) |
| Serves dashboard via Phoenix LiveView | Could render entirely client-side |

**You're storing data in the browser's native language, then shipping it to a completely different runtime to process it.**

---

## Specific Capabilities You're Not Using

### 1. The DOM API as Your Graph Engine

The browser gives you a full graph traversal API for free:

```javascript
// Walk every work item and its edges — zero libraries needed
const items = document.querySelectorAll('[data-type="feature"]')
items.forEach(item => {
  const deps = item.querySelectorAll('[data-edge-type="blocked_by"] a')
  const status = item.dataset.status
  // You just traversed your graph
})
```

**`TreeWalker`** and **`NodeIterator`** are purpose-built graph traversal APIs that already exist in every browser. Your BFS, shortest path, and topological sort could run directly on the DOM.

---

### 2. `MutationObserver` — Real-Time Graph Change Detection

The browser can **watch the DOM for changes** and react:

```javascript
const observer = new MutationObserver(mutations => {
  // A node's status changed, an edge was added, etc.
  // Trigger re-render, update analytics, notify agents
})
observer.observe(document.body, { 
  subtree: true, attributes: true, childList: true 
})
```

This replaces your event-driven hook system with something the browser does natively. No JSONL event stream needed — **the DOM itself is the event source.**

---

### 3. CSS `:has()` — Graph Queries in Pure CSS

Modern CSS can query based on **relationships between nodes**:

```css
/* Highlight any feature that has unresolved blockers */
article[data-type="feature"]:has([data-edge-type="blocked_by"] a) {
  border-left: 4px solid red;
}

/* Style completed features differently */
article[data-status="done"] {
  opacity: 0.6;
}
```

**Your status visualization, dependency warnings, and bottleneck detection could be pure CSS.** No JavaScript, no Python, no dashboard server. Just open the HTML file.

---

### 4. Web Components — Self-Aware Graph Nodes

Instead of `<article data-type="feature">`, imagine:

```html
<work-item id="feature-001" status="in-progress" priority="high">
  <dependency-edge type="blocked_by" href="feature-002.html"/>
  <step completed="true">Create auth routes</step>
  <step completed="false">Add middleware</step>
</work-item>
```

Custom elements can carry their **own behavior** — they know how to render themselves, validate their state, compute their completion percentage, and communicate with neighboring nodes. The graph nodes become active, not passive.

---

### 5. `fetch()` + DOM Parsing — Client-Side Graph Assembly

The browser can follow your `<a href>` edges and build the full graph:

```javascript
async function buildGraph(entryFile) {
  const visited = new Set()
  const graph = new Map()
  
  async function crawl(url) {
    if (visited.has(url)) return
    visited.add(url)
    const html = await fetch(url).then(r => r.text())
    const doc = new DOMParser().parseFromString(html, 'text/html')
    const edges = doc.querySelectorAll('[data-graph-edges] a')
    graph.set(url, [...edges].map(a => a.getAttribute('href')))
    await Promise.all([...edges].map(a => crawl(a.href)))
  }
  
  await crawl(entryFile)
  return graph
}
```

**The browser becomes the graph database.** No SQLite, no Python, no server. Open `index.html` and the entire work graph assembles itself.

---

## The Strategic Reframe

Right now HtmlGraph's architecture is:

```
HTML files → Python parser → Python graph → SQLite → Phoenix dashboard → Browser
```

It could be:

```
HTML files → Browser (it already IS the parser + graph engine + renderer)
```

**The value HtmlGraph should add isn't rebuilding what the browser does.** It's the things the browser *can't* do natively:

- **Multi-agent coordination protocols** (who's working on what)
- **Git integration** (commit hooks, diff-aware updates)
- **Cross-file graph assembly** (the browser's same-origin restrictions make this tricky without a local server)
- **CLI tooling** for agents that don't have a browser (Claude Code, Codex)

---

## The Bottom Line

Your fundamental insight — "HTML is all you need" — is more true than your current architecture reflects. The browser is already the most sophisticated HTML graph engine ever built. The question is: **what if you leaned into that instead of working around it?**

# Creation vs. Consumption — Two Different Problems

You're asking about the **write path**. Everything we discussed before was about the **read path** (parsing, querying, traversing). These need completely different solutions.

---

## The Key Distinction

| Path | What Happens | What Handles It |
|------|-------------|-----------------|
| **Read** | Parse HTML → build graph → query → render | Browser (native) |
| **Write** | Agent produces structured data → valid HTML file on disk | ??? |

**Playwright/Selenium are massive overkill.** They spin up an entire browser runtime just to build a string. That's like hiring a construction crew to write a blueprint.

---

## What Agents Actually Need to Do

When the SDK does this:

```python
feature = sdk.features.create("User Authentication") \
    .set_priority("high") \
    .add_steps(["Create login endpoint", "Add JWT middleware"]) \
    .save()
```

The only thing that happens at `.save()` is: **structured data gets serialized into an HTML string and written to disk.**

That's it. No parsing. No DOM. No browser. Just **data → text file.**

---

## The Solution: Separate Data from Serialization

### What justhtml Does Now (Both Directions)

```
Write: Python objects → justhtml → HTML string → disk
Read:  disk → HTML string → justhtml → Python objects
```

### What It Should Be

```
Write: Python objects → Template/Serializer → HTML string → disk
Read:  disk → Browser DOM → CSS selectors → results
```

**You don't need a parser to create files. You need a serializer.**

---

## Three Approaches (No Browser Required)

### Option 1: Jinja2 Templates (You Already Have This Dependency)

```python
# templates/feature.html.j2
FEATURE_TEMPLATE = """<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>{{ title }}</title></head>
<body>
  <article id="{{ id }}" 
           data-type="feature" 
           data-status="{{ status }}" 
           data-priority="{{ priority }}">
    <header><h1>{{ title }}</h1></header>
    <nav data-graph-edges>
      {% for edge_type, targets in edges.items() %}
      <section data-edge-type="{{ edge_type }}">
        <ul>
          {% for target in targets %}
          <li><a href="{{ target.href }}">{{ target.label }}</a></li>
          {% endfor %}
        </ul>
      </section>
      {% endfor %}
    </nav>
    <section data-steps>
      <ol>
        {% for step in steps %}
        <li data-completed="{{ step.completed | lower }}">{{ step.text }}</li>
        {% endfor %}
      </ol>
    </section>
  </article>
</body>
</html>"""
```

**The agent never touches HTML.** It fills in a Pydantic model, the template handles the rest.

```python
class FeatureData(BaseModel):
    id: str
    title: str
    status: str = "todo"
    priority: str = "medium"
    steps: list[Step] = []
    edges: dict[str, list[Edge]] = {}

def save(self):
    html = jinja_env.get_template("feature.html.j2").render(**self.dict())
    Path(f"features/{self.id}.html").write_text(html)
```

**Deterministic because the template controls the structure.** Same data always produces identical HTML.

---

### Option 2: Python String Builder (Zero Dependencies)

```python
class HtmlSerializer:
    """Produces valid HtmlGraph HTML from structured data."""
    
    def serialize_feature(self, data: FeatureData) -> str:
        steps_html = "\n".join(
            f'<li data-completed="{s.completed}">{s.text}</li>'
            for s in data.steps
        )
        edges_html = self._serialize_edges(data.edges)
        
        return f"""<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>{data.title}</title></head>
<body>
  <article id="{data.id}" data-type="feature" 
           data-status="{data.status}" data-priority="{data.priority}">
    <header><h1>{data.title}</h1></header>
    <nav data-graph-edges>{edges_html}</nav>
    <section data-steps><ol>{steps_html}</ol></section>
  </article>
</body>
</html>"""
```

Even simpler. No template engine, no parser, no browser.

---

### Option 3: `xml.etree.ElementTree` (Standard Library)

```python
from xml.etree.ElementTree import Element, SubElement, tostring

def build_feature(data: FeatureData) -> str:
    article = Element("article", id=data.id, 
                      **{"data-type": "feature", "data-status": data.status})
    header = SubElement(article, "header")
    h1 = SubElement(header, "h1")
    h1.text = data.title
    # ... build the rest programmatically
    return tostring(article, encoding="unicode")
```

This gives you **programmatic DOM construction** without a browser. But honestly, it's more complex than templates for less benefit.

---

## The Architecture Shift

```
BEFORE:
  justhtml does everything (parse + build + query)
  ↓
  Heavy Python dependency doing what the browser already does

AFTER:
  Write path: Pydantic model → Jinja2 template → HTML file (simple, deterministic)
  Read path:  Browser opens HTML → native DOM → CSS queries (powerful, free)
  Agent path: CLI still works for headless agents (thin wrapper)
```

---

## Where Playwright Actually Makes Sense

Not for creation — but for **automated graph queries from Python**:

```python
# If an agent WITHOUT a browser needs to query the graph
from playwright.async_api import async_playwright

async def query_graph(selector: str):
    browser = await playwright.chromium.launch()
    page = await browser.new_page()
    await page.goto(f"file:///project/index.html")
    results = await page.query_selector_all(selector)
    # Now you have the browser's full CSS selector engine
```

But even this is only needed if agents must query the graph **outside** a browser context. For the dashboard, the browser handles it directly.

---

## Bottom Line

**Drop justhtml from the write path.** Replace it with Jinja2 templates (which you already depend on) plus Pydantic validation. The agent produces data, the template produces HTML, the browser consumes it.

The determinism comes from **controlling the template**, not from controlling a parser. Same inputs → same template → identical HTML output every time.