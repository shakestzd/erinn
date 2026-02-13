from __future__ import annotations

"""
Minimal MCP (Model Context Protocol) server for HtmlGraph.

Goals:
- Keep tool surface tiny (3 tools) to avoid context bloat
- Route all state to HtmlGraph's existing filesystem-first API
- Run over stdio with no extra dependencies

Tools:
- log_event(tool, summary, files?, success?, feature_id?, payload?, agent?)
- get_active_feature()
- set_active_feature(feature_id, collection?)

For AI agents working with HtmlGraph:
- Use the Python SDK for feature management (see AGENTS.md)
- Use these MCP tools only for low-level event logging and session tracking
- NEVER edit .htmlgraph HTML files directly - use SDK/API/CLI instead

Example SDK usage:
    from htmlgraph import SDK
    sdk = SDK(agent="claude")
    feature = sdk.features.create("Title").add_steps([...]).save()
    with sdk.features.edit(feature.id) as f:
        f.status = "done"
"""


import json
import os
import subprocess
import sys
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any, cast

from htmlgraph.session_manager import SessionManager


def _resolve_project_dir(cwd: str | None = None) -> Path:
    env_dir = os.environ.get("HTMLGRAPH_PROJECT_DIR") or os.environ.get(
        "CLAUDE_PROJECT_DIR"
    )
    if env_dir:
        return Path(env_dir)

    start_dir = Path(cwd or os.getcwd())
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"],
            capture_output=True,
            text=True,
            cwd=str(start_dir),
            timeout=5,
        )
        if result.returncode == 0:
            return Path(result.stdout.strip())
    except Exception:
        pass

    return start_dir


def _json_dumps(obj: Any) -> str:
    return json.dumps(obj, ensure_ascii=False, default=str)


class StdioTransport:
    """
    Stdio transport that supports both:
    - Content-Length framed JSON-RPC messages (LSP-style; used by many MCP clients)
    - Newline-delimited JSON messages (useful for manual testing)
    """

    def __init__(
        self,
        inp: Any = None,
        out: Any = None,
        *,
        force_content_length: bool | None = None,
        log_to_stderr: bool = True,
    ) -> None:
        self.inp = inp or sys.stdin.buffer
        self.out = out or sys.stdout.buffer
        self.log_to_stderr = log_to_stderr
        self.use_content_length: bool | None = force_content_length

    def _log(self, msg: str) -> None:
        if not self.log_to_stderr:
            return
        try:
            sys.stderr.write(msg + "\n")
            sys.stderr.flush()
        except Exception:
            pass

    def read_message(self) -> dict[str, Any] | None:
        """
        Read the next JSON-RPC message dict. Returns None on EOF.
        """
        # If framing is forced, read accordingly.
        if self.use_content_length is True:
            return self._read_content_length_message()

        # Try to detect Content-Length framing by reading the first non-empty line.
        while True:
            line = self.inp.readline()
            if not line:
                return None
            if line in (b"\n", b"\r\n"):
                continue

            lower = line.lower()
            if lower.startswith(b"content-length:"):
                self.use_content_length = True
                return self._read_content_length_message(first_line=line)

            # Otherwise treat as newline-delimited JSON.
            self.use_content_length = (
                False if self.use_content_length is None else self.use_content_length
            )
            try:
                return cast(dict[str, Any], json.loads(line.decode("utf-8").strip()))
            except Exception:
                # If we can't parse, keep scanning (avoids hanging on unexpected headers).
                self._log(f"mcp: skipped non-json line: {line[:120]!r}")
                continue

    def _read_content_length_message(
        self, first_line: bytes | None = None
    ) -> dict[str, Any] | None:
        headers: dict[str, str] = {}

        def add_header(h: bytes) -> None:
            try:
                s = h.decode("utf-8").strip()
            except Exception:
                return
            if ":" not in s:
                return
            k, v = s.split(":", 1)
            headers[k.strip().lower()] = v.strip()

        if first_line is not None:
            add_header(first_line)
        else:
            # Read until we find a Content-Length header line.
            while True:
                line = self.inp.readline()
                if not line:
                    return None
                if line in (b"\n", b"\r\n"):
                    continue
                if line.lower().startswith(b"content-length:"):
                    add_header(line)
                    break

        # Read remaining headers until blank line
        while True:
            line = self.inp.readline()
            if not line:
                return None
            if line in (b"\n", b"\r\n"):
                break
            add_header(line)

        length_str = headers.get("content-length")
        if not length_str:
            self._log("mcp: missing Content-Length header")
            return None

        try:
            length = int(length_str)
        except ValueError:
            self._log(f"mcp: invalid Content-Length: {length_str!r}")
            return None

        body = self.inp.read(length)
        if not body:
            return None
        try:
            return cast(dict[str, Any], json.loads(body.decode("utf-8")))
        except Exception as e:
            self._log(f"mcp: invalid json body: {e}")
            return None

    def write_message(self, msg: dict[str, Any]) -> None:
        data = _json_dumps(msg).encode("utf-8")

        # Prefer Content-Length when detected or forced.
        if self.use_content_length is True:
            header = f"Content-Length: {len(data)}\r\n\r\n".encode("ascii")
            self.out.write(header)
            self.out.write(data)
            self.out.flush()
            return

        # Default: newline-delimited JSON
        self.out.write(data + b"\n")
        self.out.flush()


