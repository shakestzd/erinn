"""NetworkX graph manager for HtmlGraph.

Builds a NetworkX DiGraph from HtmlGraph's HTML files and SQLite graph_edges,
providing battle-tested graph algorithms for dependency analysis, cycle detection,
and critical path computation.
"""

from __future__ import annotations

import logging
import sqlite3
from pathlib import Path
from typing import Any

import networkx as nx

logger = logging.getLogger(__name__)

# Relationship types where from_node blocks to_node
_FORWARD_RELS = {"blocks"}
# Relationship types where from_node is blocked BY to_node (reverse direction)
_REVERSE_RELS = {"blocked_by"}


def _get_default_db_path() -> str:
    """Return the default database path for the current project."""
    import os
    import subprocess

    env_dir = os.environ.get("HTMLGRAPH_PROJECT_DIR") or os.environ.get(
        "CLAUDE_PROJECT_DIR"
    )
    if env_dir:
        return str(Path(env_dir) / ".htmlgraph" / "htmlgraph.db")

    try:
        result = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if result.returncode == 0:
            project_root = Path(result.stdout.strip())
            return str(project_root / ".htmlgraph" / "htmlgraph.db")
    except Exception:
        pass

    return str(Path.home() / ".htmlgraph" / "htmlgraph.db")


