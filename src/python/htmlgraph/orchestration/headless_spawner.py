"""Headless AI spawner for multi-AI orchestration.

This module provides backward compatibility by delegating to modular spawner implementations.
"""

from typing import Any

from .spawners import (
    AIResult,
    ClaudeSpawner,
    CodexSpawner,
    CopilotSpawner,
    GeminiSpawner,
)

# Re-export AIResult for backward compatibility
__all__ = ["HeadlessSpawner", "AIResult"]


class HeadlessSpawner:
    """
    Spawn AI agents in headless CLI mode.

    Supports multiple AI CLIs:
    - spawn_gemini(): Google Gemini (free tier)
    - spawn_codex(): OpenAI Codex (ChatGPT Plus+)
    - spawn_copilot(): GitHub Copilot (GitHub subscription)
    - spawn_claude(): Claude Code (same login as Task tool)

    spawn_claude() vs Task() Tool:
    --------------------------------
    Both use the same Claude Code authentication and billing, but:

    spawn_claude():
    - Isolated execution (no context sharing)
    - Fresh session each call
    - Best for: independent tasks, external scripts, parallel processing
    - Cache miss on each call (higher token usage)

    Task():
    - Shared conversation context
    - Builds on previous work
    - Best for: orchestration, related sequential work
    - Cache hits in session (5x cheaper for related work)

    Example - When to use spawn_claude():
        # Independent tasks in external script
        spawner = HeadlessSpawner()
        for file in files:
            result = spawner.spawn_claude(f"Analyze {file} independently")
            save_result(file, result)

    Example - When to use Task() instead:
        # Related tasks in orchestration workflow
        Task(prompt="Analyze all files and compare them")
        # Better: shares context, uses caching
    """

    def __init__(self) -> None:
        """Initialize spawner with modular implementations."""
        self._gemini_spawner = GeminiSpawner()
        self._codex_spawner = CodexSpawner()
        self._copilot_spawner = CopilotSpawner()
        self._claude_spawner = ClaudeSpawner()

    # Expose internal methods for backward compatibility with tests
    def _parse_and_track_gemini_events(self, jsonl_output: str, sdk: Any) -> list[dict]:
        """Parse and track Gemini events (delegates to GeminiSpawner)."""
        return self._gemini_spawner._parse_and_track_events(jsonl_output, sdk)

    def _parse_and_track_codex_events(self, jsonl_output: str, sdk: Any) -> list[dict]:
        """Parse and track Codex events (delegates to CodexSpawner)."""
        return self._codex_spawner._parse_and_track_events(jsonl_output, sdk)

    def _parse_and_track_copilot_events(
        self, prompt: str, response: str, sdk: Any
    ) -> list[dict]:
        """Parse and track Copilot events (delegates to CopilotSpawner)."""
        return self._copilot_spawner._parse_and_track_events(prompt, response, sdk)

    def _get_sdk(self) -> Any:
        """Get SDK instance (delegates to base spawner implementation)."""
        return self._gemini_spawner._get_sdk()

    def spawn_gemini(
        self,
        prompt: str,
        output_format: str = "stream-json",
        model: str | None = None,
        include_directories: list[str] | None = None,
        track_in_htmlgraph: bool = True,
        timeout: int = 120,
        tracker: Any = None,
        parent_event_id: str | None = None,
    ) -> AIResult:
        """
        Spawn Gemini in headless mode.

        Args:
            prompt: Task description for Gemini
            output_format: "json" or "stream-json" (enables real-time tracking)
            model: Model selection. Default: None (recommended - lets CLI choose
                   thinking-compatible models). Older models may fail.
            include_directories: Directories to include for context. Default: None
            track_in_htmlgraph: Enable HtmlGraph activity tracking. Default: True
            timeout: Max seconds to wait
            tracker: SpawnerEventTracker for recording subprocess invocation.
                     REQUIRED when parent_event_id is provided (enforces event hierarchy).
            parent_event_id: Parent event ID for event hierarchy.
                     REQUIRED when tracker is provided (enforces event hierarchy).

        Returns:
            AIResult with response, error, and tracked events if tracking enabled

        Raises:
            ValueError: If tracker is provided without parent_event_id, or vice versa.
                        Both must be provided together for proper event hierarchy tracking.
        """
        return self._gemini_spawner.spawn(
            prompt=prompt,
            output_format=output_format,
            model=model,
            include_directories=include_directories,
            track_in_htmlgraph=track_in_htmlgraph,
            timeout=timeout,
            tracker=tracker,
            parent_event_id=parent_event_id,
        )

    def spawn_codex(
        self,
        prompt: str,
        output_json: bool = True,
        model: str | None = None,
        sandbox: str | None = None,
        full_auto: bool = True,
        images: list[str] | None = None,
        output_last_message: str | None = None,
        output_schema: str | None = None,
        skip_git_check: bool = False,
        working_directory: str | None = None,
        use_oss: bool = False,
        bypass_approvals: bool = False,
        track_in_htmlgraph: bool = True,
        timeout: int = 120,
        tracker: Any = None,
        parent_event_id: str | None = None,
    ) -> AIResult:
        """
        Spawn Codex in headless mode.

        Args:
            prompt: Task description for Codex
            output_json: JSONL output flag (enables real-time tracking)
            model: Model selection (e.g., "gpt-4-turbo"). Default: None
            sandbox: Sandbox mode ("read-only", "workspace-write", or full)
            full_auto: Enable full auto mode. Default: True (required headless)
            images: List of image paths (--image). Default: None
            output_last_message: Write last message to file. Default: None
            output_schema: JSON schema for validation. Default: None
            skip_git_check: Skip git repo check. Default: False
            working_directory: Workspace directory (--cd). Default: None
            use_oss: Use local Ollama provider (--oss). Default: False
            bypass_approvals: Bypass approval checks. Default: False
            track_in_htmlgraph: Enable HtmlGraph activity tracking. Default: True
            timeout: Max seconds to wait
            tracker: SpawnerEventTracker for recording subprocess invocation.
                     REQUIRED when parent_event_id is provided (enforces event hierarchy).
            parent_event_id: Parent event ID for event hierarchy.
                     REQUIRED when tracker is provided (enforces event hierarchy).

        Returns:
            AIResult with response, error, and tracked events if tracking enabled

        Raises:
            ValueError: If tracker is provided without parent_event_id, or vice versa.
                        Both must be provided together for proper event hierarchy tracking.
        """
        return self._codex_spawner.spawn(
            prompt=prompt,
            output_json=output_json,
            model=model,
            sandbox=sandbox,
            full_auto=full_auto,
            images=images,
            output_last_message=output_last_message,
            output_schema=output_schema,
            skip_git_check=skip_git_check,
            working_directory=working_directory,
            use_oss=use_oss,
            bypass_approvals=bypass_approvals,
            track_in_htmlgraph=track_in_htmlgraph,
            timeout=timeout,
            tracker=tracker,
            parent_event_id=parent_event_id,
        )

    def spawn_copilot(
        self,
        prompt: str,
        allow_tools: list[str] | None = None,
        allow_all_tools: bool = False,
        deny_tools: list[str] | None = None,
        track_in_htmlgraph: bool = True,
        timeout: int = 120,
        tracker: Any = None,
        parent_event_id: str | None = None,
    ) -> AIResult:
        """
        Spawn GitHub Copilot in headless mode.

        Args:
            prompt: Task description for Copilot
            allow_tools: Tools to auto-approve (e.g., ["shell(git)"])
            allow_all_tools: Auto-approve all tools. Default: False
            deny_tools: Tools to deny (--deny-tool). Default: None
            track_in_htmlgraph: Enable HtmlGraph activity tracking. Default: True
            timeout: Max seconds to wait
            tracker: SpawnerEventTracker for recording subprocess invocation.
                     REQUIRED when parent_event_id is provided (enforces event hierarchy).
            parent_event_id: Parent event ID for event hierarchy.
                     REQUIRED when tracker is provided (enforces event hierarchy).

        Returns:
            AIResult with response, error, and tracked events if tracking enabled

        Raises:
            ValueError: If tracker is provided without parent_event_id, or vice versa.
                        Both must be provided together for proper event hierarchy tracking.
        """
        return self._copilot_spawner.spawn(
            prompt=prompt,
            allow_tools=allow_tools,
            allow_all_tools=allow_all_tools,
            deny_tools=deny_tools,
            track_in_htmlgraph=track_in_htmlgraph,
            timeout=timeout,
            tracker=tracker,
            parent_event_id=parent_event_id,
        )

    def spawn_claude(
        self,
        prompt: str,
        output_format: str = "json",
        permission_mode: str = "bypassPermissions",
        resume: str | None = None,
        verbose: bool = False,
        timeout: int = 300,
        extra_args: list[str] | None = None,
    ) -> AIResult:
        """
        Spawn Claude in headless mode.

        NOTE: Uses same Claude Code authentication as Task() tool, but provides
        isolated execution context. Each call creates a new session without shared
        context. Best for independent tasks or external scripts.

        For orchestration workflows with shared context, prefer Task() tool which
        leverages prompt caching (5x cheaper for related work).

        Args:
            prompt: Task description for Claude
            output_format: "text" or "json" (stream-json requires --verbose)
            permission_mode: Permission handling mode:
                - "bypassPermissions": Auto-approve all (default)
                - "acceptEdits": Auto-approve edits only
                - "dontAsk": Fail on permission prompts
                - "default": Normal interactive prompts
                - "plan": Plan mode (no execution)
                - "delegate": Delegation mode
            resume: Resume from previous session (--resume). Default: None
            verbose: Enable verbose output (--verbose). Default: False
            timeout: Max seconds (default: 300, Claude can be slow with initialization)
            extra_args: Additional arguments to pass to Claude CLI

        Returns:
            AIResult with response or error

        Example:
            >>> spawner = HeadlessSpawner()
            >>> result = spawner.spawn_claude("What is 2+2?")
            >>> if result.success:
            ...     print(result.response)  # "4"
            ...     print(f"Cost: ${result.raw_output['total_cost_usd']}")
        """
        return self._claude_spawner.spawn(
            prompt=prompt,
            output_format=output_format,
            permission_mode=permission_mode,
            resume=resume,
            verbose=verbose,
            timeout=timeout,
            extra_args=extra_args,
        )
