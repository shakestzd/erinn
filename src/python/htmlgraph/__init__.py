"""
HtmlGraph - Local-first observability and coordination for AI-assisted development.

Document-backed work items (HTML artifacts) with SQLite-indexed runtime state,
append-only JSONL event log, live FastAPI/HTMX dashboard, and multi-agent
session tracking. Single-process, local-first stack -- no external database
server required.
"""

import logging

from rich.console import Console
from rich.logging import RichHandler

# Configure Rich logging for entire SDK
# CRITICAL: Use stderr=True to prevent pollution of stdout (hooks output JSON to stdout)
logging.basicConfig(
    handlers=[
        RichHandler(
            console=Console(stderr=True),
            show_time=True,
            show_level=True,
            rich_tracebacks=True,
        )
    ],
    level=logging.INFO,
    format="%(message)s",
    datefmt="[%X]",
)

# SDK now lives in sdk/core.py - clean import from sdk package
from htmlgraph.agent_detection import detect_agent_name, get_agent_display_name
from htmlgraph.agents import AgentInterface
from htmlgraph.analytics import Analytics, DependencyAnalytics
from htmlgraph.atomic_ops import (
    AtomicFileWriter,
    DirectoryLocker,
    atomic_rename,
    cleanup_orphaned_temp_files,
    safe_temp_file,
    validate_atomic_write,
)
from htmlgraph.builders import BaseBuilder, FeatureBuilder, SpikeBuilder
from htmlgraph.collections import BaseCollection, FeatureCollection, SpikeCollection
from htmlgraph.context_analytics import ContextAnalytics, ContextUsage
from htmlgraph.decorators import RetryError, retry, retry_async
from htmlgraph.edge_index import EdgeIndex, EdgeRef
from htmlgraph.exceptions import (
    ClaimConflictError,
    HtmlGraphError,
    NodeNotFoundError,
    SessionNotFoundError,
    ValidationError,
)
from htmlgraph.find_api import FindAPI, find, find_all
from htmlgraph.graph import CompiledQuery, HtmlGraph
from htmlgraph.ids import (
    generate_hierarchical_id,
    generate_id,
    is_legacy_id,
    is_valid_id,
    parse_id,
)
from htmlgraph.learning import LearningPersistence, auto_persist_on_session_end
from htmlgraph.models import (
    ActivityEntry,
    AggregatedMetric,
    Chore,
    ContextSnapshot,
    Edge,
    Graph,
    MaintenanceType,
    Node,
    Pattern,
    Session,
    SessionInsight,
    Spike,
    SpikeType,
    Step,
    WorkType,
)
from htmlgraph.orchestration import (
    delegate_with_id,
    generate_task_id,
    get_results_by_task_id,
    parallel_delegate,
)
from htmlgraph.orchestrator_mode import OrchestratorMode, OrchestratorModeManager
from htmlgraph.parallel import AggregateResult, ParallelAnalysis, ParallelWorkflow
from htmlgraph.query_builder import Condition, Operator, QueryBuilder
from htmlgraph.reflection import ComputationalReflection, get_reflection_context
from htmlgraph.repo_hash import RepoHash
from htmlgraph.sdk import SDK
from htmlgraph.sdk.auto_help import (
    AutoHelpWrapper,
    enable_auto_help,
    get_help_for_exception,
    show_help_on_error,
)
from htmlgraph.sdk.delegation_warnings import (
    get_delegation_warning,
    is_in_subagent_context,
    is_orchestrator_mode_active,
    should_show_delegation_warning,
    show_delegation_warning,
    track_tool_execution,
)
from htmlgraph.server import serve
from htmlgraph.session_context import (
    GitHooksInstaller,
    SessionContextBuilder,
    VersionChecker,
)
from htmlgraph.session_manager import SessionManager
from htmlgraph.session_registry import SessionRegistry
from htmlgraph.types import (
    ActiveWorkItem,
    AggregateResultsDict,
    BottleneckDict,
    FeatureSummary,
    HighRiskTask,
    ImpactAnalysisDict,
    OrchestrationResult,
    ParallelGuidelines,
    ParallelPlanResult,
    ParallelWorkInfo,
    PlanningContext,
    ProjectStatus,
    RiskAssessmentDict,
    SessionAnalytics,
    SessionStartInfo,
    SessionSummary,
    SmartPlanResult,
    SubagentPrompt,
    TaskPrompt,
    TrackCreationResult,
    WorkQueueItem,
    WorkRecommendation,
)
from htmlgraph.work_type_utils import infer_work_type, infer_work_type_from_id

