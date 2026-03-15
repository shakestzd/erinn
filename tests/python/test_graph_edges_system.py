"""
Tests for the Graph Edge System (Phase 2 of Work Item Attribution Graph).

Covers:
- Database edge CRUD (insert, get, delete)
- RelationshipType enum
- Node convenience methods (relates_to, spawned_from, caused_by, implements)
- Builder convenience methods
- Collection edge methods with dual-write
- HTML <-> SQLite edge sync
- Backward compatibility with existing edge types
"""

import tempfile
from pathlib import Path

import pytest
from htmlgraph.db.schema import HtmlGraphDB
from htmlgraph.graph import HtmlGraph
from htmlgraph.models import Edge, Node, RelationshipType

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture
def db():
    """Create an in-memory HtmlGraphDB for testing."""
    with tempfile.TemporaryDirectory() as tmpdir:
        db_path = str(Path(tmpdir) / "test.db")
        database = HtmlGraphDB(db_path)
        yield database
        database.close()


@pytest.fixture
def graph():
    """Create a temporary HtmlGraph with sample nodes."""
    with tempfile.TemporaryDirectory() as tmpdir:
        g = HtmlGraph(tmpdir, auto_load=False)

        # Add some nodes
        node_a = Node(id="feat-aaaa0001", title="Feature A", type="feature")
        node_b = Node(id="feat-bbbb0002", title="Feature B", type="feature")
        node_c = Node(id="bug-cccc0003", title="Bug C", type="bug")
        node_d = Node(id="spk-dddd0004", title="Spike D", type="spike")

        g.add(node_a)
        g.add(node_b)
        g.add(node_c)
        g.add(node_d)

        yield g


# ---------------------------------------------------------------------------
# 2.1 Database Edge Methods
# ---------------------------------------------------------------------------


class TestDatabaseEdgeMethods:
    """Tests for insert_graph_edge, get_graph_edges, delete_graph_edge."""

    def test_insert_graph_edge(self, db: HtmlGraphDB):
        """DB insert works and returns an edge_id."""
        edge_id = db.insert_graph_edge(
            from_node_id="feat-001",
            from_node_type="feature",
            to_node_id="feat-002",
            to_node_type="feature",
            relationship_type="blocks",
        )
        assert edge_id is not None
        assert edge_id.startswith("evt-")

    def test_insert_graph_edge_with_metadata(self, db: HtmlGraphDB):
        """DB insert with metadata works."""
        edge_id = db.insert_graph_edge(
            from_node_id="feat-001",
            from_node_type="feature",
            to_node_id="bug-001",
            to_node_type="bug",
            relationship_type="caused_by",
            weight=0.8,
            metadata={"reason": "regression from feature"},
        )
        assert edge_id is not None

    def test_get_graph_edges_outgoing(self, db: HtmlGraphDB):
        """Query outgoing edges."""
        db.insert_graph_edge(
            from_node_id="feat-001",
            from_node_type="feature",
            to_node_id="feat-002",
            to_node_type="feature",
            relationship_type="blocks",
        )
        db.insert_graph_edge(
            from_node_id="feat-001",
            from_node_type="feature",
            to_node_id="feat-003",
            to_node_type="feature",
            relationship_type="relates_to",
        )

        outgoing = db.get_graph_edges("feat-001", direction="outgoing")
        assert len(outgoing) == 2
        target_ids = {e["to_node_id"] for e in outgoing}
        assert target_ids == {"feat-002", "feat-003"}

    def test_get_graph_edges_incoming(self, db: HtmlGraphDB):
        """Query incoming edges."""
        db.insert_graph_edge(
            from_node_id="feat-001",
            from_node_type="feature",
            to_node_id="feat-002",
            to_node_type="feature",
            relationship_type="blocks",
        )

        incoming = db.get_graph_edges("feat-002", direction="incoming")
        assert len(incoming) == 1
        assert incoming[0]["from_node_id"] == "feat-001"

    def test_get_graph_edges_both(self, db: HtmlGraphDB):
        """Query both directions."""
        db.insert_graph_edge(
            from_node_id="feat-001",
            from_node_type="feature",
            to_node_id="feat-002",
            to_node_type="feature",
            relationship_type="blocks",
        )
        db.insert_graph_edge(
            from_node_id="feat-003",
            from_node_type="feature",
            to_node_id="feat-001",
            to_node_type="feature",
            relationship_type="relates_to",
        )

        both = db.get_graph_edges("feat-001", direction="both")
        assert len(both) == 2

    def test_get_graph_edges_with_relationship_filter(self, db: HtmlGraphDB):
        """Query edges filtered by relationship type."""
        db.insert_graph_edge(
            from_node_id="feat-001",
            from_node_type="feature",
            to_node_id="feat-002",
            to_node_type="feature",
            relationship_type="blocks",
        )
        db.insert_graph_edge(
            from_node_id="feat-001",
            from_node_type="feature",
            to_node_id="feat-003",
            to_node_type="feature",
            relationship_type="relates_to",
        )

        blocks_only = db.get_graph_edges(
            "feat-001", direction="outgoing", relationship_type="blocks"
        )
        assert len(blocks_only) == 1
        assert blocks_only[0]["to_node_id"] == "feat-002"

    def test_delete_graph_edge(self, db: HtmlGraphDB):
        """Deletion works and returns True."""
        edge_id = db.insert_graph_edge(
            from_node_id="feat-001",
            from_node_type="feature",
            to_node_id="feat-002",
            to_node_type="feature",
            relationship_type="blocks",
        )
        assert edge_id is not None

        result = db.delete_graph_edge(edge_id)
        assert result is True

        # Verify it's gone
        edges = db.get_graph_edges("feat-001", direction="outgoing")
        assert len(edges) == 0

    def test_delete_nonexistent_edge(self, db: HtmlGraphDB):
        """Deleting a nonexistent edge returns False."""
        result = db.delete_graph_edge("evt-nonexistent")
        assert result is False

    def test_get_graph_edges_empty(self, db: HtmlGraphDB):
        """Querying edges on a node with no edges returns empty list."""
        edges = db.get_graph_edges("feat-no-edges", direction="both")
        assert edges == []

    def test_deduplication_in_both_direction(self, db: HtmlGraphDB):
        """An edge from A->B should appear once in 'both' query for A."""
        db.insert_graph_edge(
            from_node_id="feat-001",
            from_node_type="feature",
            to_node_id="feat-001",
            to_node_type="feature",
            relationship_type="relates_to",
        )
        edges = db.get_graph_edges("feat-001", direction="both")
        # Self-referential edge: appears in outgoing AND incoming, but should be deduplicated
        assert len(edges) == 1


