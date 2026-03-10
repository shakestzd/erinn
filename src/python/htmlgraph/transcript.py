from __future__ import annotations

"""
Claude Code Transcript Integration.

This module provides tools for reading, parsing, and integrating
Claude Code session transcripts into HtmlGraph.

Claude Code stores conversation transcripts as JSONL files in:
    ~/.claude/projects/[encoded-path]/[session-uuid].jsonl

Each line is a JSON object with fields like:
    - type: "user", "assistant", "tool_use", "tool_result"
    - message: {role, content}
    - uuid: unique message ID
    - timestamp: ISO timestamp
    - sessionId: session UUID
    - cwd: working directory
    - gitBranch: current git branch

References:
    - https://simonwillison.net/2025/Dec/25/claude-code-transcripts/
    - https://github.com/simonw/claude-code-transcripts
"""


import json
from collections.abc import Iterator
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any, Literal


@dataclass
class TranscriptEntry:
    """A single entry from a Claude Code transcript JSONL file."""

    uuid: str
    timestamp: datetime
    session_id: str
    entry_type: Literal["user", "assistant", "tool_use", "tool_result", "system"]

    # Message content
    message_role: str | None = None
    message_content: str | None = None

    # Tool use details
    tool_name: str | None = None
    tool_input: dict[str, Any] | None = None
    tool_result: str | None = None

    # Context
    cwd: str | None = None
    git_branch: str | None = None
    version: str | None = None

    # Hierarchy
    parent_uuid: str | None = None
    is_sidechain: bool = False

    # Thinking (extended thinking traces)
    thinking: str | None = None

    # Raw data for extension
    raw: dict[str, Any] = field(default_factory=dict)

    @classmethod
    def from_jsonl_line(cls, data: dict[str, Any]) -> TranscriptEntry:
        """Parse a JSONL line into a TranscriptEntry."""
        # Parse timestamp
        ts_str = data.get("timestamp", "")
        try:
            timestamp = datetime.fromisoformat(ts_str.replace("Z", "+00:00"))
        except (ValueError, AttributeError):
            timestamp = datetime.now()

        # Determine entry type
        entry_type = data.get("type", "system")
        if entry_type not in ("user", "assistant", "tool_use", "tool_result", "system"):
            entry_type = "system"

        # Extract message content
        message = data.get("message", {})
        message_role = message.get("role") if isinstance(message, dict) else None
        message_content = None

        if isinstance(message, dict):
            content = message.get("content")
            if isinstance(content, str):
                message_content = content
            elif isinstance(content, list):
                # Check for tool_result blocks (these are type=user but contain tool results)
                has_tool_result = any(
                    isinstance(b, dict) and b.get("type") == "tool_result"
                    for b in content
                )
                if has_tool_result and entry_type == "user":
                    entry_type = "tool_result"

                # Handle content blocks (text, tool_use, etc.)
                text_parts = []
                for block in content:
                    if isinstance(block, dict):
                        if block.get("type") == "text":
                            text_parts.append(block.get("text", ""))
                        elif block.get("type") == "thinking":
                            # Extended thinking trace
                            pass  # Will extract separately
                message_content = "\n".join(text_parts) if text_parts else None

        # Extract thinking trace from content blocks
        thinking = None
        if isinstance(message, dict) and isinstance(message.get("content"), list):
            for block in message["content"]:
                if isinstance(block, dict) and block.get("type") == "thinking":
                    thinking = block.get("thinking", "")
                    break

        # Extract tool details
        tool_name = None
        tool_input = None
        tool_result = None

        if entry_type == "tool_use":
            # Tool use can be in message.content as a block
            if isinstance(message, dict) and isinstance(message.get("content"), list):
                for block in message["content"]:
                    if isinstance(block, dict) and block.get("type") == "tool_use":
                        tool_name = block.get("name")
                        tool_input = block.get("input")
                        break
        elif entry_type == "assistant":
            # Web sessions embed tool_use blocks inside assistant entries
            if isinstance(message, dict) and isinstance(message.get("content"), list):
                for block in message["content"]:
                    if isinstance(block, dict) and block.get("type") == "tool_use":
                        tool_name = block.get("name")
                        tool_input = block.get("input")
                        # Mark this as a tool_use entry for counting
                        entry_type = "tool_use"
                        break
        elif entry_type == "tool_result":
            tool_result = message_content

        return cls(
            uuid=data.get("uuid", ""),
            timestamp=timestamp,
            session_id=data.get("sessionId", ""),
            entry_type=entry_type,
            message_role=message_role,
            message_content=message_content,
            tool_name=tool_name,
            tool_input=tool_input,
            tool_result=tool_result,
            cwd=data.get("cwd"),
            git_branch=data.get("gitBranch"),
            version=data.get("version"),
            parent_uuid=data.get("parentUuid"),
            is_sidechain=data.get("isSidechain", False),
            thinking=thinking,
            raw=data,
        )

    def to_summary(self) -> str:
        """Generate a human-readable summary of this entry."""
        if self.entry_type == "user":
            content = self.message_content or ""
            preview = content[:100] + "..." if len(content) > 100 else content
            return f'User: "{preview}"'
        elif self.entry_type == "assistant":
            if self.tool_name:
                return f"Assistant: {self.tool_name}"
            content = self.message_content or ""
            preview = content[:80] + "..." if len(content) > 80 else content
            return f"Assistant: {preview}"
        elif self.entry_type == "tool_use":
            return f"Tool: {self.tool_name or 'unknown'}"
        elif self.entry_type == "tool_result":
            result = self.tool_result or self.message_content or ""
            preview = result[:60] + "..." if len(result) > 60 else result
            return f"Result: {preview}"
        else:
            return f"System: {self.entry_type}"


