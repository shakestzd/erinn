"""
SessionContextBuilder - Builds session start context for AI agents.

Extracts all context-building business logic from the session-start hook
into testable SDK methods. The hook becomes a thin wrapper that calls
these methods.

Architecture:
- SessionContextBuilder: Assembles complete session start context
- VersionChecker: Checks installed vs PyPI version
- GitHooksInstaller: Installs pre-commit hooks
- All methods are independently testable

Usage:
    from htmlgraph.session_context import SessionContextBuilder

    builder = SessionContextBuilder(graph_dir, project_dir)
    context = builder.build(session_id="sess-001")
    # Returns formatted Markdown string with all session context
"""

from __future__ import annotations

import asyncio
import json
import logging
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import TYPE_CHECKING, Any

logger = logging.getLogger(__name__)

if TYPE_CHECKING:
    pass


# ---------------------------------------------------------------------------
# Static context templates (moved from session-start.py)
# ---------------------------------------------------------------------------

HTMLGRAPH_VERSION_WARNING = """## HTMLGRAPH UPDATE AVAILABLE

**Installed:** {installed} -> **Latest:** {latest}

Update now to get the latest features and fixes:
```bash
uv pip install --upgrade htmlgraph
```

---

"""

HTMLGRAPH_PROCESS_NOTICE = """## HTMLGRAPH DEVELOPMENT PROCESS ACTIVE

**HtmlGraph is tracking this session. All activity is logged to HTML files.**

### Feature Creation Decision Framework

**Use this framework for EVERY user request:**

Create a **FEATURE** if ANY apply:
- >30 minutes work
- 3+ files
- New tests needed
- Multi-component impact
- Hard to revert
- Needs docs

Implement **DIRECTLY** if ALL apply:
- Single file
- <30 minutes
- Trivial change
- Easy to revert
- No tests needed

**When in doubt, CREATE A FEATURE.** Over-tracking is better than losing attribution.

---

### Quick Reference

**IMPORTANT:** Always use `uv run` when running htmlgraph commands.

**Check Status:**
```bash
uv run htmlgraph status
uv run htmlgraph feature list
uv run htmlgraph session list
```

**Feature Commands:**
- `uv run htmlgraph feature start <id>` - Start working on a feature
- `uv run htmlgraph feature complete <id>` - Mark feature as done
- `uv run htmlgraph feature primary <id>` - Set primary feature for attribution

**Track Creation (for multi-feature work):**
```python
from htmlgraph import SDK
sdk = SDK(agent="claude")

# Create track with spec and plan in one command
track = sdk.tracks.builder() \\
    .title("Feature Name") \\
    .priority("high") \\
    .with_spec(overview="...", requirements=[...]) \\
    .with_plan_phases([("Phase 1", ["Task 1 (2h)", ...])]) \\
    .create()

# Link features to track
feature = sdk.features.create("Feature") \\
    .set_track(track.id) \\
    .add_steps([...]) \\
    .save()
```

**See:** `docs/TRACK_BUILDER_QUICK_START.md` for complete track creation guide

**Session Management:**
- Sessions auto-start when you begin working
- Activities are attributed to in-progress features
- Session history preserved in `.htmlgraph/sessions/`

**Dashboard:**
```bash
uv run htmlgraph serve
# Open http://localhost:8080
```

**Key Files:**
- `.htmlgraph/features/` - Feature HTML files
- `.htmlgraph/sessions/` - Session HTML files with activity logs
- `index.html` - Dashboard (open in browser)
"""

TRACKER_WORKFLOW = """## HTMLGRAPH TRACKING WORKFLOW

**CRITICAL: Follow this checklist for EVERY session.**

### Session Start (DO THESE FIRST)
1. Check active features: `uv run htmlgraph status`
2. Review session context and decide what to work on
3. **DECIDE:** Create feature or implement directly?
   - Create FEATURE if ANY apply: >30min, 3+ files, needs tests, multi-component, hard to revert
   - Implement DIRECTLY if ALL apply: single file, <30min, trivial, easy to revert

### During Work (DO CONTINUOUSLY)
1. **Feature MUST be in-progress before writing code**
   - Start feature: `sdk.features.start("feature-id")` or `uv run htmlgraph feature start <id>`
2. **CRITICAL:** Mark each step complete IMMEDIATELY after finishing it:
   ```python
   from htmlgraph import SDK
   sdk = SDK(agent="claude")
   with sdk.features.edit("feature-id") as f:
       f.steps[0].completed = True  # First step done
       f.steps[1].completed = True  # Second step done
   ```
3. Document decisions as you make them
4. Test incrementally - don't wait until the end

### Session End (MUST DO BEFORE COMPLETING FEATURE)
1. **RUN TESTS:** All tests MUST pass
2. **VERIFY STEPS:** ALL feature steps marked complete
3. **CLEAN CODE:** Remove debug code, console.logs, TODOs
4. **COMMIT WORK:** Git commit IMMEDIATELY (include feature ID in message)
5. **COMPLETE FEATURE:** `sdk.features.complete("feature-id")` or `uv run htmlgraph feature complete <id>`

### SDK Usage (ALWAYS USE SDK, NEVER DIRECT FILE EDITS)
**FORBIDDEN:** `Write('/path/.htmlgraph/features/...', ...)` `Edit('/path/.htmlgraph/...')`
**REQUIRED:** Use SDK for ALL operations on `.htmlgraph/` files

```python
from htmlgraph import SDK
sdk = SDK(agent="claude")

# Create and work on features
feature = sdk.features.create("Title").set_priority("high").add_steps(["Step 1", "Step 2"]).save()
with sdk.features.edit("feature-id") as f:
    f.status = "done"

# Query and batch operations
high_priority = sdk.features.where(status="todo", priority="high")
sdk.features.batch_update(["feat-1", "feat-2"], {"status": "done"})
```

**For complete SDK documentation -> see `docs/AGENTS.md`**
"""

