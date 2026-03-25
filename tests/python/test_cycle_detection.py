"""Tests for cycle detection and bounded path-finding."""

from htmlgraph.graph.networkx_manager import GraphManager


class TestCycleDetection:
    def test_no_cycles_in_dag(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("a", "b")
        gm.G.add_edge("b", "c")
        assert gm.detect_cycles() == []

    def test_detects_simple_cycle(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("a", "b")
        gm.G.add_edge("b", "c")
        gm.G.add_edge("c", "a")
        cycles = gm.detect_cycles()
        assert len(cycles) > 0

    def test_detects_self_loop(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("a", "a")
        cycles = gm.detect_cycles()
        assert len(cycles) > 0

    def test_detects_two_cycles(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        # Cycle 1: a -> b -> a
        gm.G.add_edge("a", "b")
        gm.G.add_edge("b", "a")
        # Cycle 2: c -> d -> c
        gm.G.add_edge("c", "d")
        gm.G.add_edge("d", "c")
        cycles = gm.detect_cycles()
        assert len(cycles) >= 2

    def test_get_cycle_info_no_cycles(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("a", "b")
        info = gm.get_cycle_info()
        assert info["has_cycles"] is False
        assert info["cycle_count"] == 0
        assert info["cycles"] == []

    def test_get_cycle_info_with_cycles(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("a", "b")
        gm.G.add_edge("b", "a")
        info = gm.get_cycle_info()
        assert info["has_cycles"] is True
        assert info["cycle_count"] > 0

    def test_get_cycle_info_includes_titles(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_node("a", title="Feature A")
        gm.G.add_node("b", title="Feature B")
        gm.G.add_edge("a", "b")
        gm.G.add_edge("b", "a")
        info = gm.get_cycle_info()
        assert info["has_cycles"] is True
        # At least one cycle should have titles populated
        assert len(info["cycles"]) > 0
        first_cycle = info["cycles"][0]
        assert "nodes" in first_cycle
        assert "titles" in first_cycle
        assert len(first_cycle["titles"]) > 0

    def test_get_cycle_info_caps_at_ten(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        # Create 15 independent 2-cycles
        for i in range(15):
            a, b = f"n{i}a", f"n{i}b"
            gm.G.add_edge(a, b)
            gm.G.add_edge(b, a)
        info = gm.get_cycle_info()
        assert len(info["cycles"]) <= 10

    def test_detect_cycles_returns_empty_on_isolated_nodes(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_node("x")
        gm.G.add_node("y")
        assert gm.detect_cycles() == []

    def test_critical_path_graceful_with_cycles(self, tmp_path):
        """critical_path() must not raise even when cycles exist."""
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("a", "b")
        gm.G.add_edge("b", "c")
        gm.G.add_edge("c", "a")
        # Should not raise NetworkXUnfeasible
        result = gm.critical_path()
        assert isinstance(result, list)


class TestBoundedPathFinding:
    def test_find_direct_path(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("a", "b")
        gm.G.add_edge("b", "c")
        path = gm.find_path("a", "c")
        assert path == ["a", "b", "c"]

    def test_find_single_hop_path(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("a", "b")
        path = gm.find_path("a", "b")
        assert path == ["a", "b"]

    def test_no_path_returns_none(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_node("a")
        gm.G.add_node("b")
        assert gm.find_path("a", "b") is None

    def test_nonexistent_node_returns_none(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_node("a")
        assert gm.find_path("a", "z") is None

    def test_max_depth_exceeded_returns_none(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        for i in range(20):
            gm.G.add_edge(str(i), str(i + 1))
        assert gm.find_path("0", "20", max_depth=5) is None

    def test_max_depth_exact_boundary_passes(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("a", "b")
        gm.G.add_edge("b", "c")
        # Path length == 3 nodes, max_depth=3 should pass
        path = gm.find_path("a", "c", max_depth=3)
        assert path == ["a", "b", "c"]

    def test_max_depth_one_below_boundary_fails(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("a", "b")
        gm.G.add_edge("b", "c")
        # Path needs 3 nodes but max_depth=2
        path = gm.find_path("a", "c", max_depth=2)
        assert path is None

    def test_find_all_paths_two_routes(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("a", "b")
        gm.G.add_edge("a", "c")
        gm.G.add_edge("b", "d")
        gm.G.add_edge("c", "d")
        paths = gm.find_all_paths("a", "d")
        assert len(paths) == 2
        # Both paths start at 'a' and end at 'd'
        for p in paths:
            assert p[0] == "a"
            assert p[-1] == "d"

    def test_find_all_paths_no_path(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_node("a")
        gm.G.add_node("b")
        assert gm.find_all_paths("a", "b") == []

    def test_find_all_paths_nonexistent_node(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_node("a")
        assert gm.find_all_paths("a", "z") == []

    def test_find_all_paths_max_depth_limits_results(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        # Short path: a -> b -> d (2 hops)
        gm.G.add_edge("a", "b")
        gm.G.add_edge("b", "d")
        # Long path: a -> c -> e -> d (3 hops)
        gm.G.add_edge("a", "c")
        gm.G.add_edge("c", "e")
        gm.G.add_edge("e", "d")
        all_paths = gm.find_all_paths("a", "d", max_depth=10)
        short_only = gm.find_all_paths("a", "d", max_depth=2)
        assert len(all_paths) == 2
        assert len(short_only) == 1
        assert short_only[0] == ["a", "b", "d"]

    def test_find_path_same_source_target(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_node("a")
        path = gm.find_path("a", "a")
        # NetworkX returns ["a"] for trivial same-node path
        assert path == ["a"]

    def test_find_all_paths_single_direct_edge(self, tmp_path):
        gm = GraphManager(str(tmp_path))
        gm.G.add_edge("x", "y")
        paths = gm.find_all_paths("x", "y")
        assert paths == [["x", "y"]]