# ---------------------------------------------------------------------------
# 2.2 RelationshipType Enum
# ---------------------------------------------------------------------------


class TestRelationshipTypeEnum:
    """Tests for RelationshipType enum values and JSON serialization."""

    def test_enum_values(self):
        """Enum values match expected strings."""
        assert RelationshipType.BLOCKS == "blocks"
        assert RelationshipType.BLOCKED_BY == "blocked_by"
        assert RelationshipType.RELATES_TO == "relates_to"
        assert RelationshipType.IMPLEMENTS == "implements"
        assert RelationshipType.CAUSED_BY == "caused_by"
        assert RelationshipType.SPAWNED_FROM == "spawned_from"
        assert RelationshipType.IMPLEMENTED_IN == "implemented-in"

    def test_enum_is_str(self):
        """Enum members are strings (for JSON serialization)."""
        assert isinstance(RelationshipType.BLOCKS, str)
        assert isinstance(RelationshipType.RELATES_TO, str)

    def test_enum_comparison_with_string(self):
        """Enum values compare equal to their string values."""
        assert RelationshipType.BLOCKS == "blocks"
        assert RelationshipType.RELATES_TO == "relates_to"

    def test_all_relationship_types_defined(self):
        """All expected relationship types are defined."""
        expected = {
            "blocks",
            "blocked_by",
            "relates_to",
            "implements",
            "caused_by",
            "spawned_from",
            "implemented-in",
        }
        actual = {rt.value for rt in RelationshipType}
        assert actual == expected


# ---------------------------------------------------------------------------
# 2.3 Node Convenience Methods
# ---------------------------------------------------------------------------


