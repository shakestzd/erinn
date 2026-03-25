"""Tests for SemanticQueryBuilder."""

from __future__ import annotations

from htmlgraph.sdk.query_builder import SemanticQuery, SemanticQueryBuilder


class MockSDK:
    """Minimal SDK mock that provides collection stubs."""

    pass


class TestQueryBuilderFluent:
    def test_build_simple_query(self) -> None:
        qb = SemanticQueryBuilder(MockSDK())
        query = qb.features().where_status("todo").high_priority().limit(5).build()
        assert len(query.filters) == 2
        assert query.limit == 5

    def test_chaining(self) -> None:
        qb = SemanticQueryBuilder(MockSDK())
        result = qb.bugs().active().sort_by("created").limit(10).offset(5)
        query = result.build()
        assert query.sort_by == "created"
        assert query.limit == 10
        assert query.offset == 5

    def test_created_after_filter(self) -> None:
        qb = SemanticQueryBuilder(MockSDK())
        query = qb.features().created_after("2026-03-01").build()
        assert any(f.field == "created" and f.operator == "gt" for f in query.filters)

    def test_with_tag_filter(self) -> None:
        qb = SemanticQueryBuilder(MockSDK())
        query = qb.features().with_tag("backend").build()
        assert any(
            f.field == "tags" and f.operator == "contains" for f in query.filters
        )

    def test_created_before_filter(self) -> None:
        qb = SemanticQueryBuilder(MockSDK())
        query = qb.features().created_before("2026-12-31").build()
        assert any(f.field == "created" and f.operator == "lt" for f in query.filters)

    def test_where_track_filter(self) -> None:
        qb = SemanticQueryBuilder(MockSDK())
        query = qb.features().where_track("trk-abc123").build()
        assert any(
            f.field == "track_id" and f.operator == "eq" and f.value == "trk-abc123"
            for f in query.filters
        )

    def test_where_generic_filter(self) -> None:
        qb = SemanticQueryBuilder(MockSDK())
        query = qb.features().where("agent_assigned", "eq", "claude").build()
        assert any(
            f.field == "agent_assigned" and f.operator == "eq" for f in query.filters
        )

    def test_status_shorthands(self) -> None:
        for method, expected_status in [
            ("active", "in-progress"),
            ("todo", "todo"),
            ("done", "done"),
        ]:
            qb = SemanticQueryBuilder(MockSDK())
            query = getattr(qb.features(), method)().build()
            assert any(
                f.field == "status" and f.value == expected_status
                for f in query.filters
            ), f"{method}() should produce status={expected_status!r}"

    def test_build_returns_semantic_query(self) -> None:
        qb = SemanticQueryBuilder(MockSDK())
        query = qb.features().build()
        assert isinstance(query, SemanticQuery)

    def test_build_defaults(self) -> None:
        qb = SemanticQueryBuilder(MockSDK())
        query = qb.build()
        assert query.filters == []
        assert query.sort_by is None
        assert query.sort_order == "desc"
        assert query.limit is None
        assert query.offset == 0
        assert query.include_related is False

    def test_with_related(self) -> None:
        qb = SemanticQueryBuilder(MockSDK())
        query = qb.features().with_related().build()
        assert query.include_related is True

    def test_sort_order_asc(self) -> None:
        qb = SemanticQueryBuilder(MockSDK())
        query = qb.features().sort_by("created", "asc").build()
        assert query.sort_by == "created"
        assert query.sort_order == "asc"

    def test_collection_selectors(self) -> None:
        for method in ["features", "bugs", "spikes", "all_items"]:
            qb = SemanticQueryBuilder(MockSDK())
            result = getattr(qb, method)()
            assert result is qb, f"{method}() should return self for chaining"


