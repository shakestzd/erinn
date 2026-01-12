"""Codex spawner implementation."""

import json
import subprocess
import sys
import time
from typing import TYPE_CHECKING, Any

from .base import AIResult, BaseSpawner

if TYPE_CHECKING:
    from htmlgraph.sdk import SDK


class CodexSpawner(BaseSpawner):
    """Spawner for OpenAI Codex CLI."""

    def _parse_and_track_events(self, jsonl_output: str, sdk: "SDK") -> list[dict]:
        """
        Parse Codex JSONL events and track in HtmlGraph.

        Args:
            jsonl_output: JSONL output from Codex CLI
            sdk: HtmlGraph SDK instance for tracking

        Returns:
            Parsed events list
        """
        events = []
        parse_errors = []

        for line_num, line in enumerate(jsonl_output.splitlines(), start=1):
            if not line.strip():
                continue

            try:
                event = json.loads(line)
                events.append(event)

                event_type = event.get("type")

                # Track item.started events
                if event_type == "item.started":
                    item = event.get("item", {})
                    item_type = item.get("type")

                    if item_type == "command_execution":
                        command = item.get("command", "")
                        self._track_activity(
                            sdk,
                            tool="codex_command",
                            summary=f"Codex executing: {command[:80]}",
                            payload={"command": command},
                        )

                # Track item.completed events
                elif event_type == "item.completed":
                    item = event.get("item", {})
                    item_type = item.get("type")

                    if item_type == "file_change":
                        path = item.get("path", "unknown")
                        self._track_activity(
                            sdk,
                            tool="codex_file_change",
                            summary=f"Codex modified: {path}",
                            file_paths=[path],
                            payload={"path": path},
                        )

                    elif item_type == "agent_message":
                        text = item.get("text", "")
                        summary = text[:100] + "..." if len(text) > 100 else text
                        self._track_activity(
                            sdk,
                            tool="codex_message",
                            summary=f"Codex: {summary}",
                            payload={"text_length": len(text)},
                        )

                # Track turn.completed for token usage
                elif event_type == "turn.completed":
                    usage = event.get("usage", {})
                    total_tokens = sum(usage.values())
                    self._track_activity(
                        sdk,
                        tool="codex_completion",
                        summary=f"Codex turn completed ({total_tokens} tokens)",
                        payload={"usage": usage},
                    )

            except json.JSONDecodeError as e:
                parse_errors.append(
                    {
                        "line_number": line_num,
                        "error": str(e),
                        "content": line[:100],
                    }
                )
                continue

        return events

    def spawn(
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
        if track_in_htmlgraph and output_json:
            sdk = self._get_sdk()

        # Publish live event: spawner starting
        self._publish_live_event(
            "spawner_start",
            "codex",
            prompt=prompt,
            model=model,
        )
        start_time = time.time()

        cmd = ["codex", "exec"]

        if output_json:
            cmd.append("--json")

        # Add model if specified
        if model:
            cmd.extend(["--model", model])

        # Add sandbox mode if specified
        if sandbox:
            cmd.extend(["--sandbox", sandbox])

        # Add full auto flag
        if full_auto:
            cmd.append("--full-auto")

        # Add images
        if images:
            for image in images:
                cmd.extend(["--image", image])

        # Add output last message file if specified
        if output_last_message:
            cmd.extend(["--output-last-message", output_last_message])

        # Add output schema if specified
        if output_schema:
            cmd.extend(["--output-schema", output_schema])

        # Add skip git check flag
        if skip_git_check:
            cmd.append("--skip-git-repo-check")

        # Add working directory if specified
        if working_directory:
            cmd.extend(["--cd", working_directory])

        # Add OSS flag
        if use_oss:
            cmd.append("--oss")

        # Add bypass approvals flag
        if bypass_approvals:
            cmd.append("--dangerously-bypass-approvals-and-sandbox")

        # Add prompt as final argument
        cmd.append(prompt)

        # Track spawner start if SDK available
        if sdk:
            self._track_activity(
                sdk,
                tool="codex_spawn_start",
                summary=f"Spawning Codex: {prompt[:80]}",
                payload={
                    "prompt_length": len(prompt),
                    "model": model,
                    "sandbox": sandbox,
                },
            )

        try:
            # Publish live event: executing
            self._publish_live_event(
                "spawner_phase",
                "codex",
                phase="executing",
                details="Running Codex CLI",
            )

            # Record subprocess invocation if tracker is available
            subprocess_event_id = None
            print(
                f"DEBUG: tracker={tracker is not None}, parent_event_id={parent_event_id}",
                file=sys.stderr,
            )
            if tracker and parent_event_id:
                print(
                    "DEBUG: Recording subprocess invocation for Codex...",
                    file=sys.stderr,
                )
                try:
                    subprocess_event = tracker.record_tool_call(
                        tool_name="subprocess.codex",
                        tool_input={"cmd": cmd},
                        phase_event_id=parent_event_id,
                        spawned_agent="gpt-4",
                    )
                    if subprocess_event:
                        subprocess_event_id = subprocess_event.get("event_id")
                        print(
                            f"DEBUG: Subprocess event created for Codex: {subprocess_event_id}",
                            file=sys.stderr,
                        )
                    else:
                        print("DEBUG: subprocess_event was None", file=sys.stderr)
                except Exception as e:
                    # Tracking failure should not break execution
                    print(
                        f"DEBUG: Exception recording Codex subprocess: {e}",
                        file=sys.stderr,
                    )
                    pass
            else:
                print(
                    f"DEBUG: Skipping Codex subprocess tracking - tracker={tracker is not None}, parent_event_id={parent_event_id}",
                    file=sys.stderr,
                )

            result = subprocess.run(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
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
                "codex",
                phase="processing",
                details="Parsing Codex response",
            )

            if not output_json:
                # Plain text mode - return as-is
                duration = time.time() - start_time
                success = result.returncode == 0
                self._publish_live_event(
                    "spawner_complete",
                    "codex",
                    success=success,
                    duration=duration,
                    response=result.stdout.strip()[:200] if success else None,
                    error="Command failed" if not success else None,
                )
                return AIResult(
                    success=success,
                    response=result.stdout.strip(),
                    tokens_used=None,
                    error=None if success else "Command failed",
                    raw_output=result.stdout,
                    tracked_events=tracked_events,
                )

            # Parse JSONL output
            events = []
            parse_errors = []

            # Use tracking parser if SDK is available
            if sdk:
                tracked_events = self._parse_and_track_events(result.stdout, sdk)
                events = tracked_events
            else:
                # Fallback to regular parsing without tracking
                for line_num, line in enumerate(result.stdout.splitlines(), start=1):
                    if line.strip():
                        try:
                            events.append(json.loads(line))
                        except json.JSONDecodeError as e:
                            parse_errors.append(
                                {
                                    "line_number": line_num,
                                    "error": str(e),
                                    "content": line[
                                        :100
                                    ],  # First 100 chars for debugging
                                }
                            )
                            continue

            # Extract agent message
            response = None
            for event in events:
                if event.get("type") == "item.completed":
                    item = event.get("item", {})
                    if item.get("type") == "agent_message":
                        response = item.get("text")

            # Extract token usage from turn.completed event
            tokens = None
            for event in events:
                if event.get("type") == "turn.completed":
                    usage = event.get("usage", {})
                    # Sum all token types
                    tokens = sum(usage.values())

            # Publish live event: complete
            duration = time.time() - start_time
            success = result.returncode == 0
            self._publish_live_event(
                "spawner_complete",
                "codex",
                success=success,
                duration=duration,
                response=response[:200] if response else None,
                tokens=tokens,
                error="Command failed" if not success else None,
            )
            return AIResult(
                success=success,
                response=response or "",
                tokens_used=tokens,
                error=None if success else "Command failed",
                raw_output={
                    "events": events,
                    "parse_errors": parse_errors if parse_errors else None,
                },
                tracked_events=tracked_events,
            )

        except FileNotFoundError:
            duration = time.time() - start_time
            self._publish_live_event(
                "spawner_complete",
                "codex",
                success=False,
                duration=duration,
                error="CLI not found",
            )
            return AIResult(
                success=False,
                response="",
                tokens_used=None,
                error="Codex CLI not found. Install from: https://github.com/openai/codex",
                raw_output=None,
                tracked_events=tracked_events,
            )
        except subprocess.TimeoutExpired as e:
            duration = time.time() - start_time
            self._publish_live_event(
                "spawner_complete",
                "codex",
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
                "codex",
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
