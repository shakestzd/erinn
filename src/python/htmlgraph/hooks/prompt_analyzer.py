"""
Prompt Analysis Module for HtmlGraph Hooks.

Centralizes prompt classification and workflow guidance logic used across
multiple hooks (UserPromptSubmit, PreToolUse, etc.).

This module provides:
- Intent classification (implementation, testing, refactoring, etc.)
- CIGS (Computational Imperative Guidance System) violation detection
- Active work item tracking
- Workflow guidance generation
- UserQuery event creation for parent-child linking

The module is designed to be reusable across different hook implementations,
with graceful degradation if dependencies are unavailable.
"""

import logging
import re
import uuid
from datetime import datetime, timezone
from typing import Any

from htmlgraph.hooks.context import HookContext

logger = logging.getLogger(__name__)


# Patterns that indicate implementation intent
IMPLEMENTATION_PATTERNS = [
    r"\b(implement|add|create|build|write|develop|make)\b.*\b(feature|function|method|class|component|endpoint|api)\b",
    r"\b(fix|resolve|patch|repair)\b.*\b(bug|issue|error|problem)\b",
    r"\b(refactor|rewrite|restructure|reorganize)\b",
    r"\b(update|modify|change|edit)\b.*\b(code|file|function|class)\b",
    r"\bcan you\b.*\b(add|implement|create|fix|change)\b",
    r"\bplease\b.*\b(add|implement|create|fix|change)\b",
    r"\bI need\b.*\b(feature|function|fix|change)\b",
    r"\blet'?s\b.*\b(implement|add|create|build|fix)\b",
]

# Patterns that indicate investigation/research
INVESTIGATION_PATTERNS = [
    r"\b(investigate|research|explore|analyze|understand|find out|look into)\b",
    r"\b(why|how come|what causes)\b.*\b(not working|broken|failing|error)\b",
    r"\b(where|which|what)\b.*\b(file|code|function|class)\b.*\b(handle|process|do)\b",
    r"\bcan you\b.*\b(find|search|look for|check)\b",
]

# Patterns that indicate bug/issue
BUG_PATTERNS = [
    r"\b(bug|issue|error|problem|broken|not working|fails|crash)\b",
    r"\b(something'?s? wrong|doesn'?t work|isn'?t working)\b",
    r"\bCI\b.*\b(fail|error|broken)\b",
    r"\btest.*\b(fail|error|broken)\b",
]

# Patterns for continuation
CONTINUATION_PATTERNS = [
    r"^(continue|resume|proceed|go on|keep going|next)\b",
    r"\b(where we left off|from before|last time)\b",
    r"^(ok|okay|yes|sure|do it|go ahead)\b",
]

# CIGS: Patterns for delegation-critical operations
EXPLORATION_KEYWORDS = [
    "search",
    "find",
    "what files",
    "which files",
    "where is",
    "locate",
    "analyze",
    "examine",
    "inspect",
    "review",
    "check",
    "look at",
    "show me",
    "list",
    "grep",
    "read",
    "scan",
    "explore",
]

CODE_CHANGE_KEYWORDS = [
    "implement",
    "fix",
    "update",
    "refactor",
    "change",
    "modify",
    "edit",
    "write",
    "create file",
    "add code",
    "remove code",
    "replace",
    "rewrite",
    "patch",
    "add",
]

GIT_KEYWORDS = [
    "commit",
    "push",
    "pull",
    "merge",
    "branch",
    "checkout",
    "git add",
    "git commit",
    "git push",
    "git status",
    "git diff",
    "rebase",
    "cherry-pick",
    "stash",
]

# WIP limit detection patterns
WIP_LIMIT_PATTERNS = [
    r"WIP limit.*reached",
    r"wip limit.*exceeded",
    r"WIP.*full",
    r"wip.*full",
    r"max.*WIP",
    r"maximum work in progress",
]