class TestNodeConvenienceMethods:
    """Tests for Node.relates_to, spawned_from, caused_by, implements."""

    def test_node_relates_to(self):
        """Node.relates_to() creates a relates_to edge."""
        node = Node(id="feat-001", title="Test Feature")
        node.relates_to("feat-002", title="Related feature")

        edges = node.edges.get("relates_to", [])
        assert len(edges) == 1
        assert edges[0].target_id == "feat-002"
        assert edges[0].relationship == "relates_to"
        assert edges[0].title == "Related feature"

    def test_node_spawned_from(self):
        """Node.spawned_from() creates a spawned_from edge."""
        node = Node(id="feat-001", title="Test Feature")
        node.spawned_from("spk-001", title="Investigation spike")

        edges = node.edges.get("spawned_from", [])
        assert len(edges) == 1
        assert edges[0].target_id == "spk-001"
        assert edges[0].relationship == "spawned_from"

    def test_node_caused_by(self):
        """Node.caused_by() creates a caused_by edge."""
        node = Node(id="bug-001", title="Test Bug")
        node.caused_by("feat-001", title="Caused by feature change")

        edges = node.edges.get("caused_by", [])
        assert len(edges) == 1
        assert edges[0].target_id == "feat-001"

    def test_node_implements(self):
        """Node.implements() creates an implements edge."""
        node = Node(id="feat-001", title="Auth Feature")
        node.implements("spec-001", title="Auth spec requirement")

        edges = node.edges.get("implements", [])
        assert len(edges) == 1
        assert edges[0].target_id == "spec-001"
        assert edges[0].relationship == "implements"

    def test_node_multiple_edges(self):
        """Multiple edge methods can be chained."""
        node = Node(id="feat-001", title="Test Feature")
        node.relates_to("feat-002")
        node.spawned_from("spk-001")
        node.caused_by("bug-001")

        assert len(node.edges) == 3
        assert "relates_to" in node.edges
        assert "spawned_from" in node.edges
        assert "caused_by" in node.edges

    def test_node_edge_updates_timestamp(self):
        """Adding an edge updates the node's updated timestamp."""
        node = Node(id="feat-001", title="Test Feature")

        # add_edge calls utc_now() which is timezone-aware,
        # so just verify it gets set to a timezone-aware value
        node.relates_to("feat-002")
        assert node.updated is not None
        assert node.updated.tzinfo is not None  # utc_now() sets timezone


# ---------------------------------------------------------------------------
# 2.4 Builder Convenience Methods
# ---------------------------------------------------------------------------


class TestBuilderConvenienceMethods:
    """Tests for builder .relates_to(), .spawned_from(), etc."""

    @pytest.fixture
    def sdk_with_track(self):
        """Create an SDK with a track for feature creation."""
        with tempfile.TemporaryDirectory() as tmpdir:
            # Create .htmlgraph directory structure
            hg_dir = Path(tmpdir) / ".htmlgraph"
            hg_dir.mkdir()
            (hg_dir / "features").mkdir()
            (hg_dir / "bugs").mkdir()
            (hg_dir / "spikes").mkdir()
            (hg_dir / "tracks").mkdir()

            from htmlgraph.sdk import SDK

            sdk = SDK(agent="test-agent", directory=str(hg_dir))

            # Create a track so features can be saved
            track = sdk.tracks.create("Test Track").save()

            yield sdk, track

    def test_builder_relates_to(self, sdk_with_track):
        """Builder .relates_to() adds edge to built node."""
        sdk, track = sdk_with_track
        feature = (
            sdk.features.create("Test Feature")
            .set_track(track.id)
            .relates_to("feat-other")
            .save()
        )

        edges = feature.edges.get("relates_to", [])
        assert len(edges) == 1
        assert edges[0].target_id == "feat-other"

    def test_builder_spawned_from(self, sdk_with_track):
        """Builder .spawned_from() adds edge to built node."""
        sdk, track = sdk_with_track
        feature = (
            sdk.features.create("Test Feature")
            .set_track(track.id)
            .spawned_from("spk-origin")
            .save()
        )

        edges = feature.edges.get("spawned_from", [])
        assert len(edges) == 1
        assert edges[0].target_id == "spk-origin"

    def test_builder_caused_by(self, sdk_with_track):
        """Builder .caused_by() adds edge to built node."""
        sdk, track = sdk_with_track
        bug = sdk.bugs.create("Test Bug").caused_by("feat-regression").save()

        edges = bug.edges.get("caused_by", [])
        assert len(edges) == 1
        assert edges[0].target_id == "feat-regression"

    def test_builder_implements(self, sdk_with_track):
        """Builder .implements() adds edge to built node."""
        sdk, track = sdk_with_track
        feature = (
            sdk.features.create("Test Feature")
            .set_track(track.id)
            .implements("spec-requirement")
            .save()
        )

        edges = feature.edges.get("implements", [])
        assert len(edges) == 1
        assert edges[0].target_id == "spec-requirement"

    def test_builder_chaining(self, sdk_with_track):
        """Multiple edge methods can be chained on builder."""
        sdk, track = sdk_with_track
        feature = (
            sdk.features.create("Multi-edge Feature")
            .set_track(track.id)
            .relates_to("feat-related")
            .spawned_from("spk-origin")
            .blocks("feat-downstream")
            .save()
        )

        assert "relates_to" in feature.edges
        assert "spawned_from" in feature.edges
        assert "blocks" in feature.edges


