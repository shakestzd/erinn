from __future__ import annotations

"""
Session Handoff and Continuity - Phase 2 Feature 3

Provides cross-session continuity features:
- HandoffBuilder: Fluent API for creating handoffs with context
- SessionResume: Load and resume from previous session
- HandoffTracker: Track handoff effectiveness metrics
- ContextRecommender: Suggest files to keep context for next session

Usage:
    # End session with handoff
    sdk.sessions.end(
        summary="Completed OAuth integration",
        next_focus="Implement JWT token refresh",
        blockers=["Waiting for security review"],
        keep_context=["src/auth/", "docs/security"]
    )

    # Resume next session
    resumed = sdk.sessions.continue_from_last()
    if resumed:
        logger.info("%s", resumed.summary)
        logger.info("%s", resumed.recommended_files)
"""


import json
import logging
import subprocess
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from htmlgraph.models import Session
    from htmlgraph.sdk import SDK

logger = logging.getLogger(__name__)


@dataclass
class SessionResumeInfo:
    """Information loaded from previous session for resumption."""

    session_id: str
    agent: str
    ended_at: datetime | None
    summary: str | None  # handoff_notes
    next_focus: str | None  # recommended_next
    blockers: list[str]
    recommended_files: list[str]
    worked_on_features: list[str]
    recent_commits: list[dict[str, str]]
    time_since_last: timedelta | None


@dataclass
class HandoffMetrics:
    """Metrics for a session handoff."""

    handoff_id: str
    from_session_id: str
    to_session_id: str | None
    items_in_context: int
    items_accessed: int
    time_to_resume_seconds: int
    user_rating: int | None
    created_at: datetime
    resumed_at: datetime | None


class ContextRecommender:
    """
    Recommends files to keep context for next session.

    Uses git history to identify recently edited files and
    combines with feature context.
    """

    def __init__(self, repo_root: Path | None = None):
        """
        Initialize ContextRecommender.

        Args:
            repo_root: Root of git repository (auto-detected if None)
        """
        self.repo_root = repo_root or self._find_repo_root()

    def _find_repo_root(self) -> Path | None:
        """Find git repository root."""
        try:
            result = subprocess.run(
                ["git", "rev-parse", "--show-toplevel"],
                capture_output=True,
                text=True,
                check=True,
                timeout=5,
            )
            return Path(result.stdout.strip())
        except (
            subprocess.CalledProcessError,
            FileNotFoundError,
            subprocess.TimeoutExpired,
        ):
            return None

    def get_recent_files(
        self,
        since_minutes: int = 60,
        max_files: int = 10,
        exclude_patterns: list[str] | None = None,
    ) -> list[str]:
        """
        Get recently edited files from git.

        Args:
            since_minutes: Time window to check
            max_files: Maximum files to return
            exclude_patterns: Patterns to exclude (e.g., ["*.md", "tests/*"])

        Returns:
            List of file paths (relative to repo root)
        """
        if not self.repo_root:
            return []

        exclude_patterns = exclude_patterns or []

        try:
            # Get files changed in last N minutes
            result = subprocess.run(
                [
                    "git",
                    "log",
                    f"--since={since_minutes} minutes ago",
                    "--name-only",
                    "--pretty=format:",
                    "--diff-filter=AMR",  # Added, Modified, Renamed
                ],
                cwd=str(self.repo_root),
                capture_output=True,
                text=True,
                check=True,
                timeout=10,
            )

            # Parse files and deduplicate
            files = []
            seen = set()
            for line in result.stdout.strip().split("\n"):
                line = line.strip()
                if not line or line in seen:
                    continue

                # Check exclusion patterns
                excluded = False
                for pattern in exclude_patterns:
                    if self._matches_pattern(line, pattern):
                        excluded = True
                        break

                if not excluded:
                    files.append(line)
                    seen.add(line)

                if len(files) >= max_files:
                    break

            return files

        except (
            subprocess.CalledProcessError,
            subprocess.TimeoutExpired,
            FileNotFoundError,
        ):
            logger.debug("Could not get recent files from git")
            return []

    def _matches_pattern(self, path: str, pattern: str) -> bool:
        """Check if path matches glob pattern."""
        import fnmatch

        return fnmatch.fnmatch(path, pattern)

    def recommend_for_session(
        self,
        session: Session,
        max_files: int = 10,
    ) -> list[str]:
        """
        Recommend files to keep context for next session.

        Args:
            session: Session ending with handoff
            max_files: Maximum files to recommend

        Returns:
            List of recommended file paths
        """
        # Get recently edited files
        recent_files = self.get_recent_files(
            since_minutes=120,  # 2 hours
            max_files=max_files,
            exclude_patterns=["*.md", "*.txt", "*.json", "__pycache__/*"],
        )

        # TODO: Could enhance this by:
        # - Checking which files were Read/Edit in session activity log
        # - Prioritizing files related to features worked on
        # - Using file change frequency

        return recent_files[:max_files]


