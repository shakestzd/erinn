"""Copilot spawner implementation."""

import subprocess
import sys
import time
from typing import TYPE_CHECKING, Any

from .base import AIResult, BaseSpawner

if TYPE_CHECKING:
    from htmlgraph.sdk import SDK


class CopilotSpawner(BaseSpawner):
    """Spawner for GitHub Copilot CLI."""

    def _parse_and_track_events(
        self, prompt: str, response: str, sdk: "SDK"
    ) -> list[dict]:
        """
        Track Copilot execution (start and result only).

        Args:
            prompt: Original prompt
            response: Response from Copilot
            sdk: HtmlGraph SDK instance for tracking

        Returns:
            Synthetic events list for consistency
        """
        events = []

        # Track start
        start_event = {"type": "copilot_start", "prompt": prompt[:100]}
        events.append(start_event)
        self._track_activity(
            sdk,
            tool="copilot_start",
            summary=f"Copilot started with prompt: {prompt[:80]}",
            payload={"prompt_length": len(prompt)},
        )

        # Track result
        result_event = {"type": "copilot_result", "response": response[:100]}
        events.append(result_event)
        self._track_activity(
            sdk,
            tool="copilot_result",
            summary=f"Copilot completed: {response[:80]}",
            payload={"response_length": len(response)},
        )

        return events

    def spawn(
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
        sdk = None
        tracked_events: list[dict] = []
        if track_in_htmlgraph:
            sdk = self._get_sdk()

        # Publish live event: spawner starting
        self._publish_live_event(
            "spawner_start",
            "copilot",
            prompt=prompt,
        )
        start_time = time.time()

        cmd = ["copilot", "-p", prompt]

        # Add allow all tools flag
        if allow_all_tools:
            cmd.append("--allow-all-tools")

        # Add tool permissions
        if allow_tools:
            for tool in allow_tools:
                cmd.extend(["--allow-tool", tool])

        # Add denied tools
        if deny_tools:
            for tool in deny_tools:
                cmd.extend(["--deny-tool", tool])

        # Track spawner start if SDK available
        if sdk:
            self._track_activity(
                sdk,
                tool="copilot_spawn_start",
                summary=f"Spawning Copilot: {prompt[:80]}",
                payload={"prompt_length": len(prompt)},
            )

        try:
            # Publish live event: executing
            self._publish_live_event(
                "spawner_phase",
                "copilot",
                phase="executing",
                details="Running Copilot CLI",
            )

            # Record subprocess invocation if tracker is available
            subprocess_event_id = None
            print(
                f"DEBUG: tracker={tracker is not None}, parent_event_id={parent_event_id}",
                file=sys.stderr,
            )
            if tracker and parent_event_id:
                print(
                    "DEBUG: Recording subprocess invocation for Copilot...",
                    file=sys.stderr,
                )
                try:
                    subprocess_event = tracker.record_tool_call(
                        tool_name="subprocess.copilot",
                        tool_input={"cmd": cmd},
                        phase_event_id=parent_event_id,
                        spawned_agent="github-copilot",
                    )
                    if subprocess_event:
                        subprocess_event_id = subprocess_event.get("event_id")
                        print(
                            f"DEBUG: Subprocess event created for Copilot: {subprocess_event_id}",
                            file=sys.stderr,
                        )
                    else:
                        print("DEBUG: subprocess_event was None", file=sys.stderr)
                except Exception as e:
                    # Tracking failure should not break execution
                    print(
                        f"DEBUG: Exception recording Copilot subprocess: {e}",
                        file=sys.stderr,
                    )
                    pass
            else:
                print(
                    f"DEBUG: Skipping Copilot subprocess tracking - tracker={tracker is not None}, parent_event_id={parent_event_id}",
                    file=sys.stderr,
                )

            result = subprocess.run(
                cmd,
                capture_output=True,
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

            # Publish live event: processing
            self._publish_live_event(
                "spawner_phase",
                "copilot",
                phase="processing",
                details="Parsing Copilot response",
            )

            # Parse output: response is before stats block
            lines = result.stdout.split("\n")

            # Find where stats start (look for "Total usage est:" or "Usage by model")
            stats_start = len(lines)
            for i, line in enumerate(lines):
                if "Total usage est" in line or "Usage by model" in line:
                    stats_start = i
                    break

            # Response is everything before stats
            response = "\n".join(lines[:stats_start]).strip()

            # Try to extract token count from stats
            tokens = None
            for line in lines[stats_start:]:
                # Look for token counts like "25.8k input, 5 output"
                if "input" in line and "output" in line:
                    # Simple extraction: just note we found stats
                    # TODO: More sophisticated parsing if needed
                    tokens = 0  # Placeholder
                    break

            # Track Copilot execution if SDK available
            if sdk:
                tracked_events = self._parse_and_track_events(prompt, response, sdk)

            # Publish live event: complete
            duration = time.time() - start_time
            success = result.returncode == 0
            self._publish_live_event(
                "spawner_complete",
                "copilot",
                success=success,
                duration=duration,
                response=response[:200] if response else None,
                tokens=tokens,
                error=result.stderr if not success else None,
            )
            return AIResult(
                success=success,
                response=response,
                tokens_used=tokens,
                error=None if success else result.stderr,
                raw_output=result.stdout,
                tracked_events=tracked_events,
            )

        except FileNotFoundError:
            duration = time.time() - start_time
            self._publish_live_event(
                "spawner_complete",
                "copilot",
                success=False,
                duration=duration,
                error="CLI not found",
            )
            return AIResult(
                success=False,
                response="",
                tokens_used=None,
                error="Copilot CLI not found. Install from: https://docs.github.com/en/copilot/using-github-copilot/using-github-copilot-in-the-command-line",
                raw_output=None,
                tracked_events=tracked_events,
            )
        except subprocess.TimeoutExpired as e:
            duration = time.time() - start_time
            self._publish_live_event(
                "spawner_complete",
                "copilot",
                success=False,
                duration=duration,
                error=f"Timed out after {timeout} seconds",
            )
            return AIResult(
                success=False,
                response="",
                tokens_used=None,
                error=f"Timed out after {timeout} seconds",
                raw_output={
                    "partial_stdout": e.stdout.decode() if e.stdout else None,
                    "partial_stderr": e.stderr.decode() if e.stderr else None,
                }
                if e.stdout or e.stderr
                else None,
                tracked_events=tracked_events,
            )
        except Exception as e:
            duration = time.time() - start_time
            self._publish_live_event(
                "spawner_complete",
                "copilot",
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
