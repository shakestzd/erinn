"""
Tests for server-side agent filter in the activity feed route.

Covers:
- _any_event_matches_agent helper: pure unit tests (no DB)
- /views/activity-feed route: filtering by nonexistent agent returns empty set
"""

from htmlgraph.api.routes.dashboard import _any_event_matches_agent

# ---------------------------------------------------------------------------
# Helper tests — no DB required
# ---------------------------------------------------------------------------


def _make_group(parent_agent: str, child_agents: list[str] | None = None) -> dict:
    """Build a minimal hierarchical group dict for testing."""
    children = [{"agent_id": a, "children": []} for a in (child_agents or [])]
    return {
        "parent": {"agent_id": parent_agent},
        "children": children,
    }


class TestAnyEventMatchesAgent:
    """Unit tests for _any_event_matches_agent helper."""

    def test_matches_parent_exact(self):
        group = _make_group("gemini")
        assert _any_event_matches_agent(group, "gemini") is True

    def test_matches_parent_substring(self):
        group = _make_group("htmlgraph:sonnet-coder")
        assert _any_event_matches_agent(group, "sonnet") is True

    def test_matches_parent_case_insensitive(self):
        group = _make_group("Gemini")
        assert _any_event_matches_agent(group, "gemini") is True

    def test_matches_child_agent(self):
        group = _make_group("claude-code", child_agents=["gemini"])
        assert _any_event_matches_agent(group, "gemini") is True

    def test_no_match_returns_false(self):
        group = _make_group("claude-code", child_agents=["copilot"])
        assert _any_event_matches_agent(group, "nonexistent-agent-xyz") is False

    def test_empty_filter_matches_all(self):
        # Empty string is a substring of everything — filter should not be applied
        # by the caller, but helper itself returns True for empty needle
        group = _make_group("claude-code")
        assert _any_event_matches_agent(group, "") is True

    def test_matches_nested_grandchild(self):
        grandchild = {"agent_id": "deep-agent", "children": []}
        child = {"agent_id": "claude-code", "children": [grandchild]}
        group = {"parent": {"agent_id": "claude-code"}, "children": [child]}
        assert _any_event_matches_agent(group, "deep-agent") is True

    def test_no_match_nested(self):
        grandchild = {"agent_id": "other-agent", "children": []}
        child = {"agent_id": "claude-code", "children": [grandchild]}
        group = {"parent": {"agent_id": "claude-code"}, "children": [child]}
        assert _any_event_matches_agent(group, "nonexistent-xyz") is False


# ---------------------------------------------------------------------------
# Route tests — uses TestClient with empty DB
# ---------------------------------------------------------------------------


class TestActivityFeedRouteFilter:
    """Integration tests for the /views/activity-feed agent_id filter."""

    def test_filter_nonexistent_agent_returns_empty_or_reduced(self, test_client):
        """Filtering by a nonexistent agent name should return an empty feed."""
        response = test_client.get(
            "/views/activity-feed",
            params={"agent_id": "nonexistent-agent-xyz-999"},
        )
        assert response.status_code == 200
        # With no matching events the template renders the empty-state message
        assert "nonexistent-agent-xyz-999" not in response.text or (
            "No events found" in response.text or response.text.count("<tr") == 0
        )

    def test_filter_empty_agent_returns_all(self, test_client):
        """With no agent_id filter the feed should return normally (200, no crash)."""
        response = test_client.get("/views/activity-feed")
        assert response.status_code == 200

    def test_delta_filter_nonexistent_agent_returns_empty(self, test_client):
        """Delta endpoint with nonexistent agent_id should return empty HTML."""
        response = test_client.get(
            "/views/activity-feed/delta",
            params={"agent_id": "nonexistent-agent-xyz-999"},
        )
        # Either 200 with empty body or 200 with no rows
        assert response.status_code == 200
        # Empty DB + nonexistent filter = empty response body or no <tr> elements
        assert "<tr" not in response.text or response.text.strip() == ""
