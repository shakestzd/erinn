"""
Regression tests for bug-8aec5198: spikes.get_latest() returns empty despite files existing.

Root cause: justhtml>=1.0 enabled HTML sanitization by default, stripping the <article>
element (not on the default allowlist). All HtmlGraph node HTML files use <article id="...">
as the root element. Without safe=False, every collection returns empty.

Fix: pass safe=False to JustHTML() in HtmlParser.__init__.
"""

from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path

import pytest


@pytest.fixture
def graph_dir(tmp_path: Path) -> Path:
    """Minimal .htmlgraph dir with a spikes subdirectory."""
    d = tmp_path / ".htmlgraph"
    (d / "spikes").mkdir(parents=True)
    (d / "features").mkdir(parents=True)
    (d / "bugs").mkdir(parents=True)
    return d


def _write_spike(directory: Path, spike_id: str, title: str, created: str) -> Path:
    """Write a minimal spike HTML file matching the real HtmlGraph format."""
    html = f"""<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>{title}</title>
    <link rel="stylesheet" href="../styles.css">
</head>
<body>
    <article id="{spike_id}"
             data-type="spike"
             data-status="todo"
             data-priority="medium"
             data-created="{created}"
             data-updated="{created}"
             data-spike-type="general">

        <header>
            <h1>{title}</h1>
        </header>

        <section data-findings>
            <h3>Findings</h3>
            <div class="findings-content">Investigation results here.</div>
        </section>
    </article>
</body>
</html>
"""
    path = directory / f"{spike_id}.html"
    path.write_text(html, encoding="utf-8")
    return path


class TestHtmlParserArticleParsing:
    """Verify the justhtml safe=False fix works at the parser level."""

    def test_article_element_is_found(self) -> None:
        """HtmlParser must find article[id] — broken without safe=False."""
        from htmlgraph.parser import HtmlParser

        html = (
            '<html><body>'
            '<article id="spk-abc123" data-type="spike" data-status="todo">'
            '<h1>Test Spike</h1>'
            '</article>'
            '</body></html>'
        )
        parser = HtmlParser.from_string(html)
        article = parser.get_article()
        assert article is not None, (
            "get_article() returned None — justhtml is sanitizing <article>. "
            "Ensure HtmlParser passes safe=False to JustHTML()."
        )
        assert article.attrs.get("id") == "spk-abc123"

    def test_multiline_article_attributes_parsed(self) -> None:
        """Multi-line article tag (real file format) must parse all attributes."""
        from htmlgraph.parser import HtmlParser

        html = """<!DOCTYPE html>
<html lang="en">
<body>
    <article id="spk-001"
             data-type="spike"
             data-status="in-progress"
             data-priority="high"
             data-created="2026-01-15T10:00:00">
        <header><h1>Multi-line Test</h1></header>
    </article>
</body>
</html>"""
        parser = HtmlParser.from_string(html)
        node_id = parser.get_node_id()
        assert node_id == "spk-001", f"Expected 'spk-001', got {node_id!r}"

        meta = parser.get_node_metadata()
        assert meta.get("type") == "spike"
        assert meta.get("status") == "in-progress"

    def test_node_metadata_extracted(self) -> None:
        """parse_full_node() must return id and type from article attributes."""
        from htmlgraph.parser import HtmlParser

        html = (
            '<html><body>'
            '<article id="feat-xyz" data-type="feature" data-status="todo" '
            'data-priority="high" data-created="2026-03-01T09:00:00">'
            '<header><h1>Feature Title</h1></header>'
            '</article></body></html>'
        )
        data = HtmlParser.from_string(html).parse_full_node()
        assert data["id"] == "feat-xyz"
        assert data["type"] == "feature"
        assert data["title"] == "Feature Title"


