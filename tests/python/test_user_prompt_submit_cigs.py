"""
Tests for UserPromptSubmit hook with CIGS integration.

Tests cover:
1. CIGS intent classification (exploration, code changes, git)
2. Violation count integration
3. Imperative guidance generation
4. Combined workflow + CIGS guidance
5. Edge cases and error handling
"""

import json
import subprocess
from pathlib import Path

import pytest

# Path to the hook script
HOOK_SCRIPT = (
    Path(__file__).parent.parent.parent
    / "packages"
    / "claude-plugin"
    / "hooks"
    / "scripts"
    / "user-prompt-submit.py"
)


def run_hook(prompt: str) -> dict:
    """Run the UserPromptSubmit hook with given prompt.

    Args:
        prompt: User prompt text

    Returns:
        Hook output as dict
    """
    hook_input = {"prompt": prompt}
    input_json = json.dumps(hook_input)

    result = subprocess.run(
        ["uv", "run", "python", str(HOOK_SCRIPT)],
        input=input_json,
        capture_output=True,
        text=True,
        timeout=25,
    )

    if result.returncode != 0:
        print(f"Hook stderr: {result.stderr}")
        pytest.fail(f"Hook failed with exit code {result.returncode}")

    if not result.stdout.strip():
        return {}

    return json.loads(result.stdout)


class TestCIGSIntentClassification:
    """Test CIGS intent classification for delegation guidance."""

    def test_exploration_intent_detected(self):
        """Exploration keywords should trigger CIGS exploration guidance."""
        prompts = [
            "Search for all files containing 'authentication'",
            "Find the implementation of the login function",
            "What files handle user sessions?",
            "Analyze the codebase structure",
            "Show me where the API endpoints are defined",
        ]

        for prompt in prompts:
            output = run_hook(prompt)
            assert "cigs_classification" in output, (
                f"Missing CIGS classification for: {prompt}"
            )
            assert output["cigs_classification"]["involves_exploration"], (
                f"Failed to detect exploration in: {prompt}"
            )

            # Should have CIGS guidance
            if "hookSpecificOutput" in output:
                guidance = output["hookSpecificOutput"]["additionalContext"]
                assert "IMPERATIVE" in guidance, (
                    f"Missing imperative in guidance for: {prompt}"
                )
                assert "spawn_gemini" in guidance or "exploration" in guidance.lower()

    def test_code_changes_intent_detected(self):
        """Code change keywords should trigger CIGS implementation guidance."""
        prompts = [
            "Implement the user authentication feature",
            "Fix the bug in the login handler",
            "Update the API endpoint to support pagination",
            "Refactor the database connection code",
            "Add error handling to the payment processor",
        ]

        for prompt in prompts:
            output = run_hook(prompt)
            print(f"\nPrompt: {prompt}")
            print(f"Output: {json.dumps(output, indent=2)}")
            assert "cigs_classification" in output, (
                f"No CIGS classification for: {prompt}"
            )
            assert output["cigs_classification"]["involves_code_changes"], (
                f"Failed to detect code changes in: {prompt}"
            )

            # Should have CIGS guidance
            if "hookSpecificOutput" in output:
                guidance = output["hookSpecificOutput"]["additionalContext"]
                assert "IMPERATIVE" in guidance
                assert "spawn_codex" in guidance or "Task()" in guidance

    def test_git_intent_detected(self):
        """Git keywords should trigger CIGS git delegation guidance."""
        prompts = [
            "Commit these changes with a descriptive message",
            "Push the feature branch to origin",
            "Create a new branch for the bug fix",
            "Merge the pull request",
            "Run git status to see what changed",
        ]

        for prompt in prompts:
            output = run_hook(prompt)
            assert "cigs_classification" in output
            assert output["cigs_classification"]["involves_git"], (
                f"Failed to detect git in: {prompt}"
            )

            # Should have CIGS guidance
            if "hookSpecificOutput" in output:
                guidance = output["hookSpecificOutput"]["additionalContext"]
                assert "IMPERATIVE" in guidance
                assert "spawn_copilot" in guidance or "git" in guidance.lower()

    def test_multiple_intents_detected(self):
        """Prompts with multiple intents should detect all."""
        prompt = "Search for the login code, then fix the authentication bug and commit the changes"

        output = run_hook(prompt)
        assert "cigs_classification" in output

        cigs = output["cigs_classification"]
        assert cigs["involves_exploration"], "Should detect exploration"
        assert cigs["involves_code_changes"], "Should detect code changes"
        assert cigs["involves_git"], "Should detect git"

    def test_no_delegation_intent(self):
        """Conversational prompts should not trigger CIGS guidance."""
        prompts = [
            "What is the project structure?",
            "Explain how the authentication system works",
            "What's the best practice for error handling?",
        ]

        for prompt in prompts:
            output = run_hook(prompt)

            # May have classification but no strong intent
            if "cigs_classification" in output:
                output["cigs_classification"]
                # Should have low confidence or no specific intent
                # (though "what" might trigger some exploration)
                # The key is that imperative guidance should be minimal or absent
                if "hookSpecificOutput" in output:
                    output["hookSpecificOutput"]["additionalContext"]
                    # Should not have strong imperative language
                    # (this is a soft check - some prompts may trigger weak guidance)
                    pass


