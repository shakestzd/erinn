#!/usr/bin/env python3
"""
Performance benchmarks for HtmlGraph query operations.

Run with: python benchmarks/query_benchmarks.py

Benchmarks:
- EdgeIndex vs linear scan for reverse edge lookups
- QueryBuilder execution time
- Find API performance
- Graph traversal performance
"""

import random
import shutil
import string
import tempfile
import time
from collections.abc import Callable
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path

from htmlgraph import HtmlGraph
from htmlgraph.models import Edge, Node


@dataclass
class BenchmarkResult:
    name: str
    iterations: int
    total_time: float
    avg_time: float
    ops_per_sec: float

    def __str__(self):
        return (
            f"{self.name}: {self.avg_time * 1000:.3f}ms avg "
            f"({self.ops_per_sec:.0f} ops/sec, {self.iterations} iterations)"
        )


@contextmanager
def timer():
    """Context manager for timing operations."""
    start = time.perf_counter()
    yield
    end = time.perf_counter()
    return end - start


def benchmark(name: str, func: Callable, iterations: int = 100) -> BenchmarkResult:
    """Run a benchmark and return results."""
    times = []
    for _ in range(iterations):
        start = time.perf_counter()
        func()
        end = time.perf_counter()
        times.append(end - start)

    total = sum(times)
    avg = total / iterations
    ops_per_sec = iterations / total if total > 0 else float("inf")

    return BenchmarkResult(
        name=name,
        iterations=iterations,
        total_time=total,
        avg_time=avg,
        ops_per_sec=ops_per_sec,
    )


def create_test_graph(
    num_nodes: int, edge_density: float = 0.1
) -> tuple[HtmlGraph, Path]:
    """Create a test graph with specified number of nodes and edge density."""
    temp_dir = Path(tempfile.mkdtemp())
    graph_dir = temp_dir / ".htmlgraph"
    features_dir = graph_dir / "features"
    features_dir.mkdir(parents=True)

    graph = HtmlGraph(str(graph_dir))

    # Create nodes
    statuses = ["todo", "in-progress", "blocked", "done"]
    priorities = ["low", "medium", "high", "critical"]

    node_ids = []
    for i in range(num_nodes):
        node_id = f"feature-{i:04d}"
        node_ids.append(node_id)

        node = Node(
            id=node_id,
            title=f"Feature {i}: {''.join(random.choices(string.ascii_lowercase, k=10))}",
            type="feature",
            status=random.choice(statuses),
            priority=random.choice(priorities),
            properties={
                "effort": random.randint(1, 20),
                "completion": random.randint(0, 100),
            },
        )
        graph.add(node)

    # Create edges based on density
    num_edges = int(num_nodes * num_nodes * edge_density)
    for _ in range(num_edges):
        source = random.choice(node_ids)
        target = random.choice(node_ids)
        if source != target:
            edge = Edge(target_id=target, relationship="blocked_by")
            node = graph.get(source)
            if node:
                if "blocked_by" not in node.edges:
                    node.edges["blocked_by"] = []
                # Avoid duplicate edges
                if not any(e.target_id == target for e in node.edges["blocked_by"]):
                    node.edges["blocked_by"].append(edge)
                    graph.update(node)

    # Rebuild edge index
    graph._edge_index.rebuild(graph._nodes)

    return graph, temp_dir


def linear_scan_reverse_lookup(graph: HtmlGraph, target_id: str) -> list:
    """O(V*E) linear scan for reverse edge lookup (old method)."""
    result = []
    for node in graph._nodes.values():
        for edge in node.edges.get("blocked_by", []):
            if edge.target_id == target_id:
                result.append(node.id)
    return result