def classify_prompt(prompt: str) -> dict[str, Any]:
    """
    Classify the user's prompt intent.

    Analyzes the prompt text to determine the primary intent using pattern matching.
    Returns classification results with confidence scores.

    Args:
        prompt: User's prompt text

    Returns:
        dict with:
            - is_implementation: bool - Implementation/coding request
            - is_investigation: bool - Research/exploration request
            - is_bug_report: bool - Bug/issue report
            - is_continuation: bool - Continuation of previous work
            - confidence: float - Overall confidence (0.0-1.0)
            - matched_patterns: list - Patterns that matched

    Example:
        >>> result = classify_prompt("Can you implement a new feature?")
        >>> result['is_implementation']
        True
    """
    prompt_lower = prompt.lower().strip()

    result: dict[str, Any] = {
        "is_implementation": False,
        "is_investigation": False,
        "is_bug_report": False,
        "is_continuation": False,
        "confidence": 0.0,
        "matched_patterns": [],
    }

    # Check for continuation first (short prompts like "ok", "continue")
    for pattern in CONTINUATION_PATTERNS:
        if re.search(pattern, prompt_lower):
            result["is_continuation"] = True
            result["confidence"] = 0.9
            result["matched_patterns"].append(f"continuation: {pattern}")
            return result

    # Check for implementation patterns
    for pattern in IMPLEMENTATION_PATTERNS:
        if re.search(pattern, prompt_lower):
            result["is_implementation"] = True
            result["confidence"] = max(result["confidence"], 0.8)
            result["matched_patterns"].append(f"implementation: {pattern}")

    # Check for investigation patterns
    for pattern in INVESTIGATION_PATTERNS:
        if re.search(pattern, prompt_lower):
            result["is_investigation"] = True
            result["confidence"] = max(result["confidence"], 0.7)
            result["matched_patterns"].append(f"investigation: {pattern}")

    # Check for bug patterns
    for pattern in BUG_PATTERNS:
        if re.search(pattern, prompt_lower):
            result["is_bug_report"] = True
            result["confidence"] = max(result["confidence"], 0.75)
            result["matched_patterns"].append(f"bug: {pattern}")

    return result


def classify_cigs_intent(prompt: str) -> dict[str, Any]:
    """
    Classify prompt for CIGS delegation guidance.

    Analyzes the prompt to detect patterns that indicate exploration, code changes,
    or git operations. Used to generate pre-response delegation imperatives.

    Args:
        prompt: User's prompt text

    Returns:
        dict with:
            - involves_exploration: bool - Exploration/search activity
            - involves_code_changes: bool - Code modification activity
            - involves_git: bool - Git operation activity
            - intent_confidence: float - Overall confidence (0.0-1.0)

    Example:
        >>> result = classify_cigs_intent("Search for all error handling code")
        >>> result['involves_exploration']
        True
    """
    prompt_lower = prompt.lower().strip()

    result: dict[str, Any] = {
        "involves_exploration": False,
        "involves_code_changes": False,
        "involves_git": False,
        "intent_confidence": 0.0,
    }

    # Check for exploration keywords
    exploration_matches = sum(1 for kw in EXPLORATION_KEYWORDS if kw in prompt_lower)
    if exploration_matches > 0:
        result["involves_exploration"] = True
        result["intent_confidence"] = min(1.0, exploration_matches * 0.3)

    # Check for code change keywords
    code_matches = sum(1 for kw in CODE_CHANGE_KEYWORDS if kw in prompt_lower)
    if code_matches > 0:
        result["involves_code_changes"] = True
        result["intent_confidence"] = max(
            result["intent_confidence"], min(1.0, code_matches * 0.35)
        )

    # Check for git keywords
    git_matches = sum(1 for kw in GIT_KEYWORDS if kw in prompt_lower)
    if git_matches > 0:
        result["involves_git"] = True
        result["intent_confidence"] = max(
            result["intent_confidence"], min(1.0, git_matches * 0.4)
        )

    return result


def get_session_violation_count(context: HookContext) -> tuple[int, int]:
    """
    Get violation count for current session using CIGS ViolationTracker.

    Queries the CIGS violation tracker to retrieve session violation metrics.
    Gracefully degrades to (0, 0) if CIGS is unavailable.

    Args:
        context: HookContext with session and graph directory info

    Returns:
        Tuple of (violation_count, total_waste_tokens)
        Returns (0, 0) if CIGS is unavailable

    Example:
        >>> violation_count, waste_tokens = get_session_violation_count(context)
        >>> if violation_count > 0:
        ...     logger.info(f"Violations this session: {violation_count}")
    """
    try:
        from htmlgraph.cigs import ViolationTracker

        tracker = ViolationTracker()
        summary = tracker.get_session_violations()
        return summary.total_violations, summary.total_waste_tokens
    except Exception as e:
        # Graceful degradation if CIGS not available
        logger.debug(f"Could not get violation count: {e}")
        return 0, 0