# ---------------------------------------------------------------------------
# 2.5 Collection Edge Methods with Dual-Write
# ---------------------------------------------------------------------------


class TestCollectionEdgeMethods:
    """Tests for collection add_edge, related_to, query_blocked_by, etc."""

    @pytest.fixture
    def sdk_with_nodes(self):
        """Create an SDK with pre-populated nodes."""
        with tempfile.TemporaryDirectory() as tmpdir:
            hg_dir = Path(tmpdir) / ".htmlgraph"
            hg_dir.mkdir()
            (hg_dir / "features").mkdir()
            (hg_dir / "bugs").mkdir()
            (hg_dir / "spikes").mkdir()
            (hg_dir / "tracks").mkdir()

            from htmlgraph.sdk import SDK

            sdk = SDK(agent="test-agent", directory=str(hg_dir))

            # Create a track
            track = sdk.tracks.create("Test Track").save()

            # Create features
            feat_a = sdk.features.create("Feature A").set_track(track.id).save()
            feat_b = sdk.features.create("Feature B").set_track(track.id).save()

            yield sdk, feat_a, feat_b

    def test_collection_add_edge_dual_write(self, sdk_with_nodes):
        """add_edge writes to both HTML and SQLite."""
        sdk, feat_a, feat_b = sdk_with_nodes

        edge_id = sdk.features.add_edge(
            feat_a.id, feat_b.id, "relates_to", title="Related"
        )

        # Verify SQLite write
        assert edge_id is not None

        # Verify HTML write - reload the node
        node = sdk.features.get(feat_a.id)
        assert node is not None
        relates_to_edges = node.edges.get("relates_to", [])
        assert len(relates_to_edges) == 1
        assert relates_to_edges[0].target_id == feat_b.id

    def test_collection_related_to_query(self, sdk_with_nodes):
        """related_to() returns related nodes."""
        sdk, feat_a, feat_b = sdk_with_nodes

        sdk.features.add_edge(feat_a.id, feat_b.id, "relates_to")

        related = sdk.features.related_to(feat_a.id)
        related_ids = {n.id for n in related}
        assert feat_b.id in related_ids

    def test_collection_blocked_by_query(self, sdk_with_nodes):
        """query_blocked_by() returns blocking nodes."""
        sdk, feat_a, feat_b = sdk_with_nodes

        sdk.features.add_edge(feat_a.id, feat_b.id, "blocked_by")

        blockers = sdk.features.query_blocked_by(feat_a.id)
        blocker_ids = {n.id for n in blockers}
        assert feat_b.id in blocker_ids

    def test_collection_blocks_query(self, sdk_with_nodes):
        """query_blocks() returns blocked nodes."""
        sdk, feat_a, feat_b = sdk_with_nodes

        sdk.features.add_edge(feat_a.id, feat_b.id, "blocks")

        blocked = sdk.features.query_blocks(feat_a.id)
        blocked_ids = {n.id for n in blocked}
        assert feat_b.id in blocked_ids

    def test_collection_edges_of(self, sdk_with_nodes):
        """edges_of() returns all edges for a node."""
        sdk, feat_a, feat_b = sdk_with_nodes

        sdk.features.add_edge(feat_a.id, feat_b.id, "relates_to")
        sdk.features.add_edge(feat_a.id, feat_b.id, "blocks")

        all_edges = sdk.features.edges_of(feat_a.id)
        assert len(all_edges) >= 2

        relationships = {e["relationship"] for e in all_edges}
        assert "relates_to" in relationships
        assert "blocks" in relationships

    def test_collection_edges_of_with_filter(self, sdk_with_nodes):
        """edges_of() with relationship filter."""
        sdk, feat_a, feat_b = sdk_with_nodes

        sdk.features.add_edge(feat_a.id, feat_b.id, "relates_to")
        sdk.features.add_edge(feat_a.id, feat_b.id, "blocks")

        filtered = sdk.features.edges_of(feat_a.id, relationship="blocks")
        assert all(e["relationship"] == "blocks" for e in filtered)

    def test_infer_node_type(self):
        """_infer_node_type correctly maps prefixes to types."""
        from htmlgraph.collections.base import BaseCollection

        assert BaseCollection._infer_node_type("feat-abc123") == "feature"
        assert BaseCollection._infer_node_type("bug-abc123") == "bug"
        assert BaseCollection._infer_node_type("spk-abc123") == "spike"
        assert BaseCollection._infer_node_type("trk-abc123") == "track"