def run_benchmarks():
    """Run all benchmarks."""
    print("=" * 70)
    print("HtmlGraph Query Performance Benchmarks")
    print("=" * 70)

    # Test with different graph sizes
    for num_nodes in [100, 500, 1000]:
        print(f"\n{'=' * 70}")
        print(f"Graph Size: {num_nodes} nodes")
        print("=" * 70)

        graph, temp_dir = create_test_graph(num_nodes, edge_density=0.02)

        try:
            # Pick a random target node
            target_id = f"feature-{num_nodes // 2:04d}"

            # Benchmark 1: EdgeIndex vs Linear Scan
            print("\n--- Edge Lookup Benchmarks ---")

            result = benchmark(
                "EdgeIndex reverse lookup (O(1))",
                lambda: graph.get_incoming_edges(target_id, "blocked_by"),
                iterations=1000,
            )
            print(result)

            result = benchmark(
                "Linear scan reverse lookup (O(V*E))",
                lambda: linear_scan_reverse_lookup(graph, target_id),
                iterations=100,
            )
            print(result)

            # Benchmark 2: QueryBuilder
            print("\n--- QueryBuilder Benchmarks ---")

            result = benchmark(
                "QueryBuilder simple equality",
                lambda: graph.query_builder().where("status", "blocked").execute(),
                iterations=100,
            )
            print(result)

            result = benchmark(
                "QueryBuilder with AND",
                lambda: (
                    graph.query_builder()
                    .where("status", "blocked")
                    .and_("priority", "high")
                    .execute()
                ),
                iterations=100,
            )
            print(result)

            result = benchmark(
                "QueryBuilder with OR",
                lambda: (
                    graph.query_builder()
                    .where("priority", "high")
                    .or_("priority", "critical")
                    .execute()
                ),
                iterations=100,
            )
            print(result)

            result = benchmark(
                "QueryBuilder numeric comparison",
                lambda: (
                    graph.query_builder().where("properties.effort").gt(10).execute()
                ),
                iterations=100,
            )
            print(result)

            result = benchmark(
                "QueryBuilder text contains",
                lambda: (
                    graph.query_builder().where("title").contains("Feature").execute()
                ),
                iterations=100,
            )
            print(result)

            # Benchmark 3: Find API
            print("\n--- Find API Benchmarks ---")

            result = benchmark(
                "find() single result",
                lambda: graph.find(status="blocked"),
                iterations=100,
            )
            print(result)

            result = benchmark(
                "find_all() multiple criteria",
                lambda: graph.find_all(status="blocked", priority="high"),
                iterations=100,
            )
            print(result)

            result = benchmark(
                "find_all() with lookup suffix",
                lambda: graph.find_all(properties__effort__gt=10),
                iterations=100,
            )
            print(result)

            # Benchmark 4: CSS Selector Query
            print("\n--- CSS Selector Benchmarks ---")

            result = benchmark(
                "CSS selector query",
                lambda: graph.query('[data-status="blocked"]'),
                iterations=100,
            )
            print(result)

            # Benchmark 5: Graph Traversal
            print("\n--- Graph Traversal Benchmarks ---")

            result = benchmark(
                "ancestors() unlimited depth",
                lambda: graph.ancestors(target_id),
                iterations=100,
            )
            print(result)

            result = benchmark(
                "ancestors() max_depth=2",
                lambda: graph.ancestors(target_id, max_depth=2),
                iterations=100,
            )
            print(result)

            result = benchmark(
                "descendants() unlimited depth",
                lambda: graph.descendants(target_id),
                iterations=100,
            )
            print(result)

            result = benchmark(
                "connected_component()",
                lambda: graph.connected_component(target_id),
                iterations=100,
            )
            print(result)

        finally:
            shutil.rmtree(temp_dir)

    print("\n" + "=" * 70)
    print("Benchmarks complete!")
    print("=" * 70)


def run_acceptance_benchmark():
    """Run the acceptance criteria benchmark: <100ms for 10K node graphs."""
    print("\n" + "=" * 70)
    print("Acceptance Criteria Benchmark: 10K Node Graph")
    print("=" * 70)

    print("\nCreating 10,000 node graph (this may take a moment)...")
    graph, temp_dir = create_test_graph(10000, edge_density=0.001)

    try:
        target_id = "feature-5000"

        benchmarks = [
            ("EdgeIndex lookup", lambda: graph.get_incoming_edges(target_id)),
            (
                "QueryBuilder query",
                lambda: graph.query_builder().where("status", "blocked").execute(),
            ),
            (
                "find_all() query",
                lambda: graph.find_all(status="blocked", priority="high"),
            ),
            ("CSS selector query", lambda: graph.query('[data-status="blocked"]')),
            ("ancestors()", lambda: graph.ancestors(target_id, max_depth=3)),
            ("descendants()", lambda: graph.descendants(target_id, max_depth=3)),
        ]

        print("\nResults (target: <100ms):")
        print("-" * 50)

        all_passed = True
        for name, func in benchmarks:
            result = benchmark(name, func, iterations=10)
            passed = result.avg_time < 0.1  # 100ms
            status = "PASS" if passed else "FAIL"
            print(f"  {status}: {result}")
            if not passed:
                all_passed = False

        print("-" * 50)
        if all_passed:
            print("All benchmarks PASSED (<100ms)")
        else:
            print("Some benchmarks FAILED (>=100ms)")

    finally:
        shutil.rmtree(temp_dir)


if __name__ == "__main__":
    run_benchmarks()
    run_acceptance_benchmark()
