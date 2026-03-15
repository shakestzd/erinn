"""
Tests for src/python/htmlgraph/analytics/critical_path.py

Covers:
- find_critical_path: simple chain, diamond, no edges, cycle detection
- find_bottlenecks: ranking by transitive dependents
- get_dependency_graph: node/edge structure
"""

from __future__ import annotations

import sqlite3
import uuid
from pathlib import Path

from htmlgraph.analytics.critical_path import (
    find_bottlenecks,
    find_critical_path,
    get_dependency_graph,
)

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_db(tmp_path: Path) -> Path:
    """Create a minimal SQLite DB with features + graph_edges tables."""
    db_path = tmp_path / "test.db"
    conn = sqlite3.connect(str(db_path))
    conn.executescript("""
        CREATE TABLE IF NOT EXISTS features (
            id TEXT PRIMARY KEY,
            type TEXT NOT NULL DEFAULT 'feature',
            title TEXT NOT NULL,
            status TEXT NOT NULL DEFAULT 'todo',
            track_id TEXT
        );

        CREATE TABLE IF NOT EXISTS graph_edges (
            edge_id TEXT PRIMARY KEY,
            from_node_id TEXT NOT NULL,
            from_node_type TEXT NOT NULL DEFAULT 'feature',
            to_node_id TEXT NOT NULL,
            to_node_type TEXT NOT NULL DEFAULT 'feature',
            relationship_type TEXT NOT NULL,
            weight REAL DEFAULT 1.0,
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
            metadata TEXT
        );
    """)
    conn.commit()
    conn.close()
    return db_path


def _add_feature(
    db_path: Path,
    feature_id: str,
    title: str,
    track_id: str | None = None,
    status: str = "todo",
    ftype: str = "feature",
) -> None:
    conn = sqlite3.connect(str(db_path))
    conn.execute(
        "INSERT INTO features (id, type, title, status, track_id) VALUES (?, ?, ?, ?, ?)",
        (feature_id, ftype, title, status, track_id),
    )
    conn.commit()
    conn.close()


def _add_edge(
    db_path: Path,
    from_id: str,
    to_id: str,
    rel: str = "blocks",
) -> None:
    edge_id = f"edge-{uuid.uuid4().hex[:8]}"
    conn = sqlite3.connect(str(db_path))
    conn.execute(
        """INSERT INTO graph_edges
           (edge_id, from_node_id, from_node_type, to_node_id, to_node_type, relationship_type)
           VALUES (?, ?, 'feature', ?, 'feature', ?)""",
        (edge_id, from_id, to_id, rel),
    )
    conn.commit()
    conn.close()


# ---------------------------------------------------------------------------
# find_critical_path tests
# ---------------------------------------------------------------------------


