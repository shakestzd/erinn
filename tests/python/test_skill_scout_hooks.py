"""Tests for Skill Scout hook integration.

Covers:
- get_plugin_recommendations() in prompt_analyzer
- dismiss_plugin() in cli.skill_scout
"""

from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import MagicMock

from htmlgraph.cli.skill_scout import dismiss_plugin
from htmlgraph.hooks.prompt_analyzer import get_plugin_recommendations

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_context(graph_dir: Path) -> MagicMock:
    ctx = MagicMock()
    ctx.graph_dir = graph_dir
    return ctx


def _write_cache(
    cache_file: Path, recs: list[dict], dismissed: list[str] | None = None
) -> None:
    data = {
        "timestamp": "2026-03-24T12:00:00+00:00",
        "project_signals": {"languages": ["python"], "frameworks": []},
        "recommendations": recs,
        "dismissed": dismissed or [],
    }
    cache_file.write_text(json.dumps(data))


# ---------------------------------------------------------------------------
# get_plugin_recommendations tests
# ---------------------------------------------------------------------------


def test_get_plugin_recommendations_no_cache(tmp_path: Path) -> None:
    ctx = _make_context(tmp_path)
    result = get_plugin_recommendations(ctx)
    assert result is None


def test_get_plugin_recommendations_with_cache(tmp_path: Path) -> None:
    cache_file = tmp_path / "plugin-recommendations.json"
    _write_cache(
        cache_file,
        [{"plugin_name": "pyright-lsp", "score": 10, "reasons": ["Python project"]}],
    )
    ctx = _make_context(tmp_path)
    result = get_plugin_recommendations(ctx)
    assert result is not None
    assert "pyright-lsp" in result
    assert "10pts" in result
    assert "Python project" in result


def test_get_plugin_recommendations_empty_recs(tmp_path: Path) -> None:
    cache_file = tmp_path / "plugin-recommendations.json"
    _write_cache(cache_file, [])
    ctx = _make_context(tmp_path)
    result = get_plugin_recommendations(ctx)
    assert result is None


def test_get_plugin_recommendations_corrupt_json(tmp_path: Path) -> None:
    cache_file = tmp_path / "plugin-recommendations.json"
    cache_file.write_text("not valid json {{{")
    ctx = _make_context(tmp_path)
    result = get_plugin_recommendations(ctx)
    assert result is None


def test_get_plugin_recommendations_max_three(tmp_path: Path) -> None:
    cache_file = tmp_path / "plugin-recommendations.json"
    recs = [
        {"plugin_name": f"plugin-{i}", "score": 10 - i, "reasons": [f"reason-{i}"]}
        for i in range(5)
    ]
    _write_cache(cache_file, recs)
    ctx = _make_context(tmp_path)
    result = get_plugin_recommendations(ctx)
    assert result is not None
    # Only top 3 should appear
    assert "plugin-0" in result
    assert "plugin-1" in result
    assert "plugin-2" in result
    assert "plugin-3" not in result
    assert "plugin-4" not in result


def test_get_plugin_recommendations_no_reasons(tmp_path: Path) -> None:
    """Recommendations with empty reasons list should not crash."""
    cache_file = tmp_path / "plugin-recommendations.json"
    _write_cache(
        cache_file,
        [{"plugin_name": "some-plugin", "score": 5, "reasons": []}],
    )
    ctx = _make_context(tmp_path)
    result = get_plugin_recommendations(ctx)
    assert result is not None
    assert "some-plugin" in result


# ---------------------------------------------------------------------------
# dismiss_plugin tests
# ---------------------------------------------------------------------------


def test_dismiss_plugin(tmp_path: Path) -> None:
    graph_dir = tmp_path / ".htmlgraph"
    graph_dir.mkdir()
    cache_file = graph_dir / "plugin-recommendations.json"
    _write_cache(
        cache_file, [{"plugin_name": "pyright-lsp", "score": 10, "reasons": []}]
    )

    dismiss_plugin("pyright-lsp", project_root=tmp_path)

    data = json.loads(cache_file.read_text())
    assert "pyright-lsp" in data["dismissed"]


def test_dismiss_plugin_removes_from_recs(tmp_path: Path) -> None:
    graph_dir = tmp_path / ".htmlgraph"
    graph_dir.mkdir()
    cache_file = graph_dir / "plugin-recommendations.json"
    _write_cache(
        cache_file,
        [
            {"plugin_name": "pyright-lsp", "score": 10, "reasons": []},
            {"plugin_name": "typescript-lsp", "score": 8, "reasons": []},
        ],
    )

    dismiss_plugin("pyright-lsp", project_root=tmp_path)

    data = json.loads(cache_file.read_text())
    rec_names = [r["plugin_name"] for r in data["recommendations"]]
    assert "pyright-lsp" not in rec_names
    assert "typescript-lsp" in rec_names


def test_dismiss_plugin_no_duplicate(tmp_path: Path) -> None:
    graph_dir = tmp_path / ".htmlgraph"
    graph_dir.mkdir()
    cache_file = graph_dir / "plugin-recommendations.json"
    _write_cache(
        cache_file,
        [],
        dismissed=["pyright-lsp"],
    )

    dismiss_plugin("pyright-lsp", project_root=tmp_path)

    data = json.loads(cache_file.read_text())
    assert data["dismissed"].count("pyright-lsp") == 1


def test_dismiss_plugin_no_cache_file(tmp_path: Path) -> None:
    """dismiss_plugin creates the cache file if it doesn't exist."""
    dismiss_plugin("some-plugin", project_root=tmp_path)

    cache_file = tmp_path / ".htmlgraph" / "plugin-recommendations.json"
    assert cache_file.exists()
    data = json.loads(cache_file.read_text())
    assert "some-plugin" in data["dismissed"]