RESEARCH_FIRST_DEBUGGING = """## RESEARCH-FIRST DEBUGGING (IMPERATIVE)

**CRITICAL: NEVER implement solutions based on assumptions. ALWAYS research documentation first.**

This principle emerged from dogfooding HtmlGraph development. Violating it results in:
- Multiple trial-and-error attempts before researching
- Implementing "fixes" based on guesses instead of documentation
- Not using available research tools and agents
- Wasted time and context on wrong approaches

### The Research-First Workflow (ALWAYS FOLLOW)

1. **Research First** - Use `sdk.help()` to understand the API
   ```python
   from htmlgraph import SDK
   sdk = SDK(agent="claude")

   # ALWAYS START HERE
   print(sdk.help())               # Overview of all SDK methods
   print(sdk.help('tracks'))       # Tracks-specific help
   print(sdk.help('planning'))     # Planning workflow help
   print(sdk.help('features'))     # Feature collection help
   print(sdk.help('analytics'))    # Analytics methods
   ```

2. **Understand** - Read the help output carefully
   - Look for correct method signatures
   - Note parameter types and names
   - Understand return types
   - Find examples in the help text

3. **Implement** - Apply fix based on actual understanding
   - Use the correct API signature from help
   - Copy example patterns from help text
   - Test incrementally

4. **Validate** - Test to confirm the approach works
   - Run tests before and after
   - Verify behavior matches expectations

5. **Document** - Capture learning in HtmlGraph spike
   - Record what you learned
   - Note what the correct approach was
   - Help future debugging

### When You Get an Error

**WRONG APPROACH (what NOT to do):**
```python
# Error: "object has no attribute 'set_priority'"
# Response: Try track.with_priority() -> error
#           Try track._priority = "high" -> error
#           Try track.priority("high") -> error
```

**CORRECT APPROACH (what TO do):**
```python
# Error: "object has no attribute 'set_priority'"
# IMMEDIATE RESPONSE:
#   1. Stop and use sdk.help('tracks')
#   2. Read the help output
#   3. Look for correct method: create_track_from_plan() with requirements parameter
#   4. Implement based on actual API
#   5. Test and verify
```

### Remember

**"Fixing errors immediately by researching is faster than letting them accumulate through trial-and-error."**

Your context is precious. Use `sdk.help()` first, implement second, test third.
"""

ORCHESTRATOR_DIRECTIVES = """## ORCHESTRATOR DIRECTIVES (IMPERATIVE)

**YOU ARE THE ORCHESTRATOR.** Follow these directives:

### 1. ALWAYS DELEGATE - Even "Simple" Operations

**CRITICAL INSIGHT:** What looks like "one tool call" often becomes 2, 3, 4+ calls.

**ALWAYS delegate, even if you think it's simple:**
- "Just read one file" -> Delegate to Explorer
- "Just edit one file" -> Delegate to Coder
- "Just run tests" -> Delegate to Tester
- "Just search for X" -> Delegate to Explorer

**Why ALWAYS delegate:**
- Tool outputs are unknown until execution
- "One operation" often expands into many
- Each subagent has self-contained context
- Orchestrator only pays for: Task() call + Task output (not intermediate tool calls)
- Your context stays strategic, not filled with implementation details

**ONLY execute directly:**
- Task() - Delegation itself
- AskUserQuestion() - Clarifying with user
- TodoWrite() - Tracking work
- SDK operations - Creating features/work items

**Everything else -> DELEGATE.**

### 2. YOUR ONLY JOB: Provide Clear Task Descriptions

**You don't execute, you describe what needs executing.**

**Good delegation:**
```python
Task(
    prompt="Find all files in src/auth/ that handle JWT validation.
            List the files and explain what each one does.",
    subagent_type="Explore"
)

Task(
    prompt="Fix the bug in src/auth/jwt.py where tokens expire immediately.
            The issue is in the validate_token() function.
            Run tests after fixing to verify the fix works.",
    subagent_type="general-purpose"
)
```

**Your job:**
- Describe the task clearly
- Provide context the subagent needs
- Specify what success looks like
- Give enough detail for self-contained execution

**Not your job:**
- Execute the task yourself
- Guess how many tool calls it will take
- Read files to "check if it's simple"

### 3. CREATE Work Items FIRST
**Before ANY implementation, create features:**
```python
from htmlgraph import SDK
sdk = SDK(agent="claude-code")
feature = sdk.features.create("Feature Title").save()
```

**Why:** Work items enable learning, pattern detection, and progress tracking.

### 4. PARALLELIZE Independent Tasks
**Spawn multiple `Task()` calls in a single message when tasks don't depend on each other.**

### 5. CONTEXT COST MODEL

**Understand what uses YOUR context:**
- Task() call (tiny - just the prompt)
- Task output (small - summary from subagent)
- Subagent's tool calls (NOT in your context!)
- Subagent's file reads (NOT in your context!)
- Subagent's intermediate results (NOT in your context!)

**Your context is precious. Delegate everything.**

### 6. HTMLGRAPH DELEGATION PATTERN (CRITICAL)

**PROBLEM:** TaskOutput tool is unreliable - subagent results often can't be retrieved.

**SOLUTION:** Use HtmlGraph for subagent communication.

**Step 1 - Orchestrator delegates with reporting instructions:**

Include this in every Task prompt:
  CRITICAL - Report Results to HtmlGraph:
  from htmlgraph import SDK
  sdk = SDK(agent='explorer')
  sdk.spikes.create('Task Results').set_findings('...').save()

**Step 2 - Wait for Task completion.**

**Step 3 - Retrieve results from HtmlGraph:**

Use this command:
  uv run python -c "from htmlgraph import SDK; findings = SDK().spikes.get_latest(agent='explorer'); print(findings[0].findings if findings else 'No results')"

---

**YOU ARE THE ARCHITECT. SUBAGENTS ARE BUILDERS. DELEGATE EVERYTHING.**
"""


# ---------------------------------------------------------------------------
# VersionChecker - Checks installed vs latest version
# ---------------------------------------------------------------------------