# ---------------------------------------------------------------------------
# 2.6 HTML <-> SQLite Edge Sync
# ---------------------------------------------------------------------------


class TestEdgeSync:
    """Tests for sync_html_edges_to_sqlite and sync_sqlite_edge_to_html."""

    def test_edge_sync_html_to_sqlite(self):
        """sync_html_edges_to_sqlite reads HTML edges and inserts them."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir)
            features_dir = graph_dir / "features"
            features_dir.mkdir()

            # Create an HTML file with edges
            node = Node(
                id="feat-sync001",
                title="Sync Test Feature",
                type="feature",
                edges={
                    "blocks": [Edge(target_id="feat-sync002", relationship="blocks")],
                    "relates_to": [
                        Edge(target_id="feat-sync003", relationship="relates_to")
                    ],
                },
            )
            html_path = features_dir / "feat-sync001.html"
            html_path.write_text(node.to_html())

            # Create DB
            db_path = str(graph_dir / "test.db")
            db = HtmlGraphDB(db_path)

            from htmlgraph.db.edge_sync import sync_html_edges_to_sqlite

            synced = sync_html_edges_to_sqlite(graph_dir, db)
            assert synced == 2

            # Verify edges in DB
            edges = db.get_graph_edges("feat-sync001", direction="outgoing")
            assert len(edges) == 2

            target_ids = {e["to_node_id"] for e in edges}
            assert "feat-sync002" in target_ids
            assert "feat-sync003" in target_ids

            db.close()

    def test_edge_sync_idempotent(self):
        """Running sync_html_edges_to_sqlite twice doesn't create duplicates."""
        with tempfile.TemporaryDirectory() as tmpdir:
            graph_dir = Path(tmpdir)
            features_dir = graph_dir / "features"
            features_dir.mkdir()

            node = Node(
                id="feat-idem001",
                title="Idempotent Test",
                type="feature",
                edges={
                    "blocks": [Edge(target_id="feat-idem002", relationship="blocks")],
                },
            )
            html_path = features_dir / "feat-idem001.html"
            html_path.write_text(node.to_html())

            db_path = str(graph_dir / "test.db")
            db = HtmlGraphDB(db_path)

            from htmlgraph.db.edge_sync import sync_html_edges_to_sqlite

            first_run = sync_html_edges_to_sqlite(graph_dir, db)
            assert first_run == 1

            second_run = sync_html_edges_to_sqlite(graph_dir, db)
            assert second_run == 0  # No new edges

            edges = db.get_graph_edges("feat-idem001", direction="outgoing")
            assert len(edges) == 1

            db.close()

    def test_sync_sqlite_edge_to_html(self):
        """sync_sqlite_edge_to_html adds edge to HTML file."""
        with tempfile.TemporaryDirectory() as tmpdir:
            g = HtmlGraph(tmpdir, auto_load=False)

            node = Node(id="feat-html001", title="HTML Sync Test", type="feature")
            g.add(node)

            db_path = str(Path(tmpdir) / "test.db")
            db = HtmlGraphDB(db_path)

            from htmlgraph.db.edge_sync import sync_sqlite_edge_to_html

            result = sync_sqlite_edge_to_html(
                db, g, "feat-html001", "feat-html002", "relates_to", "Related"
            )
            assert result is True

            # Verify the edge is now in the node
            updated_node = g.get("feat-html001")
            assert updated_node is not None
            relates_edges = updated_node.edges.get("relates_to", [])
            assert len(relates_edges) == 1
            assert relates_edges[0].target_id == "feat-html002"

            db.close()

    def test_sync_sqlite_edge_to_html_no_duplicate(self):
        """sync_sqlite_edge_to_html doesn't add duplicate edges."""
        with tempfile.TemporaryDirectory() as tmpdir:
            g = HtmlGraph(tmpdir, auto_load=False)

            node = Node(
                id="feat-dedup01",
                title="Dedup Test",
                type="feature",
                edges={
                    "relates_to": [
                        Edge(target_id="feat-dedup02", relationship="relates_to")
                    ]
                },
            )
            g.add(node)

            db_path = str(Path(tmpdir) / "test.db")
            db = HtmlGraphDB(db_path)

            from htmlgraph.db.edge_sync import sync_sqlite_edge_to_html

            result = sync_sqlite_edge_to_html(
                db, g, "feat-dedup01", "feat-dedup02", "relates_to"
            )
            assert result is False  # Already exists

            db.close()

    def test_sync_sqlite_edge_to_html_missing_node(self):
        """sync_sqlite_edge_to_html returns False for missing node."""
        with tempfile.TemporaryDirectory() as tmpdir:
            g = HtmlGraph(tmpdir, auto_load=False)

            db_path = str(Path(tmpdir) / "test.db")
            db = HtmlGraphDB(db_path)

            from htmlgraph.db.edge_sync import sync_sqlite_edge_to_html

            result = sync_sqlite_edge_to_html(
                db, g, "feat-missing", "feat-other", "relates_to"
            )
            assert result is False

            db.close()


