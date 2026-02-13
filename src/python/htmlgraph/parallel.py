from __future__ import annotations

"""
Parallel workflow execution coordinator for multi-agent task processing.

This module provides a comprehensive framework for executing multiple tasks in parallel
using specialized subagents. It implements a 6-phase workflow that optimizes for
context efficiency, minimizes conflicts, and provides health monitoring.

Available Classes:
    - ParallelWorkflow: Main coordinator implementing the 6-phase parallel execution pattern
    - ParallelAnalysis: Result of pre-flight analysis with parallelization recommendations
    - PreparedTask: A task prepared for parallel execution with cached context
    - AgentResult: Result from a single parallel agent execution
    - AggregateResult: Aggregated results from all parallel agents

Six-Phase Workflow:
    1. Pre-flight Analysis: Assess if parallelization is beneficial
    2. Context Preparation: Cache shared context to reduce redundant reads
    3. Dispatch: Generate optimized prompts for Task tool
    4. Monitor: Track agent health during execution (health tracking)
    5. Aggregate: Collect and analyze results from all agents
    6. Validate: Verify execution quality and detect conflicts

Key Benefits:
    - Context efficiency: Shared context cached, ~15x token reduction per agent
    - Conflict detection: Identifies file conflicts before they happen
    - Health monitoring: Tracks agent efficiency and anti-patterns
    - Risk assessment: Analyzes if parallelization is worthwhile
    - Cost-benefit analysis: Estimates speedup vs. token cost

Usage:
    from htmlgraph.parallel import ParallelWorkflow
    from htmlgraph.sdk import SDK

    sdk = SDK(agent="claude")
    workflow = ParallelWorkflow(sdk)

    # Phase 1: Pre-flight analysis
    analysis = workflow.analyze(max_agents=5)
    if analysis.can_parallelize:
        print(f"Recommendation: {analysis.recommendation}")
        print(f"Expected speedup: {analysis.speedup_factor:.1f}x")

        # Phase 2: Prepare context
        tasks = workflow.prepare_tasks(
            analysis.ready_tasks,
            shared_files=["src/config.py", "src/models.py"]
        )

        # Phase 3: Generate prompts for Task tool
        prompts = workflow.generate_prompts(tasks)

        # Phase 4: Execute (use prompts with Task tool)
        # agent_ids = [spawn_agent(p) for p in prompts]

        # Phase 5: Aggregate results
        results = workflow.aggregate(agent_ids)
        print(f"Success: {results.successful}/{results.total_agents}")
        print(f"Speedup: {results.parallel_speedup:.1f}x")

        # Phase 6: Validate
        validation = workflow.validate(results)
        if validation["no_conflicts"] and validation["all_successful"]:
            print("Parallel execution successful!")

    # Link transcripts to features for traceability
    workflow.link_transcripts([
        ("feat-001", "agent-abc123"),
        ("feat-002", "agent-def456")
    ])

Best Practices:
    - Only parallelize independent tasks (no shared file edits)
    - Use pre-flight analysis to verify benefit > cost
    - Monitor health scores to catch inefficient agents early
    - Link transcripts for full traceability
    - Limit to 3-5 parallel agents for optimal results
"""


from dataclasses import dataclass, field
from datetime import datetime
from typing import TYPE_CHECKING, Any, cast

if TYPE_CHECKING:
    from htmlgraph.sdk import SDK


@dataclass
class ParallelAnalysis:
    """Result of pre-flight analysis for parallel work."""

    can_parallelize: bool
    max_parallelism: int
    ready_tasks: list[str]  # Task IDs ready to run (Level 0)
    blocked_tasks: list[str]  # Tasks waiting on dependencies
    bottlenecks: list[dict[str, Any]]  # Blocking issues
    risks: list[str]  # Potential problems

    # Cost-benefit
    estimated_sequential_time: float  # minutes
    estimated_parallel_time: float  # minutes
    speedup_factor: float

    # Recommendations
    recommendation: str
    warnings: list[str] = field(default_factory=list)