@dataclass(frozen=True)
class Tool:
    name: str
    description: str
    input_schema: dict[str, Any]


def _tools() -> list[Tool]:
    return [
        Tool(
            name="log_event",
            description="Append an activity event to HtmlGraph (Git-friendly JSONL + optional SQLite cache).",
            input_schema={
                "type": "object",
                "properties": {
                    "tool": {
                        "type": "string",
                        "description": "Event tool name (e.g. Bash, Edit, Deploy).",
                    },
                    "summary": {
                        "type": "string",
                        "description": "Human-readable summary.",
                    },
                    "files": {
                        "type": "array",
                        "items": {"type": "string"},
                        "description": "Optional file paths.",
                    },
                    "success": {
                        "type": "boolean",
                        "description": "Optional success flag (default true).",
                    },
                    "feature_id": {
                        "type": "string",
                        "description": "Optional explicit feature id (skips attribution).",
                    },
                    "payload": {
                        "type": "object",
                        "description": "Optional structured payload.",
                    },
                    "agent": {
                        "type": "string",
                        "description": "Optional agent name for the session (default mcp).",
                    },
                },
                "required": ["tool", "summary"],
                "additionalProperties": True,
            },
        ),
        Tool(
            name="get_active_feature",
            description="Return the current primary feature and active in-progress work items.",
            input_schema={
                "type": "object",
                "properties": {
                    "agent": {
                        "type": "string",
                        "description": "Optional agent override for auto-logging/session scoping.",
                    },
                },
                "additionalProperties": False,
            },
        ),
        Tool(
            name="set_active_feature",
            description="Mark a feature/bug as primary (and in-progress) for attribution.",
            input_schema={
                "type": "object",
                "properties": {
                    "feature_id": {"type": "string"},
                    "collection": {
                        "type": "string",
                        "description": "Optional: features or bugs.",
                    },
                    "agent": {
                        "type": "string",
                        "description": "Optional agent override for session attribution.",
                    },
                },
                "required": ["feature_id"],
                "additionalProperties": False,
            },
        ),
    ]


@dataclass(frozen=True)
class Resource:
    uri: str
    name: str
    description: str
    mime_type: str


def _project_root_from_graph_dir(graph_dir: Path) -> Path:
    # Default layout is <repo>/.htmlgraph; fall back to parent if the directory isn't named .htmlgraph.
    if graph_dir.name == ".htmlgraph":
        return graph_dir.parent
    return graph_dir.parent