def get_active_work_item(context: HookContext) -> dict[str, Any] | None:
    """
    Query HtmlGraph for active feature/spike.

    Attempts to load the active work item from the session manager.
    Returns None if no active work item or if SDK is unavailable.

    Args:
        context: HookContext with session and graph directory info

    Returns:
        dict with work item details (id, title, type) or None if not found

    Example:
        >>> active = get_active_work_item(context)
        >>> if active and active['type'] == 'feature':
        ...     logger.info(f"Active feature: {active['title']}")
    """
    try:
        from htmlgraph import SDK

        sdk = SDK()
        work_item = sdk.get_active_work_item()
        if work_item is None:
            return None
        # Convert ActiveWorkItem TypedDict to dict
        return dict(work_item) if hasattr(work_item, "__iter__") else work_item  # type: ignore
    except Exception as e:
        logger.debug(f"Could not get active work item: {e}")
        return None


def get_open_work_items(context: HookContext) -> list[dict]:
    """Get all todo and in-progress work items for attribution guidance.

    Queries the SDK for all open work items across features, bugs, and spikes.
    Used to provide Claude with a list of candidate work items so it can
    self-evaluate and call sdk.features.start() / sdk.bugs.start() /
    sdk.spikes.start() to set the correct attribution before proceeding.

    Args:
        context: HookContext with session and graph directory info

    Returns:
        List of dicts with keys: id, title, type, status.
        Returns empty list if SDK is unavailable or no items exist.

    Example:
        >>> items = get_open_work_items(context)
        >>> for item in items:
        ...     logger.info(f"Open item: {item['type']} {item['id']}: {item['title']}")
    """
    try:
        from htmlgraph import SDK  # noqa: PLC0415

        sdk = SDK()
        items: list[dict] = []

        # Get in-progress features
        for feat in sdk.features.where(status="in-progress"):
            items.append(
                {
                    "id": feat.id,
                    "title": feat.title or feat.id,
                    "type": "feature",
                    "status": "in-progress",
                }
            )

        # Get todo features (top 5 by priority)
        for feat in sdk.features.where(status="todo")[:5]:
            items.append(
                {
                    "id": feat.id,
                    "title": feat.title or feat.id,
                    "type": "feature",
                    "status": "todo",
                }
            )

        # Get in-progress bugs
        for bug in sdk.bugs.where(status="in-progress"):
            items.append(
                {
                    "id": bug.id,
                    "title": bug.title or bug.id,
                    "type": "bug",
                    "status": "in-progress",
                }
            )

        # Get todo bugs (top 5 by priority)
        for bug in sdk.bugs.where(status="todo")[:5]:
            items.append(
                {
                    "id": bug.id,
                    "title": bug.title or bug.id,
                    "type": "bug",
                    "status": "todo",
                }
            )

        # Get in-progress spikes (cap at 5 to avoid noise)
        for spike in sdk.spikes.where(status="in-progress")[:5]:
            items.append(
                {
                    "id": spike.id,
                    "title": spike.title or spike.id,
                    "type": "spike",
                    "status": "in-progress",
                }
            )

        return items
    except Exception:  # noqa: BLE001
        return []