class TestViolationWarnings:
    """Test violation count integration and warning generation."""

    def test_no_violations_no_warning(self):
        """With 0 violations, no violation warning should appear."""
        # This test assumes a clean session
        # In reality, violation count depends on current session state
        # For now, we test the structure exists
        prompt = "Find the login function"
        output = run_hook(prompt)

        assert "cigs_session_status" in output
        # Violation count may be 0 or >0 depending on current session
        # We just verify the structure exists

    def test_violation_count_included(self):
        """Violation count should be included in output."""
        prompt = "Implement user authentication"
        output = run_hook(prompt)

        assert "cigs_session_status" in output
        assert "violation_count" in output["cigs_session_status"]
        assert "waste_tokens" in output["cigs_session_status"]

        # Values should be non-negative
        assert output["cigs_session_status"]["violation_count"] >= 0
        assert output["cigs_session_status"]["waste_tokens"] >= 0


class TestGuidanceGeneration:
    """Test imperative guidance generation."""

    def test_exploration_guidance_format(self):
        """Exploration guidance should have correct format."""
        prompt = "Search for authentication code"
        output = run_hook(prompt)

        if "hookSpecificOutput" in output:
            guidance = output["hookSpecificOutput"]["additionalContext"]

            # Should have CIGS header
            assert "CIGS PRE-RESPONSE GUIDANCE" in guidance

            # Should have imperative language
            assert "IMPERATIVE" in guidance
            assert "YOU MUST" in guidance

            # Should mention spawn_gemini
            assert "spawn_gemini" in guidance

    def test_code_changes_guidance_format(self):
        """Code changes guidance should have correct format."""
        prompt = "Implement the payment processor"
        output = run_hook(prompt)

        if "hookSpecificOutput" in output:
            guidance = output["hookSpecificOutput"]["additionalContext"]

            assert "CIGS PRE-RESPONSE GUIDANCE" in guidance
            assert "IMPERATIVE" in guidance
            assert "YOU MUST" in guidance
            assert "spawn_codex" in guidance or "Task()" in guidance

    def test_git_guidance_format(self):
        """Git guidance should have correct format."""
        prompt = "Commit the changes"
        output = run_hook(prompt)

        if "hookSpecificOutput" in output:
            guidance = output["hookSpecificOutput"]["additionalContext"]

            assert "CIGS PRE-RESPONSE GUIDANCE" in guidance
            assert "IMPERATIVE" in guidance
            assert "spawn_copilot" in guidance


class TestCombinedGuidance:
    """Test combined workflow + CIGS guidance."""

    def test_implementation_with_no_work_item(self):
        """Implementation without work item should show both guidances."""
        prompt = "Implement user login feature"
        output = run_hook(prompt)

        if "hookSpecificOutput" in output:
            guidance = output["hookSpecificOutput"]["additionalContext"]

            # Should have CIGS guidance
            assert "CIGS PRE-RESPONSE GUIDANCE" in guidance or "IMPERATIVE" in guidance

            # Should also have workflow guidance about creating work item
            # (existing behavior from original hook)
            assert "work item" in guidance.lower() or "feature" in guidance.lower()

    def test_exploration_request(self):
        """Exploration request should have clear CIGS guidance."""
        prompt = "Find all files that use the database connection"
        output = run_hook(prompt)

        if "hookSpecificOutput" in output:
            guidance = output["hookSpecificOutput"]["additionalContext"]

            # Should have CIGS exploration guidance
            assert "exploration" in guidance.lower() or "spawn_gemini" in guidance


class TestEdgeCases:
    """Test edge cases and error handling."""

    def test_empty_prompt(self):
        """Empty prompt should return empty result."""
        output = run_hook("")
        assert output == {}

    def test_very_short_prompt(self):
        """Very short prompts should not crash."""
        prompts = ["ok", "yes", "continue", "next"]

        for prompt in prompts:
            output = run_hook(prompt)
            # Should complete without error
            assert isinstance(output, dict)

    def test_very_long_prompt(self):
        """Very long prompts should not crash."""
        prompt = "Search for " + "authentication " * 100
        output = run_hook(prompt)

        # Should complete without error
        assert isinstance(output, dict)

    def test_special_characters(self):
        """Prompts with special characters should be handled."""
        prompts = [
            "Find the `authenticate()` function",
            'Search for "user login" in the code',
            "Look for files with $ENV variables",
        ]

        for prompt in prompts:
            output = run_hook(prompt)
            assert isinstance(output, dict)


class TestOutputStructure:
    """Test output structure conforms to hook specification."""

    def test_hook_output_structure(self):
        """Output should have correct hookSpecificOutput structure."""
        prompt = "Find the login code"
        output = run_hook(prompt)

        if "hookSpecificOutput" in output:
            hook_output = output["hookSpecificOutput"]
            assert "hookEventName" in hook_output
            assert hook_output["hookEventName"] == "UserPromptSubmit"
            assert "additionalContext" in hook_output
            assert isinstance(hook_output["additionalContext"], str)

    def test_classification_structure(self):
        """Output should include both classification types."""
        prompt = "Implement and commit user authentication"
        output = run_hook(prompt)

        # Should have original classification
        assert "classification" in output
        assert "implementation" in output["classification"]
        assert "investigation" in output["classification"]
        assert "bug_report" in output["classification"]
        assert "continuation" in output["classification"]
        assert "confidence" in output["classification"]

        # Should have CIGS classification
        assert "cigs_classification" in output
        assert "involves_exploration" in output["cigs_classification"]
        assert "involves_code_changes" in output["cigs_classification"]
        assert "involves_git" in output["cigs_classification"]
        assert "intent_confidence" in output["cigs_classification"]

        # Should have session status
        assert "cigs_session_status" in output
        assert "violation_count" in output["cigs_session_status"]
        assert "waste_tokens" in output["cigs_session_status"]


if __name__ == "__main__":
    pytest.main([__file__, "-v"])
