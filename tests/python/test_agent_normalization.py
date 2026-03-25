"""Tests for agent name normalization."""

from htmlgraph.sdk import normalize_agent_name


class TestAgentNormalization:
    def test_model_names(self):
        assert normalize_agent_name("opus") == "claude-opus"
        assert normalize_agent_name("sonnet") == "claude-sonnet"
        assert normalize_agent_name("haiku") == "claude-haiku"

    def test_platform_names(self):
        assert normalize_agent_name("gemini") == "gemini-cli"
        assert normalize_agent_name("codex") == "openai-codex"
        assert normalize_agent_name("copilot") == "github-copilot"

    def test_full_names_pass_through(self):
        assert normalize_agent_name("claude-code") == "claude-code"
        assert normalize_agent_name("phoenix-dashboard") == "phoenix-dashboard"

    def test_case_insensitive(self):
        assert normalize_agent_name("Opus") == "claude-opus"
        assert normalize_agent_name("SONNET") == "claude-sonnet"

    def test_unknown_names_pass_through(self):
        assert normalize_agent_name("my-custom-agent") == "my-custom-agent"
        assert normalize_agent_name("researcher") == "researcher"

    def test_dashboard_aliases(self):
        assert normalize_agent_name("phoenix") == "phoenix-dashboard"
        assert normalize_agent_name("dashboard") == "phoenix-dashboard"