class HandoffBuilder:
    """
    Fluent builder for creating session handoffs.

    Example:
        handoff = HandoffBuilder(session)
            .add_summary("Completed OAuth integration")
            .add_next_focus("Implement JWT token refresh")
            .add_blockers(["Waiting for security review"])
            .add_context_files(["src/auth/oauth.py", "docs/security.md"])
            .build()
    """

    def __init__(self, session: Session):
        """
        Initialize HandoffBuilder.

        Args:
            session: Session to add handoff to
        """
        self.session = session
        self._summary: str | None = None
        self._next_focus: str | None = None
        self._blockers: list[str] = []
        self._context_files: list[str] = []

    def add_summary(self, summary: str) -> HandoffBuilder:
        """
        Add handoff summary (what was accomplished).

        Args:
            summary: Summary of what was done

        Returns:
            Self for chaining
        """
        self._summary = summary
        return self

    def add_next_focus(self, next_focus: str) -> HandoffBuilder:
        """
        Add recommended next focus.

        Args:
            next_focus: What should be done next

        Returns:
            Self for chaining
        """
        self._next_focus = next_focus
        return self

    def add_blocker(self, blocker: str) -> HandoffBuilder:
        """
        Add a single blocker.

        Args:
            blocker: Description of blocker

        Returns:
            Self for chaining
        """
        self._blockers.append(blocker)
        return self

    def add_blockers(self, blockers: list[str]) -> HandoffBuilder:
        """
        Add multiple blockers.

        Args:
            blockers: List of blocker descriptions

        Returns:
            Self for chaining
        """
        self._blockers.extend(blockers)
        return self

    def add_context_file(self, file_path: str) -> HandoffBuilder:
        """
        Add a file to keep context for.

        Args:
            file_path: Path to file

        Returns:
            Self for chaining
        """
        self._context_files.append(file_path)
        return self

    def add_context_files(self, file_paths: list[str]) -> HandoffBuilder:
        """
        Add multiple files to keep context for.

        Args:
            file_paths: List of file paths

        Returns:
            Self for chaining
        """
        self._context_files.extend(file_paths)
        return self

    def auto_recommend_context(
        self,
        recommender: ContextRecommender | None = None,
        max_files: int = 10,
    ) -> HandoffBuilder:
        """
        Automatically recommend context files.

        Args:
            recommender: ContextRecommender instance (creates new if None)
            max_files: Maximum files to recommend

        Returns:
            Self for chaining
        """
        if recommender is None:
            recommender = ContextRecommender()

        recommended = recommender.recommend_for_session(
            self.session, max_files=max_files
        )
        self._context_files.extend(recommended)
        return self

    def build(self) -> dict[str, Any]:
        """
        Build handoff data dictionary.

        Returns:
            Dictionary with handoff data
        """
        return {
            "handoff_notes": self._summary,
            "recommended_next": self._next_focus,
            "blockers": self._blockers,
            "recommended_context": self._context_files,
        }