class GraphManager:
    """NetworkX-backed graph intelligence for HtmlGraph work items.

    Builds a directed graph from SQLite ``features`` and ``graph_edges``
    tables and exposes NetworkX algorithms for dependency analysis,
    cycle detection, critical-path computation, and subgraph queries.

    Example::

        from htmlgraph import SDK
        sdk = SDK(agent="claude")
        cycles = sdk.graph.find_cycles()
        path = sdk.graph.critical_path(track_id="trk-abc")
    """

    def __init__(
        self,
        graph_dir: str | Path | None = None,
        db_path: str | None = None,
    ) -> None:
        """Initialise the graph manager.

        Args:
            graph_dir: Path to ``.htmlgraph/`` directory (used as fallback
                       for HTML-file-based graph construction).
            db_path:   Path to the SQLite database. Resolved automatically
                       when *None*.
        """
        self._graph_dir = Path(graph_dir) if graph_dir else None
        self._db_path = db_path or _get_default_db_path()
        self._g: nx.DiGraph | None = None
        # Cache feature metadata keyed by id
        self._features: dict[str, dict[str, Any]] = {}

    # ------------------------------------------------------------------
    # Graph construction
    # ------------------------------------------------------------------

    def build_graph(self) -> nx.DiGraph:
        """Build a ``nx.DiGraph`` from the SQLite database.

        Nodes carry ``title``, ``status``, ``priority``, and ``type``
        attributes.  Edges carry ``relationship_type``.

        Falls back to an empty graph when the database is unavailable.
        """
        G = nx.DiGraph()  # noqa: N806

        try:
            conn = sqlite3.connect(self._db_path)
            conn.row_factory = sqlite3.Row
            cursor = conn.cursor()

            # Load features as nodes (exclude spikes and chores — investigation docs, not work items)
            cursor.execute(
                "SELECT id, title, status, priority, type, track_id FROM features"
                " WHERE type NOT IN ('spike', 'chore')"
            )
            for row in cursor.fetchall():
                row_dict = dict(row)
                fid = row_dict["id"]
                self._features[fid] = row_dict
                G.add_node(
                    fid,
                    title=row_dict.get("title", ""),
                    status=row_dict.get("status", "todo"),
                    priority=row_dict.get("priority", "medium"),
                    type=row_dict.get("type", "feature"),
                    track_id=row_dict.get("track_id"),
                )

            # Load edges
            cursor.execute(
                "SELECT from_node_id, to_node_id, relationship_type FROM graph_edges"
            )
            for row in cursor.fetchall():
                from_id = row["from_node_id"]
                to_id = row["to_node_id"]
                rel = row["relationship_type"]

                # Normalise direction: edge A -> B means "A must finish before B"
                if rel in _FORWARD_RELS:
                    # "blocks": from blocks to => from -> to
                    if from_id in G and to_id in G:
                        G.add_edge(from_id, to_id, relationship_type=rel)
                elif rel in _REVERSE_RELS:
                    # "blocked_by": from is blocked by to => to -> from
                    if from_id in G and to_id in G:
                        G.add_edge(to_id, from_id, relationship_type=rel)
                else:
                    # Other relationships: keep original direction
                    if from_id in G and to_id in G:
                        G.add_edge(from_id, to_id, relationship_type=rel)

            conn.close()
        except sqlite3.Error as exc:
            logger.warning("Could not load graph from DB: %s", exc)

        self._g = G
        return G

    def refresh(self) -> nx.DiGraph:
        """Rebuild the graph from current data."""
        self._features.clear()
        return self.build_graph()

    @property
    def G(self) -> nx.DiGraph:  # noqa: N802
        """Return the cached DiGraph, building it on first access."""
        if self._g is None:
            self.build_graph()
        assert self._g is not None
        return self._g

    # ------------------------------------------------------------------
    # Cycle detection
    # ------------------------------------------------------------------

    def find_cycles(self) -> list[list[str]]:
        """Return all simple cycles in the dependency graph.

        Uses ``nx.simple_cycles`` which implements Johnson's algorithm.
        """
        return list(nx.simple_cycles(self.G))

    def detect_cycles(self) -> list[list[str]]:
        """Detect all cycles in the dependency graph.

        Returns list of cycles, where each cycle is a list of node IDs.
        """
        try:
            return list(nx.simple_cycles(self.G))
        except Exception:
            return []

    def has_cycles(self) -> bool:
        """Quick boolean check for cycles."""
        return not nx.is_directed_acyclic_graph(self.G)

    def get_cycle_info(self) -> dict[str, Any]:
        """Get cycle information for dashboard display.

        Returns a dict with ``has_cycles``, ``cycle_count``, and ``cycles``
        (capped at 10 entries for display purposes).
        """
        cycles = self.detect_cycles()
        return {
            "has_cycles": len(cycles) > 0,
            "cycle_count": len(cycles),
            "cycles": [
                {
                    "nodes": cycle,
                    "titles": [
                        self.G.nodes[n].get("title", n)
                        for n in cycle
                        if n in self.G.nodes
                    ],
                }
                for cycle in cycles[:10]
            ],
        }

    # ------------------------------------------------------------------
    # Critical path
    # ------------------------------------------------------------------

    def _subgraph_for_track(self, track_id: str | None) -> nx.DiGraph:
        """Return the full graph or a track-scoped subgraph."""
        if track_id is None:
            return self.G

        nodes = [n for n, d in self.G.nodes(data=True) if d.get("track_id") == track_id]
        return self.G.subgraph(nodes).copy()

    def critical_path(self, track_id: str | None = None) -> list[str]:
        """Find the longest dependency chain (critical path).

        For a DAG the result is the path whose total length determines
        the minimum schedule.  If the graph contains cycles they are
        reported via a warning and the cyclic edges are removed before
        computing the path.

        Args:
            track_id: Restrict analysis to nodes in this track.

        Returns:
            Ordered list of node IDs on the critical path.
        """
        sub = self._subgraph_for_track(track_id)

        if sub.number_of_nodes() == 0:
            return []

        if not nx.is_directed_acyclic_graph(sub):
            cycles = list(nx.simple_cycles(sub))
            logger.warning(
                "Cycles detected in graph (%d cycle(s)); "
                "breaking them to compute critical path.",
                len(cycles),
            )
            # Break cycles by removing one edge from each cycle
            sub = sub.copy()
            for cycle in cycles:
                if len(cycle) >= 2 and sub.has_edge(cycle[-1], cycle[0]):
                    sub.remove_edge(cycle[-1], cycle[0])
                # Re-check after each removal — may have resolved others
                if nx.is_directed_acyclic_graph(sub):
                    break

        if not nx.is_directed_acyclic_graph(sub):
            logger.error("Could not break all cycles; returning empty path.")
            return []

        return list(nx.dag_longest_path(sub))

    def bottlenecks(self, top_n: int = 5) -> list[dict[str, Any]]:
        """Return nodes with the highest out-degree (blocking most others).

        Args:
            top_n: Number of top bottlenecks to return.

        Returns:
            List of dicts with ``id``, ``title``, ``blocks_count``, and
            ``degree`` keys, sorted by ``blocks_count`` descending.
        """
        results: list[dict[str, Any]] = []
        for node_id in self.G.nodes:
            out_deg = self.G.out_degree(node_id)
            if out_deg == 0:
                continue
            data = self.G.nodes[node_id]
            results.append(
                {
                    "id": node_id,
                    "title": data.get("title", ""),
                    "blocks_count": out_deg,
                    "degree": self.G.degree(node_id),
                }
            )
        results.sort(key=lambda r: r["blocks_count"], reverse=True)
        return results[:top_n]

    # ------------------------------------------------------------------
    # Topological sort
    # ------------------------------------------------------------------

    def topological_sort(self, track_id: str | None = None) -> list[str]:
        """Return node IDs in dependency order.

        Raises ``nx.NetworkXUnfeasible`` if the graph has cycles.

        Args:
            track_id: Restrict to nodes in this track.
        """
        sub = self._subgraph_for_track(track_id)
        return list(nx.topological_sort(sub))

    def execution_order(self, track_id: str | None = None) -> list[dict[str, Any]]:
        """Return enriched dependency order with ``can_start`` flags.

        ``can_start`` is *True* when every predecessor has status
        ``'done'``.

        Args:
            track_id: Restrict to nodes in this track.

        Returns:
            List of dicts: ``id``, ``title``, ``status``, ``can_start``.
        """
        sub = self._subgraph_for_track(track_id)
        order = list(nx.topological_sort(sub))
        results: list[dict[str, Any]] = []
        for node_id in order:
            data = sub.nodes[node_id]
            preds = list(sub.predecessors(node_id))
            can_start = all(sub.nodes[p].get("status") == "done" for p in preds)
            results.append(
                {
                    "id": node_id,
                    "title": data.get("title", ""),
                    "status": data.get("status", "todo"),
                    "can_start": can_start,
                }
            )
        return results

    # ------------------------------------------------------------------
    # Neighborhood & subgraph queries
    # ------------------------------------------------------------------

    def neighborhood(self, node_id: str, depth: int = 1) -> nx.DiGraph:
        """Return the subgraph within *depth* hops of *node_id*.

        Uses ``nx.ego_graph`` with undirected reachability so both
        predecessors and successors within *depth* are included.
        """
        if node_id not in self.G:
            return nx.DiGraph()
        return nx.ego_graph(self.G, node_id, radius=depth, undirected=True)

    def ancestors(self, node_id: str) -> set[str]:
        """All transitive dependencies (predecessors) of *node_id*."""
        if node_id not in self.G:
            return set()
        return set(nx.ancestors(self.G, node_id))

    def descendants(self, node_id: str) -> set[str]:
        """All transitive dependents (successors) of *node_id*."""
        if node_id not in self.G:
            return set()
        return set(nx.descendants(self.G, node_id))

    def shortest_path(self, from_id: str, to_id: str) -> list[str] | None:
        """Shortest path between two nodes, or *None* if unreachable."""
        try:
            return list(nx.shortest_path(self.G, from_id, to_id))
        except (nx.NetworkXNoPath, nx.NodeNotFound):
            return None

    def find_path(
        self, source: str, target: str, max_depth: int = 10
    ) -> list[str] | None:
        """Find shortest path between two nodes with bounded depth.

        Returns list of node IDs in path, or None if no path exists or
        the path exceeds *max_depth* hops.

        Args:
            source:    Starting node ID.
            target:    Destination node ID.
            max_depth: Maximum number of hops allowed (inclusive path length).
        """
        try:
            path = nx.shortest_path(self.G, source, target)
            if len(path) > max_depth:
                return None
            return list(path)
        except (nx.NetworkXNoPath, nx.NodeNotFound):
            return None

    def find_all_paths(
        self, source: str, target: str, max_depth: int = 10
    ) -> list[list[str]]:
        """Find all simple paths between two nodes with bounded depth.

        Uses ``nx.all_simple_paths`` with *cutoff=max_depth* to cap search.

        Args:
            source:    Starting node ID.
            target:    Destination node ID.
            max_depth: Maximum path length cutoff passed to NetworkX.
        """
        try:
            return [
                list(p)
                for p in nx.all_simple_paths(self.G, source, target, cutoff=max_depth)
            ]
        except (nx.NetworkXNoPath, nx.NodeNotFound):
            return []

    def connected_components(self) -> list[set[str]]:
        """Return weakly connected components of the graph."""
        return [set(c) for c in nx.weakly_connected_components(self.G)]