@dataclass
class TranscriptSession:
    """A complete Claude Code session transcript."""

    session_id: str
    path: Path
    entries: list[TranscriptEntry] = field(default_factory=list)

    # Metadata extracted from entries
    cwd: str | None = None
    git_branch: str | None = None
    version: str | None = None
    started_at: datetime | None = None
    ended_at: datetime | None = None

    @property
    def duration_seconds(self) -> float | None:
        """Calculate session duration in seconds."""
        if self.started_at and self.ended_at:
            return (self.ended_at - self.started_at).total_seconds()
        return None

    @property
    def user_message_count(self) -> int:
        """Count of user messages."""
        return sum(1 for e in self.entries if e.entry_type == "user")

    @property
    def tool_call_count(self) -> int:
        """Count of tool uses."""
        return sum(1 for e in self.entries if e.entry_type == "tool_use")

    @property
    def tool_breakdown(self) -> dict[str, int]:
        """Breakdown of tool calls by tool name."""
        breakdown: dict[str, int] = {}
        for e in self.entries:
            if e.entry_type == "tool_use" and e.tool_name:
                breakdown[e.tool_name] = breakdown.get(e.tool_name, 0) + 1
        return breakdown

    def has_thinking_traces(self) -> bool:
        """Check if session has any thinking traces."""
        return any(e.thinking for e in self.entries)

    def to_html(self, include_thinking: bool = False) -> str:
        """
        Export transcript to HTML format.

        Compatible with claude-code-transcripts format.

        Args:
            include_thinking: Include thinking traces in output

        Returns:
            HTML string of the transcript
        """
        import html as html_module

        lines = [
            "<!DOCTYPE html>",
            '<html lang="en">',
            "<head>",
            '    <meta charset="UTF-8">',
            '    <meta name="viewport" content="width=device-width, initial-scale=1.0">',
            f"    <title>Claude Code Session: {self.session_id}</title>",
            "    <style>",
            "        body { font-family: system-ui, -apple-system, sans-serif; max-width: 800px; margin: 0 auto; padding: 2rem; line-height: 1.6; }",
            "        .metadata { background: #f5f5f5; padding: 1rem; border-radius: 8px; margin-bottom: 2rem; }",
            "        .metadata dt { font-weight: bold; display: inline; }",
            "        .metadata dd { display: inline; margin: 0 1rem 0 0; }",
            "        .entry { margin-bottom: 1.5rem; padding: 1rem; border-radius: 8px; }",
            "        .entry-user { background: #e3f2fd; border-left: 4px solid #1976d2; }",
            "        .entry-assistant { background: #f3e5f5; border-left: 4px solid #7b1fa2; }",
            "        .entry-tool { background: #e8f5e9; border-left: 4px solid #388e3c; }",
            "        .entry-result { background: #fff3e0; border-left: 4px solid #f57c00; }",
            "        .entry-header { display: flex; justify-content: space-between; margin-bottom: 0.5rem; }",
            "        .entry-type { font-weight: bold; text-transform: capitalize; }",
            "        .entry-time { color: #666; font-size: 0.875rem; }",
            "        .entry-content { white-space: pre-wrap; font-family: inherit; }",
            "        .tool-name { font-family: monospace; background: #e0e0e0; padding: 0.2rem 0.5rem; border-radius: 4px; }",
            "        .tool-input { background: #f5f5f5; padding: 0.5rem; border-radius: 4px; margin-top: 0.5rem; font-family: monospace; font-size: 0.875rem; overflow-x: auto; }",
            "        .thinking { background: #fff8e1; padding: 0.5rem; border-radius: 4px; margin-top: 0.5rem; font-style: italic; color: #666; }",
            "        summary { cursor: pointer; font-weight: bold; }",
            "        pre { margin: 0; white-space: pre-wrap; word-wrap: break-word; }",
            "    </style>",
            "</head>",
            "<body>",
            f"    <h1>Session: {html_module.escape(self.session_id[:20])}...</h1>",
            "",
            '    <dl class="metadata">',
        ]

        if self.cwd:
            lines.append(
                f"        <dt>Directory:</dt><dd>{html_module.escape(self.cwd)}</dd>"
            )
        if self.git_branch:
            lines.append(
                f"        <dt>Branch:</dt><dd>{html_module.escape(self.git_branch)}</dd>"
            )
        if self.started_at:
            lines.append(
                f"        <dt>Started:</dt><dd>{self.started_at.isoformat()}</dd>"
            )
        if self.ended_at:
            lines.append(f"        <dt>Ended:</dt><dd>{self.ended_at.isoformat()}</dd>")
        if self.duration_seconds:
            mins = int(self.duration_seconds // 60)
            lines.append(f"        <dt>Duration:</dt><dd>{mins} minutes</dd>")

        lines.append(f"        <dt>Messages:</dt><dd>{self.user_message_count}</dd>")
        lines.append(f"        <dt>Tool Calls:</dt><dd>{self.tool_call_count}</dd>")
        lines.append("    </dl>")
        lines.append("")

        # Output entries
        for entry in self.entries:
            entry_class = {
                "user": "entry-user",
                "assistant": "entry-assistant",
                "tool_use": "entry-tool",
                "tool_result": "entry-result",
            }.get(entry.entry_type, "entry")

            lines.append(f'    <div class="entry {entry_class}">')
            lines.append('        <div class="entry-header">')

            if entry.entry_type == "tool_use" and entry.tool_name:
                lines.append(
                    f'            <span class="entry-type">Tool: <span class="tool-name">{html_module.escape(entry.tool_name)}</span></span>'
                )
            else:
                lines.append(
                    f'            <span class="entry-type">{entry.entry_type}</span>'
                )

            lines.append(
                f'            <span class="entry-time">{entry.timestamp.strftime("%H:%M:%S")}</span>'
            )
            lines.append("        </div>")

            # Content
            if entry.message_content:
                content = html_module.escape(entry.message_content)
                lines.append(f'        <div class="entry-content">{content}</div>')

            # Tool input
            if entry.tool_input and entry.entry_type == "tool_use":
                lines.append("        <details>")
                lines.append("            <summary>Input</summary>")
                input_str = json.dumps(entry.tool_input, indent=2)
                lines.append(
                    f'            <pre class="tool-input">{html_module.escape(input_str)}</pre>'
                )
                lines.append("        </details>")

            # Thinking (if enabled)
            if include_thinking and entry.thinking:
                lines.append("        <details>")
                lines.append("            <summary>Thinking</summary>")
                lines.append(
                    f'            <div class="thinking">{html_module.escape(entry.thinking)}</div>'
                )
                lines.append("        </details>")

            lines.append("    </div>")
            lines.append("")

        lines.append("</body>")
        lines.append("</html>")

        return "\n".join(lines)


class TranscriptReader:
    """
    Read and parse Claude Code transcript JSONL files.

    Usage:
        reader = TranscriptReader()

        # List all available transcripts
        for session in reader.list_sessions():
            print(f"{session.session_id}: {session.user_message_count} messages")

        # Read a specific session
        session = reader.read_session("abc-123-def")
        for entry in session.entries:
            print(entry.to_summary())
    """

    # Default Claude Code projects directory
    DEFAULT_CLAUDE_DIR = Path.home() / ".claude" / "projects"

    def __init__(self, claude_dir: Path | str | None = None):
        """
        Initialize TranscriptReader.

        Args:
            claude_dir: Path to Claude Code projects directory.
                       Defaults to ~/.claude/projects/
        """
        if claude_dir is None:
            self.claude_dir = self.DEFAULT_CLAUDE_DIR
        else:
            self.claude_dir = Path(claude_dir)

    def encode_project_path(self, project_path: str | Path) -> str:
        """
        Encode a project path to Claude Code's directory naming scheme.

        Claude encodes paths by replacing forward slashes with hyphens.
        Example: /home/user/myproject -> -home-user-myproject

        On macOS, paths may have /System/Volumes/Data prefix which is stripped
        to normalize the encoding.
        """
        path_str = str(Path(project_path).resolve())

        # Normalize macOS volume paths - strip /System/Volumes/Data prefix
        # This is the APFS volume mount point that macOS adds to paths
        if path_str.startswith("/System/Volumes/Data/"):
            path_str = path_str.replace("/System/Volumes/Data", "", 1)

        # Replace forward slashes with hyphens
        encoded = path_str.replace("/", "-")
        # Handle Windows paths (replace backslashes too)
        encoded = encoded.replace("\\", "-")
        return encoded

    def decode_project_path(self, encoded: str) -> str:
        """
        Decode Claude Code's directory name back to a path.

        Note: This is lossy - we can't distinguish between
        path separators and actual hyphens in directory names.
        """
        # Simple heuristic: leading hyphen is root /
        if encoded.startswith("-"):
            return "/" + encoded[1:].replace("-", "/")
        return encoded.replace("-", "/")

    def find_project_dir(self, project_path: str | Path) -> Path | None:
        """
        Find the Claude Code project directory for a given project path.

        Args:
            project_path: Path to the project

        Returns:
            Path to the Claude Code project directory, or None if not found
        """
        if not self.claude_dir.exists():
            return None

        encoded = self.encode_project_path(project_path)
        project_dir = self.claude_dir / encoded

        if project_dir.exists():
            return project_dir
        return None

    def list_project_dirs(self) -> Iterator[tuple[Path, str]]:
        """
        List all Claude Code project directories.

        Yields:
            (project_dir, decoded_path) tuples
        """
        if not self.claude_dir.exists():
            return

        for item in self.claude_dir.iterdir():
            if item.is_dir() and not item.name.startswith("."):
                decoded = self.decode_project_path(item.name)
                yield item, decoded

    def list_transcript_files(
        self, project_path: str | Path | None = None
    ) -> Iterator[Path]:
        """
        List all transcript JSONL files.

        Args:
            project_path: Optional project path to filter by.
                         If None, lists all transcripts.

        Yields:
            Paths to JSONL transcript files
        """
        if project_path:
            project_dir = self.find_project_dir(project_path)
            if project_dir:
                for jsonl in project_dir.glob("*.jsonl"):
                    yield jsonl
        else:
            if not self.claude_dir.exists():
                return
            for jsonl in self.claude_dir.rglob("*.jsonl"):
                yield jsonl

    def read_jsonl(self, path: Path) -> Iterator[dict[str, Any]]:
        """
        Read and parse a JSONL file.

        Args:
            path: Path to JSONL file

        Yields:
            Parsed JSON objects
        """
        if not path.exists():
            return

        with path.open("r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    yield json.loads(line)
                except json.JSONDecodeError:
                    continue

    def read_transcript(self, path: Path) -> TranscriptSession:
        """
        Read a transcript file into a TranscriptSession.

        Args:
            path: Path to transcript JSONL file

        Returns:
            TranscriptSession with parsed entries
        """
        entries: list[TranscriptEntry] = []
        session_id = path.stem  # UUID from filename

        for data in self.read_jsonl(path):
            entry = TranscriptEntry.from_jsonl_line(data)
            entries.append(entry)

            # Use session ID from first entry if available
            if entry.session_id and not session_id:
                session_id = entry.session_id

        session = TranscriptSession(
            session_id=session_id,
            path=path,
            entries=entries,
        )

        # Extract metadata from entries
        if entries:
            session.started_at = entries[0].timestamp
            session.ended_at = entries[-1].timestamp

            # Get first non-None cwd and git_branch
            for entry in entries:
                if entry.cwd and not session.cwd:
                    session.cwd = entry.cwd
                if entry.git_branch and not session.git_branch:
                    session.git_branch = entry.git_branch
                if entry.version and not session.version:
                    session.version = entry.version
                if session.cwd and session.git_branch and session.version:
                    break

        return session

    def read_session(
        self, session_id: str, project_path: str | None = None
    ) -> TranscriptSession | None:
        """
        Read a session by ID.

        Uses direct path construction as a fast path to avoid scanning all
        JSONL files (which can be 2,000+ files / 2+ GB on active machines).

        Args:
            session_id: Session UUID
            project_path: Optional project path for a targeted lookup

        Returns:
            TranscriptSession or None if not found
        """
        if self.claude_dir.exists():
            if project_path:
                # Single-directory targeted lookup
                encoded = self.encode_project_path(project_path)
                candidate = self.claude_dir / encoded / f"{session_id}.jsonl"
                if candidate.exists():
                    return self.read_transcript(candidate)
            else:
                # Try every project subdirectory with a direct filename lookup
                # O(number of projects), not O(number of JSONL files)
                for project_dir in self.claude_dir.iterdir():
                    if project_dir.is_dir():
                        candidate = project_dir / f"{session_id}.jsonl"
                        if candidate.exists():
                            return self.read_transcript(candidate)

        # Slow fallback: full scan (should rarely be needed)
        for path in self.list_transcript_files():
            if path.stem == session_id:
                return self.read_transcript(path)
        return None

    def list_sessions(
        self,
        project_path: str | Path | None = None,
        limit: int | None = None,
        since: datetime | None = None,
        deduplicate: bool = False,
    ) -> list[TranscriptSession]:
        """
        List available transcript sessions.

        Args:
            project_path: Optional project path to filter by
            limit: Maximum number of sessions to return
            since: Only sessions started after this time
            deduplicate: If True, remove context snapshot duplicates
                        (keeps longest session per unique start time)

        Returns:
            List of TranscriptSession objects, newest first
        """
        from datetime import timezone

        def normalize_dt(dt: datetime | None) -> datetime:
            """Normalize datetime to UTC for comparison."""
            if dt is None:
                return datetime.min.replace(tzinfo=timezone.utc)
            if dt.tzinfo is None:
                # Assume naive datetimes are UTC
                return dt.replace(tzinfo=timezone.utc)
            return dt.astimezone(timezone.utc)

        sessions: list[TranscriptSession] = []

        for path in self.list_transcript_files(project_path):
            session = self.read_transcript(path)

            # Filter by time
            if since and session.started_at:
                if normalize_dt(session.started_at) < normalize_dt(since):
                    continue

            sessions.append(session)

        # De-duplicate context snapshots if requested
        # Context snapshots have the same start time but different end times
        if deduplicate and sessions:
            sessions = self._deduplicate_context_snapshots(sessions)

        # Sort by start time, newest first (normalize for comparison)
        sessions.sort(key=lambda s: normalize_dt(s.started_at), reverse=True)

        if limit:
            sessions = sessions[:limit]

        return sessions

    def _deduplicate_context_snapshots(
        self, sessions: list[TranscriptSession]
    ) -> list[TranscriptSession]:
        """
        Remove duplicate context snapshots, keeping the longest per start time.

        Context snapshots occur when a conversation is resumed - Claude Code
        creates a new transcript file with the same start time but extended
        content. This keeps only the most complete version.

        Args:
            sessions: List of sessions to deduplicate

        Returns:
            Deduplicated list with longest session per start time
        """
        from collections import defaultdict

        # Group by start time (rounded to second for tolerance)
        by_start: dict[str, list[TranscriptSession]] = defaultdict(list)

        for session in sessions:
            if session.started_at:
                # Use ISO format truncated to seconds as key
                key = session.started_at.strftime("%Y-%m-%dT%H:%M:%S")
            else:
                # No start time, use session ID as unique key
                key = f"unknown-{session.session_id}"
            by_start[key].append(session)

        # Keep the longest session per start time
        deduplicated = []
        for start_key, group in by_start.items():
            if len(group) == 1:
                deduplicated.append(group[0])
            else:
                # Multiple sessions with same start - keep longest duration
                longest = max(
                    group,
                    key=lambda s: s.duration_seconds if s.duration_seconds else 0,
                )
                deduplicated.append(longest)

        return deduplicated

    def calculate_duration_metrics(
        self,
        sessions: list[TranscriptSession] | None = None,
        project_path: str | Path | None = None,
    ) -> dict[str, float]:
        """
        Calculate duration metrics accounting for overlaps and parallelism.

        Returns both wall clock time (actual elapsed) and total agent time
        (sum of all agent work, including parallel).

        Args:
            sessions: Sessions to analyze (or fetches all if None)
            project_path: Filter by project if fetching sessions

        Returns:
            dict with:
                - wall_clock_seconds: Actual elapsed time
                - total_agent_seconds: Sum of all agent durations
                - parallelism_factor: Ratio of agent time to wall clock
                - context_snapshot_count: Number of duplicate snapshots removed
                - subagent_count: Number of parallel subagents detected
        """
        if sessions is None:
            sessions = self.list_sessions(project_path=project_path)

        if not sessions:
            return {
                "wall_clock_seconds": 0.0,
                "total_agent_seconds": 0.0,
                "parallelism_factor": 1.0,
                "context_snapshot_count": 0,
                "subagent_count": 0,
            }

        # Calculate total agent time (simple sum)
        total_agent_seconds = sum(
            s.duration_seconds for s in sessions if s.duration_seconds
        )

        # Detect context snapshots vs subagents
        from collections import defaultdict

        by_start: dict[str, list[TranscriptSession]] = defaultdict(list)
        for session in sessions:
            if session.started_at:
                key = session.started_at.strftime("%Y-%m-%dT%H:%M:%S")
            else:
                key = f"unknown-{session.session_id}"
            by_start[key].append(session)

        # Count context snapshots (same start, different durations = snapshots)
        context_snapshot_count = sum(
            len(group) - 1 for group in by_start.values() if len(group) > 1
        )

        # Detect subagents (session IDs starting with "agent-")
        subagent_count = sum(1 for s in sessions if s.session_id.startswith("agent-"))

        # Calculate wall clock time using interval merging
        # This gives actual elapsed time accounting for overlaps
        intervals = []
        for session in sessions:
            if session.started_at and session.ended_at:
                intervals.append((session.started_at, session.ended_at))

        wall_clock_seconds = self._merge_intervals_duration(intervals)

        # Calculate parallelism factor
        parallelism_factor = (
            total_agent_seconds / wall_clock_seconds if wall_clock_seconds > 0 else 1.0
        )

        return {
            "wall_clock_seconds": wall_clock_seconds,
            "total_agent_seconds": total_agent_seconds,
            "parallelism_factor": parallelism_factor,
            "context_snapshot_count": context_snapshot_count,
            "subagent_count": subagent_count,
        }

    def _merge_intervals_duration(
        self, intervals: list[tuple[datetime, datetime]]
    ) -> float:
        """
        Merge overlapping time intervals and calculate total duration.

        This gives "wall clock time" - the actual elapsed time accounting
        for parallel/overlapping sessions.

        Args:
            intervals: List of (start, end) datetime tuples

        Returns:
            Total duration in seconds after merging overlaps
        """
        if not intervals:
            return 0.0

        # Sort by start time
        sorted_intervals = sorted(intervals, key=lambda x: x[0])

        # Merge overlapping intervals
        merged = [sorted_intervals[0]]
        for start, end in sorted_intervals[1:]:
            last_start, last_end = merged[-1]
            if start <= last_end:
                # Overlapping - extend the last interval
                merged[-1] = (last_start, max(last_end, end))
            else:
                # Non-overlapping - add new interval
                merged.append((start, end))

        # Sum durations of merged intervals
        total_seconds = sum((end - start).total_seconds() for start, end in merged)

        return total_seconds

    def find_sessions_for_branch(
        self,
        git_branch: str,
        project_path: str | Path | None = None,
    ) -> list[TranscriptSession]:
        """
        Find sessions that worked on a specific git branch.

        Args:
            git_branch: Git branch name to search for
            project_path: Optional project path to filter by

        Returns:
            List of matching sessions
        """
        matching = []

        for path in self.list_transcript_files(project_path):
            session = self.read_transcript(path)
            if session.git_branch == git_branch:
                matching.append(session)

        return matching

    def get_current_project_sessions(self) -> list[TranscriptSession]:
        """
        Get sessions for the current working directory.

        Returns:
            List of sessions for current project
        """
        cwd = Path.cwd()
        return self.list_sessions(project_path=cwd)


class TranscriptWatcher:
    """
    Watch for new/updated Claude Code transcripts.

    This can be used to actively track transcript changes
    and sync them to HtmlGraph sessions.
    """

    def __init__(
        self,
        reader: TranscriptReader | None = None,
        project_path: str | Path | None = None,
    ):
        """
        Initialize TranscriptWatcher.

        Args:
            reader: TranscriptReader instance
            project_path: Optional project path to watch
        """
        self.reader = reader or TranscriptReader()
        self.project_path = Path(project_path) if project_path else None
        self._known_sessions: dict[str, datetime] = {}

    def scan(self) -> list[TranscriptSession]:
        """
        Scan for new or updated transcripts.

        Returns:
            List of new/updated TranscriptSession objects
        """
        changed: list[TranscriptSession] = []

        for path in self.reader.list_transcript_files(self.project_path):
            session_id = path.stem
            mtime = datetime.fromtimestamp(path.stat().st_mtime)

            # Check if new or modified
            if session_id not in self._known_sessions:
                # New session
                session = self.reader.read_transcript(path)
                changed.append(session)
                self._known_sessions[session_id] = mtime
            elif self._known_sessions[session_id] < mtime:
                # Modified session
                session = self.reader.read_transcript(path)
                changed.append(session)
                self._known_sessions[session_id] = mtime

        return changed

    def get_latest(self) -> TranscriptSession | None:
        """Get the most recently modified transcript."""
        latest_path: Path | None = None
        latest_mtime: float = 0

        for path in self.reader.list_transcript_files(self.project_path):
            mtime = path.stat().st_mtime
            if mtime > latest_mtime:
                latest_mtime = mtime
                latest_path = path

        if latest_path:
            return self.reader.read_transcript(latest_path)
        return None