def _build_attribution_block(
    active_work: dict[str, Any] | None,
    open_work_items: list[dict] | None,
) -> str | None:
    """
    Build the Work Item Attribution block for injection into guidance.

    Returns a formatted block instructing Claude to self-evaluate work item
    attribution and call the appropriate SDK method to correct it if needed.
    Returns None if open_work_items is empty or None.

    The attribution instruction is a HARD BLOCKING GATE placed at the top of
    Claude's response. Claude must call the appropriate SDK method to set
    correct attribution BEFORE proceeding with any other work.

    Args:
        active_work: Currently active work item dict (id, title, type) or None
        open_work_items: List of open work item dicts from get_open_work_items()

    Returns:
        Formatted attribution block string, or None if no open items
    """
    if not open_work_items:
        return None

    lines = [
        "## ⛔ MANDATORY FIRST STEP — DO NOT SKIP",
        "",
        "Before writing ANY response or taking ANY action:",
        "",
        "1. Read the prompt and identify what work item this belongs to",
        '2. Call `sdk.features.start("id")` / `sdk.bugs.start("id")` / `sdk.spikes.start("id")`',
        "3. THEN proceed with your response",
        "",
        "If no existing item matches, create one:",
        "",
        "```python",
        "from htmlgraph import SDK",
        'sdk = SDK(agent="claude")',
        'f = sdk.features.create("Brief title").set_track("trk-...").save()',
        "sdk.features.start(f.id)",
        "```",
        "",
        "**Why this is non-negotiable:** Turn 1's UserQuery is already in the DB unattributed. "
        "Calling `start()` retroactively fixes it. If you skip this, that entire conversation "
        'shows "—" in the dashboard forever.',
        "",
        "═══════════════════════════════════════════════════════════",
        "",
        "## Open Work Items (Pick from these if applicable):",
        "",
    ]
    for item in open_work_items:
        lines.append(
            f"- [{item['status']}] {item['type']} {item['id']}: {item['title']}"
        )

    lines.append("")
    if active_work:
        active_id = active_work.get("id", "unknown")
        active_title = active_work.get("title", "")
        lines.append(f"Currently active: {active_id}: {active_title}")
    else:
        lines.append("Currently active: None")

    return "\n".join(lines)


