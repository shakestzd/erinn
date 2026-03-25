"""Tests for skill_scout.recommender."""

from __future__ import annotations

from pathlib import Path

from htmlgraph.skill_scout.github_search import PluginInfo
from htmlgraph.skill_scout.project_analyzer import ProjectAnalysis
from htmlgraph.skill_scout.recommender import Recommendation, _check_signal, recommend

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_signals(**kwargs) -> ProjectAnalysis:  # type: ignore[type-arg]
    """Build a ProjectAnalysis with defaults for unspecified fields."""
    defaults: dict = {
        "root": Path("/tmp/fake"),
        "languages": [],
        "frameworks": [],
        "has_tests": False,
        "has_ci": False,
        "has_docker": False,
        "has_htmlgraph": False,
        "manifest_files": [],
    }
    defaults.update(kwargs)
    return ProjectAnalysis(**defaults)


def _make_plugin(name: str, description: str = "", category: str = "") -> PluginInfo:
    return PluginInfo(
        name=name, repo="owner/repo", description=description, category=category
    )


# ---------------------------------------------------------------------------
# Basic recommendation tests
# ---------------------------------------------------------------------------


class TestRecommendBasic:
    def test_recommend_python_project(self) -> None:
        signals = _make_signals(languages=["python"])
        recs = recommend(signals)
        names = [r.plugin_name for r in recs]
        assert "pyright-lsp" in names

    def test_recommend_excludes_installed(self) -> None:
        signals = _make_signals(languages=["python"])
        recs = recommend(signals, existing_plugins=["pyright-lsp"])
        names = [r.plugin_name for r in recs]
        assert "pyright-lsp" not in names

    def test_recommend_multiple_signals_sorted_by_score(self) -> None:
        signals = _make_signals(languages=["python"], has_ci=True)
        recs = recommend(signals, limit=10)
        assert len(recs) >= 2
        # Results must be sorted descending
        scores = [r.score for r in recs]
        assert scores == sorted(scores, reverse=True)

    def test_recommend_limit(self) -> None:
        signals = _make_signals(
            languages=["python", "javascript"],
            has_ci=True,
            has_tests=True,
            has_docker=True,
        )
        recs = recommend(signals, limit=2)
        assert len(recs) <= 2

    def test_recommend_empty_signals(self) -> None:
        signals = _make_signals()
        recs = recommend(signals)
        assert recs == []

    def test_recommend_enriches_descriptions(self) -> None:
        signals = _make_signals(languages=["python"])
        available = [
            _make_plugin("pyright-lsp", description="Python LSP", category="lsp")
        ]
        recs = recommend(signals, available_plugins=available)
        pyright = next((r for r in recs if r.plugin_name == "pyright-lsp"), None)
        assert pyright is not None
        assert pyright.description == "Python LSP"
        assert pyright.category == "lsp"

    def test_recommend_no_enrichment_when_plugin_not_in_available(self) -> None:
        signals = _make_signals(languages=["python"])
        available = [_make_plugin("some-other-plugin", description="Other")]
        recs = recommend(signals, available_plugins=available)
        pyright = next((r for r in recs if r.plugin_name == "pyright-lsp"), None)
        assert pyright is not None
        assert pyright.description == ""

    def test_recommend_default_limit_is_five(self) -> None:
        signals = _make_signals(
            languages=["python", "javascript", "rust", "go"],
            has_ci=True,
            has_tests=True,
            has_docker=True,
            frameworks=["FastAPI", "React", "Next.js", "Phoenix"],
        )
        recs = recommend(signals)
        assert len(recs) <= 5


# ---------------------------------------------------------------------------
# Score accumulation tests
# ---------------------------------------------------------------------------


class TestRecommendScoring:
    def test_multiple_matching_rules_accumulate_score(self) -> None:
        # has_ci=True matches both commit-commands (7pts) and pr-review-toolkit (6pts)
        signals = _make_signals(has_ci=True)
        recs = recommend(signals, limit=10)
        commit_rec = next((r for r in recs if r.plugin_name == "commit-commands"), None)
        assert commit_rec is not None
        assert commit_rec.score == 7

    def test_reasons_list_populated(self) -> None:
        signals = _make_signals(languages=["python"])
        recs = recommend(signals)
        pyright = next((r for r in recs if r.plugin_name == "pyright-lsp"), None)
        assert pyright is not None
        assert len(pyright.reasons) >= 1
        assert any("Python" in r for r in pyright.reasons)

    def test_has_tests_triggers_multiple_plugins(self) -> None:
        signals = _make_signals(has_tests=True)
        recs = recommend(signals, limit=10)
        names = [r.plugin_name for r in recs]
        # has_tests=True should trigger code-review and hookify
        assert "code-review" in names
        assert "hookify" in names

    def test_hookify_lower_score_than_code_review(self) -> None:
        signals = _make_signals(has_tests=True)
        recs = recommend(signals, limit=10)
        hookify = next((r for r in recs if r.plugin_name == "hookify"), None)
        code_review = next((r for r in recs if r.plugin_name == "code-review"), None)
        assert hookify is not None and code_review is not None
        assert code_review.score > hookify.score


# ---------------------------------------------------------------------------
# _check_signal unit tests
# ---------------------------------------------------------------------------


class TestCheckSignal:
    def test_check_signal_bool_true(self) -> None:
        signals = _make_signals(has_ci=True)
        assert _check_signal(signals, "has_ci", True) is True

    def test_check_signal_bool_false(self) -> None:
        signals = _make_signals(has_ci=False)
        assert _check_signal(signals, "has_ci", True) is False

    def test_check_signal_list_match(self) -> None:
        signals = _make_signals(languages=["python"])
        assert _check_signal(signals, "languages", "python") is True

    def test_check_signal_list_no_match(self) -> None:
        signals = _make_signals(languages=["python"])
        assert _check_signal(signals, "languages", "rust") is False

    def test_check_signal_list_empty(self) -> None:
        signals = _make_signals(languages=[])
        assert _check_signal(signals, "languages", "python") is False

    def test_check_signal_wildcard_non_empty(self) -> None:
        signals = _make_signals(languages=["python"])
        assert _check_signal(signals, "languages", "*") is True

    def test_check_signal_wildcard_empty(self) -> None:
        signals = _make_signals(languages=[])
        assert _check_signal(signals, "languages", "*") is False

    def test_check_signal_missing_attribute(self) -> None:
        signals = _make_signals()
        assert _check_signal(signals, "nonexistent_key", "value") is False


# ---------------------------------------------------------------------------
# Recommendation dataclass
# ---------------------------------------------------------------------------


class TestRecommendationDataclass:
    def test_default_score_is_zero(self) -> None:
        rec = Recommendation(
            plugin_name="test", repo="owner/repo", description="", category=""
        )
        assert rec.score == 0

    def test_default_reasons_is_empty_list(self) -> None:
        rec = Recommendation(
            plugin_name="test", repo="owner/repo", description="", category=""
        )
        assert rec.reasons == []