class VersionChecker:
    """Check installed htmlgraph version against PyPI."""

    @staticmethod
    def get_installed_version() -> str | None:
        """Get the installed htmlgraph version."""
        try:
            result = subprocess.run(
                [
                    "uv",
                    "run",
                    "python",
                    "-c",
                    "import htmlgraph; print(htmlgraph.__version__)",
                ],
                capture_output=True,
                text=True,
                timeout=10,
            )
            if result.returncode == 0:
                return result.stdout.strip()
        except Exception:
            pass

        # Fallback to pip show
        try:
            result = subprocess.run(
                ["pip", "show", "htmlgraph"],
                capture_output=True,
                text=True,
                timeout=10,
            )
            if result.returncode == 0:
                for line in result.stdout.splitlines():
                    if line.startswith("Version:"):
                        return line.split(":", 1)[1].strip()
        except Exception:
            pass

        return None

    @staticmethod
    def get_latest_version() -> str | None:
        """Get the latest version from PyPI."""
        try:
            import urllib.request

            req = urllib.request.Request(
                "https://pypi.org/pypi/htmlgraph/json",
                headers={
                    "Accept": "application/json",
                    "User-Agent": "htmlgraph-version-check",
                },
            )
            with urllib.request.urlopen(req, timeout=5) as response:
                data = json.loads(response.read().decode())
                version: str | None = data.get("info", {}).get("version")
                return version
        except Exception:
            return None

    @staticmethod
    def compare_versions(installed: str, latest: str) -> bool:
        """
        Check if installed version is outdated.

        Returns True if installed < latest.
        """
        try:
            installed_parts = [int(x) for x in installed.split(".")]
            latest_parts = [int(x) for x in latest.split(".")]
            return installed_parts < latest_parts
        except ValueError:
            return installed != latest

    @classmethod
    def get_version_status(cls) -> dict[str, Any]:
        """
        Get version status information.

        Returns:
            Dict with keys: installed_version, latest_version, is_outdated
        """
        installed = cls.get_installed_version()
        latest = cls.get_latest_version()

        is_outdated = False
        if installed and latest and installed != latest:
            is_outdated = cls.compare_versions(installed, latest)

        return {
            "installed_version": installed,
            "latest_version": latest,
            "is_outdated": is_outdated,
        }


# ---------------------------------------------------------------------------
# GitHooksInstaller - Installs pre-commit hooks
# ---------------------------------------------------------------------------


class GitHooksInstaller:
    """Install pre-commit hooks from project scripts."""

    @staticmethod
    def install(project_dir: str | Path) -> bool:
        """
        Install pre-commit hooks if not already installed.

        Args:
            project_dir: Path to the project root

        Returns:
            True if hooks were installed or already exist
        """
        project_dir = Path(project_dir)
        hooks_source = project_dir / "scripts" / "hooks" / "pre-commit"
        hooks_target = project_dir / ".git" / "hooks" / "pre-commit"

        # Skip if not a git repo or hooks source doesn't exist
        if not (project_dir / ".git").exists():
            return False
        if not hooks_source.exists():
            return False

        # Skip if hook already installed and up to date
        if hooks_target.exists():
            try:
                if hooks_source.read_text() == hooks_target.read_text():
                    return True  # Already installed and current
            except Exception:
                pass

        # Install the hook
        try:
            shutil.copy2(hooks_source, hooks_target)
            hooks_target.chmod(0o755)
            return True
        except Exception:
            return False


# ---------------------------------------------------------------------------
# SessionContextBuilder - Assembles session start context
# ---------------------------------------------------------------------------