def generate_guidance(
    classification: dict[str, Any],
    active_work: dict[str, Any] | None,
    prompt: str,
    open_work_items: list[dict] | None = None,
) -> str | None:
    """
    Generate workflow guidance based on classification and context.

    Produces orchestrator directives and workflow suggestions based on
    the prompt classification and current active work item. When open_work_items
    is provided, appends a Work Item Attribution block instructing Claude to
    self-evaluate and call sdk.features.start() / sdk.bugs.start() /
    sdk.spikes.start() to set the correct attribution before proceeding.

    Args:
        classification: Result from classify_prompt()
        active_work: Result from get_active_work_item()
        prompt: Original user prompt
        open_work_items: Optional list from get_open_work_items(). When non-empty,
            an attribution block is appended to the returned guidance.

    Returns:
        Guidance string to display to user, or None if no guidance needed
        (but still returns attribution block alone if open_work_items is provided)

    Example:
        >>> classification = classify_prompt("Implement new API endpoint")
        >>> guidance = generate_guidance(classification, None, prompt)
        >>> if guidance:
        ...     logger.info("%s", guidance)
    """

    # Helper to optionally append attribution block to a guidance string
    def _with_attribution(guidance: str) -> str:
        block = _build_attribution_block(active_work, open_work_items)
        return guidance + "\n\n" + block if block else guidance

    # If continuing and has active work, only inject attribution if needed
    if classification["is_continuation"] and active_work:
        block = _build_attribution_block(active_work, open_work_items)
        return block if block else None

    # If has active work item, check if it matches intent
    if active_work:
        work_type = active_work.get("type", "")
        work_id = active_work.get("id", "")
        work_title = active_work.get("title", "")

        # Implementation request with spike active - suggest creating feature
        if classification["is_implementation"] and work_type == "spike":
            return _with_attribution(
                f"⚡ ORCHESTRATOR DIRECTIVE: Implementation requested during spike.\n\n"
                f"Active work: {work_id} ({work_title}) - Type: spike\n\n"
                f"Spikes are for investigation, NOT implementation.\n\n"
                f"REQUIRED WORKFLOW:\n\n"
                f"1. COMPLETE OR PAUSE the spike:\n"
                f"   sdk = SDK(agent='claude')\n"
                f"   sdk.spikes.complete('{work_id}')  # or sdk.spikes.pause('{work_id}')\n\n"
                f"2. CREATE A FEATURE for implementation:\n"
                f"   feature = sdk.features.create('Feature title').save()\n"
                f"   sdk.features.start(feature.id)\n\n"
                f"3. DELEGATE TO SUBAGENT:\n"
                f"   from htmlgraph.tasks import Task\n"
                f"   Task(\n"
                f"       subagent_type='general-purpose',\n"
                f"       prompt='Implement: [details]'\n"
                f"   ).execute()\n\n"
                f"Proceed with orchestration.\n"
            )

        # Implementation request with feature active - remind to delegate
        if classification["is_implementation"] and work_type == "feature":
            return _with_attribution(
                f"⚡ ORCHESTRATOR DIRECTIVE: Implementation work detected.\n\n"
                f"Active work: {work_id} ({work_title}) - Type: feature\n\n"
                f"REQUIRED: DELEGATE TO SUBAGENT:\n\n"
                f"  from htmlgraph.tasks import Task\n"
                f"  Task(\n"
                f"      subagent_type='general-purpose',\n"
                f"      prompt='Implement: [specific implementation details for {work_title}]'\n"
                f"  ).execute()\n\n"
                f"DO NOT EXECUTE CODE DIRECTLY IN THIS CONTEXT.\n"
                f"Orchestrators coordinate, subagents implement.\n\n"
                f"Proceed with orchestration.\n"
            )

        # Bug report with feature active - might want bug instead
        if classification["is_bug_report"] and work_type == "feature":
            return _with_attribution(
                f"📋 WORKFLOW GUIDANCE:\n"
                f"Active work: {work_id} ({work_title}) - Type: feature\n\n"
                f"This looks like a bug report. Consider:\n"
                f"1. If this bug is part of {work_title}, continue with current feature\n"
                f"2. If this is a separate issue, create a bug:\n\n"
                f"  sdk = SDK(agent='claude')\n"
                f"  bug = sdk.bugs.create('Bug title').save()\n"
                f"  sdk.bugs.start(bug.id)\n"
            )

        # Has appropriate work item - only inject attribution if open items exist
        block = _build_attribution_block(active_work, open_work_items)
        return block if block else None

    # No active work item - provide guidance based on intent
    if classification["is_implementation"]:
        return _with_attribution(
            "⚡ ORCHESTRATOR DIRECTIVE: This is implementation work.\n\n"
            "REQUIRED WORKFLOW (execute in order):\n\n"
            "1. CREATE A WORK ITEM:\n"
            "   sdk = SDK(agent='claude')\n"
            "   feature = sdk.features.create('Your feature title').save()\n"
            "   sdk.features.start(feature.id)\n\n"
            "2. DELEGATE TO SUBAGENT:\n"
            "   from htmlgraph.tasks import Task\n"
            "   Task(\n"
            "       subagent_type='general-purpose',\n"
            "       prompt='Implement: [specific implementation details]'\n"
            "   ).execute()\n\n"
            "3. DO NOT EXECUTE CODE DIRECTLY IN THIS CONTEXT\n"
            "   - Orchestrators coordinate, subagents implement\n"
            "   - This ensures proper work tracking and session management\n\n"
            "Proceed with orchestration.\n"
        )

    if classification["is_bug_report"]:
        return _with_attribution(
            "📋 WORKFLOW GUIDANCE - BUG REPORT DETECTED:\n\n"
            "Create a bug work item to track this:\n\n"
            "  sdk = SDK(agent='claude')\n"
            "  bug = sdk.bugs.create('Bug title').save()\n"
            "  sdk.bugs.start(bug.id)\n\n"
            "Then investigate and fix the issue.\n"
        )

    if classification["is_investigation"]:
        return _with_attribution(
            "📋 WORKFLOW GUIDANCE - INVESTIGATION REQUEST DETECTED:\n\n"
            "Create a spike for time-boxed investigation:\n\n"
            "  sdk = SDK(agent='claude')\n"
            "  spike = sdk.spikes.create('Investigation title').save()\n"
            "  sdk.spikes.start(spike.id)\n\n"
            "Spikes help track research and exploration work.\n"
        )

    # Low confidence or unclear intent - provide gentle reminder
    if classification["confidence"] < 0.5:
        base_guidance = (
            "💡 REMINDER: Consider creating a work item if this is a task:\n"
            "- Feature: sdk.features.create('Title').save()\n"
            "- Bug: sdk.bugs.create('Title').save()\n"
            "- Spike: sdk.spikes.create('Title').save()\n"
        )
        attribution_block = _build_attribution_block(active_work, open_work_items)
        if attribution_block:
            return base_guidance + "\n\n" + attribution_block
        return base_guidance

    # No specific guidance — but still inject attribution block if there are open items
    attribution_block = _build_attribution_block(active_work, open_work_items)
    return attribution_block if attribution_block else None