def _default_resources(graph_dir: Path) -> list[Resource]:
    """
    Keep resources minimal and high-signal; most code browsing should still use normal file tools.
    """
    root = _project_root_from_graph_dir(graph_dir)
    candidates: list[tuple[str, str]] = [
        ("AGENTS.md", "Contributor guide and workflow instructions."),
        ("docs/MCP.md", "HtmlGraph minimal MCP server usage."),
        ("docs/GIT_HOOKS.md", "Git hook continuity spine documentation."),
    ]

    resources: list[Resource] = []
    for rel, desc in candidates:
        p = root / rel
        if not p.exists():
            continue
        resources.append(
            Resource(
                uri=f"htmlgraph://file/{rel}",
                name=rel,
                description=desc,
                mime_type="text/markdown",
            )
        )
    return resources


def _read_resource_uri(graph_dir: Path, uri: str) -> tuple[str, str]:
    """
    Return (mime_type, text) for supported resources.

    Supported URI scheme:
      - htmlgraph://file/<repo-relative-path>
    """
    prefix = "htmlgraph://file/"
    if not uri.startswith(prefix):
        raise ValueError(f"Unsupported resource uri: {uri}")

    rel = uri[len(prefix) :]
    if not rel or rel.startswith("/") or ".." in Path(rel).parts:
        raise ValueError("Invalid resource path")

    root = _project_root_from_graph_dir(graph_dir)
    path = root / rel
    if not path.exists() or not path.is_file():
        raise ValueError(f"Resource not found: {rel}")

    text = path.read_text(encoding="utf-8")
    # Only markdown for now; expand if needed.
    mime = "text/markdown" if rel.lower().endswith(".md") else "text/plain"
    return mime, text


