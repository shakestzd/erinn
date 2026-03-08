"""
Real-Time Cost Monitoring & Alerts for HtmlGraph - Phase 3.1

Provides real-time token consumption tracking, cost calculation, and alert generation.

Features:
- Real-time token consumption tracking per session
- Cost calculation based on model rates from config
- Cost breakdown by model, agent, and tool type
- Alert generation with <1s latency via WebSocket
- Cost threshold detection (80% budget, trajectory overage, model overage)
- 5% tracking accuracy target

Architecture:
- CostMonitor: Core monitoring service
- CostAlert: Alert data model
- CostBreakdown: Cost analysis by dimension
- Integration with PostToolUse hook for token tracking
- WebSocket streaming for real-time alerts

Design Reference:
- Phase 3.1: Real-Time Cost Monitoring & Alerts
- WebSocket foundation from api/websocket.py
- CostCalculator from cigs/cost.py
"""

import json
import logging
import sqlite3
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from htmlgraph.db.pragmas import apply_sync_pragmas

logger = logging.getLogger(__name__)


@dataclass
class TokenCost:
    """Token consumption record."""

    timestamp: datetime
    tool_name: str
    model: str
    input_tokens: int
    output_tokens: int
    total_tokens: int
    cost_usd: float
    session_id: str
    event_id: str | None = None
    agent_id: str | None = None
    subagent_type: str | None = None

    def to_dict(self) -> dict[str, Any]:
        """Convert to dictionary."""
        return {
            "timestamp": self.timestamp.isoformat(),
            "tool_name": self.tool_name,
            "model": self.model,
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "total_tokens": self.total_tokens,
            "cost_usd": self.cost_usd,
            "session_id": self.session_id,
            "event_id": self.event_id,
            "agent_id": self.agent_id,
            "subagent_type": self.subagent_type,
        }


@dataclass
class CostAlert:
    """Cost alert data model."""

    alert_id: str
    alert_type: str  # "budget_warning", "trajectory_overage", "model_overage", "breach"
    session_id: str
    timestamp: datetime
    message: str
    current_cost_usd: float
    budget_usd: float | None = None
    predicted_cost_usd: float | None = None
    model: str | None = None
    severity: str = "warning"  # "info", "warning", "critical"
    acknowledged: bool = False

    def to_dict(self) -> dict[str, Any]:
        """Convert to dictionary."""
        return {
            "alert_id": self.alert_id,
            "alert_type": self.alert_type,
            "session_id": self.session_id,
            "timestamp": self.timestamp.isoformat(),
            "message": self.message,
            "current_cost_usd": self.current_cost_usd,
            "budget_usd": self.budget_usd,
            "predicted_cost_usd": self.predicted_cost_usd,
            "model": self.model,
            "severity": self.severity,
            "acknowledged": self.acknowledged,
        }


@dataclass
class CostBreakdown:
    """Cost breakdown analysis by dimensions."""

    by_model: dict[str, float] = field(default_factory=dict)
    by_tool: dict[str, float] = field(default_factory=dict)
    by_agent: dict[str, float] = field(default_factory=dict)
    by_subagent_type: dict[str, float] = field(default_factory=dict)
    total_cost_usd: float = 0.0
    total_tokens: int = 0
    session_count: int = 0

    def to_dict(self) -> dict[str, Any]:
        """Convert to dictionary."""
        return asdict(self)


