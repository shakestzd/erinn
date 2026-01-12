"""Gemini spawner implementation."""

import json
import subprocess
import sys
import time
from typing import TYPE_CHECKING, Any

from .base import AIResult, BaseSpawner

if TYPE_CHECKING:
    from htmlgraph.sdk import SDK


class GeminiSpawner(BaseSpawner):
    """Spawner for Google Gemini CLI.

    Model Selection:
        The `model` parameter defaults to None, which is the RECOMMENDED approach.
        When model=None, the Gemini CLI automatically selects the best available model
        based on the task and current availability.

        As of Gemini CLI v0.22+, the default models include:
        - gemini-2.5-flash-lite: Fast, efficient model for most tasks
        - gemini-3-flash-preview: Preview of Gemini 3 with enhanced capabilities

        Explicitly specifying a model is DISCOURAGED because:
        1. Older models (gemini-2.0-flash, gemini-1.5-flash) may fail due to
           "thinking mode" incompatibility in newer CLI versions
        2. Using None automatically benefits from Google's model updates
        3. The CLI handles model selection and fallback logic

        Supported models (if you must specify):
        - None (recommended): CLI chooses best available model
        - "gemini-2.5-flash-lite": Fast, efficient
        - "gemini-3-flash-preview": Gemini 3 preview (enhanced capabilities)
        - "gemini-2.5-pro": More capable, slower

        DEPRECATED models (may cause errors):
        - "gemini-2.0-flash": Deprecated, use None instead
        - "gemini-1.5-flash": Deprecated, use None instead
        - "gemini-1.5-pro": Deprecated, use None instead
    """

    def _parse_and_track_events(self, jsonl_output: str, sdk: "SDK") -> list[dict]:
        """
        Parse Gemini stream-json events and track in HtmlGraph.

        Args:
            jsonl_output: JSONL output from Gemini CLI
            sdk: HtmlGraph SDK instance for tracking

        Returns:
            Parsed events list
        """
        events = []

        for line in jsonl_output.splitlines():
            if not line.strip():
                continue

            try:
                event = json.loads(line)
                events.append(event)

                # Track based on event type
                event_type = event.get("type")

                if event_type == "tool_use":
                    tool_name = event.get("tool_name", "unknown_tool")
                    parameters = event.get("parameters", {})
                    self._track_activity(
                        sdk,
                        tool="gemini_tool_call",
                        summary=f"Gemini called {tool_name}",
                        payload={
                            "tool_name": tool_name,
                            "parameters": parameters,
                        },
                    )

                elif event_type == "tool_result":
                    status = event.get("status", "unknown")
                    success = status == "success"
                    tool_id = event.get("tool_id", "unknown")
                    self._track_activity(
                        sdk,
                        tool="gemini_tool_result",
                        summary=f"Gemini tool result: {status}",
                        success=success,
                        payload={"tool_id": tool_id, "status": status},
                    )

                elif event_type == "message":
                    role = event.get("role")
                    if role == "assistant":
                        content = event.get("content", "")
                        # Truncate for summary
                        summary = (
                            content[:100] + "..." if len(content) > 100 else content
                        )
                        self._track_activity(
                            sdk,
                            tool="gemini_message",
                            summary=f"Gemini: {summary}",
                            payload={"role": role, "content_length": len(content)},
                        )

                elif event_type == "result":
                    stats = event.get("stats", {})
                    self._track_activity(
                        sdk,
                        tool="gemini_completion",
                        summary="Gemini task completed",
                        payload={"stats": stats},
                    )

            except json.JSONDecodeError:
                # Skip malformed lines
                continue

        return events

    def spawn(
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
            model: Model selection. Default: None (RECOMMENDED).

                   When model=None (default), the Gemini CLI automatically selects
                   the best available model, which includes:
                   - gemini-2.5-flash-lite: Fast, efficient model
                   - gemini-3-flash-preview: Gemini 3 with enhanced capabilities

                   Using None is STRONGLY RECOMMENDED because:
                   1. Automatically benefits from Google's latest models
                   2. Avoids deprecation issues with older model names
                   3. CLI handles optimal model selection and fallback

                   DEPRECATED models (may cause errors with CLI v0.22+):
                   - gemini-2.0-flash, gemini-1.5-flash, gemini-1.5-pro

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

        Example:
            >>> spawner = GeminiSpawner()
            >>> result = spawner.spawn(
            ...     prompt="Analyze this codebase",
            ...     # model=None is the default - uses latest Gemini models
            ...     track_in_htmlgraph=True
            ... )
        """
        # Validate tracker and parent_event_id are provided together
        if tracker is not None and parent_event_id is None:
            raise ValueError(
                "parent_event_id is required when tracker is provided. "
                "Spawner events must be linked to a parent in the event hierarchy. "
                "Pass the parent_event_id from the orchestrator context."
            )
        if parent_event_id is not None and tracker is None:
            raise ValueError(
                "tracker is required when parent_event_id is provided. "
                "Both tracker and parent_event_id must be provided together "
                "to properly record spawner events in the event hierarchy."
            )
        # Initialize tracking if enabled
        sdk: SDK | None = None
        tracked_events: list[dict] = []
        if track_in_htmlgraph:
            sdk = self._get_sdk()

        # Publish live event: spawner starting
        self._publish_live_event(
            "spawner_start",
            "gemini",
            prompt=prompt,
            model=model,
        )
        start_time = time.time()

        try:
            # Build command based on tested pattern from spike spk-4029eef3
            cmd = ["gemini", "-p", prompt, "--output-format", output_format]

            # Add model option if specified
            if model:
                cmd.extend(["-m", model])

            # Add include directories if specified
            if include_directories:
                for directory in include_directories:
                    cmd.extend(["--include-directories", directory])

            # CRITICAL: Add --yolo for headless mode (auto-approve all tools)
            cmd.append("--yolo")

            # Track spawner start if SDK available
            if sdk:
                self._track_activity(
                    sdk,
                    tool="gemini_spawn_start",
                    summary=f"Spawning Gemini: {prompt[:80]}",
                    payload={"prompt_length": len(prompt), "model": model},
                )

            # Publish live event: executing
            self._publish_live_event(
                "spawner_phase",
                "gemini",
                phase="executing",
                details="Running Gemini CLI",
            )

            # Record subprocess invocation if tracker is available
            subprocess_event_id = None
            print(
                f"DEBUG: tracker={tracker is not None}, parent_event_id={parent_event_id}",
                file=sys.stderr,
            )
            if tracker and parent_event_id:
                print(
                    "DEBUG: Recording subprocess invocation for Gemini...",
                    file=sys.stderr,
                )
                try:
                    subprocess_event = tracker.record_tool_call(
                        tool_name="subprocess.gemini",
                        tool_input={"cmd": cmd},
                        phase_event_id=parent_event_id,
                        spawned_agent=model or "gemini-default",
                    )
                    if subprocess_event:
                        subprocess_event_id = subprocess_event.get("event_id")
                        print(
                            f"DEBUG: Subprocess event created for Gemini: {subprocess_event_id}",
                            file=sys.stderr,
                        )
                    else:
                        print("DEBUG: subprocess_event was None", file=sys.stderr)
                except Exception as e:
                    # Tracking failure should not break execution
                    print(
                        f"DEBUG: Exception recording Gemini subprocess: {e}",
                        file=sys.stderr,
                    )
                    pass
            else:
                print(
                    f"DEBUG: Skipping Gemini subprocess tracking - tracker={tracker is not None}, parent_event_id={parent_event_id}",
                    file=sys.stderr,
                )

            # Execute with timeout and stderr redirection
            # Note: Cannot use capture_output with stderr parameter
            result = subprocess.run(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,  # Redirect stderr to avoid polluting JSON
                text=True,
                timeout=timeout,
            )

            # Complete subprocess invocation tracking
            if tracker and subprocess_event_id:
                try:
                    tracker.complete_tool_call(
                        event_id=subprocess_event_id,
                        output_summary=result.stdout[:500] if result.stdout else "",
                        success=result.returncode == 0,
                    )
                except Exception:
                    # Tracking failure should not break execution
                    pass

            # Publish live event: processing response
            self._publish_live_event(
                "spawner_phase",
                "gemini",
                phase="processing",
                details="Parsing Gemini response",
            )

            # Check for command execution errors
            if result.returncode != 0:
                duration = time.time() - start_time
                self._publish_live_event(
                    "spawner_complete",
                    "gemini",
                    success=False,
                    duration=duration,
                    error=f"CLI failed with exit code {result.returncode}",
                )
                return AIResult(
                    success=False,
                    response="",
                    tokens_used=None,
                    error=f"Gemini CLI failed with exit code {result.returncode}",
                    raw_output=None,
                    tracked_events=tracked_events,
                )

            # Handle stream-json format with real-time tracking
            if output_format == "stream-json" and sdk:
                try:
                    tracked_events = self._parse_and_track_events(result.stdout, sdk)
                    # Only use stream-json parsing if we got valid events
                    if tracked_events:
                        # For stream-json, we need to extract response differently
                        # Collect all assistant message content, then check result
                        response_text = ""
                        for event in tracked_events:
                            if event.get("type") == "message":
                                # Only collect assistant messages
                                if event.get("role") == "assistant":
                                    content = event.get("content", "")
                                    if content:
                                        response_text += content
                            elif event.get("type") == "result":
                                # Result event may have response field (override if present)
                                if "response" in event and event["response"]:
                                    response_text = event["response"]
                                # Don't break - we've already collected messages

                        # Token usage from stats in result event
                        tokens = None
                        for event in tracked_events:
                            if event.get("type") == "result":
                                stats = event.get("stats", {})
                                if stats and "models" in stats:
                                    total_tokens = 0
                                    for model_stats in stats["models"].values():
                                        model_tokens = model_stats.get(
                                            "tokens", {}
                                        ).get("total", 0)
                                        total_tokens += model_tokens
                                    tokens = total_tokens if total_tokens > 0 else None
                                break

                        # Publish live event: complete
                        duration = time.time() - start_time
                        self._publish_live_event(
                            "spawner_complete",
                            "gemini",
                            success=True,
                            duration=duration,
                            response=response_text,
                            tokens=tokens,
                        )
                        return AIResult(
                            success=True,
                            response=response_text,
                            tokens_used=tokens,
                            error=None,
                            raw_output={"events": tracked_events},
                            tracked_events=tracked_events,
                        )

                except Exception:
                    # Fall back to regular JSON parsing if tracking fails
                    pass

            # Parse JSON response (for json format or fallback)
            try:
                output = json.loads(result.stdout)
            except json.JSONDecodeError as e:
                duration = time.time() - start_time
                self._publish_live_event(
                    "spawner_complete",
                    "gemini",
                    success=False,
                    duration=duration,
                    error=f"Failed to parse JSON: {e}",
                )
                return AIResult(
                    success=False,
                    response="",
                    tokens_used=None,
                    error=f"Failed to parse JSON output: {e}",
                    raw_output={"stdout": result.stdout},
                    tracked_events=tracked_events,
                )

            # Extract response and token usage from parsed output
            # Response is at top level in JSON output
            response_text = output.get("response", "")

            # Token usage is in stats.models (sum across all models)
            tokens = None
            stats = output.get("stats", {})
            if stats and "models" in stats:
                total_tokens = 0
                for model_stats in stats["models"].values():
                    model_tokens = model_stats.get("tokens", {}).get("total", 0)
                    total_tokens += model_tokens
                tokens = total_tokens if total_tokens > 0 else None

            # Publish live event: complete
            duration = time.time() - start_time
            self._publish_live_event(
                "spawner_complete",
                "gemini",
                success=True,
                duration=duration,
                response=response_text,
                tokens=tokens,
            )
            return AIResult(
                success=True,
                response=response_text,
                tokens_used=tokens,
                error=None,
                raw_output=output,
                tracked_events=tracked_events,
            )

        except subprocess.TimeoutExpired as e:
            duration = time.time() - start_time
            self._publish_live_event(
                "spawner_complete",
                "gemini",
                success=False,
                duration=duration,
                error=f"Timed out after {timeout} seconds",
            )
            return AIResult(
                success=False,
                response="",
                tokens_used=None,
                error=f"Gemini CLI timed out after {timeout} seconds",
                raw_output={
                    "partial_stdout": e.stdout.decode() if e.stdout else None,
                    "partial_stderr": e.stderr.decode() if e.stderr else None,
                }
                if e.stdout or e.stderr
                else None,
                tracked_events=tracked_events,
            )
        except FileNotFoundError:
            duration = time.time() - start_time
            self._publish_live_event(
                "spawner_complete",
                "gemini",
                success=False,
                duration=duration,
                error="CLI not found",
            )
            return AIResult(
                success=False,
                response="",
                tokens_used=None,
                error="Gemini CLI not found. Ensure 'gemini' is installed and in PATH.",
                raw_output=None,
                tracked_events=tracked_events,
            )
        except Exception as e:
            duration = time.time() - start_time
            self._publish_live_event(
                "spawner_complete",
                "gemini",
                success=False,
                duration=duration,
                error=str(e),
            )
            return AIResult(
                success=False,
                response="",
                tokens_used=None,
                error=f"Unexpected error: {type(e).__name__}: {e}",
                raw_output=None,
                tracked_events=tracked_events,
            )
