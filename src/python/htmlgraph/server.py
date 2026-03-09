import logging

logger = logging.getLogger(__name__)

"""
HtmlGraph REST API Server.

Provides HTTP endpoints for CRUD operations on the graph database.
Uses only Python standard library (http.server) for zero dependencies.

Usage:
    from htmlgraph.server import serve
    serve(port=8080, directory=".htmlgraph")

Or via CLI:
    htmlgraph serve --port 8080
"""

import json
import socket
import sys
import urllib.parse
from datetime import datetime, timezone
from http.server import SimpleHTTPRequestHandler
from pathlib import Path
from typing import Any, Literal, cast

from htmlgraph.analytics_index import AnalyticsIndex
from htmlgraph.converter import dict_to_node, node_to_dict
from htmlgraph.event_log import JsonlEventLog
from htmlgraph.graph import HtmlGraph
from htmlgraph.ids import generate_id
from htmlgraph.models import Node


class HtmlGraphAPIHandler(SimpleHTTPRequestHandler):
    """HTTP request handler with REST API support."""

    # Class-level config (set by serve())
    graph_dir: Path = Path(".htmlgraph")
    static_dir: Path = Path(".")
    graphs: dict[str, HtmlGraph] = {}
    analytics_db: AnalyticsIndex | None = None

    # Work item types (subfolders in .htmlgraph/)
    COLLECTIONS = [
        "features",
        "bugs",
        "spikes",
        "chores",
        "epics",
        "sessions",
        "agents",
        "tracks",
        "task-delegations",
    ]

    def __init__(self, *args: Any, **kwargs: Any) -> None:
        # Set directory for static file serving
        self.directory = str(self.static_dir)
        super().__init__(*args, **kwargs)

    def _get_graph(self, collection: str) -> HtmlGraph:
        """Get or create graph for a collection."""
        if collection not in self.graphs:
            collection_dir = self.graph_dir / collection
            collection_dir.mkdir(parents=True, exist_ok=True)

            # Tracks support both file-based (track-xxx.html) and directory-based (track-xxx/index.html)
            if collection == "tracks":
                from htmlgraph.converter import html_to_node
                from htmlgraph.planning import Track

                graph = HtmlGraph(
                    collection_dir,
                    stylesheet_path="../styles.css",
                    auto_load=False,  # Manual load to convert to Track objects
                    pattern=["*.html", "*/index.html"],
                )

                # Helper to convert Node to Track with has_spec/has_plan detection
                def node_to_track(node: Node, filepath: Path) -> Track:
                    # Check if this is a consolidated single-file track or directory-based
                    is_consolidated = filepath.name != "index.html"
                    track_dir = filepath.parent if not is_consolidated else None

                    if is_consolidated:
                        # Consolidated format: spec/plan are in the same file
                        # Check for data-section attributes in the file
                        content = filepath.read_text(encoding="utf-8")
                        has_spec = (
                            'data-section="overview"' in content
                            or 'data-section="requirements"' in content
                        )
                        has_plan = 'data-section="plan"' in content
                    else:
                        # Directory format: separate spec.html and plan.html files
                        has_spec = (
                            (track_dir / "spec.html").exists() if track_dir else False
                        )
                        has_plan = (
                            (track_dir / "plan.html").exists() if track_dir else False
                        )

                    # Map Node status to Track status
                    track_status: Literal["planned", "active", "completed", "abandoned"]
                    if node.status in ["planned", "active", "completed", "abandoned"]:
                        track_status = cast(
                            Literal["planned", "active", "completed", "abandoned"],
                            node.status,
                        )
                    else:
                        track_status = "planned"

                    return Track(
                        id=node.id,
                        title=node.title,
                        description=node.content or "",
                        status=track_status,
                        priority=node.priority,
                        created=node.created,
                        updated=node.updated,
                        has_spec=has_spec,
                        has_plan=has_plan,
                        features=[],
                        sessions=[],
                    )

                # Load and convert tracks
                patterns = (
                    graph.pattern
                    if isinstance(graph.pattern, list)
                    else [graph.pattern]
                )
                for pat in patterns:
                    for filepath in collection_dir.glob(pat):
                        if filepath.is_file():
                            try:
                                node = html_to_node(filepath)
                                track = node_to_track(node, filepath)
                                graph._nodes[track.id] = track  # type: ignore[assignment]
                            except Exception:
                                continue

                # Override reload to maintain Track conversion
                def reload_tracks() -> int:
                    graph._nodes.clear()
                    for pat in patterns:
                        for filepath in collection_dir.glob(pat):
                            if filepath.is_file():
                                try:
                                    node = html_to_node(filepath)
                                    track = node_to_track(node, filepath)
                                    graph._nodes[track.id] = track  # type: ignore[assignment]
                                except Exception:
                                    continue
                    return len(graph._nodes)

                graph.reload = reload_tracks  # type: ignore[method-assign]
                self.graphs[collection] = graph
            else:
                self.graphs[collection] = HtmlGraph(
                    collection_dir, stylesheet_path="../styles.css", auto_load=True
                )
        return self.graphs[collection]

    def _send_json(self, data: Any, status: int = 200) -> None:
        """Send JSON response."""
        body = json.dumps(data, indent=2, default=str).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(body)

    def _send_error_json(self, message: str, status: int = 400) -> None:
        """Send JSON error response."""
        self._send_json({"error": message, "status": status}, status)

    def _read_body(self) -> dict:
        """Read and parse JSON request body."""
        content_length = int(self.headers.get("Content-Length", 0))
        if content_length == 0:
            return {}
        body = self.rfile.read(content_length).decode("utf-8")
        return json.loads(body) if body else {}

    def _parse_path(self) -> tuple[str | None, str | None, str | None, dict]:
        """
        Parse request path into components.

        Returns: (api_prefix, collection, node_id, query_params)

        Examples:
            /api/features -> ("api", "features", None, {})
            /api/features/feat-001 -> ("api", "features", "feat-001", {})
            /api/query?status=todo -> ("api", "query", None, {"status": "todo"})
        """
        parsed = urllib.parse.urlparse(self.path)
        query_params = dict(urllib.parse.parse_qsl(parsed.query))

        parts = [p for p in parsed.path.split("/") if p]

        if not parts:
            return None, None, None, query_params

        if parts[0] != "api":
            return None, None, None, query_params

        collection = parts[1] if len(parts) > 1 else None
        node_id = parts[2] if len(parts) > 2 else None

        return "api", collection, node_id, query_params

    def do_OPTIONS(self) -> None:
        """Handle CORS preflight."""
        self.send_response(200)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header(
            "Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS"
        )
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    def do_GET(self) -> None:
        """Handle GET requests."""
        api, collection, node_id, params = self._parse_path()
        logger.debug(
            f"do_GET: api={api}, collection={collection}, node_id={node_id}, params={params}"
        )

        # Not an API request - serve static files
        if api != "api":
            return super().do_GET()

        # GET /api/status - Overall status
        if collection == "status":
            return self._handle_status()

        # GET /api/query?selector=... - CSS selector query
        if collection == "query":
            return self._handle_query(params)

        # GET /api/analytics/... - Analytics endpoints backed by SQLite index
        if collection == "analytics":
            return self._handle_analytics(node_id, params)

        # GET /api/orchestration - Get delegation chains and agent coordination
        if collection == "orchestration":
            logger.info(f"DEBUG: Handling orchestration request, params={params}")
            return self._handle_orchestration_view(params)

        # GET /api/task-delegations/stats - Get aggregated delegation statistics
        if collection == "task-delegations" and params.get("stats") == "true":
            return self._handle_task_delegations_stats()

        # GET /api/tracks/{track_id}/features - Get features for a track
        if collection == "tracks" and node_id and params.get("features") == "true":
            return self._handle_track_features(node_id)

        # GET /api/features/{feature_id}/context - Get track/plan/spec context
        if collection == "features" and node_id and params.get("context") == "true":
            return self._handle_feature_context(node_id)

        # GET /api/sessions/{session_id}?transcript=true - Get transcript stats
        if collection == "sessions" and node_id and params.get("transcript") == "true":
            return self._handle_session_transcript(node_id)

        # GET /api/collections - List available collections
        if collection == "collections":
            return self._send_json({"collections": self.COLLECTIONS})

        # GET /api/{collection} - List all nodes in collection
        if collection in self.COLLECTIONS and not node_id:
            return self._handle_list(collection, params)

        # GET /api/{collection}/{id} - Get single node
        if collection in self.COLLECTIONS and node_id:
            return self._handle_get(collection, node_id)

        self._send_error_json(f"Unknown endpoint: {self.path}", 404)

    def do_POST(self) -> None:
        """Handle POST requests (create)."""
        api, collection, node_id, params = self._parse_path()

        if api != "api":
            self._send_error_json("API endpoint required", 400)
            return

        # POST /api/tracks/{track_id}/generate-features - Generate features from plan
        if (
            collection == "tracks"
            and node_id
            and params.get("generate-features") == "true"
        ):
            try:
                self._handle_generate_features(node_id)
                return
            except Exception as e:
                self._send_error_json(str(e), 500)
                return

        # POST /api/tracks/{track_id}/sync - Sync task/spec completion
        if collection == "tracks" and node_id and params.get("sync") == "true":
            try:
                self._handle_sync_track(node_id)
                return
            except Exception as e:
                self._send_error_json(str(e), 500)
                return

        if collection not in self.COLLECTIONS:
            self._send_error_json(f"Unknown collection: {collection}", 404)
            return

        try:
            data = self._read_body()
            self._handle_create(collection, data)
        except json.JSONDecodeError as e:
            self._send_error_json(f"Invalid JSON: {e}", 400)
        except Exception as e:
            self._send_error_json(str(e), 500)

    def do_PUT(self) -> None:
        """Handle PUT requests (full update)."""
        api, collection, node_id, params = self._parse_path()

        if api != "api" or not node_id:
            self._send_error_json("PUT requires /api/{collection}/{id}", 400)
            return

        if collection not in self.COLLECTIONS:
            self._send_error_json(f"Unknown collection: {collection}", 404)
            return

        try:
            data = self._read_body()
            self._handle_update(collection, node_id, data, partial=False)
        except json.JSONDecodeError as e:
            self._send_error_json(f"Invalid JSON: {e}", 400)
        except Exception as e:
            self._send_error_json(str(e), 500)

    def do_PATCH(self) -> None:
        """Handle PATCH requests (partial update)."""
        api, collection, node_id, params = self._parse_path()

        if api != "api" or not node_id:
            self._send_error_json("PATCH requires /api/{collection}/{id}", 400)
            return

        if collection not in self.COLLECTIONS:
            self._send_error_json(f"Unknown collection: {collection}", 404)
            return

        try:
            data = self._read_body()
            self._handle_update(collection, node_id, data, partial=True)
        except json.JSONDecodeError as e:
            self._send_error_json(f"Invalid JSON: {e}", 400)
        except Exception as e:
            self._send_error_json(str(e), 500)

    def do_DELETE(self) -> None:
        """Handle DELETE requests."""
        api, collection, node_id, params = self._parse_path()

        if api != "api" or not node_id:
            self._send_error_json("DELETE requires /api/{collection}/{id}", 400)
            return

        if collection not in self.COLLECTIONS:
            self._send_error_json(f"Unknown collection: {collection}", 404)
            return

        self._handle_delete(collection, node_id)

    # =========================================================================
    # API Handlers
    # =========================================================================

    def _handle_status(self) -> None:
        """Return overall graph status."""
        status: dict[str, Any] = {
            "collections": {},
            "total_nodes": 0,
            "by_status": {},
            "by_priority": {},
        }

        for collection in self.COLLECTIONS:
            graph = self._get_graph(collection)
            stats = graph.stats()
            status["collections"][collection] = stats["total"]
            status["total_nodes"] += stats["total"]

            for s, count in stats["by_status"].items():
                status["by_status"][s] = status["by_status"].get(s, 0) + count
            for p, count in stats["by_priority"].items():
                status["by_priority"][p] = status["by_priority"].get(p, 0) + count

        self._send_json(status)

    def _get_analytics(self) -> AnalyticsIndex:
        if self.analytics_db is None:
            self.analytics_db = AnalyticsIndex(self.graph_dir / "index.sqlite")
        return self.analytics_db

    def _reset_analytics_cache(self) -> None:
        self.analytics_db = None

    def _remove_analytics_db_files(self, db_path: Path) -> None:
        # SQLite WAL mode leaves sidecar files. This DB is a rebuildable cache.
        for suffix in ("", "-wal", "-shm"):
            p = db_path if suffix == "" else Path(str(db_path) + suffix)
            try:
                if p.exists():
                    p.unlink()
            except Exception:
                pass

    def _rebuild_analytics_db(self, db_path: Path) -> None:
        events_dir = self.graph_dir / "events"
        if not events_dir.exists() or not any(events_dir.glob("*.jsonl")):
            raise FileNotFoundError(
                "No event logs found under .htmlgraph/events/*.jsonl"
            )

        log = JsonlEventLog(events_dir)
        index = AnalyticsIndex(db_path)
        events = (event for _, event in log.iter_events())
        index.rebuild_from_events(events)

    def _handle_analytics(self, endpoint: str | None, params: dict) -> None:
        """
        Analytics endpoints.

        Backed by a rebuildable SQLite index at `.htmlgraph/index.sqlite`.
        If the index doesn't exist yet, we build it on-demand from `.htmlgraph/events/*.jsonl`.
        """
        if endpoint is None:
            return self._send_error_json(
                "Specify an analytics endpoint (overview, features, session)", 400
            )

        db_path = self.graph_dir / "index.sqlite"

        def ensure_db_exists() -> None:
            if db_path.exists():
                return
            self._rebuild_analytics_db(db_path)

        # Build-on-demand if missing
        if not db_path.exists():
            try:
                ensure_db_exists()
            except FileNotFoundError:
                return self._send_error_json(
                    "Analytics index not found and no event logs present. Start tracking, or run: htmlgraph events export-sessions",
                    404,
                )
            except Exception as e:
                return self._send_error_json(
                    f"Failed to build analytics index: {e}", 500
                )

        def should_reset_index(err: Exception) -> bool:
            msg = str(err).lower()
            return (
                "unsupported analytics index schema" in msg
                or "no such table" in msg
                or "malformed" in msg
                or "file is not a database" in msg
                or "schema_version" in msg
            )

        def with_rebuild(fn: Any) -> Any:
            try:
                return fn()
            except Exception as e:
                if not should_reset_index(e):
                    raise
                # Reset cache and rebuild once.
                self._reset_analytics_cache()
                self._remove_analytics_db_files(db_path)
                ensure_db_exists()
                self._reset_analytics_cache()
                return fn()

        since = params.get("since")
        until = params.get("until")

        if endpoint == "overview":
            try:
                return self._send_json(
                    with_rebuild(
                        lambda: self._get_analytics().overview(since=since, until=until)
                    )
                )
            except Exception as e:
                return self._send_error_json(
                    f"Failed analytics query (overview): {e}", 500
                )

        if endpoint == "features":
            limit = int(params.get("limit", 50))
            try:
                return self._send_json(
                    {
                        "features": with_rebuild(
                            lambda: self._get_analytics().top_features(
                                since=since, until=until, limit=limit
                            )
                        )
                    }
                )
            except Exception as e:
                return self._send_error_json(
                    f"Failed analytics query (features): {e}", 500
                )

        if endpoint == "session":
            session_id = params.get("id")
            if not session_id:
                return self._send_error_json("Missing required param: id", 400)
            limit = int(params.get("limit", 500))
            try:
                return self._send_json(
                    {
                        "events": with_rebuild(
                            lambda: self._get_analytics().session_events(
                                session_id=session_id, limit=limit
                            )
                        )
                    }
                )
            except Exception as e:
                return self._send_error_json(
                    f"Failed analytics query (session): {e}", 500
                )

        if endpoint == "continuity":
            feature_id = params.get("feature_id") or params.get("feature")
            if not feature_id:
                return self._send_error_json("Missing required param: feature_id", 400)
            limit = int(params.get("limit", 200))
            try:
                return self._send_json(
                    {
                        "sessions": with_rebuild(
                            lambda: self._get_analytics().feature_continuity(
                                feature_id=feature_id,
                                since=since,
                                until=until,
                                limit=limit,
                            )
                        )
                    }
                )
            except Exception as e:
                return self._send_error_json(
                    f"Failed analytics query (continuity): {e}", 500
                )

        if endpoint == "transitions":
            limit = int(params.get("limit", 50))
            feature_id = params.get("feature_id") or params.get("feature")
            try:
                return self._send_json(
                    {
                        "transitions": with_rebuild(
                            lambda: self._get_analytics().top_tool_transitions(
                                since=since,
                                until=until,
                                feature_id=feature_id,
                                limit=limit,
                            )
                        )
                    }
                )
            except Exception as e:
                return self._send_error_json(
                    f"Failed analytics query (transitions): {e}", 500
                )

        if endpoint == "commits":
            feature_id = params.get("feature_id") or params.get("feature")
            if not feature_id:
                return self._send_error_json("Missing required param: feature_id", 400)
            limit = int(params.get("limit", 200))
            try:
                return self._send_json(
                    {
                        "commits": with_rebuild(
                            lambda: self._get_analytics().feature_commits(
                                feature_id=feature_id, limit=limit
                            )
                        )
                    }
                )
            except Exception as e:
                return self._send_error_json(
                    f"Failed analytics query (commits): {e}", 500
                )

        if endpoint == "commit-graph":
            feature_id = params.get("feature_id") or params.get("feature")
            if not feature_id:
                return self._send_error_json("Missing required param: feature_id", 400)
            limit = int(params.get("limit", 200))
            try:
                return self._send_json(
                    {
                        "graph": with_rebuild(
                            lambda: self._get_analytics().feature_commit_graph(
                                feature_id=feature_id, limit=limit
                            )
                        )
                    }
                )
            except Exception as e:
                return self._send_error_json(
                    f"Failed analytics query (commit-graph): {e}", 500
                )

        return self._send_error_json(f"Unknown analytics endpoint: {endpoint}", 404)

    def _handle_query(self, params: dict) -> None:
        """Handle CSS selector query across collections."""
        selector = params.get("selector", "")
        collection = params.get("collection")  # Optional filter to single collection

        if not selector:
            # If no selector, return all nodes matching other params
            selector = self._build_selector_from_params(params)

        results = []
        collections = (
            [collection] if collection in self.COLLECTIONS else self.COLLECTIONS
        )

        for coll in collections:
            graph = self._get_graph(coll)
            matches = graph.query(selector) if selector else list(graph)
            for node in matches:
                node_data = node_to_dict(node)
                node_data["_collection"] = coll
                results.append(node_data)

        self._send_json({"count": len(results), "nodes": results})

    def _build_selector_from_params(self, params: dict) -> str:
        """Build CSS selector from query params."""
        parts = []
        for key in ["status", "priority", "type"]:
            if key in params:
                parts.append(f"[data-{key}='{params[key]}']")
        return "".join(parts)

    def _handle_list(self, collection: str, params: dict) -> None:
        """List all nodes in a collection."""
        graph = self._get_graph(collection)

        # Apply filters if provided
        nodes = list(graph)

        if "status" in params:
            nodes = [n for n in nodes if n.status == params["status"]]
        if "priority" in params:
            nodes = [n for n in nodes if n.priority == params["priority"]]
        if "type" in params:
            nodes = [n for n in nodes if n.type == params["type"]]

        # Sort options
        sort_by = params.get("sort", "updated")
        reverse = params.get("order", "desc") == "desc"

        # Helper to ensure timezone-aware datetimes for comparison
        def ensure_tz_aware(dt: datetime) -> datetime:
            if dt.tzinfo is None:
                return dt.replace(tzinfo=timezone.utc)
            return dt

        if sort_by == "priority":
            priority_order = {"critical": 0, "high": 1, "medium": 2, "low": 3}
            nodes.sort(
                key=lambda n: priority_order.get(n.priority, 99), reverse=not reverse
            )
        elif sort_by == "created":
            nodes.sort(key=lambda n: ensure_tz_aware(n.created), reverse=reverse)
        else:  # default: updated
            nodes.sort(key=lambda n: ensure_tz_aware(n.updated), reverse=reverse)

        # Pagination
        limit = int(params.get("limit", 100))
        offset = int(params.get("offset", 0))

        total = len(nodes)
        nodes = nodes[offset : offset + limit]

        self._send_json(
            {
                "collection": collection,
                "total": total,
                "limit": limit,
                "offset": offset,
                "nodes": [node_to_dict(n) for n in nodes],
            }
        )

    def _handle_get(self, collection: str, node_id: str) -> None:
        """Get a single node."""
        graph = self._get_graph(collection)
        node = graph.get(node_id)

        if not node:
            self._send_error_json(f"Node not found: {node_id}", 404)
            return

        data = node_to_dict(node)
        data["_collection"] = collection
        data["_context"] = node.to_context()  # Include lightweight context

        self._send_json(data)

    def _handle_create(self, collection: str, data: dict) -> None:
        """Create a new node."""
        # Set defaults based on collection
        type_map = {
            "features": "feature",
            "bugs": "bug",
            "spikes": "spike",
            "chores": "chore",
            "epics": "epic",
            "sessions": "session",
            "agents": "agent",
        }
        if "type" not in data:
            data["type"] = type_map.get(collection, "node")

        # Generate collision-resistant ID if not provided
        if "id" not in data:
            node_type = data.get("type", type_map.get(collection, "node"))
            title = data.get("title", "")
            data["id"] = generate_id(node_type=node_type, title=title)

        # Require title
        if "title" not in data:
            self._send_error_json("'title' is required", 400)
            return

        # Convert steps if provided as strings
        if "steps" in data and data["steps"]:
            if isinstance(data["steps"][0], str):
                data["steps"] = [
                    {"description": s, "completed": False} for s in data["steps"]
                ]

        try:
            node = dict_to_node(data)
            graph = self._get_graph(collection)
            graph.add(node)

            response = node_to_dict(node)
            response["_collection"] = collection
            response["_location"] = f"/api/{collection}/{node.id}"

            self._send_json(response, 201)
        except ValueError as e:
            self._send_error_json(str(e), 400)

    def _handle_update(
        self, collection: str, node_id: str, data: dict, partial: bool
    ) -> None:
        """Update a node (full or partial)."""
        graph = self._get_graph(collection)
        existing = graph.get(node_id)

        if not existing:
            self._send_error_json(f"Node not found: {node_id}", 404)
            return

        agent = data.get("agent")
        if agent is not None:
            agent = str(agent).strip() or None

        old_status = existing.status

        if partial:
            # Merge with existing
            existing_data = node_to_dict(existing)
            existing_data.update(data)
            data = existing_data

        # Ensure ID matches
        data["id"] = node_id

        # Handle step completion shorthand: {"complete_step": 0}
        if "complete_step" in data:
            step_idx = data.pop("complete_step")
            if 0 <= step_idx < len(existing.steps):
                existing.complete_step(step_idx, agent)
                graph.update(existing)
                if agent:
                    try:
                        from htmlgraph.session_manager import SessionManager

                        sm = SessionManager(self.graph_dir)
                        session = sm.get_active_session_for_agent(
                            agent
                        ) or sm.start_session(agent=agent, title="API session")
                        step_desc = None
                        try:
                            step_desc = existing.steps[step_idx].description
                        except Exception:
                            step_desc = None
                        sm.track_activity(
                            session_id=session.id,
                            tool="StepComplete",
                            summary=f"Completed step {step_idx + 1}: {collection}/{node_id}",
                            success=True,
                            feature_id=node_id,
                            payload={
                                "collection": collection,
                                "node_id": node_id,
                                "step_index": step_idx,
                                "step_description": step_desc,
                            },
                        )
                    except Exception:
                        pass
                self._send_json(node_to_dict(existing))
                return

        # Handle status transitions
        if "status" in data and data["status"] != existing.status:
            data["updated"] = datetime.now().isoformat()

        try:
            node = dict_to_node(data)
            graph.update(node)
            new_status = node.status
            if (
                agent
                and (collection in {"features", "bugs", "spikes", "chores", "epics"})
                and (new_status != old_status)
            ):
                try:
                    from htmlgraph.session_manager import SessionManager

                    sm = SessionManager(self.graph_dir)
                    session = sm.get_active_session_for_agent(
                        agent
                    ) or sm.start_session(agent=agent, title="API session")
                    sm.track_activity(
                        session_id=session.id,
                        tool="WorkItemStatus",
                        summary=f"Status {old_status} → {new_status}: {collection}/{node_id}",
                        success=True,
                        feature_id=node_id,
                        payload={
                            "collection": collection,
                            "node_id": node_id,
                            "from": old_status,
                            "to": new_status,
                        },
                    )
                except Exception:
                    pass
            self._send_json(node_to_dict(node))
        except Exception as e:
            self._send_error_json(str(e), 400)

    def _handle_delete(self, collection: str, node_id: str) -> None:
        """Delete a node."""
        # Special handling for tracks (directories, not single files)
        if collection == "tracks":
            from htmlgraph.track_manager import TrackManager

            manager = TrackManager(self.graph_dir)
            try:
                manager.delete_track(node_id)
                self._send_json({"deleted": node_id, "collection": collection})
            except ValueError as e:
                self._send_error_json(str(e), 404)
            return

        graph = self._get_graph(collection)

        if node_id not in graph:
            self._send_error_json(f"Node not found: {node_id}", 404)
            return

        graph.remove(node_id)
        self._send_json({"deleted": node_id, "collection": collection})

    # =========================================================================
    # Track-Feature Integration Handlers
    # =========================================================================

    def _handle_track_features(self, track_id: str) -> None:
        """Get all features for a track."""
        features_graph = self._get_graph("features")

        # Filter features by track_id
        track_features = [
            node_to_dict(node)
            for node in features_graph
            if hasattr(node, "track_id") and node.track_id == track_id
        ]

        self._send_json(
            {
                "track_id": track_id,
                "features": track_features,
                "count": len(track_features),
            }
        )

    def _handle_feature_context(self, feature_id: str) -> None:
        """Get track/plan/spec context for a feature."""
        features_graph = self._get_graph("features")

        if feature_id not in features_graph:
            self._send_error_json(f"Feature not found: {feature_id}", 404)
            return

        feature = features_graph.get(feature_id)

        if not feature:
            self._send_error_json(f"Feature not found: {feature_id}", 404)
            return

        context: dict[str, str | list[str] | bool | None] = {
            "feature_id": feature_id,
            "feature_title": feature.title,
            "track_id": feature.track_id if hasattr(feature, "track_id") else None,
            "plan_task_id": feature.plan_task_id
            if hasattr(feature, "plan_task_id")
            else None,
            "spec_requirements": feature.spec_requirements
            if hasattr(feature, "spec_requirements")
            else [],
            "track_exists": False,
            "has_spec": False,
            "has_plan": False,
            "is_consolidated": False,
        }

        # Load track info if linked
        track_id = context["track_id"]
        if track_id and isinstance(track_id, str):
            from htmlgraph.track_manager import TrackManager

            manager = TrackManager(self.graph_dir)
            track_dir = manager.tracks_dir / track_id
            track_file = manager.tracks_dir / f"{track_id}.html"

            # Support both consolidated (single file) and directory-based tracks
            if track_file.exists():
                # Consolidated format
                context["track_exists"] = True
                content = track_file.read_text(encoding="utf-8")
                context["has_spec"] = (
                    'data-section="overview"' in content
                    or 'data-section="requirements"' in content
                )
                context["has_plan"] = 'data-section="plan"' in content
                context["is_consolidated"] = True
            elif track_dir.exists():
                # Directory format
                context["track_exists"] = True
                context["has_spec"] = (track_dir / "spec.html").exists()
                context["has_plan"] = (track_dir / "plan.html").exists()
                context["is_consolidated"] = False
            else:
                context["track_exists"] = False
                context["has_spec"] = False
                context["has_plan"] = False

        self._send_json(context)

    def _handle_session_transcript(self, session_id: str) -> None:
        """Get transcript stats for a session."""
        try:
            from htmlgraph.session_manager import SessionManager

            manager = SessionManager(self.graph_dir)
            stats = manager.get_transcript_stats(session_id)

            if stats is None:
                self._send_json(
                    {
                        "session_id": session_id,
                        "transcript_linked": False,
                        "message": "No transcript linked to this session",
                    }
                )
                return

            self._send_json(
                {"session_id": session_id, "transcript_linked": True, **stats}
            )
        except Exception as e:
            self._send_error_json(f"Error getting transcript stats: {e}", 500)

    def _handle_generate_features(self, track_id: str) -> None:
        """Generate features from plan tasks."""
        from htmlgraph.track_manager import TrackManager

        manager = TrackManager(self.graph_dir)

        # Load the plan
        try:
            plan = manager.load_plan(track_id)
        except FileNotFoundError:
            self._send_error_json(f"Plan not found for track: {track_id}", 404)
            return

        # Generate features
        try:
            features = manager.generate_features_from_plan(
                track_id=track_id, plan=plan, features_dir=self.graph_dir / "features"
            )

            # Reload features graph to include new features
            self.graphs.pop("features", None)

            self._send_json(
                {
                    "track_id": track_id,
                    "generated": len(features),
                    "feature_ids": [f.id for f in features],
                }
            )
        except Exception as e:
            self._send_error_json(f"Failed to generate features: {str(e)}", 500)

    def _handle_orchestration_view(self, params: dict) -> None:
        """
        Get delegation chains and agent coordination information.

        Queries the SQLite database for delegation events and builds
        a view of agent coordination and handoff patterns.

        Returns:
            {
                "delegation_count": int,
                "unique_agents": int,
                "agents": [str],
                "delegation_chains": {
                    "from_agent": [
                        {
                            "to_agent": str,
                            "event_type": str,
                            "timestamp": str,
                            "task": str,
                            "status": str
                        }
                    ]
                }
            }
        """
        try:
            from htmlgraph.db.schema import HtmlGraphDB

            # Use unified index.sqlite database
            db_path = str(self.graph_dir / "index.sqlite")
            db = HtmlGraphDB(db_path=db_path)
            db.connect()

            # Get all delegation events
            delegations = db.get_delegations(limit=1000)
            db.close()

            # Build delegation chains grouped by from_agent
            delegation_chains: dict[str, list[dict]] = {}
            agents = set()
            delegation_count = 0

            for delegation in delegations:
                from_agent = delegation.get("from_agent", "unknown")
                to_agent = delegation.get("to_agent", "unknown")
                timestamp = delegation.get("timestamp", "")
                reason = delegation.get("reason", "")
                status = delegation.get("status", "pending")

                agents.add(from_agent)
                agents.add(to_agent)
                delegation_count += 1

                if from_agent not in delegation_chains:
                    delegation_chains[from_agent] = []

                delegation_chains[from_agent].append(
                    {
                        "to_agent": to_agent,
                        "event_type": "delegation",
                        "timestamp": timestamp,
                        "task": reason or "Unnamed task",
                        "status": status,
                    }
                )

            self._send_json(
                {
                    "delegation_count": delegation_count,
                    "unique_agents": len(agents),
                    "agents": sorted(list(agents)),
                    "delegation_chains": delegation_chains,
                }
            )

        except Exception as e:
            self._send_error_json(f"Failed to get orchestration view: {str(e)}", 500)

    def _handle_task_delegations_stats(self) -> None:
        """Get aggregated statistics about task delegations."""
        try:
            delegations_graph = self._get_graph("task-delegations")

            # Get all delegations
            all_delegations = list(delegations_graph)

            if not all_delegations:
                self._send_json(
                    {
                        "total_delegations": 0,
                        "by_agent_type": {},
                        "by_status": {},
                        "total_tokens": 0,
                        "total_cost": 0.0,
                        "average_duration": 0.0,
                        "agent_stats": [],
                    }
                )
                return

            # Aggregate by agent type
            agent_stats: dict = {}
            by_status: dict[str, int] = {}
            total_tokens = 0
            total_cost = 0.0
            durations = []

            for delegation in all_delegations:
                agent_type = str(getattr(delegation, "agent_type", "unknown"))
                status = str(getattr(delegation, "status", "unknown"))
                tokens_val = getattr(delegation, "tokens_used", 0)
                tokens = int(tokens_val) if tokens_val else 0
                cost_val = getattr(delegation, "cost_usd", 0)
                cost = float(cost_val) if cost_val else 0.0
                duration_val = getattr(delegation, "duration_seconds", 0)
                duration = int(duration_val) if duration_val else 0

                # Track by agent
                if agent_type not in agent_stats:
                    agent_stats[agent_type] = {
                        "agent_type": agent_type,
                        "tasks_completed": 0,
                        "total_duration": 0,
                        "total_tokens": 0,
                        "total_cost": 0.0,
                        "success_count": 0,
                        "failure_count": 0,
                    }

                agent_stats[agent_type]["tasks_completed"] += 1
                agent_stats[agent_type]["total_duration"] += duration
                agent_stats[agent_type]["total_tokens"] += tokens
                agent_stats[agent_type]["total_cost"] += cost

                if status == "success":
                    agent_stats[agent_type]["success_count"] += 1
                else:
                    agent_stats[agent_type]["failure_count"] += 1

                # Track by status
                by_status[status] = by_status.get(status, 0) + 1

                # Aggregate totals
                total_tokens += tokens
                total_cost += cost
                if duration:
                    durations.append(duration)

            # Calculate success rate for each agent
            for agent_stats_item in agent_stats.values():
                total = agent_stats_item["tasks_completed"]
                if total > 0:
                    agent_stats_item["success_rate"] = (
                        agent_stats_item["success_count"] / total
                    )
                else:
                    agent_stats_item["success_rate"] = 0.0

            average_duration = sum(durations) / len(durations) if durations else 0.0

            self._send_json(
                {
                    "total_delegations": len(all_delegations),
                    "by_agent_type": {
                        agent: stats["tasks_completed"]
                        for agent, stats in agent_stats.items()
                    },
                    "by_status": by_status,
                    "total_tokens": total_tokens,
                    "total_cost": round(total_cost, 4),
                    "average_duration": round(average_duration, 2),
                    "agent_stats": sorted(
                        agent_stats.values(),
                        key=lambda x: x["total_cost"],
                        reverse=True,
                    ),
                }
            )
        except Exception as e:
            self._send_error_json(f"Failed to get delegation stats: {str(e)}", 500)

    def _handle_sync_track(self, track_id: str) -> None:
        """Sync task and spec completion based on features."""
        from htmlgraph.track_manager import TrackManager

        manager = TrackManager(self.graph_dir)
        features_graph = self._get_graph("features")

        try:
            # Sync task completion
            plan = manager.sync_task_completion(track_id, features_graph)

            # Sync spec satisfaction
            spec = manager.check_spec_satisfaction(track_id, features_graph)

            # Reload tracks graph
            self.graphs.pop("tracks", None)

            self._send_json(
                {
                    "track_id": track_id,
                    "plan_updated": True,
                    "spec_updated": True,
                    "plan_completion": plan.completion_percentage,
                    "spec_status": spec.status,
                }
            )
        except Exception as e:
            self._send_error_json(f"Failed to sync track: {str(e)}", 500)

    def log_message(self, format: str, *args: str) -> None:
        """Custom log format."""
        logger.info(f"[{datetime.now().strftime('%H:%M:%S')}] {args[0]}")