# ---------------------------------------------------------------------------
# Backward Compatibility
# ---------------------------------------------------------------------------


class TestBackwardCompatibility:
    """Tests that existing edge patterns still work."""

    def test_backward_compat_existing_edges(self):
        """Existing 'implemented-in' edges still work as strings."""
        node = Node(
            id="feat-compat01",
            title="Compat Test",
            type="feature",
            edges={
                "implemented-in": [
                    Edge(
                        target_id="feat-impl01",
                        relationship="implemented-in",
                        title="Implementation",
                    )
                ]
            },
        )

        edges = node.get_edges_by_type("implemented-in")
        assert len(edges) == 1
        assert edges[0].target_id == "feat-impl01"
        assert edges[0].relationship == "implemented-in"

    def test_backward_compat_blocks_blocked_by(self):
        """Existing blocks/blocked_by pattern still works."""
        node = Node(
            id="feat-blocks01",
            title="Blocks Test",
            type="feature",
            edges={
                "blocks": [Edge(target_id="feat-002", relationship="blocks")],
                "blocked_by": [Edge(target_id="feat-003", relationship="blocked_by")],
            },
        )

        blocking = node.blocking_edges
        assert len(blocking) == 2

    def test_enum_matches_existing_string_values(self):
        """RelationshipType enum values match strings used in existing HTML files."""
        assert RelationshipType.IMPLEMENTED_IN.value == "implemented-in"
        assert RelationshipType.BLOCKS.value == "blocks"
        assert RelationshipType.BLOCKED_BY.value == "blocked_by"

    def test_edge_html_serialization_with_new_types(self):
        """New relationship types serialize correctly to HTML."""
        node = Node(id="feat-serial01", title="Serialization Test", type="feature")
        node.relates_to("feat-serial02", title="Related")
        node.spawned_from("spk-serial01", title="Origin")

        html = node.to_html()
        assert 'data-edge-type="relates_to"' in html
        assert 'data-edge-type="spawned_from"' in html
        assert 'data-relationship="relates_to"' in html
        assert 'data-relationship="spawned_from"' in html

    def test_edge_html_roundtrip(self):
        """Edges survive HTML serialization and re-parsing."""
        from htmlgraph.parser import HtmlParser

        node = Node(id="feat-round01", title="Roundtrip Test", type="feature")
        node.relates_to("feat-round02", title="Related Feature")
        node.caused_by("bug-round01", title="Root Cause")

        html = node.to_html()
        parser = HtmlParser.from_string(html)
        parsed_edges = parser.get_edges()

        assert "relates_to" in parsed_edges
        assert len(parsed_edges["relates_to"]) == 1
        assert parsed_edges["relates_to"][0]["target_id"] == "feat-round02"

        assert "caused_by" in parsed_edges
        assert len(parsed_edges["caused_by"]) == 1
        assert parsed_edges["caused_by"][0]["target_id"] == "bug-round01"