class TestSpikeGetLatest:
    """Integration tests for SpikeCollection.get_latest()."""

    def test_get_latest_returns_spikes_when_files_exist(self, graph_dir: Path) -> None:
        """get_latest() must return spikes when HTML files are present — was returning []."""
        _write_spike(graph_dir / "spikes", "spk-aaa111", "Alpha Spike", "2026-01-10T10:00:00")
        _write_spike(graph_dir / "spikes", "spk-bbb222", "Beta Spike", "2026-01-15T12:00:00")
        _write_spike(graph_dir / "spikes", "spk-ccc333", "Gamma Spike", "2026-01-20T08:00:00")

        from htmlgraph import SDK

        sdk = SDK(agent="test-agent", directory=graph_dir)
        results = sdk.spikes.get_latest(limit=1)

        assert len(results) == 1, (
            f"Expected 1 spike, got {len(results)}. "
            "spikes.get_latest() returns empty — likely justhtml sanitizing <article>."
        )
        assert results[0].id == "spk-ccc333", (
            f"Expected newest spike 'spk-ccc333', got {results[0].id!r}"
        )

    def test_get_latest_respects_limit(self, graph_dir: Path) -> None:
        """get_latest(limit=2) returns at most 2 spikes, newest first."""
        _write_spike(graph_dir / "spikes", "spk-001", "First", "2026-01-01T00:00:00")
        _write_spike(graph_dir / "spikes", "spk-002", "Second", "2026-01-02T00:00:00")
        _write_spike(graph_dir / "spikes", "spk-003", "Third", "2026-01-03T00:00:00")

        from htmlgraph import SDK

        sdk = SDK(agent="test-agent", directory=graph_dir)
        results = sdk.spikes.get_latest(limit=2)

        assert len(results) == 2
        assert results[0].id == "spk-003"  # newest first
        assert results[1].id == "spk-002"

    def test_get_latest_default_limit_is_one(self, graph_dir: Path) -> None:
        """get_latest() with no arguments returns exactly 1 spike."""
        _write_spike(graph_dir / "spikes", "spk-x1", "Spike X1", "2026-02-01T00:00:00")
        _write_spike(graph_dir / "spikes", "spk-x2", "Spike X2", "2026-02-02T00:00:00")

        from htmlgraph import SDK

        sdk = SDK(agent="test-agent", directory=graph_dir)
        results = sdk.spikes.get_latest()

        assert len(results) == 1
        assert results[0].id == "spk-x2"

    def test_get_latest_empty_when_no_files(self, graph_dir: Path) -> None:
        """get_latest() returns [] when the spikes directory is empty."""
        from htmlgraph import SDK

        sdk = SDK(agent="test-agent", directory=graph_dir)
        results = sdk.spikes.get_latest()
        assert results == []

    def test_get_latest_filter_by_agent(self, graph_dir: Path) -> None:
        """get_latest(agent='explorer') returns only spikes assigned to that agent."""
        html_explorer = """<!DOCTYPE html>
<html><body>
    <article id="spk-exp1" data-type="spike" data-status="todo"
             data-priority="medium" data-created="2026-03-01T10:00:00"
             data-agent-assigned="explorer">
        <header><h1>Explorer Spike</h1></header>
    </article>
</body></html>"""
        html_coder = """<!DOCTYPE html>
<html><body>
    <article id="spk-cod1" data-type="spike" data-status="todo"
             data-priority="medium" data-created="2026-03-02T10:00:00"
             data-agent-assigned="coder">
        <header><h1>Coder Spike</h1></header>
    </article>
</body></html>"""
        (graph_dir / "spikes" / "spk-exp1.html").write_text(html_explorer)
        (graph_dir / "spikes" / "spk-cod1.html").write_text(html_coder)

        from htmlgraph import SDK

        sdk = SDK(agent="test-agent", directory=graph_dir)
        results = sdk.spikes.get_latest(agent="explorer", limit=5)

        assert len(results) == 1
        assert results[0].id == "spk-exp1"

    def test_spikes_all_returns_all_files(self, graph_dir: Path) -> None:
        """spikes.all() must return all spike nodes when HTML files exist."""
        for i in range(3):
            _write_spike(
                graph_dir / "spikes",
                f"spk-t{i:03d}",
                f"Test Spike {i}",
                f"2026-01-{i+1:02d}T00:00:00",
            )

        from htmlgraph import SDK

        sdk = SDK(agent="test-agent", directory=graph_dir)
        all_spikes = sdk.spikes.all()

        assert len(all_spikes) == 3, (
            f"Expected 3 spikes from all(), got {len(all_spikes)}. "
            "Check that HtmlParser uses safe=False with JustHTML."
        )