class TestQueryBuilderExecution:
    """Tests that exercise .execute() with a mock collection."""

    def _make_node(self, **kwargs: object) -> object:
        """Create a simple object with the given attributes."""

        class Node:
            pass

        n = Node()
        for k, v in kwargs.items():
            setattr(n, k, v)
        return n

    def _make_sdk(self, nodes: list[object]) -> object:
        """Build a mock SDK whose .features collection returns the given nodes."""

        class FakeCollection:
            def __init__(self, _nodes: list[object]) -> None:
                self._nodes = _nodes

            def where(self, **kwargs: object) -> list[object]:
                result = list(self._nodes)
                for key, val in kwargs.items():
                    result = [n for n in result if getattr(n, key, None) == val]
                return result

            def all(self) -> list[object]:
                return list(self._nodes)

        class FakeSDK:
            def __init__(self, nodes: list[object]) -> None:
                self.features = FakeCollection(nodes)
                self.bugs = FakeCollection([])
                self.spikes = FakeCollection([])

        return FakeSDK(nodes)

    def test_execute_no_filters_returns_all(self) -> None:
        nodes = [self._make_node(status="todo"), self._make_node(status="done")]
        sdk = self._make_sdk(nodes)
        results = SemanticQueryBuilder(sdk).features().execute()
        assert len(results) == 2

    def test_execute_eq_filter(self) -> None:
        nodes = [
            self._make_node(status="todo", priority="high"),
            self._make_node(status="done", priority="low"),
        ]
        sdk = self._make_sdk(nodes)
        results = SemanticQueryBuilder(sdk).features().where_status("todo").execute()
        assert len(results) == 1
        assert results[0].status == "todo"

    def test_execute_contains_filter(self) -> None:
        nodes = [
            self._make_node(tags=["backend", "api"]),
            self._make_node(tags=["frontend"]),
        ]
        sdk = self._make_sdk(nodes)
        results = SemanticQueryBuilder(sdk).features().with_tag("backend").execute()
        assert len(results) == 1
        assert "backend" in results[0].tags

    def test_execute_gt_filter(self) -> None:
        nodes = [
            self._make_node(created="2026-03-10"),
            self._make_node(created="2026-02-01"),
        ]
        sdk = self._make_sdk(nodes)
        results = (
            SemanticQueryBuilder(sdk).features().created_after("2026-03-01").execute()
        )
        assert len(results) == 1
        assert results[0].created == "2026-03-10"

    def test_execute_lt_filter(self) -> None:
        nodes = [
            self._make_node(created="2026-01-01"),
            self._make_node(created="2026-06-01"),
        ]
        sdk = self._make_sdk(nodes)
        results = (
            SemanticQueryBuilder(sdk).features().created_before("2026-03-01").execute()
        )
        assert len(results) == 1
        assert results[0].created == "2026-01-01"

    def test_execute_limit(self) -> None:
        nodes = [self._make_node(status="todo") for _ in range(5)]
        sdk = self._make_sdk(nodes)
        results = SemanticQueryBuilder(sdk).features().limit(3).execute()
        assert len(results) == 3

    def test_execute_offset(self) -> None:
        nodes = [self._make_node(idx=i) for i in range(5)]
        sdk = self._make_sdk(nodes)
        results = SemanticQueryBuilder(sdk).features().offset(2).execute()
        assert len(results) == 3
        assert results[0].idx == 2

    def test_execute_sort_desc(self) -> None:
        nodes = [self._make_node(created="2026-01-0" + str(i)) for i in range(1, 4)]
        sdk = self._make_sdk(nodes)
        results = (
            SemanticQueryBuilder(sdk).features().sort_by("created", "desc").execute()
        )
        assert results[0].created > results[-1].created

    def test_execute_sort_asc(self) -> None:
        nodes = [self._make_node(created="2026-01-0" + str(i)) for i in [3, 1, 2]]
        sdk = self._make_sdk(nodes)
        results = (
            SemanticQueryBuilder(sdk).features().sort_by("created", "asc").execute()
        )
        assert results[0].created < results[-1].created

    def test_count(self) -> None:
        nodes = [self._make_node(status="todo") for _ in range(4)]
        sdk = self._make_sdk(nodes)
        assert SemanticQueryBuilder(sdk).features().count() == 4

    def test_first_returns_single(self) -> None:
        nodes = [self._make_node(status="todo"), self._make_node(status="done")]
        sdk = self._make_sdk(nodes)
        result = SemanticQueryBuilder(sdk).features().first()
        assert result is not None

    def test_first_returns_none_when_empty(self) -> None:
        sdk = self._make_sdk([])
        result = SemanticQueryBuilder(sdk).features().first()
        assert result is None

    def test_execute_no_collection_returns_empty(self) -> None:
        results = SemanticQueryBuilder(MockSDK()).execute()
        assert results == []