__version__ = "0.33.40"
__all__ = [
    # Exceptions
    "HtmlGraphError",
    "NodeNotFoundError",
    "SessionNotFoundError",
    "ClaimConflictError",
    "ValidationError",
    "RetryError",
    # Decorators
    "retry",
    "retry_async",
    # Core models
    "Node",
    "Edge",
    "Step",
    "Graph",
    "Session",
    "ActivityEntry",
    "ContextSnapshot",
    "Spike",
    "Chore",
    "Pattern",
    "SessionInsight",
    "AggregatedMetric",
    # Work type classification (Phase 1)
    "WorkType",
    "SpikeType",
    "MaintenanceType",
    # Graph operations
    "HtmlGraph",
    "CompiledQuery",
    "EdgeIndex",
    "EdgeRef",
    "QueryBuilder",
    "Condition",
    "Operator",
    "FindAPI",
    "find",
    "find_all",
    "AgentInterface",
    "SessionManager",
    "SessionContextBuilder",
    "VersionChecker",
    "GitHooksInstaller",
    "SessionRegistry",
    "RepoHash",
    "SDK",
    "Analytics",  # Phase 2: Work Type Analytics
    "DependencyAnalytics",  # Advanced dependency-aware analytics
    "ContextAnalytics",  # Context usage tracking and analytics
    "ContextUsage",  # Context usage data structure
    "serve",
    # ID generation (collision-resistant, multi-agent safe)
    "generate_id",
    "generate_hierarchical_id",
    "parse_id",
    "is_valid_id",
    "is_legacy_id",
    # Work type utilities
    "infer_work_type",
    "infer_work_type_from_id",
    # Builders (modular SDK components)
    "BaseBuilder",
    "FeatureBuilder",
    "SpikeBuilder",
    # Collections (modular SDK components)
    "BaseCollection",
    "FeatureCollection",
    "SpikeCollection",
    # Agent detection
    "detect_agent_name",
    "get_agent_display_name",
    # Parallel workflow coordination
    "ParallelWorkflow",
    "ParallelAnalysis",
    "AggregateResult",
    # Type definitions (TypedDict for SDK return types)
    "BottleneckDict",
    "WorkRecommendation",
    "ParallelWorkInfo",
    "RiskAssessmentDict",
    "HighRiskTask",
    "ImpactAnalysisDict",
    "SessionStartInfo",
    "ProjectStatus",
    "ActiveWorkItem",
    "FeatureSummary",
    "SessionSummary",
    "SessionAnalytics",
    "WorkQueueItem",
    "SmartPlanResult",
    "PlanningContext",
    "TrackCreationResult",
    "SubagentPrompt",
    "OrchestrationResult",
    "ParallelPlanResult",
    "TaskPrompt",
    "ParallelGuidelines",
    "AggregateResultsDict",
    # Active Learning Persistence
    "LearningPersistence",
    "auto_persist_on_session_end",
    # Computational Reflection (pre-computed context injection)
    "ComputationalReflection",
    "get_reflection_context",
    # Orchestrator Mode Management
    "OrchestratorMode",
    "OrchestratorModeManager",
    # Orchestration helpers for parallel task coordination
    "delegate_with_id",
    "get_results_by_task_id",
    "parallel_delegate",
    "generate_task_id",
    # Atomic file operations (Phase 1.3: Session File Tracking)
    "AtomicFileWriter",
    "DirectoryLocker",
    "atomic_rename",
    "cleanup_orphaned_temp_files",
    "safe_temp_file",
    "validate_atomic_write",
    # Auto-help system (Feature 1: Auto-Help on Failure)
    "AutoHelpWrapper",
    "enable_auto_help",
    "get_help_for_exception",
    "show_help_on_error",
    # Delegation warnings (Feature 2: Orchestrator Mode Warnings)
    "get_delegation_warning",
    "is_in_subagent_context",
    "is_orchestrator_mode_active",
    "should_show_delegation_warning",
    "show_delegation_warning",
    "track_tool_execution",
]
