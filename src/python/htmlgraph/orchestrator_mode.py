"""
Orchestrator Mode State Management

Manages orchestrator mode state for enforcement hooks.
State is persisted in .htmlgraph/orchestrator-mode.json
"""

import json
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Literal

from pydantic import BaseModel

from htmlgraph.orchestrator_config import (
    get_effective_violation_count,
    load_orchestrator_config,
)


class OrchestratorMode(BaseModel):
    """Orchestrator mode state."""

    enabled: bool = False
    """Whether orchestrator mode is currently active."""

    activated_at: datetime | None = None
    """When orchestrator mode was last activated."""

    session_id: str | None = None
    """Session ID that activated orchestrator mode."""

    enforcement_level: Literal["strict", "guidance"] = "strict"
    """Enforcement level: 'strict' blocks operations, 'guidance' warns only."""

    auto_activated: bool = False
    """Whether mode was auto-activated (vs manually activated)."""

    disabled_by_user: bool = False
    """Whether user explicitly disabled mode (prevents auto-reactivation)."""

    violations: int = 0
    """Count of delegation violations in current session."""

    last_violation_at: datetime | None = None
    """Timestamp of most recent violation."""

    circuit_breaker_triggered: bool = False
    """Whether circuit breaker has been triggered (N+ violations, configurable)."""

    violation_history: list[dict[str, Any]] = []
    """Full history of violations with timestamps for time-based decay."""

    disabled_at: float | None = None
    """Unix timestamp when orchestrator was disabled (for auto-re-enable timeout)."""

    disable_scope: str = "session"
    """Scope of the disable: 'session', 'task', or 'timed'."""

    disable_prompt_count: int = 0
    """Number of prompts received since disable (used for task-scoped re-enable)."""

    auto_re_enable_after_minutes: int = 10
    """Minutes of inactivity before auto-re-enable (for timed/session scopes)."""

    def to_dict(self) -> dict[str, Any]:
        """Convert to dict for JSON serialization."""
        return {
            "enabled": self.enabled,
            "activated_at": (
                self.activated_at.isoformat() if self.activated_at else None
            ),
            "session_id": self.session_id,
            "enforcement_level": self.enforcement_level,
            "auto_activated": self.auto_activated,
            "disabled_by_user": self.disabled_by_user,
            "violations": self.violations,
            "last_violation_at": (
                self.last_violation_at.isoformat() if self.last_violation_at else None
            ),
            "circuit_breaker_triggered": self.circuit_breaker_triggered,
            "violation_history": self.violation_history,
            "disabled_at": self.disabled_at,
            "disable_scope": self.disable_scope,
            "disable_prompt_count": self.disable_prompt_count,
            "auto_re_enable_after_minutes": self.auto_re_enable_after_minutes,
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "OrchestratorMode":
        """Create from dict loaded from JSON."""
        activated_at = data.get("activated_at")
        if activated_at:
            # Handle both 'Z' suffix and '+00:00' timezone format
            if activated_at.endswith("Z"):
                activated_at = activated_at[:-1] + "+00:00"
            activated_at = datetime.fromisoformat(activated_at)

        last_violation_at = data.get("last_violation_at")
        if last_violation_at:
            # Handle both 'Z' suffix and '+00:00' timezone format
            if last_violation_at.endswith("Z"):
                last_violation_at = last_violation_at[:-1] + "+00:00"
            last_violation_at = datetime.fromisoformat(last_violation_at)

        return cls(
            enabled=data.get("enabled", False),
            activated_at=activated_at,
            session_id=data.get("session_id"),
            enforcement_level=data.get("enforcement_level", "strict"),
            auto_activated=data.get("auto_activated", False),
            disabled_by_user=data.get("disabled_by_user", False),
            violations=data.get("violations", 0),
            last_violation_at=last_violation_at,
            circuit_breaker_triggered=data.get("circuit_breaker_triggered", False),
            violation_history=data.get("violation_history", []),
            disabled_at=data.get("disabled_at"),
            disable_scope=data.get("disable_scope", "session"),
            disable_prompt_count=data.get("disable_prompt_count", 0),
            auto_re_enable_after_minutes=data.get("auto_re_enable_after_minutes", 10),
        )


class OrchestratorModeManager:
    """Manages orchestrator mode state with persistence."""

    def __init__(self, graph_dir: Path | str | None = None):
        """
        Initialize mode manager.

        Args:
            graph_dir: Path to .htmlgraph directory. If None, uses current directory's .htmlgraph
        """
        if graph_dir is None:
            graph_dir = Path.cwd() / ".htmlgraph"
        else:
            graph_dir = Path(graph_dir)

        self.graph_dir = graph_dir
        self.state_file = graph_dir / "orchestrator-mode.json"

    def load(self) -> OrchestratorMode:
        """
        Load orchestrator mode state from disk.

        Returns:
            OrchestratorMode with current state, or default (disabled) if file doesn't exist
        """
        if not self.state_file.exists():
            return OrchestratorMode()

        try:
            data = json.loads(self.state_file.read_text())
            return OrchestratorMode.from_dict(data)
        except Exception:
            # If file is corrupted, return default state
            return OrchestratorMode()

    def save(self, mode: OrchestratorMode) -> None:
        """
        Save orchestrator mode state to disk.

        Args:
            mode: OrchestratorMode to persist
        """
        # Ensure directory exists
        self.graph_dir.mkdir(parents=True, exist_ok=True)

        # Write state
        self.state_file.write_text(json.dumps(mode.to_dict(), indent=2))

    def is_enabled(self) -> bool:
        """Check if orchestrator mode is currently enabled."""
        mode = self.load()
        return mode.enabled

    def get_enforcement_level(self) -> Literal["strict", "guidance"]:
        """Get current enforcement level."""
        mode = self.load()
        return mode.enforcement_level

    def enable(
        self,
        session_id: str | None = None,
        level: Literal["strict", "guidance"] = "strict",
        auto: bool = False,
    ) -> OrchestratorMode:
        """
        Enable orchestrator mode.

        Args:
            session_id: Session ID enabling the mode
            level: Enforcement level ('strict' or 'guidance')
            auto: Whether this is an auto-activation

        Returns:
            Updated OrchestratorMode
        """
        mode = self.load()
        mode.enabled = True
        mode.activated_at = datetime.now(timezone.utc)
        mode.session_id = session_id
        mode.enforcement_level = level
        mode.auto_activated = auto
        mode.disabled_by_user = False
        mode.disabled_at = None
        mode.disable_scope = "session"
        mode.disable_prompt_count = 0
        self.save(mode)
        return mode

    def disable(
        self,
        by_user: bool = False,
        scope: str = "session",
        auto_re_enable_after_minutes: int = 10,
    ) -> OrchestratorMode:
        """
        Disable orchestrator mode.

        Args:
            by_user: Whether user explicitly disabled (prevents auto-reactivation)
            scope: Disable scope — 'session', 'task', or 'timed'
            auto_re_enable_after_minutes: Minutes before auto-re-enable (for
                'timed' and 'session' scopes)

        Returns:
            Updated OrchestratorMode
        """
        mode = self.load()
        mode.enabled = False
        if by_user:
            mode.disabled_by_user = True
        mode.disabled_at = time.time()
        mode.disable_scope = scope
        mode.disable_prompt_count = 0
        mode.auto_re_enable_after_minutes = auto_re_enable_after_minutes
        self.save(mode)
        return mode

    def set_level(self, level: Literal["strict", "guidance"]) -> OrchestratorMode:
        """
        Change enforcement level without enabling/disabling.

        Args:
            level: New enforcement level

        Returns:
            Updated OrchestratorMode
        """
        mode = self.load()
        mode.enforcement_level = level
        self.save(mode)
        return mode

    def can_auto_activate(self) -> bool:
        """
        Check if auto-activation is allowed.

        Auto-activation is blocked if user explicitly disabled mode.

        Returns:
            True if auto-activation is allowed
        """
        mode = self.load()
        return not mode.disabled_by_user

    def status(self) -> dict[str, Any]:
        """
        Get human-readable status.

        Returns:
            Dict with status information
        """
        mode = self.load()
        return {
            "enabled": mode.enabled,
            "enforcement_level": mode.enforcement_level,
            "activated_at": (
                mode.activated_at.strftime("%Y-%m-%d %H:%M:%S")
                if mode.activated_at
                else None
            ),
            "auto_activated": mode.auto_activated,
            "disabled_by_user": mode.disabled_by_user,
            "violations": mode.violations,
            "circuit_breaker_triggered": mode.circuit_breaker_triggered,
        }

    def increment_violation(self, tool: str | None = None) -> OrchestratorMode:
        """
        Increment violation counter and update timestamp.

        Uses configurable thresholds and time-based decay.

        Args:
            tool: Optional tool name that caused violation

        Returns:
            Updated OrchestratorMode with incremented violations
        """
        mode = self.load()
        config = load_orchestrator_config()

        # Add to violation history with timestamp
        violation = {
            "timestamp": datetime.now(timezone.utc).isoformat(),
            "tool": tool,
        }
        mode.violation_history.append(violation)

        # Calculate effective violation count with decay and collapsing
        effective_count = get_effective_violation_count(mode.violation_history, config)

        # Update counters
        mode.violations = effective_count
        mode.last_violation_at = datetime.now(timezone.utc)

        # Trigger circuit breaker if threshold reached (configurable)
        threshold = config.thresholds.circuit_breaker_violations
        if effective_count >= threshold:
            mode.circuit_breaker_triggered = True

        self.save(mode)
        return mode

    def reset_violations(self) -> OrchestratorMode:
        """
        Reset violation counter and circuit breaker.

        Returns:
            Updated OrchestratorMode with reset violations
        """
        mode = self.load()
        mode.violations = 0
        mode.last_violation_at = None
        mode.circuit_breaker_triggered = False
        mode.violation_history = []
        self.save(mode)
        return mode

    def increment_prompt_count(self) -> None:
        """Increment the prompt counter since orchestrator was disabled."""
        mode = self.load()
        if not mode.enabled and mode.disabled_by_user:
            mode.disable_prompt_count += 1
            self.save(mode)

    def check_auto_re_enable(self) -> tuple[bool, str]:
        """Check if orchestrator should auto-re-enable.

        Returns:
            Tuple of (re_enabled, reason) where re_enabled is True if the
            orchestrator was re-enabled, and reason describes why.
        """
        mode = self.load()
        if mode.enabled or not mode.disabled_by_user:
            return False, "already_enabled"

        if mode.disable_scope == "task":
            if mode.disable_prompt_count >= 1:
                self.enable(auto=True)
                return True, "task_scope_complete"

        if mode.disabled_at is not None:
            elapsed_minutes = (time.time() - mode.disabled_at) / 60
            if elapsed_minutes >= mode.auto_re_enable_after_minutes:
                self.enable(auto=True)
                return True, f"timeout_{int(elapsed_minutes)}m"

        return False, "still_disabled"

    def is_circuit_breaker_triggered(self) -> bool:
        """
        Check if circuit breaker is currently triggered.

        Returns:
            True if circuit breaker is active
        """
        mode = self.load()
        return mode.circuit_breaker_triggered

    def get_violation_count(self) -> int:
        """
        Get current violation count (with time-based decay applied).

        Returns:
            Effective number of violations in current session
        """
        mode = self.load()
        config = load_orchestrator_config()

        # Return effective count with decay and collapsing
        return get_effective_violation_count(mode.violation_history, config)