def detect_wip_limit_hit(prompt: str) -> bool:
    """
    Detect if prompt contains WIP limit reached message.

    Checks for patterns indicating that a WIP limit has been hit
    (useful for detecting when tool output contains error messages
    about WIP limits being exceeded).

    Args:
        prompt: Text to check for WIP limit patterns

    Returns:
        True if WIP limit pattern detected, False otherwise

    Example:
        >>> detect_wip_limit_hit("Error: WIP limit reached for feature tracking")
        True
    """
    prompt_lower = prompt.lower().strip()
    for pattern in WIP_LIMIT_PATTERNS:
        if re.search(pattern, prompt_lower):
            return True
    return False


def generate_cigs_guidance(
    cigs_intent: dict[str, Any],
    violation_count: int,
    waste_tokens: int,
    prompt: str = "",
) -> str:
    """
    Generate CIGS-specific guidance for detected violations.

    Produces pre-response imperative guidance based on CIGS intent
    classification and current session violation metrics.

    Args:
        cigs_intent: Result from classify_cigs_intent()
        violation_count: Number of violations this session
        waste_tokens: Total wasted tokens this session
        prompt: Optional prompt text to check for WIP limit hits

    Returns:
        Imperative guidance string (empty if no guidance needed)

    Example:
        >>> cigs = classify_cigs_intent("Search for all error handling")
        >>> guidance = generate_cigs_guidance(cigs, 0, 0, "")
        >>> if guidance:
        ...     logger.info("%s", guidance)
    """
    imperatives = []

    # WIP limit detection
    if prompt and detect_wip_limit_hit(prompt):
        imperatives.append(
            "⚠️ WIP LIMIT HIT: Don't iterate with Bash — delegate instead.\n\n"
            "Quick fix:\n"
            "  uv run htmlgraph wip          # See what's counting (includes spikes!)\n"
            "  uv run htmlgraph wip reset <id>  # Reset a stale item\n\n"
            "Remember: spikes (spk-*) count toward WIP limit alongside features.\n"
            "Delegate to Agent(haiku-coder) to reset stale items and start new features in one step."
        )

    # Exploration guidance
    if cigs_intent["involves_exploration"]:
        imperatives.append(
            "🔴 IMPERATIVE: This request involves exploration.\n"
            "YOU MUST use spawn_gemini() for exploration (FREE cost).\n"
            "DO NOT use Read/Grep/Glob directly - delegate to Explorer subagent."
        )

    # Code changes guidance
    if cigs_intent["involves_code_changes"]:
        imperatives.append(
            "🔴 IMPERATIVE: This request involves code changes.\n"
            "YOU MUST use spawn_codex() or Task() for implementation.\n"
            "DO NOT use Edit/Write directly - delegate to Coder subagent."
        )

    # Git operations guidance
    if cigs_intent["involves_git"]:
        imperatives.append(
            "🔴 IMPERATIVE: This request involves git operations.\n"
            "YOU MUST use spawn_copilot() for git commands (60% cheaper).\n"
            "DO NOT run git commands directly via Bash."
        )

    # Violation warning
    if violation_count > 0:
        warning_emoji = "⚠️" if violation_count < 3 else "🚨"
        imperatives.append(
            f"{warning_emoji} VIOLATION WARNING: You have {violation_count} delegation "
            f"violations this session ({waste_tokens:,} tokens wasted).\n"
            f"Circuit breaker triggers at 3 violations."
        )

    if not imperatives:
        return ""

    # Combine with header
    guidance_parts = [
        "═══════════════════════════════════════════════════════════",
        "CIGS PRE-RESPONSE GUIDANCE (Computational Imperative Guidance System)",
        "═══════════════════════════════════════════════════════════",
        "",
    ]
    guidance_parts.extend(imperatives)
    guidance_parts.append("")
    guidance_parts.append("═══════════════════════════════════════════════════════════")

    return "\n".join(guidance_parts)


def _get_active_feature_id() -> str | None:
    """
    Query HtmlGraph for the currently active (in-progress) work item ID.

    Lightweight lookup used at UserQuery creation time to stamp each
    prompt event with the feature it belongs to. This removes the
    dependency on Claude calling ``sdk.features.start()`` during the
    conversation -- the hook already knows what is active.

    Returns:
        The feature/bug/spike ID if one is in-progress, else None.
    """
    try:
        from htmlgraph import SDK  # noqa: PLC0415

        sdk = SDK()
        work_item = sdk.get_active_work_item()
        if work_item is not None:
            return work_item.get("id") if hasattr(work_item, "get") else None  # type: ignore[union-attr]
        return None
    except Exception:  # noqa: BLE001
        return None