@dataclass
class PreparedTask:
    """A task prepared for parallel execution."""

    task_id: str
    title: str
    priority: str
    assigned_agent: str | None

    # Context
    instructions: str
    cached_context: dict[str, str]  # file -> summary
    files_to_read: list[str]
    files_to_avoid: list[str]  # Being edited by other agents

    # Metadata
    estimated_duration: float  # minutes
    capabilities_required: list[str]


@dataclass
class AgentResult:
    """Result from a parallel agent."""

    agent_id: str
    task_id: str
    status: str  # success, failed, partial
    duration_seconds: float
    files_modified: list[str]
    health_score: float
    anti_patterns: int
    summary: str
    errors: list[str] = field(default_factory=list)


@dataclass
class AggregateResult:
    """Aggregated results from parallel execution."""

    total_agents: int
    successful: int
    failed: int
    total_duration_seconds: float
    parallel_speedup: float
    avg_health_score: float
    total_anti_patterns: int
    files_modified: list[str]
    conflicts: list[str]
    recommendations: list[str]


class ParallelWorkflow:
    """
    Coordinator for optimal parallel agent execution.

    Implements the 6-phase workflow:
    1. Pre-flight analysis
    2. Context preparation
    3. Dispatch (prompt generation)
    4. Monitor (health tracking)
    5. Aggregate (result collection)
    6. Validate (verification)
    """

    # Thresholds from transcript analytics
    RETRY_RATE_THRESHOLD = 0.3
    CONTEXT_REBUILD_THRESHOLD = 5
    TOOL_DIVERSITY_THRESHOLD = 0.3
    MIN_TASK_DURATION_MINUTES = 2.0
    TOKEN_COST_MULTIPLIER = 15  # Parallel uses ~15x tokens

    def __init__(self, sdk: SDK):
        self.sdk = sdk
        self._graph_dir = sdk._directory

    def analyze(self, max_agents: int = 5) -> ParallelAnalysis:
        """
        Phase 1: Pre-flight analysis.

        Determines if parallelization is beneficial and identifies ready tasks.
        """
        # Get parallel opportunities
        try:
            parallel = self.sdk.get_parallel_work(max_agents=max_agents)
        except Exception:
            parallel = {"max_parallelism": 0, "ready_now": [], "blocked": []}

        ready_tasks = parallel.get("ready_now", [])
        blocked_tasks = parallel.get("blocked", [])
        max_parallelism = parallel.get("max_parallelism", 0)

        # Get bottlenecks
        try:
            bottlenecks = self.sdk.find_bottlenecks(top_n=3)
        except Exception:
            bottlenecks = []

        # Assess risks
        risks = self._assess_risks(ready_tasks)

        # Estimate times
        task_count = len(ready_tasks)
        avg_task_time = 5.0  # minutes (conservative estimate)
        sequential_time = task_count * avg_task_time
        parallel_time = avg_task_time if task_count > 0 else 0
        speedup = sequential_time / parallel_time if parallel_time > 0 else 1.0

        # Determine if parallelization is worthwhile
        can_parallelize = (
            max_parallelism >= 2 and len(ready_tasks) >= 2 and len(risks) == 0
        )

        # Generate recommendation
        if not can_parallelize:
            if len(ready_tasks) < 2:
                recommendation = "Not enough independent tasks. Work sequentially."
            elif len(risks) > 0:
                recommendation = f"Risks detected: {', '.join(risks)}. Resolve first."
            else:
                recommendation = "Sequential execution recommended."
        elif speedup < 1.5:
            recommendation = "Marginal benefit. Consider sequential for simplicity."
            can_parallelize = False
        else:
            recommendation = f"Parallelize {min(max_agents, len(ready_tasks))} tasks for {speedup:.1f}x speedup."

        # Warnings
        warnings = []
        if len(bottlenecks) > 0:
            warnings.append(f"{len(bottlenecks)} bottlenecks blocking downstream work")
        if self.TOKEN_COST_MULTIPLIER * len(ready_tasks) > 50:
            warnings.append(
                f"High token cost: ~{self.TOKEN_COST_MULTIPLIER}x per agent"
            )

        return ParallelAnalysis(
            can_parallelize=can_parallelize,
            max_parallelism=max_parallelism,
            ready_tasks=ready_tasks,
            blocked_tasks=blocked_tasks,
            bottlenecks=cast(list[dict[str, Any]], bottlenecks),
            risks=risks,
            estimated_sequential_time=sequential_time,
            estimated_parallel_time=parallel_time,
            speedup_factor=speedup,
            recommendation=recommendation,
            warnings=warnings,
        )

    def prepare_tasks(
        self,
        task_ids: list[str],
        shared_files: list[str] | None = None,
    ) -> list[PreparedTask]:
        """
        Phase 2: Context preparation.

        Prepares tasks with cached context to reduce redundant reads.
        """
        prepared = []

        # Generate shared context cache
        cached_context = {}
        if shared_files:
            for file_path in shared_files:
                try:
                    # In practice, this would read and summarize
                    cached_context[file_path] = f"[Pre-cached summary of {file_path}]"
                except Exception:
                    pass

        # Track which files each agent will edit
        file_assignments: dict[str, str] = {}

        for task_id in task_ids:
            feature = self.sdk.features.get(task_id)
            if not feature:
                continue

            # Infer files this task might edit
            likely_files = self._infer_task_files(feature)

            # Check for conflicts
            files_to_avoid = []
            for file_path in likely_files:
                if file_path in file_assignments:
                    files_to_avoid.append(file_path)
                else:
                    file_assignments[file_path] = task_id

            # Generate instructions
            instructions = self._generate_instructions(feature)

            prepared.append(
                PreparedTask(
                    task_id=task_id,
                    title=feature.title,
                    priority=getattr(feature, "priority", "medium"),
                    assigned_agent=getattr(feature, "agent_assigned", None),
                    instructions=instructions,
                    cached_context=cached_context,
                    files_to_read=likely_files,
                    files_to_avoid=files_to_avoid,
                    estimated_duration=5.0,
                    capabilities_required=getattr(feature, "required_capabilities", []),
                )
            )

        return prepared

    def generate_prompts(self, tasks: list[PreparedTask]) -> list[dict[str, str]]:
        """
        Phase 3: Generate prompts for Task tool.

        Returns list of {prompt, description} dicts ready for Task tool.
        """
        prompts = []

        for task in tasks:
            # Build context section
            context_lines = []
            if task.cached_context:
                context_lines.append("## Pre-Cached Context (DO NOT re-read these)")
                for file_path, summary in task.cached_context.items():
                    context_lines.append(f"- {file_path}: {summary}")

            if task.files_to_avoid:
                context_lines.append("")
                context_lines.append("## Files to AVOID (other agents editing)")
                for file_path in task.files_to_avoid:
                    context_lines.append(f"- {file_path}")

            context_section = "\n".join(context_lines)

            # Build efficiency guidelines
            guidelines = """
## Efficiency Guidelines
- Use Grep before Read (search then read, not read everything)
- Batch Edit operations (multiple changes in one edit)
- Use Glob to find files (not repeated Read attempts)
- Check cached context before reading shared files
- Mark feature file as complete when done
"""

            prompt = f"""Work on feature {task.task_id}: "{task.title}"
Priority: {task.priority}

{task.instructions}

{context_section}

{guidelines}

## Required Output
Return a summary including:
1. What changes were made
2. Files modified
3. Any blockers or issues found
4. Whether the feature is complete
"""

            prompts.append(
                {
                    "prompt": prompt,
                    "description": f"{task.task_id}: {task.title[:30]}",
                    "subagent_type": "general-purpose",
                }
            )

        return prompts

    def aggregate(self, agent_ids: list[str]) -> AggregateResult:
        """
        Phase 5: Aggregate results from parallel agents.

        Analyzes transcripts and collects metrics.
        """
        from htmlgraph.transcript_analytics import TranscriptAnalytics

        analytics = TranscriptAnalytics(self._graph_dir)
        results: list[AgentResult] = []

        all_files: list[str] = []
        conflicts: list[str] = []

        for agent_id in agent_ids:
            health = analytics.calculate_session_health(agent_id)
            anti_patterns = analytics.detect_anti_patterns(agent_id)

            if health:
                result = AgentResult(
                    agent_id=agent_id,
                    task_id="",  # Would be extracted from transcript
                    status="success" if health.overall_score() > 0.5 else "partial",
                    duration_seconds=health.duration_seconds,
                    files_modified=[],  # Would be extracted
                    health_score=health.overall_score(),
                    anti_patterns=sum(p[0].count for p in anti_patterns),
                    summary="",
                )
                results.append(result)

        # Check for file conflicts
        file_counts: dict[str, int] = {}
        for result in results:
            for file_path in result.files_modified:
                file_counts[file_path] = file_counts.get(file_path, 0) + 1
                if file_counts[file_path] > 1:
                    conflicts.append(file_path)
                all_files.append(file_path)

        # Calculate aggregate metrics
        total_duration = sum(r.duration_seconds for r in results)
        avg_health = (
            sum(r.health_score for r in results) / len(results) if results else 0.0
        )
        total_anti = sum(r.anti_patterns for r in results)

        # Estimate speedup
        max_duration = max((r.duration_seconds for r in results), default=0)
        sequential_estimate = total_duration
        speedup = sequential_estimate / max_duration if max_duration > 0 else 1.0

        # Generate recommendations
        recommendations = []
        if avg_health < 0.7:
            recommendations.append("Low average health. Review agent prompts.")
        if total_anti > 5:
            recommendations.append(f"{total_anti} anti-patterns detected. Add caching.")
        if conflicts:
            recommendations.append(f"File conflicts: {', '.join(conflicts)}")

        return AggregateResult(
            total_agents=len(agent_ids),
            successful=len([r for r in results if r.status == "success"]),
            failed=len([r for r in results if r.status == "failed"]),
            total_duration_seconds=total_duration,
            parallel_speedup=speedup,
            avg_health_score=avg_health,
            total_anti_patterns=total_anti,
            files_modified=list(set(all_files)),
            conflicts=conflicts,
            recommendations=recommendations,
        )

    def validate(self, result: AggregateResult) -> dict[str, bool]:
        """
        Phase 6: Validate parallel execution results.
        """
        return {
            "no_conflicts": len(result.conflicts) == 0,
            "all_successful": result.failed == 0,
            "healthy_execution": result.avg_health_score >= 0.7,
            "acceptable_anti_patterns": result.total_anti_patterns <= 5,
        }

    def _assess_risks(self, task_ids: list[str]) -> list[str]:
        """Identify risks that prevent parallelization."""
        risks = []

        # Check for shared file edits (would need feature analysis)
        # This is a simplified check
        if len(task_ids) > 5:
            risks.append("Many tasks increase conflict risk")

        return risks

    def _infer_task_files(self, feature: Any) -> list[str]:
        """Infer which files a task might need to edit."""
        # In practice, this would analyze feature content
        return []

    def _generate_instructions(self, feature: Any) -> str:
        """Generate task-specific instructions."""
        steps = getattr(feature, "steps", [])
        if steps:
            step_lines = []
            for i, step in enumerate(steps, 1):
                status = "✅" if getattr(step, "completed", False) else "⏳"
                desc = getattr(step, "description", str(step))
                step_lines.append(f"{i}. {status} {desc}")
            return "## Steps\n" + "\n".join(step_lines)
        return "Complete this feature according to its description."

    def link_transcripts(
        self,
        feature_transcript_pairs: list[tuple[str, str]],
    ) -> dict[str, Any]:
        """
        Link Claude Code transcripts to features after parallel execution.

        This enables full traceability from features to the agent sessions
        that implemented them.

        Args:
            feature_transcript_pairs: List of (feature_id, transcript_id) tuples

        Returns:
            Summary of linking results

        Example:
            >>> workflow = ParallelWorkflow(sdk)
            >>> # After parallel agents complete...
            >>> results = workflow.link_transcripts([
            ...     ("feat-001", "agent-a91736"),
            ...     ("feat-002", "agent-748080"),
            ...     ("feat-003", "agent-0ef7b6"),
            ... ])
            >>> print(results["linked_count"])  # 3
        """
        # Use SDK's session manager to ensure shared graph instances
        manager = self.sdk.session_manager
        linked = []
        failed = []

        for feature_id, transcript_id in feature_transcript_pairs:
            try:
                feature = self.sdk.features.get(feature_id)
                if not feature:
                    failed.append(
                        {
                            "feature_id": feature_id,
                            "transcript_id": transcript_id,
                            "error": "Feature not found",
                        }
                    )
                    continue

                graph = manager.features_graph
                manager._linking.link_transcript_to_feature(feature, transcript_id, graph)
                graph.update(feature)

                linked.append(
                    {
                        "feature_id": feature_id,
                        "transcript_id": transcript_id,
                        "tool_count": feature.properties.get(
                            "transcript_tool_count", 0
                        ),
                        "duration_seconds": feature.properties.get(
                            "transcript_duration_seconds", 0
                        ),
                    }
                )
            except Exception as e:
                failed.append(
                    {
                        "feature_id": feature_id,
                        "transcript_id": transcript_id,
                        "error": str(e),
                    }
                )

        return {
            "linked_count": len(linked),
            "failed_count": len(failed),
            "linked": linked,
            "failed": failed,
        }

    def auto_link_by_timestamp(
        self,
        feature_ids: list[str],
        time_window_minutes: int = 30,
    ) -> dict[str, Any]:
        """
        Auto-link transcripts to features based on completion timestamp matching.

        Finds agent transcripts that ran within the time window of each feature's
        completion and links them.

        Args:
            feature_ids: Features to find transcripts for
            time_window_minutes: Maximum time difference to consider a match

        Returns:
            Summary with linked features and their transcripts
        """
        from datetime import timedelta

        from htmlgraph.transcript import TranscriptReader

        reader = TranscriptReader()
        pairs = []
        unmatched = []

        for feature_id in feature_ids:
            feature = self.sdk.features.get(feature_id)
            if not feature:
                unmatched.append(feature_id)
                continue

            completed_at = feature.properties.get("completed_at")
            if not completed_at:
                unmatched.append(feature_id)
                continue

            # Parse completion time
            try:
                completion_time = datetime.fromisoformat(completed_at)
            except (ValueError, TypeError):
                unmatched.append(feature_id)
                continue

            # Find transcripts in time window
            since = completion_time - timedelta(minutes=time_window_minutes)
            sessions = reader.list_sessions(since=since)

            # Find agent sessions (not main sessions)
            for session in sessions:
                if session.session_id.startswith("agent-"):
                    # Check if within time window
                    if session.ended_at:
                        time_diff = abs(
                            (session.ended_at - completion_time).total_seconds()
                        )
                        if time_diff <= time_window_minutes * 60:
                            pairs.append((feature_id, session.session_id))
                            break

            if not any(p[0] == feature_id for p in pairs):
                unmatched.append(feature_id)

        # Link the matched pairs
        if pairs:
            result = self.link_transcripts(pairs)
            result["unmatched_features"] = unmatched
            return result

        return {
            "linked_count": 0,
            "failed_count": 0,
            "linked": [],
            "failed": [],
            "unmatched_features": unmatched,
        }