class SessionResume:
    """
    Loads and presents context from previous session for resumption.
    """

    def __init__(self, sdk: SDK):
        """
        Initialize SessionResume.

        Args:
            sdk: SDK instance
        """
        self.sdk = sdk
        self.graph_dir = sdk._directory

    def get_last_session(self, agent: str | None = None) -> Session | None:
        """
        Get the most recent completed session.

        Args:
            agent: Filter by agent (None = any agent)

        Returns:
            Most recent session or None
        """
        from htmlgraph.converter import SessionConverter

        converter = SessionConverter(self.graph_dir / "sessions")
        sessions = converter.load_all()

        # Filter by ended sessions
        ended = [s for s in sessions if s.status == "completed"]

        # Filter by agent if specified
        if agent:
            ended = [s for s in ended if s.agent == agent]

        if not ended:
            return None

        # Sort by ended_at (most recent first)
        ended.sort(key=lambda s: s.ended_at or datetime.min, reverse=True)
        return ended[0]

    def build_resume_info(self, session: Session) -> SessionResumeInfo:
        """
        Build resumption information from a session.

        Args:
            session: Previous session

        Returns:
            SessionResumeInfo with context for resumption
        """
        # Calculate time since last session
        time_since = None
        if session.ended_at:
            time_since = datetime.now(timezone.utc) - session.ended_at

        # Get recent commits
        recent_commits = self._get_recent_commits(since_commit=session.start_commit)

        return SessionResumeInfo(
            session_id=session.id,
            agent=session.agent,
            ended_at=session.ended_at,
            summary=session.handoff_notes,
            next_focus=session.recommended_next,
            blockers=session.blockers,
            recommended_files=self._parse_json_list(session, "recommended_context"),
            worked_on_features=session.worked_on,
            recent_commits=recent_commits,
            time_since_last=time_since,
        )

    def _parse_json_list(self, session: Session, field_name: str) -> list[str]:
        """Parse JSON list field from session."""
        # Session model stores these as Python lists already
        value = getattr(session, field_name, None)
        if isinstance(value, list):
            return [str(item) for item in value]  # Ensure list[str]
        if isinstance(value, str):
            try:
                result = json.loads(value)
                return (
                    [str(item) for item in result] if isinstance(result, list) else []
                )
            except json.JSONDecodeError:
                return []
        return []

    def _get_recent_commits(
        self, since_commit: str | None = None, limit: int = 5
    ) -> list[dict[str, str]]:
        """
        Get recent git commits.

        Args:
            since_commit: Get commits since this one
            limit: Maximum commits to return

        Returns:
            List of commit dictionaries with hash, message, author, date
        """
        try:
            args = ["git", "log", f"-{limit}", "--oneline", "--no-merges"]
            if since_commit:
                args.append(f"{since_commit}..HEAD")

            result = subprocess.run(
                args,
                capture_output=True,
                text=True,
                check=True,
                timeout=5,
            )

            commits = []
            for line in result.stdout.strip().split("\n"):
                if not line:
                    continue
                parts = line.split(" ", 1)
                if len(parts) == 2:
                    commits.append({"hash": parts[0], "message": parts[1]})

            return commits

        except (
            subprocess.CalledProcessError,
            subprocess.TimeoutExpired,
            FileNotFoundError,
        ):
            logger.debug("Could not get recent commits")
            return []

    def format_resume_prompt(self, info: SessionResumeInfo) -> str:
        """
        Format a user-friendly resumption prompt.

        Args:
            info: Session resumption information

        Returns:
            Formatted multi-line string for display
        """
        lines = [
            "═" * 70,
            "CONTINUE FROM LAST SESSION",
            "═" * 70,
        ]

        # Session info
        if info.ended_at:
            lines.append(
                f'Last: {info.ended_at.strftime("%A %I:%M %p")} - "{info.summary or "No summary"}"'
            )
        else:
            lines.append(f"Last: {info.session_id}")

        # Time gap
        if info.time_since_last:
            hours = info.time_since_last.total_seconds() / 3600
            if hours < 1:
                time_str = (
                    f"{int(info.time_since_last.total_seconds() / 60)} minutes ago"
                )
            elif hours < 24:
                time_str = f"{int(hours)} hours ago"
            else:
                time_str = f"{int(hours / 24)} days ago"
            lines.append(f"Gap: {time_str}")

        lines.append("")

        # Next focus
        if info.next_focus:
            lines.append("Next Focus:")
            lines.append(f"  {info.next_focus}")
            lines.append("")

        # Blockers
        if info.blockers:
            lines.append("Blockers:")
            for blocker in info.blockers:
                lines.append(f"  ⚠️  {blocker}")
            lines.append("")

        # Context files
        if info.recommended_files:
            lines.append("Context to Load:")
            for i, file_path in enumerate(info.recommended_files[:5], 1):
                lines.append(f"  {i}. {file_path}")
            if len(info.recommended_files) > 5:
                lines.append(f"  ... and {len(info.recommended_files) - 5} more")
            lines.append("")

        # Features worked on
        if info.worked_on_features:
            lines.append("Features in Progress:")
            for feature_id in info.worked_on_features[:3]:
                lines.append(f"  - {feature_id}")
            if len(info.worked_on_features) > 3:
                lines.append(f"  ... and {len(info.worked_on_features) - 3} more")
            lines.append("")

        # Recent commits
        if info.recent_commits:
            lines.append("Recent Commits:")
            for commit in info.recent_commits[:3]:
                lines.append(f"  {commit['hash']} {commit['message']}")
            lines.append("")

        lines.append(
            "[L]oad context files  [O]pen in editor  [S]how summary  [C]ontinue"
        )

        return "\n".join(lines)