class SessionContextBuilder:
    """
    Builds complete session start context for AI agents.

    Extracts and encapsulates all context-building logic from the
    session-start hook into a testable, reusable class.

    Usage:
        builder = SessionContextBuilder(graph_dir, project_dir)
        context = builder.build(session_id="sess-001")
    """

    def __init__(
        self,
        graph_dir: str | Path,
        project_dir: str | Path,
    ) -> None:
        self.graph_dir = Path(graph_dir)
        self.project_dir = Path(project_dir)

        # Lazy-loaded components
        self._features: list[dict[str, Any]] | None = None
        self._stats: dict[str, Any] | None = None
        self._sessions: list[dict[str, Any]] | None = None

    # -------------------------------------------------------------------
    # Data loading
    # -------------------------------------------------------------------

    def _load_features(self) -> list[dict[str, Any]]:
        """Load features from the graph directory."""
        if self._features is not None:
            return self._features

        features_dir = self.graph_dir / "features"
        if not features_dir.exists():
            self._features = []
            return self._features

        try:
            from htmlgraph.converter import node_to_dict
            from htmlgraph.graph import HtmlGraph

            graph = HtmlGraph(features_dir, auto_load=True)
            self._features = [node_to_dict(node) for node in graph.nodes.values()]
        except Exception as e:
            logger.warning(f"Could not load features: {e}")
            self._features = []

        return self._features

    def _load_sessions(self) -> list[dict[str, Any]]:
        """Load sessions from the graph directory."""
        if self._sessions is not None:
            return self._sessions

        sessions_dir = self.graph_dir / "sessions"
        if not sessions_dir.exists():
            self._sessions = []
            return self._sessions

        try:
            from htmlgraph.converter import SessionConverter, session_to_dict

            converter = SessionConverter(sessions_dir)
            sessions = converter.load_all()
            self._sessions = [session_to_dict(s) for s in sessions]
        except Exception as e:
            logger.warning(f"Could not load sessions: {e}")
            self._sessions = []

        return self._sessions

    def get_feature_summary(self) -> tuple[list[dict[str, Any]], dict[str, Any]]:
        """
        Get features and calculate statistics.

        Returns:
            Tuple of (features_list, stats_dict)
        """
        features = self._load_features()

        stats: dict[str, Any] = {
            "total": len(features),
            "done": sum(1 for f in features if f.get("status") == "done"),
            "in_progress": sum(1 for f in features if f.get("status") == "in-progress"),
            "blocked": sum(1 for f in features if f.get("status") == "blocked"),
            "todo": sum(1 for f in features if f.get("status") == "todo"),
        }
        stats["percentage"] = (
            int(stats["done"] * 100 / stats["total"]) if stats["total"] > 0 else 0
        )

        self._stats = stats
        return features, stats

    def get_session_summary(self) -> dict[str, Any] | None:
        """Get the most recent ended session as a summary dict."""
        sessions = self._load_sessions()

        def parse_ts(value: str | None) -> datetime:
            if not value:
                return datetime.min.replace(tzinfo=timezone.utc)
            try:
                dt = datetime.fromisoformat(value.replace("Z", "+00:00"))
                if dt.tzinfo is None:
                    dt = dt.replace(tzinfo=timezone.utc)
                return dt
            except Exception:
                return datetime.min.replace(tzinfo=timezone.utc)

        ended = [s for s in sessions if s.get("status") == "completed"]
        if ended:
            ended.sort(
                key=lambda s: parse_ts(s.get("ended_at") or s.get("last_activity")),
                reverse=True,
            )
            return ended[0]
        return None

    def get_strategic_recommendations(self, agent_count: int = 1) -> dict[str, Any]:
        """
        Get strategic recommendations using SDK analytics.

        Args:
            agent_count: Number of agents for parallel work calculation

        Returns:
            Dict with recommendations, bottlenecks, parallel_capacity
        """
        try:
            from htmlgraph.sdk import SDK

            sdk = SDK(directory=self.graph_dir, agent="claude-code")

            recs = sdk.recommend_next_work(agent_count=agent_count)
            bottlenecks = sdk.find_bottlenecks(top_n=3)
            parallel = sdk.get_parallel_work(max_agents=5)

            return {
                "recommendations": recs[:3] if recs else [],
                "bottlenecks": bottlenecks,
                "parallel_capacity": parallel,
            }
        except Exception as e:
            logger.warning(f"Could not get strategic recommendations: {e}")
            return {
                "recommendations": [],
                "bottlenecks": [],
                "parallel_capacity": {
                    "max_parallelism": 0,
                    "ready_now": 0,
                    "total_ready": 0,
                },
            }

    def get_active_agents(self) -> list[dict[str, Any]]:
        """Get information about other active agents."""
        try:
            sessions_dir = self.graph_dir / "sessions"
            if not sessions_dir.exists():
                return []

            from htmlgraph.converter import SessionConverter

            converter = SessionConverter(sessions_dir)
            all_sessions = converter.load_all()

            active_agents = []
            for session in all_sessions:
                if session.status == "active":
                    active_agents.append(
                        {
                            "agent": session.agent,
                            "session_id": session.id,
                            "started_at": (
                                session.started_at.isoformat()
                                if session.started_at
                                else None
                            ),
                            "event_count": session.event_count,
                            "worked_on": (
                                list(session.worked_on)
                                if hasattr(session, "worked_on")
                                else []
                            ),
                        }
                    )

            return active_agents
        except Exception as e:
            logger.warning(f"Could not get active agents: {e}")
            return []

    def detect_feature_conflicts(
        self,
        features: list[dict[str, Any]] | None = None,
        active_agents: list[dict[str, Any]] | None = None,
    ) -> list[dict[str, Any]]:
        """
        Detect features being worked on by multiple agents simultaneously.

        Args:
            features: Features list (loaded if not provided)
            active_agents: Active agents list (loaded if not provided)

        Returns:
            List of conflict dicts with feature_id, title, agents
        """
        if features is None:
            features = self._load_features()
        if active_agents is None:
            active_agents = self.get_active_agents()

        conflicts: list[dict[str, Any]] = []

        try:
            # Build map of feature -> agents
            feature_agents: dict[str, list[str]] = {}

            for agent_info in active_agents:
                for feature_id in agent_info.get("worked_on", []):
                    if feature_id not in feature_agents:
                        feature_agents[feature_id] = []
                    feature_agents[feature_id].append(agent_info["agent"])

            # Find features with multiple agents
            for feature_id, agents in feature_agents.items():
                if len(agents) > 1:
                    feature = next(
                        (f for f in features if f.get("id") == feature_id), None
                    )
                    if feature:
                        conflicts.append(
                            {
                                "feature_id": feature_id,
                                "title": feature.get("title", "Unknown"),
                                "agents": agents,
                            }
                        )
        except Exception as e:
            logger.warning(f"Could not detect conflicts: {e}")

        return conflicts

    # -------------------------------------------------------------------
    # Git helpers
    # -------------------------------------------------------------------

    def get_head_commit(self) -> str | None:
        """Get current HEAD commit hash (short form)."""
        try:
            result = subprocess.run(
                ["git", "rev-parse", "--short", "HEAD"],
                capture_output=True,
                text=True,
                cwd=str(self.project_dir),
                timeout=5,
            )
            if result.returncode == 0:
                return result.stdout.strip()
        except Exception:
            pass
        return None

    def get_recent_commits(self, count: int = 5) -> list[str]:
        """Get recent git commits."""
        try:
            result = subprocess.run(
                ["git", "log", "--oneline", f"-{count}"],
                capture_output=True,
                text=True,
                cwd=str(self.project_dir),
                timeout=5,
            )
            if result.returncode == 0:
                return result.stdout.strip().split("\n")
        except Exception:
            pass
        return []

    # -------------------------------------------------------------------
    # Orchestrator mode
    # -------------------------------------------------------------------

    def activate_orchestrator_mode(self, session_id: str) -> tuple[bool, str]:
        """
        Activate orchestrator mode unconditionally.

        Plugin installed = Orchestrator mode enabled.
        This is the default operating mode for all htmlgraph projects.

        Args:
            session_id: Current session ID

        Returns:
            (is_active, enforcement_level)
        """
        try:
            from htmlgraph.orchestrator_mode import OrchestratorModeManager

            manager = OrchestratorModeManager(self.graph_dir)
            mode = manager.load()

            if mode.disabled_by_user:
                return False, "disabled"

            if not mode.enabled:
                manager.enable(session_id=session_id, level="strict", auto=True)
                return True, "strict"

            return True, mode.enforcement_level

        except Exception as e:
            logger.warning(f"Could not manage orchestrator mode: {e}")
            return False, "error"

    def _build_orchestrator_status(self, active: bool, level: str) -> str:
        """
        Build orchestrator status section for context.

        Args:
            active: Whether orchestrator mode is active
            level: Enforcement level

        Returns:
            Formatted status message
        """
        if not active or level == "disabled":
            return (
                "## ORCHESTRATOR MODE: INACTIVE\n\n"
                "Orchestrator mode has been manually disabled. "
                "This is unusual - the default mode is ORCHESTRATOR ENABLED.\n\n"
                "**Note:** Without orchestrator mode, you will fill context with "
                "implementation details instead of delegating to subagents.\n\n"
                "To re-enable: `uv run htmlgraph orchestrator enable`\n"
            )

        if level == "error":
            return (
                "## ORCHESTRATOR MODE: ERROR\n\n"
                "Warning: Could not determine orchestrator mode status. "
                "Proceeding without enforcement.\n"
            )

        enforcement_desc = (
            "blocks direct implementation"
            if level == "strict"
            else "provides guidance only"
        )

        return (
            f"## ORCHESTRATOR MODE: ACTIVE ({level} enforcement)\n\n"
            f"**Default operating mode** - Plugin installed = Orchestrator enabled.\n\n"
            f"**Enforcement:** This mode {enforcement_desc}. "
            f"Follow the delegation workflow in ORCHESTRATOR DIRECTIVES below.\n\n"
            f"**Why:** Orchestrator mode saves 80%+ context by delegating "
            f"implementation to subagents instead of executing directly.\n\n"
            f"To disable: `uv run htmlgraph orchestrator disable`\n"
            f"To change level: `uv run htmlgraph orchestrator set-level guidance`\n"
        )

    # -------------------------------------------------------------------
    # CIGS context
    # -------------------------------------------------------------------

    def get_cigs_context(self, session_id: str) -> str:
        """
        Generate CIGS (Computational Imperative Guidance System) context.

        Args:
            session_id: Current session ID

        Returns:
            Formatted CIGS context string
        """
        try:
            from htmlgraph.cigs import (
                AutonomyRecommender,
                PatternDetector,
                ViolationTracker,
            )

            tracker = ViolationTracker(self.graph_dir)
            tracker.set_session_id(session_id)

            recent_violations = tracker.get_recent_violations(sessions=5)
            session_summary = tracker.get_session_violations()

            # Convert violations to tool history format
            history = [
                {
                    "tool": v.tool,
                    "command": v.tool_params.get("command", ""),
                    "file_path": v.tool_params.get("file_path", ""),
                    "prompt": "",
                    "timestamp": v.timestamp,
                }
                for v in recent_violations
            ]

            detector = PatternDetector()
            patterns = detector.detect_all_patterns(history)

            # Calculate compliance history
            compliance_history = [
                max(
                    0.0,
                    1.0
                    - (
                        len([v for v in recent_violations if v.session_id == sid]) / 5.0
                    ),
                )
                for sid in set(v.session_id for v in recent_violations[-5:])
            ]

            recommender = AutonomyRecommender()
            autonomy = recommender.recommend(
                violations=session_summary,
                patterns=patterns,
                compliance_history=compliance_history if compliance_history else None,
            )

            # Build CIGS context
            context_parts = [
                "## CIGS Status (Computational Imperative Guidance System)",
                "",
                f"**Autonomy Level:** {autonomy.level.upper()}",
                f"**Messaging Intensity:** {autonomy.messaging_intensity}",
                f"**Enforcement Mode:** {autonomy.enforcement_mode}",
                "",
                f"**Reason:** {autonomy.reason}",
            ]

            if session_summary.total_violations > 0:
                context_parts.extend(
                    [
                        "",
                        "### Session Violations",
                        f"- Total violations: {session_summary.total_violations}",
                        f"- Compliance rate: {session_summary.compliance_rate:.0%}",
                        f"- Wasted tokens: {session_summary.total_waste_tokens}",
                    ]
                )

                if session_summary.circuit_breaker_triggered:
                    context_parts.append("- **Circuit breaker active** (3+ violations)")

            if patterns:
                context_parts.extend(
                    [
                        "",
                        "### Detected Anti-Patterns",
                    ]
                )
                for pattern in patterns:
                    context_parts.append(f"- **{pattern.name}**: {pattern.description}")
                    if pattern.delegation_suggestion:
                        context_parts.append(
                            f"  - Fix: {pattern.delegation_suggestion}"
                        )

            context_parts.extend(
                [
                    "",
                    "### Delegation Reminders",
                ]
            )

            if autonomy.level == "operator":
                context_parts.extend(
                    [
                        "STRICT MODE ACTIVE - You MUST delegate ALL operations except:",
                        "- Task() - Delegation itself",
                        "- AskUserQuestion() - User clarification",
                        "- TodoWrite() - Work tracking",
                        "- SDK operations - Feature/session management",
                        "",
                        "**ALL other operations MUST be delegated to subagents.**",
                    ]
                )
            elif autonomy.level == "collaborator":
                context_parts.extend(
                    [
                        "ACTIVE GUIDANCE - Focus on delegation:",
                        "- Exploration: Use spawn_gemini() (FREE)",
                        "- Code changes: Use spawn_codex() or Task()",
                        "- Git operations: Use spawn_copilot()",
                        "",
                        "Direct tool use should be rare and well-justified.",
                    ]
                )
            elif autonomy.level == "consultant":
                context_parts.extend(
                    [
                        "MODERATE GUIDANCE - Remember delegation patterns:",
                        "- Multi-file exploration -> spawn_gemini()",
                        "- Code changes with tests -> Task() or spawn_codex()",
                        "- Git operations -> spawn_copilot()",
                    ]
                )
            else:  # observer
                context_parts.extend(
                    [
                        "MINIMAL GUIDANCE - You're doing well!",
                        "Continue delegating as appropriate. Guidance will escalate if patterns change.",
                    ]
                )

            return "\n".join(context_parts)

        except Exception as e:
            logger.warning(f"Could not generate CIGS context: {e}")
            return ""

    # -------------------------------------------------------------------
    # System prompt
    # -------------------------------------------------------------------

    def load_system_prompt(self) -> str | None:
        """
        Load system prompt from plugin default or project override.

        Returns:
            System prompt content, or None if not available
        """
        try:
            from htmlgraph.system_prompts import SystemPromptManager

            manager = SystemPromptManager(self.graph_dir)
            prompt: str | None = manager.get_active()

            if prompt:
                logger.info(f"Loaded system prompt ({len(prompt)} chars)")
                return prompt
            else:
                logger.warning("System prompt not found")
                return None

        except ImportError:
            logger.warning("SDK not available, falling back to legacy loading")
            prompt_file = self.project_dir / ".claude" / "system-prompt.md"
            if not prompt_file.exists():
                return None
            try:
                content = prompt_file.read_text(encoding="utf-8")
                logger.info(f"Loaded system prompt ({len(content)} chars)")
                return content
            except Exception as e:
                logger.error(f"Failed to load system prompt: {e}")
                return None

        except Exception as e:
            logger.error(f"Failed to load system prompt via SDK: {e}")
            prompt_file = self.project_dir / ".claude" / "system-prompt.md"
            if prompt_file.exists():
                try:
                    content = prompt_file.read_text(encoding="utf-8")
                    logger.info(
                        f"Loaded system prompt via fallback ({len(content)} chars)"
                    )
                    return content
                except Exception:
                    pass
            return None

    def validate_token_count(
        self, prompt: str, max_tokens: int = 500
    ) -> tuple[bool, int]:
        """
        Validate prompt token count using SDK validator.

        Args:
            prompt: Text to count tokens for
            max_tokens: Maximum allowed tokens

        Returns:
            (is_valid, token_count) tuple
        """
        try:
            from htmlgraph.system_prompts import SystemPromptValidator

            result = SystemPromptValidator.validate(prompt, max_tokens=max_tokens)
            tokens = result["tokens"]
            is_valid = result["is_valid"]

            if not is_valid:
                logger.warning(f"Prompt exceeds budget: {tokens} > {max_tokens}")
            else:
                logger.info(f"Prompt tokens: {tokens}/{max_tokens}")

            return is_valid, tokens

        except ImportError:
            logger.debug("SDK validator not available, using fallback estimation")
            try:
                import tiktoken

                encoding = tiktoken.encoding_for_model("gpt-4")
                tokens = len(encoding.encode(prompt))
            except Exception:
                tokens = max(1, len(prompt) // 4)

            is_valid = tokens <= max_tokens
            return is_valid, tokens

        except Exception as e:
            logger.error(f"Token validation failed: {e}")
            tokens = max(1, len(prompt) // 4)
            is_valid = tokens <= max_tokens
            return is_valid, tokens

    # -------------------------------------------------------------------
    # Reflection context
    # -------------------------------------------------------------------

    def get_reflection_context(
        self, current_feature_id: str | None = None
    ) -> str | None:
        """
        Get computational reflections (pre-computed context from history).

        Args:
            current_feature_id: ID of the currently active feature

        Returns:
            Formatted reflection context string, or None
        """
        try:
            from htmlgraph.reflection import get_reflection_context
            from htmlgraph.sdk import SDK

            sdk = SDK(directory=self.graph_dir, agent="claude-code")
            return get_reflection_context(
                sdk,
                feature_id=current_feature_id,
                track=None,
            )
        except Exception as e:
            logger.warning(f"Could not compute reflections: {e}")
            return None

    # -------------------------------------------------------------------
    # Async parallelization helpers
    # -------------------------------------------------------------------

    async def _load_system_prompt_async(self) -> str | None:
        """Asynchronously load system prompt."""
        loop = asyncio.get_event_loop()
        return await loop.run_in_executor(None, self.load_system_prompt)

    async def _load_analytics_async(self) -> dict[str, Any]:
        """Asynchronously compute analytics and strategic recommendations."""

        def _compute() -> dict[str, Any]:
            try:
                return self.get_strategic_recommendations(agent_count=1)
            except Exception as e:
                logger.warning(f"Analytics computation failed: {e}")
                return {}

        loop = asyncio.get_event_loop()
        return await loop.run_in_executor(None, _compute)

    async def _parallelize_initialization(self) -> dict[str, Any]:
        """Parallelize system prompt loading and analytics computation."""
        try:
            system_prompt, analytics = await asyncio.gather(
                self._load_system_prompt_async(),
                self._load_analytics_async(),
                return_exceptions=False,
            )

            return {
                "system_prompt": system_prompt,
                "analytics": analytics or {},
                "parallelized": True,
            }
        except Exception as e:
            logger.warning(f"Parallel initialization failed: {e}")
            return {
                "system_prompt": None,
                "analytics": {},
                "parallelized": False,
            }

    def run_parallel_init(self) -> dict[str, Any]:
        """
        Run parallelized initialization using asyncio.

        Runs system prompt loading and analytics computation in parallel
        to reduce latency.

        Returns:
            Dict with system_prompt, analytics, and parallelized flag
        """
        try:
            loop = asyncio.new_event_loop()
            asyncio.set_event_loop(loop)
            result = loop.run_until_complete(self._parallelize_initialization())
            loop.close()
            return result
        except Exception as e:
            logger.warning(f"Could not run parallelized init: {e}")
            return {
                "system_prompt": None,
                "analytics": {},
                "parallelized": False,
            }

    # -------------------------------------------------------------------
    # Context assembly - the main build method
    # -------------------------------------------------------------------

    def build_version_section(self) -> str:
        """Build version warning section if outdated."""
        try:
            status = VersionChecker.get_version_status()
            if (
                status["is_outdated"]
                and status["installed_version"]
                and status["latest_version"]
            ):
                return HTMLGRAPH_VERSION_WARNING.format(
                    installed=status["installed_version"],
                    latest=status["latest_version"],
                ).strip()
        except Exception:
            pass
        return ""

    def build_features_section(
        self, features: list[dict[str, Any]], stats: dict[str, Any]
    ) -> str:
        """
        Build the features context section.

        Args:
            features: Feature dicts
            stats: Feature statistics

        Returns:
            Formatted features context
        """
        context_parts: list[str] = []

        active_features = [f for f in features if f.get("status") == "in-progress"]
        pending_features = [f for f in features if f.get("status") == "todo"]

        # Project status
        context_parts.append(
            f"## Project Status\n\n"
            f"**Progress:** {stats['done']}/{stats['total']} features complete "
            f"({stats['percentage']}%)\n"
            f"**Active:** {stats['in_progress']} | "
            f"**Blocked:** {stats['blocked']} | "
            f"**Todo:** {stats['todo']}"
        )

        # Active features
        if active_features:
            active_list = "\n".join(
                [f"- **{f['id']}**: {f['title']}" for f in active_features[:3]]
            )
            context_parts.append(
                f"## Active Features\n\n{active_list}\n\n"
                f"*Activity will be attributed to these features based on "
                f"file patterns and keywords.*"
            )
        else:
            context_parts.append(
                "## No Active Features\n\n"
                "Start working on a feature:\n"
                "```bash\n"
                "htmlgraph feature start <feature-id>\n"
                "```"
            )

        # Pending features
        if pending_features:
            pending_list = "\n".join(
                [f"- {f['id']}: {f['title'][:50]}" for f in pending_features[:5]]
            )
            context_parts.append(f"## Pending Features\n\n{pending_list}")

        return "\n\n".join(context_parts)

    def build_previous_session_section(self) -> str:
        """Build previous session summary section."""
        prev_session = self.get_session_summary()
        if not prev_session:
            return ""

        handoff_lines: list[str] = []
        if prev_session.get("handoff_notes"):
            handoff_lines.append(f"**Notes:** {prev_session.get('handoff_notes')}")
        if prev_session.get("recommended_next"):
            handoff_lines.append(
                f"**Recommended Next:** {prev_session.get('recommended_next')}"
            )
        blockers = prev_session.get("blockers") or []
        if blockers:
            handoff_lines.append(f"**Blockers:** {', '.join(blockers)}")

        handoff_text = ""
        if handoff_lines:
            handoff_text = "\n\n" + "\n".join(handoff_lines)

        worked_on = prev_session.get("worked_on", [])
        worked_on_text = ", ".join(worked_on[:3]) if worked_on else "N/A"
        if len(worked_on) > 3:
            worked_on_text += f" (+{len(worked_on) - 3} more)"

        return (
            f"## Previous Session\n\n"
            f"**Session:** {prev_session.get('id', 'unknown')[:12]}...\n"
            f"**Events:** {prev_session.get('event_count', 0)}\n"
            f"**Worked On:** {worked_on_text}"
            f"{handoff_text}"
        )

    def build_commits_section(self) -> str:
        """Build recent commits section."""
        recent_commits = self.get_recent_commits(count=5)
        if not recent_commits:
            return ""

        commits_text = "\n".join([f"  {commit}" for commit in recent_commits])
        return f"## Recent Commits\n\n{commits_text}"

    def build_strategic_insights_section(
        self, analytics: dict[str, Any] | None = None
    ) -> str:
        """
        Build strategic insights section.

        Args:
            analytics: Pre-computed analytics dict (loaded if not provided)

        Returns:
            Formatted insights section
        """
        if analytics is None:
            analytics = self.get_strategic_recommendations(agent_count=1)

        recommendations = analytics.get("recommendations", [])
        bottlenecks = analytics.get("bottlenecks", [])
        parallel = analytics.get("parallel_capacity", {})

        if (
            not recommendations
            and not bottlenecks
            and not parallel.get("max_parallelism", 0)
        ):
            return ""

        insights_parts: list[str] = []

        if bottlenecks:
            bottleneck_count = len(bottlenecks)
            bottleneck_list = "\n".join(
                [
                    f"  - **{bn['title']}** (blocks {bn['blocks_count']} tasks, "
                    f"impact: {bn['impact_score']:.1f})"
                    for bn in bottlenecks[:3]
                ]
            )
            insights_parts.append(
                f"#### Bottlenecks ({bottleneck_count})\n{bottleneck_list}"
            )

        if recommendations:
            rec_list = "\n".join(
                [
                    f"  {i + 1}. **{rec['title']}** (score: {rec['score']:.1f})\n"
                    f"     - Why: {', '.join(rec['reasons'][:2])}"
                    for i, rec in enumerate(recommendations[:3])
                ]
            )
            insights_parts.append(f"#### Top Recommendations\n{rec_list}")

        if parallel.get("max_parallelism", 0) > 0:
            ready_now = parallel.get("ready_now", 0)
            total_ready = parallel.get("total_ready", 0)
            insights_parts.append(
                f"#### Parallel Work\n"
                f"**Can work on {parallel['max_parallelism']} tasks simultaneously**\n"
                f"- {ready_now} tasks ready now\n"
                f"- {total_ready} total tasks ready"
            )

        if insights_parts:
            return "## Strategic Insights\n\n" + "\n\n".join(insights_parts)
        return ""

    def build_agents_section(self, active_agents: list[dict[str, Any]]) -> str:
        """Build active agents section."""
        other_agents = [a for a in active_agents if a["agent"] != "claude-code"]
        if not other_agents:
            return ""

        agents_list = "\n".join(
            [
                f"  - **{agent['agent']}**: {agent['event_count']} events, "
                f"working on {', '.join(agent.get('worked_on', [])[:2]) or 'unknown'}"
                for agent in other_agents[:5]
            ]
        )
        return (
            f"## Other Active Agents\n\n{agents_list}\n\n"
            f"**Note:** Coordinate with other agents to avoid conflicts."
        )

    def build_conflicts_section(self, conflicts: list[dict[str, Any]]) -> str:
        """Build conflict warnings section."""
        if not conflicts:
            return ""

        conflict_list = "\n".join(
            [
                f"  - **{conf['title']}** ({conf['feature_id']}): "
                f"{', '.join(conf['agents'])}"
                for conf in conflicts
            ]
        )
        return (
            f"## CONFLICT DETECTED\n\n"
            f"**Multiple agents working on the same features:**\n\n"
            f"{conflict_list}\n\n"
            f"**Action required:** Coordinate with other agents or choose a different feature."
        )

    @staticmethod
    def build_continuity_section() -> str:
        """Build session continuity instructions."""
        return (
            "## Session Continuity\n\n"
            "Greet the user with a brief status update:\n"
            "- Previous session summary (if any)\n"
            "- Current feature progress\n"
            "- What remains to be done\n"
            "- Ask what they'd like to work on next\n\n"
            "**Note:** Orchestrator directives are loaded via system prompt. "
            "Skills activate on-demand when needed."
        )

    def build(
        self,
        session_id: str,
        compute_async: bool = True,
    ) -> str:
        """
        Build complete session start context.

        This is the main entry point that assembles all context sections
        into a single Markdown string suitable for injection via
        additionalContext.

        Args:
            session_id: Current session ID
            compute_async: Use parallel async operations for performance

        Returns:
            Complete formatted Markdown context string
        """
        # Run parallelized initialization if requested
        if compute_async:
            init_results = self.run_parallel_init()
            system_prompt = init_results.get("system_prompt")
            analytics = init_results.get("analytics", {})
        else:
            system_prompt = self.load_system_prompt()
            analytics = {}

        # Load features
        features, stats = self.get_feature_summary()

        # Activate orchestrator mode
        orchestrator_active, orchestrator_level = self.activate_orchestrator_mode(
            session_id
        )

        # No features case - return minimal context
        if not features:
            context = f"""{HTMLGRAPH_PROCESS_NOTICE}

---

{ORCHESTRATOR_DIRECTIVES}

---

{TRACKER_WORKFLOW}

---

## No Features Found

Initialize HtmlGraph in this project:
```bash
uv pip install htmlgraph
htmlgraph init
```

Or create features manually in `.htmlgraph/features/`
"""
            return self._wrap_with_system_prompt(context, system_prompt, session_id)

        # Load analytics if not already computed
        if not analytics:
            analytics = self.get_strategic_recommendations(agent_count=1)

        # Get active agents and detect conflicts
        active_agents = self.get_active_agents()
        conflicts = self.detect_feature_conflicts(features, active_agents)

        # Get CIGS context
        cigs_context = self.get_cigs_context(session_id)

        # Build all context sections
        context_parts: list[str] = []

        # Version warning
        version_warning = self.build_version_section()
        if version_warning:
            context_parts.append(version_warning)

        # Static sections
        context_parts.append(HTMLGRAPH_PROCESS_NOTICE)
        if cigs_context:
            context_parts.append(cigs_context)
        context_parts.append(
            self._build_orchestrator_status(orchestrator_active, orchestrator_level)
        )
        context_parts.append(ORCHESTRATOR_DIRECTIVES)
        context_parts.append(TRACKER_WORKFLOW)
        context_parts.append(RESEARCH_FIRST_DEBUGGING)

        # Previous session
        prev_session_section = self.build_previous_session_section()
        if prev_session_section:
            context_parts.append(prev_session_section)

        # Features and status
        context_parts.append(self.build_features_section(features, stats))

        # Commits
        commits_section = self.build_commits_section()
        if commits_section:
            context_parts.append(commits_section)

        # Strategic insights
        insights_section = self.build_strategic_insights_section(analytics)
        if insights_section:
            context_parts.append(insights_section)

        # Active agents
        agents_section = self.build_agents_section(active_agents)
        if agents_section:
            context_parts.append(agents_section)

        # Conflicts
        conflicts_section = self.build_conflicts_section(conflicts)
        if conflicts_section:
            context_parts.append(conflicts_section)

        # Reflections
        active_features = [f for f in features if f.get("status") == "in-progress"]
        current_feature_id = active_features[0]["id"] if active_features else None
        reflection_context = self.get_reflection_context(current_feature_id)
        if reflection_context:
            context_parts.append(reflection_context)

        # Session continuity
        context_parts.append(self.build_continuity_section())

        context = "\n\n---\n\n".join(context_parts)

        return self._wrap_with_system_prompt(context, system_prompt, session_id)

    def _wrap_with_system_prompt(
        self,
        context: str,
        system_prompt: str | None,
        session_id: str,
    ) -> str:
        """
        Prepend system prompt to context if available.

        Args:
            context: Main context string
            system_prompt: System prompt string or None
            session_id: Session ID for source tracking

        Returns:
            Context with system prompt prepended (if available)
        """
        if not system_prompt:
            return context

        # Validate token count (informational only)
        self.validate_token_count(system_prompt, max_tokens=500)

        system_section = (
            "## SYSTEM PROMPT RESTORED (via SessionStart)\n\n"
            "This system prompt was injected at session startup to maintain "
            "context across compacts and session transitions.\n\n"
            "---\n\n"
            f"{system_prompt}\n\n"
            "---\n\n"
            "*This prompt persists across tool executions and survives "
            "compact/resume cycles.*"
        )

        return f"{system_section}\n\n---\n\n{context}"

    def build_status_summary(
        self, features: list[dict[str, Any]], stats: dict[str, Any]
    ) -> str:
        """
        Build a brief terminal status summary line.

        Args:
            features: Feature dicts
            stats: Feature statistics

        Returns:
            Single-line status summary
        """
        active_features = [f for f in features if f.get("status") == "in-progress"]
        pending_features = [f for f in features if f.get("status") == "todo"]

        if active_features:
            return (
                f"Feature: {active_features[0]['title'][:40]} | "
                f"Progress: {stats['done']}/{stats['total']} ({stats['percentage']}%)"
            )
        else:
            return (
                f"No active feature | Progress: {stats['done']}/{stats['total']} "
                f"({stats['percentage']}%) | {len(pending_features)} pending"
            )