class McpServer:
    def __init__(
        self,
        graph_dir: Path,
        default_agent: str = "mcp",
        *,
        autolog: bool | None = None,
        autolog_min_seconds: float | None = None,
    ):
        self.graph_dir = graph_dir
        self.default_agent = default_agent
        self._initialized = False
        self.autolog = (
            autolog
            if autolog is not None
            else (os.environ.get("HTMLGRAPH_MCP_AUTOLOG", "1") != "0")
        )
        self.autolog_min_seconds = (
            autolog_min_seconds
            if autolog_min_seconds is not None
            else float(os.environ.get("HTMLGRAPH_MCP_AUTOLOG_MIN_SECONDS", "5"))
        )
        self._autolog_last: dict[tuple[str, str], datetime] = {}

    def _manager(self) -> SessionManager:
        return SessionManager(self.graph_dir)

    def _ensure_session(self, agent: str | None = None) -> str:
        manager = self._manager()
        target_agent = agent or self.default_agent
        active = manager.get_active_session_for_agent(target_agent)
        if active:
            return active.id
        created = manager.start_session(
            session_id=None,
            agent=target_agent,
            title=f"MCP {datetime.now().strftime('%Y-%m-%d %H:%M')}",
        )
        return created.id

    def _infer_collection(self, feature_id: str, collection: str | None) -> str:
        if collection in {"features", "bugs"}:
            return collection
        if (self.graph_dir / "features" / f"{feature_id}.html").exists():
            return "features"
        if (self.graph_dir / "bugs" / f"{feature_id}.html").exists():
            return "bugs"
        return "features"

    def _maybe_autolog(
        self,
        *,
        agent: str,
        tool_name: str,
        summary: str,
        feature_id: str | None = None,
        success: bool = True,
        payload: dict[str, Any] | None = None,
    ) -> None:
        if not self.autolog:
            return
        try:
            now = datetime.now()
            key = (agent, tool_name)
            last = self._autolog_last.get(key)
            if last and (now - last).total_seconds() < self.autolog_min_seconds:
                return
            self._autolog_last[key] = now

            session_id = self._ensure_session(agent=agent)
            manager = self._manager()
            manager.track_activity(
                session_id=session_id,
                tool=tool_name,
                summary=summary,
                success=success,
                feature_id=feature_id,
                payload=payload,
                agent=agent,
            )
        except Exception:
            return

    def _handle_log_event(self, args: dict[str, Any]) -> dict[str, Any]:
        tool = str(args.get("tool") or "").strip()
        summary = str(args.get("summary") or "").strip()
        if not tool or not summary:
            raise ValueError("log_event requires tool and summary")

        files = args.get("files") or []
        if not isinstance(files, list):
            files = []
        files = [str(f) for f in files if f]

        success = args.get("success", True)
        if not isinstance(success, bool):
            success = True

        feature_id = args.get("feature_id")
        if feature_id is not None:
            feature_id = str(feature_id).strip() or None

        payload = args.get("payload")
        if payload is not None and not isinstance(payload, dict):
            payload = {"value": payload}

        agent = args.get("agent")
        if agent is not None:
            agent = str(agent).strip() or None

        session_id = self._ensure_session(agent=agent)
        manager = self._manager()
        entry = manager.track_activity(
            session_id=session_id,
            tool=tool,
            summary=summary,
            file_paths=files,
            success=success,
            feature_id=feature_id,
            payload=payload,
            agent=agent,
        )
        return {
            "session_id": session_id,
            "event_id": entry.id,
            "feature_id": entry.feature_id,
            "drift_score": entry.drift_score,
        }

    def _handle_get_active_feature(
        self, args: dict[str, Any] | None = None
    ) -> dict[str, Any]:
        args = args or {}
        manager = self._manager()
        primary = manager.get_primary_feature()
        active = manager.get_active_features()
        return {
            "primary": (
                {"id": primary.id, "title": primary.title, "type": primary.type}
                if primary
                else None
            ),
            "active": [{"id": n.id, "title": n.title, "type": n.type} for n in active],
        }

    def _handle_set_active_feature(self, args: dict[str, Any]) -> dict[str, Any]:
        feature_id = str(args.get("feature_id") or "").strip()
        if not feature_id:
            raise ValueError("set_active_feature requires feature_id")

        agent = args.get("agent")
        if agent is not None:
            agent = str(agent).strip() or None
        agent = agent or self.default_agent

        collection = self._infer_collection(feature_id, args.get("collection"))
        manager = self._manager()
        node = manager.activate_feature(feature_id, collection=collection, agent=agent)
        if node is None:
            raise ValueError(f"Feature not found: {feature_id}")
        return {"primary": {"id": node.id, "title": node.title, "type": node.type}}

    def handle(self, message: dict[str, Any]) -> dict[str, Any] | None:
        method = message.get("method")
        msg_id = message.get("id")
        params = message.get("params") or {}

        if method is None:
            return None

        if method == "initialize":
            self._initialized = True
            return {
                "jsonrpc": "2.0",
                "id": msg_id,
                "result": {
                    "protocolVersion": params.get("protocolVersion") or "2024-11-05",
                    "capabilities": {"tools": {}},
                    "serverInfo": {"name": "htmlgraph", "version": "0.1.0"},
                },
            }

        if method == "notifications/initialized":
            self._initialized = True
            return None

        if not self._initialized and method != "initialize":
            return {
                "jsonrpc": "2.0",
                "id": msg_id,
                "error": {"code": -32002, "message": "Server not initialized"},
            }

        if method == "tools/list":
            tools = _tools()
            return {
                "jsonrpc": "2.0",
                "id": msg_id,
                "result": {
                    "tools": [
                        {
                            "name": t.name,
                            "description": t.description,
                            "inputSchema": t.input_schema,
                        }
                        for t in tools
                    ]
                },
            }

        # Resources: Codex expects these methods even if you only expose tools.
        if method == "resources/list":
            resources = _default_resources(self.graph_dir)
            return {
                "jsonrpc": "2.0",
                "id": msg_id,
                "result": {
                    "resources": [
                        {
                            "uri": r.uri,
                            "name": r.name,
                            "description": r.description,
                            "mimeType": r.mime_type,
                        }
                        for r in resources
                    ]
                },
            }

        if method == "resources/read":
            uri = (params.get("uri") or "").strip()
            if not uri:
                return {
                    "jsonrpc": "2.0",
                    "id": msg_id,
                    "error": {"code": -32602, "message": "Missing required param: uri"},
                }
            try:
                mime, text = _read_resource_uri(self.graph_dir, uri)
                return {
                    "jsonrpc": "2.0",
                    "id": msg_id,
                    "result": {
                        "contents": [
                            {
                                "uri": uri,
                                "mimeType": mime,
                                "text": text,
                            }
                        ]
                    },
                }
            except Exception as e:
                return {
                    "jsonrpc": "2.0",
                    "id": msg_id,
                    "error": {"code": -32000, "message": str(e)},
                }

        if method == "resources/templates/list":
            # No templates yet; keep surface area tiny.
            return {
                "jsonrpc": "2.0",
                "id": msg_id,
                "result": {"resourceTemplates": []},
            }

        if method == "tools/call":
            name = params.get("name")
            arguments = params.get("arguments") or {}
            if not isinstance(arguments, dict):
                arguments = {}

            try:
                if name == "log_event":
                    result = self._handle_log_event(arguments)
                elif name == "get_active_feature":
                    result = self._handle_get_active_feature(arguments)
                    agent = self.default_agent
                    if isinstance(arguments, dict) and arguments.get("agent"):
                        agent = str(arguments.get("agent")).strip() or agent
                    primary_id = None
                    try:
                        primary = (
                            result.get("primary") if isinstance(result, dict) else None
                        )
                        if isinstance(primary, dict):
                            primary_id = primary.get("id")
                    except Exception:
                        primary_id = None
                    self._maybe_autolog(
                        agent=agent,
                        tool_name="MCP:get_active_feature",
                        summary="MCP get_active_feature",
                        feature_id=str(primary_id).strip() if primary_id else None,
                        success=True,
                        payload={"tool": "get_active_feature"},
                    )
                elif name == "set_active_feature":
                    result = self._handle_set_active_feature(arguments)
                else:
                    raise ValueError(f"Unknown tool: {name}")

                return {
                    "jsonrpc": "2.0",
                    "id": msg_id,
                    "result": {
                        "content": [{"type": "text", "text": _json_dumps(result)}],
                        "isError": False,
                    },
                }
            except Exception as e:
                return {
                    "jsonrpc": "2.0",
                    "id": msg_id,
                    "result": {
                        "content": [{"type": "text", "text": str(e)}],
                        "isError": True,
                    },
                }

        return {
            "jsonrpc": "2.0",
            "id": msg_id,
            "error": {"code": -32601, "message": f"Method not found: {method}"},
        }


def serve_stdio(graph_dir: Path, default_agent: str = "mcp") -> None:
    server = McpServer(graph_dir=graph_dir, default_agent=default_agent)
    transport = StdioTransport(
        force_content_length=(os.environ.get("HTMLGRAPH_MCP_CONTENT_LENGTH") == "1"),
    )

    # Helpful for manual runs; stderr-only so it won't break protocol.
    try:
        if sys.stdin.isatty():
            sys.stderr.write("htmlgraph mcp: waiting for stdio JSON-RPC messages...\n")
            sys.stderr.flush()
    except Exception:
        pass

    while True:
        msg = transport.read_message()
        if msg is None:
            return

        resp = server.handle(msg)
        if resp is None:
            continue
        transport.write_message(resp)


def main() -> None:
    project_dir = _resolve_project_dir()
    graph_dir = Path(os.environ.get("HTMLGRAPH_DIR") or (project_dir / ".htmlgraph"))
    agent = os.environ.get("HTMLGRAPH_AGENT") or "mcp"
    serve_stdio(graph_dir=graph_dir, default_agent=agent)


if __name__ == "__main__":
    main()