class TestFindCriticalPath:
    def test_find_critical_path_simple_chain(self, tmp_path: Path) -> None:
        """A -> B -> C: critical path must contain all three in order."""
        db = _make_db(tmp_path)
        _add_feature(db, "A", "Feature A")
        _add_feature(db, "B", "Feature B")
        _add_feature(db, "C", "Feature C")
        _add_edge(db, "A", "B", "blocks")
        _add_edge(db, "B", "C", "blocks")

        path = find_critical_path(db_path=str(db))

        assert path == ["A", "B", "C"], f"Expected [A,B,C], got {path}"

    def test_find_critical_path_diamond(self, tmp_path: Path) -> None:
        """
        Diamond: A -> B, A -> C, B -> D, C -> D.
        Critical path is A -> B -> D or A -> C -> D (length 3).
        The returned path must start at A and end at D with length 3.
        """
        db = _make_db(tmp_path)
        for fid in ["A", "B", "C", "D"]:
            _add_feature(db, fid, f"Feature {fid}")
        _add_edge(db, "A", "B", "blocks")
        _add_edge(db, "A", "C", "blocks")
        _add_edge(db, "B", "D", "blocks")
        _add_edge(db, "C", "D", "blocks")

        path = find_critical_path(db_path=str(db))

        assert len(path) == 3, f"Diamond critical path should be length 3, got {path}"
        assert path[0] == "A", f"Path should start at A, got {path}"
        assert path[-1] == "D", f"Path should end at D, got {path}"

    def test_find_critical_path_no_edges(self, tmp_path: Path) -> None:
        """No dependency edges → no critical path (returns empty list)."""
        db = _make_db(tmp_path)
        _add_feature(db, "X", "Feature X")
        _add_feature(db, "Y", "Feature Y")

        path = find_critical_path(db_path=str(db))

        # No edges means no dependency chain
        assert path == [], f"Expected [] for no edges, got {path}"

    def test_find_critical_path_no_features(self, tmp_path: Path) -> None:
        """Empty features table → empty critical path."""
        db = _make_db(tmp_path)

        path = find_critical_path(db_path=str(db))

        assert path == []

    def test_find_critical_path_track_scoped(self, tmp_path: Path) -> None:
        """track_id filter restricts analysis to features in that track."""
        db = _make_db(tmp_path)
        _add_feature(db, "T1", "Track Feature 1", track_id="trk-001")
        _add_feature(db, "T2", "Track Feature 2", track_id="trk-001")
        _add_feature(db, "Other", "Other Feature", track_id="trk-999")
        _add_edge(db, "T1", "T2", "blocks")
        # Cross-track edge (should be ignored because Other is not in trk-001)
        _add_edge(db, "Other", "T1", "blocks")

        path = find_critical_path(track_id="trk-001", db_path=str(db))

        assert set(path) <= {"T1", "T2"}, (
            f"Path should only contain trk-001 features: {path}"
        )
        assert path == ["T1", "T2"], f"Expected [T1,T2], got {path}"

    def test_find_critical_path_blocked_by_relationship(self, tmp_path: Path) -> None:
        """'blocked_by' edges are normalised: B blocked_by A means A -> B."""
        db = _make_db(tmp_path)
        _add_feature(db, "A", "Feature A")
        _add_feature(db, "B", "Feature B")
        _add_feature(db, "C", "Feature C")
        # B is blocked_by A => A -> B
        _add_edge(db, "B", "A", "blocked_by")
        # C is blocked_by B => B -> C
        _add_edge(db, "C", "B", "blocked_by")

        path = find_critical_path(db_path=str(db))

        assert path == ["A", "B", "C"], (
            f"blocked_by edges should produce A->B->C, got {path}"
        )

    def test_cycle_detection(self, tmp_path: Path) -> None:
        """A -> B -> A cycle must not crash; cyclic nodes are excluded."""
        db = _make_db(tmp_path)
        _add_feature(db, "A", "Feature A")
        _add_feature(db, "B", "Feature B")
        _add_feature(db, "C", "Feature C")
        _add_edge(db, "A", "B", "blocks")
        _add_edge(db, "B", "A", "blocks")  # cycle
        _add_edge(
            db, "C", "B", "blocks"
        )  # C -> B (B is cyclic, but edge still recorded)

        # Must not raise; may return [] or a path excluding cyclic nodes
        path = find_critical_path(db_path=str(db))

        assert isinstance(path, list), "Should return a list even with cycles"
        # A and B are cyclic — they must not appear in the path
        assert "A" not in path, "Cyclic node A should not be on critical path"
        assert "B" not in path, "Cyclic node B should not be on critical path"


# ---------------------------------------------------------------------------
# find_bottlenecks tests
# ---------------------------------------------------------------------------


class TestFindBottlenecks:
    def test_find_bottlenecks_ranking(self, tmp_path: Path) -> None:
        """Node blocking the most others should rank first."""
        db = _make_db(tmp_path)
        # A -> B, A -> C, A -> D  (A blocks 3)
        # B -> D                  (B blocks 1)
        for fid, title in [
            ("A", "Auth"),
            ("B", "Login"),
            ("C", "Signup"),
            ("D", "Dashboard"),
        ]:
            _add_feature(db, fid, title)
        _add_edge(db, "A", "B", "blocks")
        _add_edge(db, "A", "C", "blocks")
        _add_edge(db, "A", "D", "blocks")
        _add_edge(db, "B", "D", "blocks")

        bottlenecks = find_bottlenecks(db_path=str(db))

        assert len(bottlenecks) >= 1
        assert bottlenecks[0]["feature_id"] == "A", (
            f"A blocks the most; expected first, got {bottlenecks[0]['feature_id']}"
        )
        assert bottlenecks[0]["blocks_count"] == 3
        assert bottlenecks[0]["transitive_dependents"] == 3

    def test_find_bottlenecks_fields(self, tmp_path: Path) -> None:
        """Each bottleneck entry must contain required fields."""
        db = _make_db(tmp_path)
        _add_feature(db, "X", "Feature X")
        _add_feature(db, "Y", "Feature Y")
        _add_edge(db, "X", "Y", "blocks")

        bottlenecks = find_bottlenecks(db_path=str(db))

        assert len(bottlenecks) == 1
        entry = bottlenecks[0]
        assert "feature_id" in entry
        assert "title" in entry
        assert "blocks_count" in entry
        assert "transitive_dependents" in entry
        assert entry["feature_id"] == "X"
        assert entry["title"] == "Feature X"

    def test_find_bottlenecks_no_edges(self, tmp_path: Path) -> None:
        """No edges → no bottlenecks."""
        db = _make_db(tmp_path)
        _add_feature(db, "A", "Feature A")
        _add_feature(db, "B", "Feature B")

        bottlenecks = find_bottlenecks(db_path=str(db))

        assert bottlenecks == []

    def test_find_bottlenecks_transitive_count(self, tmp_path: Path) -> None:
        """Transitive dependents count follows chains: A->B->C means A has 2 transitive."""
        db = _make_db(tmp_path)
        for fid in ["A", "B", "C"]:
            _add_feature(db, fid, f"Feature {fid}")
        _add_edge(db, "A", "B", "blocks")
        _add_edge(db, "B", "C", "blocks")

        bottlenecks = find_bottlenecks(db_path=str(db))

        a_entry = next(b for b in bottlenecks if b["feature_id"] == "A")
        b_entry = next(b for b in bottlenecks if b["feature_id"] == "B")

        assert a_entry["transitive_dependents"] == 2, (
            f"A should have 2 transitive dependents (B and C), got {a_entry['transitive_dependents']}"
        )
        assert b_entry["transitive_dependents"] == 1, (
            f"B should have 1 transitive dependent (C), got {b_entry['transitive_dependents']}"
        )