class CostMonitor:
    """
    Real-time cost monitoring service.

    Tracks token consumption, calculates costs, and generates alerts.
    """

    def __init__(self, db_path: str | None = None, config_path: str | None = None):
        """
        Initialize CostMonitor.

        Args:
            db_path: Path to SQLite database
            config_path: Path to cost_models.json configuration
        """
        if db_path is None:
            db_path = str(Path.home() / ".htmlgraph" / "htmlgraph.db")

        self.db_path = db_path
        self.config = self._load_config(config_path)
        self.connection: sqlite3.Connection | None = None
        self._alert_cache: dict[str, CostAlert] = {}
        self._session_costs: dict[str, dict[str, Any]] = {}

    def _load_config(self, config_path: str | None = None) -> dict[str, Any]:
        """Load cost model configuration."""
        if config_path is None:
            # Try to find config_path relative to this module
            module_dir = Path(__file__).parent.parent
            config_path = str(module_dir / "config" / "cost_models.json")

        try:
            with open(config_path) as f:
                config: dict[str, Any] = json.load(f)
                return config
        except FileNotFoundError:
            logger.warning(
                f"Cost models config not found at {config_path}, using defaults"
            )
            return self._default_config()

    def _default_config(self) -> dict[str, Any]:
        """Return default cost configuration."""
        return {
            "models": {
                "claude-haiku-4-5-20251001": {
                    "name": "Claude Haiku",
                    "input_cost_per_mtok": 0.80,
                    "output_cost_per_mtok": 4.00,
                },
                "claude-sonnet-4-20250514": {
                    "name": "Claude Sonnet",
                    "input_cost_per_mtok": 3.00,
                    "output_cost_per_mtok": 15.00,
                },
                "claude-opus-4-1-20250805": {
                    "name": "Claude Opus",
                    "input_cost_per_mtok": 15.00,
                    "output_cost_per_mtok": 75.00,
                },
            },
            "defaults": {
                "input_cost_per_mtok": 2.00,
                "output_cost_per_mtok": 10.00,
            },
        }

    def connect(self) -> sqlite3.Connection:
        """Connect to database."""
        if self.connection is None:
            self.connection = sqlite3.connect(self.db_path)
            self.connection.row_factory = sqlite3.Row
            apply_sync_pragmas(self.connection)
        return self.connection

    def disconnect(self) -> None:
        """Close database connection."""
        if self.connection:
            self.connection.close()
            self.connection = None

    def calculate_cost_usd(
        self, model: str, input_tokens: int, output_tokens: int
    ) -> float:
        """
        Calculate cost in USD for token usage.

        Args:
            model: Model identifier (e.g., "claude-haiku-4-5-20251001")
            input_tokens: Number of input tokens
            output_tokens: Number of output tokens

        Returns:
            Cost in USD
        """
        models = self.config.get("models", {})
        defaults = self.config.get("defaults", {})

        if model in models:
            model_config = models[model]
        else:
            model_config = defaults

        input_cost_per_mtok: float = model_config.get("input_cost_per_mtok", 0.0)
        output_cost_per_mtok: float = model_config.get("output_cost_per_mtok", 0.0)
        input_cost = (input_tokens / 1_000_000) * input_cost_per_mtok
        output_cost = (output_tokens / 1_000_000) * output_cost_per_mtok

        return float(input_cost + output_cost)

    def track_token_usage(
        self,
        session_id: str,
        event_id: str,
        tool_name: str,
        model: str,
        input_tokens: int,
        output_tokens: int,
        agent_id: str | None = None,
        subagent_type: str | None = None,
    ) -> TokenCost:
        """
        Track token usage and record in database.

        Args:
            session_id: Session identifier
            event_id: Event identifier
            tool_name: Name of tool used
            model: Model used for processing
            input_tokens: Number of input tokens
            output_tokens: Number of output tokens
            agent_id: Optional agent identifier
            subagent_type: Optional subagent type

        Returns:
            TokenCost record
        """
        cost_usd = self.calculate_cost_usd(model, input_tokens, output_tokens)
        timestamp = datetime.now(timezone.utc)

        token_cost = TokenCost(
            timestamp=timestamp,
            tool_name=tool_name,
            model=model,
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            total_tokens=input_tokens + output_tokens,
            cost_usd=cost_usd,
            session_id=session_id,
            event_id=event_id,
            agent_id=agent_id,
            subagent_type=subagent_type,
        )

        # Record in database
        self._store_token_cost(token_cost)

        # Update session cost tracking
        self._update_session_cost(session_id, token_cost)

        # Check for alerts
        self._check_alerts(session_id, token_cost)

        return token_cost

    def _store_token_cost(self, token_cost: TokenCost) -> None:
        """Store token cost in database."""
        conn = self.connect()
        cursor = conn.cursor()

        cursor.execute(
            """
            INSERT INTO cost_events (
                event_id, session_id, tool_name, model,
                input_tokens, output_tokens, total_tokens,
                cost_usd, agent_id, subagent_type, timestamp
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                token_cost.event_id,
                token_cost.session_id,
                token_cost.tool_name,
                token_cost.model,
                token_cost.input_tokens,
                token_cost.output_tokens,
                token_cost.total_tokens,
                token_cost.cost_usd,
                token_cost.agent_id,
                token_cost.subagent_type,
                token_cost.timestamp.isoformat(),
            ),
        )
        conn.commit()

    def _update_session_cost(self, session_id: str, token_cost: TokenCost) -> None:
        """Update session cost tracking in memory and database."""
        if session_id not in self._session_costs:
            self._session_costs[session_id] = {
                "total_cost_usd": 0.0,
                "total_tokens": 0,
                "by_model": {},
                "by_tool": {},
            }

        session_data = self._session_costs[session_id]
        session_data["total_cost_usd"] += token_cost.cost_usd
        session_data["total_tokens"] += token_cost.total_tokens

        # Track by model
        model = token_cost.model
        if model not in session_data["by_model"]:
            session_data["by_model"][model] = 0.0
        session_data["by_model"][model] += token_cost.cost_usd

        # Track by tool
        tool = token_cost.tool_name
        if tool not in session_data["by_tool"]:
            session_data["by_tool"][tool] = 0.0
        session_data["by_tool"][tool] += token_cost.cost_usd

        # Update database session record
        conn = self.connect()
        cursor = conn.cursor()
        cursor.execute(
            """
            UPDATE sessions
            SET total_tokens_used = ?, metadata = ?
            WHERE session_id = ?
            """,
            (
                session_data["total_tokens"],
                json.dumps(
                    {
                        "cost_breakdown": session_data,
                        "updated_at": datetime.now(timezone.utc).isoformat(),
                    }
                ),
                session_id,
            ),
        )
        conn.commit()

    def get_session_cost(self, session_id: str) -> dict[str, Any]:
        """Get total cost for a session."""
        if session_id in self._session_costs:
            return self._session_costs[session_id]

        # Query from database
        conn = self.connect()
        cursor = conn.cursor()
        cursor.execute(
            """
            SELECT SUM(cost_usd) as total_cost, SUM(total_tokens) as total_tokens,
                   COUNT(DISTINCT model) as model_count
            FROM cost_events WHERE session_id = ?
            """,
            (session_id,),
        )
        row = cursor.fetchone()

        if row:
            return {
                "total_cost_usd": row["total_cost"] or 0.0,
                "total_tokens": row["total_tokens"] or 0,
                "model_count": row["model_count"] or 0,
            }

        return {"total_cost_usd": 0.0, "total_tokens": 0, "model_count": 0}

    def get_cost_breakdown(self, session_id: str) -> CostBreakdown:
        """Get detailed cost breakdown for a session."""
        conn = self.connect()
        cursor = conn.cursor()

        # Get totals
        cursor.execute(
            """
            SELECT SUM(cost_usd) as total_cost, SUM(total_tokens) as total_tokens
            FROM cost_events WHERE session_id = ?
            """,
            (session_id,),
        )
        row = cursor.fetchone()
        total_cost = row["total_cost"] or 0.0
        total_tokens = row["total_tokens"] or 0

        # By model
        cursor.execute(
            """
            SELECT model, SUM(cost_usd) as cost FROM cost_events
            WHERE session_id = ? GROUP BY model
            """,
            (session_id,),
        )
        by_model = {row["model"]: row["cost"] for row in cursor.fetchall()}

        # By tool
        cursor.execute(
            """
            SELECT tool_name, SUM(cost_usd) as cost FROM cost_events
            WHERE session_id = ? GROUP BY tool_name
            """,
            (session_id,),
        )
        by_tool = {row["tool_name"]: row["cost"] for row in cursor.fetchall()}

        # By agent
        cursor.execute(
            """
            SELECT agent_id, SUM(cost_usd) as cost FROM cost_events
            WHERE session_id = ? AND agent_id IS NOT NULL GROUP BY agent_id
            """,
            (session_id,),
        )
        by_agent = {row["agent_id"]: row["cost"] for row in cursor.fetchall()}

        # By subagent type
        cursor.execute(
            """
            SELECT subagent_type, SUM(cost_usd) as cost FROM cost_events
            WHERE session_id = ? AND subagent_type IS NOT NULL GROUP BY subagent_type
            """,
            (session_id,),
        )
        by_subagent_type = {
            row["subagent_type"]: row["cost"] for row in cursor.fetchall()
        }

        return CostBreakdown(
            by_model=by_model,
            by_tool=by_tool,
            by_agent=by_agent,
            by_subagent_type=by_subagent_type,
            total_cost_usd=total_cost,
            total_tokens=total_tokens,
            session_count=1,
        )

    def _check_alerts(self, session_id: str, token_cost: TokenCost) -> None:
        """Check if cost triggers any alerts."""
        conn = self.connect()
        cursor = conn.cursor()

        # Get session budget and cost info
        cursor.execute(
            """
            SELECT cost_budget, cost_threshold_breached FROM sessions WHERE session_id = ?
            """,
            (session_id,),
        )
        session_row = cursor.fetchone()

        if not session_row or not session_row["cost_budget"]:
            return  # No budget set

        budget_usd = session_row["cost_budget"]
        session_cost = self.get_session_cost(session_id)
        current_cost = session_cost["total_cost_usd"]

        # Check 80% budget warning
        if current_cost >= budget_usd * 0.8 and current_cost < budget_usd * 0.9:
            self._create_alert(
                session_id=session_id,
                alert_type="budget_warning",
                message=f"Cost at 80% of budget: ${current_cost:.2f} of ${budget_usd:.2f}",
                current_cost_usd=current_cost,
                budget_usd=budget_usd,
                severity="warning",
            )

        # Check budget breach
        if current_cost >= budget_usd:
            self._create_alert(
                session_id=session_id,
                alert_type="breach",
                message=f"Cost exceeded budget: ${current_cost:.2f} of ${budget_usd:.2f}",
                current_cost_usd=current_cost,
                budget_usd=budget_usd,
                severity="critical",
            )
            # Mark in database
            cursor.execute(
                """
                UPDATE sessions SET cost_threshold_breached = 1 WHERE session_id = ?
                """,
                (session_id,),
            )
            conn.commit()

    def _create_alert(
        self,
        session_id: str,
        alert_type: str,
        message: str,
        current_cost_usd: float,
        budget_usd: float | None = None,
        predicted_cost_usd: float | None = None,
        model: str | None = None,
        severity: str = "warning",
    ) -> CostAlert:
        """Create and store a cost alert."""
        from htmlgraph.ids import generate_id

        alert_id = generate_id("event", title=f"{alert_type}:{message}")
        timestamp = datetime.now(timezone.utc)

        alert = CostAlert(
            alert_id=alert_id,
            alert_type=alert_type,
            session_id=session_id,
            timestamp=timestamp,
            message=message,
            current_cost_usd=current_cost_usd,
            budget_usd=budget_usd,
            predicted_cost_usd=predicted_cost_usd,
            model=model,
            severity=severity,
        )

        # Store in database
        conn = self.connect()
        cursor = conn.cursor()
        cursor.execute(
            """
            INSERT INTO cost_events (
                event_id, session_id, alert_type, message,
                current_cost_usd, budget_usd, severity, timestamp
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """,
            (
                alert_id,
                session_id,
                alert_type,
                message,
                current_cost_usd,
                budget_usd,
                severity,
                timestamp.isoformat(),
            ),
        )
        conn.commit()

        # Cache alert
        self._alert_cache[alert_id] = alert

        logger.info(f"Cost alert created: {alert_type} for {session_id}: {message}")

        return alert

    def get_alerts(self, session_id: str, limit: int = 100) -> list[CostAlert]:
        """Get recent cost alerts for a session."""
        conn = self.connect()
        cursor = conn.cursor()

        cursor.execute(
            """
            SELECT * FROM cost_events
            WHERE session_id = ? AND alert_type IS NOT NULL
            ORDER BY datetime(REPLACE(SUBSTR(timestamp, 1, 19), 'T', ' ')) DESC LIMIT ?
            """,
            (session_id, limit),
        )

        alerts = []
        for row in cursor.fetchall():
            # sqlite3.Row doesn't have .get(), use dict conversion or try/except
            severity = "warning"
            try:
                severity = row["severity"]
            except (KeyError, IndexError):
                pass
            alert = CostAlert(
                alert_id=row["event_id"],
                alert_type=row["alert_type"],
                session_id=row["session_id"],
                timestamp=datetime.fromisoformat(row["timestamp"]),
                message=row["message"],
                current_cost_usd=row["current_cost_usd"],
                budget_usd=row["budget_usd"],
                severity=severity,
            )
            alerts.append(alert)

        return alerts

    def predict_cost_trajectory(
        self, session_id: str, lookback_minutes: int = 5
    ) -> dict[str, Any]:
        """
        Predict future cost based on recent trajectory.

        Args:
            session_id: Session identifier
            lookback_minutes: Minutes of history to analyze

        Returns:
            Prediction data with projected cost
        """
        conn = self.connect()
        cursor = conn.cursor()

        # Get recent costs
        cursor.execute(
            """
            SELECT timestamp, cost_usd FROM cost_events
            WHERE session_id = ? AND cost_usd > 0
            AND timestamp > datetime('now', '-' || ? || ' minutes')
            ORDER BY timestamp ASC
            """,
            (session_id, lookback_minutes),
        )

        costs = []
        for row in cursor.fetchall():
            costs.append(
                {
                    "timestamp": datetime.fromisoformat(row["timestamp"]),
                    "cost_usd": row["cost_usd"],
                }
            )

        if len(costs) < 2:
            return {"prediction_available": False, "reason": "insufficient_data"}

        # Calculate average cost per minute
        time_span = (
            costs[-1]["timestamp"] - costs[0]["timestamp"]
        ).total_seconds() / 60
        if time_span == 0:
            return {"prediction_available": False, "reason": "zero_time_span"}

        total_cost = sum(c["cost_usd"] for c in costs)
        cost_per_minute = total_cost / time_span

        # Project to 1 hour
        projected_cost = cost_per_minute * 60

        return {
            "prediction_available": True,
            "recent_cost_usd": total_cost,
            "lookback_minutes": lookback_minutes,
            "cost_per_minute": cost_per_minute,
            "projected_hourly_cost": projected_cost,
            "sample_count": len(costs),
        }