def create_user_query_event(context: HookContext, prompt: str) -> str | None:
    """
    Create UserQuery event in HtmlGraph database.

    Records the user prompt as a UserQuery event that serves as the parent
    for subsequent tool calls and delegations. Database is the single source
    of truth - no file-based state is used.

    The event is automatically attributed to the currently active work item
    (feature, bug, or spike with status ``in-progress``) so that the
    dashboard can display work-item attribution even when no child tool
    calls carry a ``feature_id``.

    Args:
        context: HookContext with session and database access
        prompt: User's prompt text

    Returns:
        UserQuery event_id if successful, None otherwise

    Note:
        Gracefully degrades if database is unavailable. Does not block
        user interaction on failure.

    Example:
        >>> context = HookContext.from_input(hook_input)
        >>> event_id = create_user_query_event(context, "Implement feature X")
        >>> if event_id:
        ...     logger.info(f"Created event: {event_id}")
    """
    try:
        session_id = context.session_id

        if not session_id or session_id == "unknown":
            logger.debug("No valid session ID for UserQuery event")
            return None

        # Create UserQuery event in database
        try:
            db = context.database

            # Ensure session exists in database before creating event
            # (sessions table has foreign key references, so we need to ensure it exists)
            cursor = db.connection.cursor()
            cursor.execute(
                "SELECT COUNT(*) FROM sessions WHERE session_id = ?",
                (session_id,),
            )
            session_exists = cursor.fetchone()[0] > 0

            if not session_exists:
                # Create session entry if it doesn't exist
                cursor.execute(
                    """
                    INSERT INTO sessions (session_id, created_at, status)
                    VALUES (?, ?, 'active')
                    """,
                    (session_id, datetime.now(timezone.utc).isoformat()),
                )
                db.connection.commit()
                logger.debug(f"Created session entry: {session_id}")

            # Generate event ID
            user_query_event_id = f"uq-{uuid.uuid4().hex[:8]}"

            # Prepare event details
            input_summary = prompt[:200]

            # Look up active work item for automatic attribution.
            # Claude handles attribution via sdk.features.start(); the hook
            # only reads the current in-progress item — no keyword matching.
            active_feature_id = _get_active_feature_id()

            if active_feature_id:
                logger.debug(
                    f"Auto-attributing UserQuery to active work item: {active_feature_id}"
                )

            # Insert UserQuery event into agent_events
            # Database is the single source of truth for parent-child linking
            # Subsequent tool calls query database via get_parent_user_query()
            success = db.insert_event(
                event_id=user_query_event_id,
                agent_id="user",
                event_type="tool_call",  # Valid event_type; find by tool_name='UserQuery'
                session_id=session_id,
                tool_name="UserQuery",
                input_summary=input_summary,
                feature_id=active_feature_id,
                context={
                    "prompt": prompt[:500],
                    "session": session_id,
                },
            )

            if not success:
                logger.warning("Failed to insert UserQuery event into database")
                return None

            logger.info(f"Created UserQuery event: {user_query_event_id}")
            return user_query_event_id

        except Exception as e:
            # Database tracking is optional - graceful degradation
            logger.error(f"UserQuery event creation failed: {e}", exc_info=True)
            return None

    except Exception as e:
        # Silent failure - don't block user interaction
        logger.error(f"Unexpected error in create_user_query_event: {e}", exc_info=True)
        return None


__all__ = [
    "classify_prompt",
    "classify_cigs_intent",
    "get_session_violation_count",
    "get_active_work_item",
    "get_open_work_items",
    "generate_guidance",
    "generate_cigs_guidance",
    "detect_wip_limit_hit",
    "create_user_query_event",
    "_get_active_feature_id",
    # Pattern constants for testing/extension
    "IMPLEMENTATION_PATTERNS",
    "INVESTIGATION_PATTERNS",
    "BUG_PATTERNS",
    "CONTINUATION_PATTERNS",
    "EXPLORATION_KEYWORDS",
    "CODE_CHANGE_KEYWORDS",
    "GIT_KEYWORDS",
    "WIP_LIMIT_PATTERNS",
]