class HandoffTracker:
    """
    Tracks handoff effectiveness metrics.

    Records how helpful handoffs are and enables optimization.
    """

    def __init__(self, sdk: SDK):
        """
        Initialize HandoffTracker.

        Args:
            sdk: SDK instance
        """
        self.sdk = sdk
        self.db = getattr(sdk, "_db", None)

    def create_handoff(
        self,
        from_session_id: str,
        items_in_context: int = 0,
    ) -> str:
        """
        Create a handoff tracking record.

        Args:
            from_session_id: Session ending with handoff
            items_in_context: Number of context items provided

        Returns:
            Handoff ID
        """
        from htmlgraph.ids import generate_id

        handoff_id = generate_id("hand")

        if self.db and self.db.connection:
            # Ensure session exists in database (handles FK constraint)
            self.db._ensure_session_exists(from_session_id)

            cursor = self.db.connection.cursor()
            cursor.execute(
                """
                INSERT INTO handoff_tracking
                (handoff_id, from_session_id, items_in_context)
                VALUES (?, ?, ?)
            """,
                (handoff_id, from_session_id, items_in_context),
            )
            self.db.connection.commit()

        return handoff_id

    def resume_handoff(
        self,
        handoff_id: str,
        to_session_id: str,
        items_accessed: int = 0,
        time_to_resume_seconds: int = 0,
    ) -> bool:
        """
        Update handoff with resumption data.

        Args:
            handoff_id: Handoff ID
            to_session_id: New session ID
            items_accessed: Number of context items accessed
            time_to_resume_seconds: Time to resume work (seconds)

        Returns:
            True if successful
        """
        if not self.db or not self.db.connection:
            return False

        try:
            # Ensure to_session exists in database (handles FK constraint)
            self.db._ensure_session_exists(to_session_id)

            cursor = self.db.connection.cursor()
            cursor.execute(
                """
                UPDATE handoff_tracking
                SET to_session_id = ?,
                    items_accessed = ?,
                    time_to_resume_seconds = ?,
                    resumed_at = CURRENT_TIMESTAMP
                WHERE handoff_id = ?
            """,
                (to_session_id, items_accessed, time_to_resume_seconds, handoff_id),
            )
            self.db.connection.commit()
            return True
        except Exception as e:
            logger.error(f"Error updating handoff: {e}")
            return False

    def rate_handoff(self, handoff_id: str, rating: int) -> bool:
        """
        Rate handoff effectiveness (1-5 scale).

        Args:
            handoff_id: Handoff ID
            rating: Rating (1-5)

        Returns:
            True if successful
        """
        if not 1 <= rating <= 5:
            raise ValueError("Rating must be between 1 and 5")

        if not self.db or not self.db.connection:
            return False

        try:
            cursor = self.db.connection.cursor()
            cursor.execute(
                """
                UPDATE handoff_tracking
                SET user_rating = ?
                WHERE handoff_id = ?
            """,
                (rating, handoff_id),
            )
            self.db.connection.commit()
            return True
        except Exception as e:
            logger.error(f"Error rating handoff: {e}")
            return False

    def get_handoff_metrics(self, limit: int = 10) -> list[HandoffMetrics]:
        """
        Get recent handoff metrics.

        Args:
            limit: Maximum records to return

        Returns:
            List of HandoffMetrics
        """
        if not self.db or not self.db.connection:
            return []

        try:
            cursor = self.db.connection.cursor()
            cursor.execute(
                """
                SELECT handoff_id, from_session_id, to_session_id,
                       items_in_context, items_accessed, time_to_resume_seconds,
                       user_rating, created_at, resumed_at
                FROM handoff_tracking
                ORDER BY created_at DESC
                LIMIT ?
            """,
                (limit,),
            )

            metrics = []
            for row in cursor.fetchall():
                metrics.append(
                    HandoffMetrics(
                        handoff_id=row[0],
                        from_session_id=row[1],
                        to_session_id=row[2],
                        items_in_context=row[3],
                        items_accessed=row[4],
                        time_to_resume_seconds=row[5],
                        user_rating=row[6],
                        created_at=datetime.fromisoformat(row[7]),
                        resumed_at=(datetime.fromisoformat(row[8]) if row[8] else None),
                    )
                )

            return metrics
        except Exception as e:
            logger.error(f"Error getting handoff metrics: {e}")
            return []