# ---------------------------------------------------------------------------
# get_dependency_graph tests
# ---------------------------------------------------------------------------


class TestGetDependencyGraph:
    def test_get_dependency_graph_structure(self, tmp_path: Path) -> None:
        """Returns dict with 'nodes' and 'edges' keys."""
        db = _make_db(tmp_path)
        _add_feature(db, "A", "Feature A")
        _add_feature(db, "B", "Feature B")
        _add_edge(db, "A", "B", "blocks")

        graph = get_dependency_graph(db_path=str(db))

        assert "nodes" in graph
        assert "edges" in graph

    def test_get_dependency_graph_node_fields(self, tmp_path: Path) -> None:
        """Each node must have id, title, status, type fields."""
        db = _make_db(tmp_path)
        _add_feature(db, "A", "Feature A", status="in-progress", ftype="feature")

        graph = get_dependency_graph(db_path=str(db))

        assert len(graph["nodes"]) == 1
        node = graph["nodes"][0]
        assert node["id"] == "A"
        assert node["title"] == "Feature A"
        assert node["status"] == "in-progress"
        assert node["type"] == "feature"

    def test_get_dependency_graph_edge_fields(self, tmp_path: Path) -> None:
        """Each edge must have from, to, relationship fields."""
        db = _make_db(tmp_path)
        _add_feature(db, "A", "Feature A")
        _add_feature(db, "B", "Feature B")
        _add_edge(db, "A", "B", "blocks")

        graph = get_dependency_graph(db_path=str(db))

        assert len(graph["edges"]) == 1
        edge = graph["edges"][0]
        assert "from" in edge
        assert "to" in edge
        assert "relationship" in edge
        assert edge["from"] == "A"
        assert edge["to"] == "B"
        assert edge["relationship"] == "blocks"

    def test_get_dependency_graph_blocked_by_normalised(self, tmp_path: Path) -> None:
        """'blocked_by' edges are normalised to 'blocks' in output."""
        db = _make_db(tmp_path)
        _add_feature(db, "A", "Feature A")
        _add_feature(db, "B", "Feature B")
        # B is blocked_by A => in graph, edge should be from=A, to=B, rel=blocks
        _add_edge(db, "B", "A", "blocked_by")

        graph = get_dependency_graph(db_path=str(db))

        assert len(graph["edges"]) == 1
        edge = graph["edges"][0]
        assert edge["from"] == "A"
        assert edge["to"] == "B"
        assert edge["relationship"] == "blocks"

    def test_get_dependency_graph_empty(self, tmp_path: Path) -> None:
        """Empty DB returns empty nodes and edges."""
        db = _make_db(tmp_path)

        graph = get_dependency_graph(db_path=str(db))

        assert graph == {"nodes": [], "edges": []}

    def test_get_dependency_graph_track_scoped(self, tmp_path: Path) -> None:
        """track_id scopes nodes to a single track."""
        db = _make_db(tmp_path)
        _add_feature(db, "T1", "Track A Feature 1", track_id="trk-A")
        _add_feature(db, "T2", "Track A Feature 2", track_id="trk-A")
        _add_feature(db, "O1", "Other Track Feature", track_id="trk-B")
        _add_edge(db, "T1", "T2", "blocks")

        graph = get_dependency_graph(track_id="trk-A", db_path=str(db))

        node_ids = {n["id"] for n in graph["nodes"]}
        assert node_ids == {"T1", "T2"}, f"Expected only trk-A nodes, got {node_ids}"
        assert len(graph["edges"]) == 1