def find_available_port(start_port: int = 8080, max_attempts: int = 10) -> int:
    """
    Find an available port starting from start_port.

    Args:
        start_port: Port to start searching from
        max_attempts: Maximum number of ports to try

    Returns:
        Available port number

    Raises:
        OSError: If no available port found in range
    """
    for port in range(start_port, start_port + max_attempts):
        try:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
                s.bind(("", port))
                return port
        except OSError:
            continue
    raise OSError(
        f"No available ports found in range {start_port}-{start_port + max_attempts}"
    )


def check_port_in_use(port: int, host: str = "localhost") -> bool:
    """
    Check if a port is already in use.

    Args:
        port: Port number to check
        host: Host to check on

    Returns:
        True if port is in use, False otherwise
    """
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.bind((host, port))
            return False
    except OSError:
        return True



def serve(
    port: int = 8080,
    graph_dir: str | Path = ".htmlgraph",
    static_dir: str | Path = ".",
    host: str = "localhost",
    watch: bool = True,
    auto_port: bool = False,
    show_progress: bool = False,
    quiet: bool = False,
) -> None:
    """
    Start the HtmlGraph server (FastAPI-based with WebSocket support).

    This function launches the FastAPI server which provides:
    - REST API for CRUD operations on graph nodes
    - WebSocket endpoint at /ws/events for real-time event streaming
    - HTMX-powered dashboard for agent observability

    Args:
        port: Port to listen on (default: 8080)
        graph_dir: Directory containing graph data (.htmlgraph/)
        static_dir: Directory for static files (index.html, etc.) - preserved for compatibility
        host: Host to bind to (default: localhost)
        watch: Enable file watching for auto-reload (default: True) - maps to reload in FastAPI
        auto_port: Automatically find available port if specified port is in use
        show_progress: Show Rich progress during startup (not used with FastAPI)
        quiet: Suppress progress output when true
    """
    import asyncio

    from htmlgraph.operations.fastapi_server import (
        FastAPIServerError,
        PortInUseError,
        run_fastapi_server,
        start_fastapi_server,
    )

    graph_dir = Path(graph_dir)

    # Ensure graph directory exists
    graph_dir.mkdir(parents=True, exist_ok=True)
    for collection in HtmlGraphAPIHandler.COLLECTIONS:
        (graph_dir / collection).mkdir(exist_ok=True)

    # Copy default stylesheet if not present
    styles_dest = graph_dir / "styles.css"
    if not styles_dest.exists():
        styles_src = Path(__file__).parent / "styles.css"
        if styles_src.exists():
            styles_dest.write_text(styles_src.read_text())

    # Database path - use htmlgraph.db in the graph directory
    db_path = str(graph_dir / "htmlgraph.db")

    try:
        result = start_fastapi_server(
            port=port,
            host=host,
            db_path=db_path,
            auto_port=auto_port,
            reload=watch,  # Map watch to reload for FastAPI
        )

        # Print warnings if any
        for warning in result.warnings:
            if not quiet:
                logger.info(f"⚠️  {warning}")

        # Print server info
        if not quiet:
            actual_port = result.config_used["port"]
            print(f"""
╔══════════════════════════════════════════════════════════════╗
║              HtmlGraph Server (FastAPI)                      ║
╠══════════════════════════════════════════════════════════════╣
║  Dashboard:   http://{host}:{actual_port}/
║  API:         http://{host}:{actual_port}/api/
║  WebSocket:   ws://{host}:{actual_port}/ws/events
║  Graph Dir:   {graph_dir}
║  Database:    {db_path}
║  Auto-reload: {"Enabled" if watch else "Disabled"}
╚══════════════════════════════════════════════════════════════╝

Features:
  • Real-time agent activity feed (HTMX + WebSocket)
  • Orchestration chains visualization
  • Feature tracker with Kanban view
  • Session metrics & performance analytics

API Endpoints:
  GET    /api/events              - List events
  GET    /api/sessions            - List sessions
  GET    /api/orchestration       - Orchestration data
  GET    /api/initial-stats       - Dashboard statistics
  WS     /ws/events               - Real-time event stream

Collections: {", ".join(HtmlGraphAPIHandler.COLLECTIONS)}

Press Ctrl+C to stop.
""")

        # Run the server
        asyncio.run(run_fastapi_server(result.handle))

    except PortInUseError:
        logger.info(f"\n❌ Port {port} is already in use\n")
        logger.info("Solutions:")
        logger.info("  1. Use a different port:")
        logger.info(f"     htmlgraph serve --port {port + 1}\n")
        logger.info("  2. Let htmlgraph automatically find an available port:")
        logger.info("     htmlgraph serve --auto-port\n")
        logger.info(f"  3. Find and kill the process using port {port}:")
        logger.info(f"     lsof -ti:{port} | xargs kill -9\n")

        # Try to find and suggest an available port
        try:
            alt_port = find_available_port(port + 1)
            logger.info(f"💡 Found available port: {alt_port}")
            logger.info(f"   Run: htmlgraph serve --port {alt_port}\n")
        except OSError:
            pass

        sys.exit(1)

    except FastAPIServerError as e:
        logger.info(f"\n❌ Server error: {e}\n")
        sys.exit(1)

    except KeyboardInterrupt:
        logger.info("\nShutting down...")


if __name__ == "__main__":
    serve()
