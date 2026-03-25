"""
Tests for Skill Scout plugin discovery module.

All tests use tmp_path for cache dirs and mock subprocess/network calls
— no real GitHub API calls are made.
"""

from __future__ import annotations

import json
import time
from pathlib import Path
from unittest.mock import MagicMock, patch

from htmlgraph.skill_scout.github_search import (
    PluginInfo,
    _normalize_gh_cli_result,
    discover_plugins,
)
from htmlgraph.skill_scout.plugin_index import PluginIndex, _score_plugin

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


def _make_plugin(**kwargs: object) -> PluginInfo:
    defaults: dict[str, object] = {
        "name": "my-plugin",
        "repo": "owner/my-plugin",
        "description": "A helpful plugin",
        "version": "1.0.0",
        "category": "productivity",
        "keywords": ["automation", "workflow"],
        "homepage": "https://example.com",
        "source_url": "https://github.com/owner/my-plugin",
    }
    defaults.update(kwargs)
    return PluginInfo(**defaults)  # type: ignore[arg-type]


# ---------------------------------------------------------------------------
# github_search tests
# ---------------------------------------------------------------------------


def test_normalize_gh_result() -> None:
    """Parsing raw gh search output produces the correct PluginInfo fields."""
    raw = {
        "path": ".claude-plugin/plugin.json",
        "url": "https://github.com/owner/repo/blob/main/.claude-plugin/plugin.json",
        "sha": "abc123",
        "repository": {
            "name": "repo",
            "nameWithOwner": "owner/repo",
            "description": "A cool plugin",
            "url": "https://github.com/owner/repo",
        },
    }
    result = _normalize_gh_cli_result(raw)

    assert result.name == "repo"
    assert result.repo == "owner/repo"
    assert result.description == "A cool plugin"
    assert result.homepage == "https://github.com/owner/repo"
    assert result.source_url == raw["url"]


def test_normalize_gh_result_missing_description() -> None:
    """Missing description defaults to empty string."""
    raw = {
        "path": ".claude-plugin/plugin.json",
        "url": "",
        "sha": "",
        "repository": {
            "name": "repo",
            "nameWithOwner": "owner/repo",
            "description": None,
            "url": "",
        },
    }
    result = _normalize_gh_cli_result(raw)
    assert result.description == ""


def test_discover_plugins_uses_gh_cli_first() -> None:
    """discover_plugins() calls gh CLI by default."""
    fake_output = json.dumps(
        [
            {
                "path": ".claude-plugin/plugin.json",
                "url": "https://github.com/owner/repo",
                "sha": "x",
                "repository": {
                    "name": "repo",
                    "nameWithOwner": "owner/repo",
                    "description": "desc",
                    "url": "https://github.com/owner/repo",
                },
            }
        ]
    )
    mock_result = MagicMock(returncode=0, stdout=fake_output, stderr="")
    with patch("subprocess.run", return_value=mock_result) as mock_run:
        plugins = discover_plugins(limit=5)

    mock_run.assert_called_once()
    assert len(plugins) == 1
    assert plugins[0].repo == "owner/repo"


def test_discover_plugins_falls_back_to_api_when_gh_missing() -> None:
    """discover_plugins() falls back to REST API when gh CLI is not found."""
    api_response = json.dumps(
        {
            "items": [
                {
                    "html_url": "https://github.com/owner/plug/blob/main/.claude-plugin/plugin.json",
                    "repository": {
                        "full_name": "owner/plug",
                        "name": "plug",
                        "description": "api plugin",
                        "html_url": "https://github.com/owner/plug",
                    },
                }
            ]
        }
    ).encode()

    mock_resp = MagicMock()
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)
    mock_resp.read.return_value = api_response

    with patch("subprocess.run", side_effect=FileNotFoundError):
        with patch("urllib.request.urlopen", return_value=mock_resp):
            plugins = discover_plugins(limit=5)

    assert len(plugins) == 1
    assert plugins[0].repo == "owner/plug"


# ---------------------------------------------------------------------------
# PluginIndex — cache staleness
# ---------------------------------------------------------------------------


def test_plugin_index_cache_stale_when_missing(tmp_path: Path) -> None:
    """A newly created index with no cache file is stale."""
    idx = PluginIndex(cache_dir=tmp_path)
    assert idx.is_stale() is True


def test_plugin_index_cache_not_stale_when_fresh(tmp_path: Path) -> None:
    """A freshly written cache file is not stale."""
    idx = PluginIndex(cache_dir=tmp_path)
    (tmp_path / "index.json").write_text("[]")
    assert idx.is_stale() is False


def test_plugin_index_cache_stale_after_ttl(tmp_path: Path) -> None:
    """Cache is stale when its mtime is older than TTL."""
    cache_file = tmp_path / "index.json"
    cache_file.write_text("[]")
    # Back-date the file by TTL + 1 second
    old_time = time.time() - 86401
    import os

    os.utime(cache_file, (old_time, old_time))

    idx = PluginIndex(cache_dir=tmp_path)
    assert idx.is_stale() is True


# ---------------------------------------------------------------------------
# PluginIndex — save / load round-trip
# ---------------------------------------------------------------------------


def test_plugin_index_save_load(tmp_path: Path) -> None:
    """Saved plugins can be reloaded with all fields intact."""
    idx = PluginIndex(cache_dir=tmp_path)
    plugins = [
        _make_plugin(name="alpha", repo="owner/alpha"),
        _make_plugin(name="beta", repo="owner/beta", keywords=["ai", "code"]),
    ]
    idx._save(plugins)
    loaded = idx.load()

    assert len(loaded) == 2
    assert loaded[0].name == "alpha"
    assert loaded[1].keywords == ["ai", "code"]


def test_plugin_index_load_returns_empty_when_missing(tmp_path: Path) -> None:
    """load() returns [] when no cache file exists."""
    idx = PluginIndex(cache_dir=tmp_path)
    assert idx.load() == []


# ---------------------------------------------------------------------------
# PluginIndex — search
# ---------------------------------------------------------------------------


def test_plugin_index_search_matches_name(tmp_path: Path) -> None:
    """Search matches on plugin name."""
    idx = PluginIndex(cache_dir=tmp_path)
    idx._save(
        [
            _make_plugin(name="git-helper", repo="o/git-helper", description=""),
            _make_plugin(name="unrelated", repo="o/unrelated", description=""),
        ]
    )
    results = idx.search("git")
    assert len(results) == 1
    assert results[0].name == "git-helper"


def test_plugin_index_search_matches_description(tmp_path: Path) -> None:
    """Search matches on plugin description."""
    idx = PluginIndex(cache_dir=tmp_path)
    idx._save(
        [
            _make_plugin(name="x", repo="o/x", description="automates deployment"),
            _make_plugin(name="y", repo="o/y", description="something else"),
        ]
    )
    results = idx.search("deploy")
    assert len(results) == 1
    assert results[0].name == "x"


def test_plugin_index_search_matches_keywords(tmp_path: Path) -> None:
    """Search matches on plugin keywords."""
    idx = PluginIndex(cache_dir=tmp_path)
    idx._save(
        [
            _make_plugin(name="a", repo="o/a", keywords=["testing", "jest"]),
            _make_plugin(name="b", repo="o/b", keywords=["linting"]),
        ]
    )
    results = idx.search("jest")
    assert len(results) == 1
    assert results[0].name == "a"


def test_plugin_index_search_ranks_name_highest(tmp_path: Path) -> None:
    """Name matches score higher than description matches."""
    idx = PluginIndex(cache_dir=tmp_path)
    idx._save(
        [
            _make_plugin(
                name="other", repo="o/other", description="has deploy in desc"
            ),
            _make_plugin(name="deploy-tool", repo="o/deploy-tool", description=""),
        ]
    )
    results = idx.search("deploy")
    # deploy-tool (name match, score 10) should come before other (desc match, score 5)
    assert results[0].name == "deploy-tool"


def test_plugin_index_search_no_results(tmp_path: Path) -> None:
    """Query with no matches returns empty list."""
    idx = PluginIndex(cache_dir=tmp_path)
    idx._save([_make_plugin(name="foo", repo="o/foo")])
    assert idx.search("zzznomatch") == []


# ---------------------------------------------------------------------------
# PluginIndex — merge / dedup
# ---------------------------------------------------------------------------


def test_merge_deduplicates_by_repo(tmp_path: Path) -> None:
    """Plugins with the same repo appear only once after merge."""
    idx = PluginIndex(cache_dir=tmp_path)
    a = _make_plugin(name="plugin", repo="owner/plugin")
    b = _make_plugin(name="plugin-copy", repo="owner/plugin")  # same repo
    c = _make_plugin(name="other", repo="owner/other")

    merged = idx._merge([a, c], [b])
    repos = [p.repo for p in merged]
    assert repos.count("owner/plugin") == 1
    assert len(merged) == 2


def test_merge_deduplicates_by_name_when_no_repo(tmp_path: Path) -> None:
    """Plugins with empty repo are deduplicated by name."""
    idx = PluginIndex(cache_dir=tmp_path)
    a = _make_plugin(name="anon", repo="")
    b = _make_plugin(name="anon", repo="")

    merged = idx._merge([a], [b])
    assert len(merged) == 1


def test_merge_preserves_order_first_seen(tmp_path: Path) -> None:
    """First occurrence wins when deduplicating."""
    idx = PluginIndex(cache_dir=tmp_path)
    first = _make_plugin(name="plugin", repo="owner/plugin", description="first")
    second = _make_plugin(name="plugin", repo="owner/plugin", description="second")

    merged = idx._merge([first], [second])
    assert merged[0].description == "first"


# ---------------------------------------------------------------------------
# Marketplace JSON parsing
# ---------------------------------------------------------------------------


def test_marketplace_json_parsing(tmp_path: Path) -> None:
    """_fetch_marketplace() correctly parses the marketplace.json schema."""
    marketplace_data = {
        "plugins": [
            {
                "name": "code-reviewer",
                "description": "Automated code review",
                "version": "2.1.0",
                "category": "quality",
                "keywords": ["review", "ci"],
                "homepage": "https://example.com/reviewer",
                "source": {
                    "repo": "owner/code-reviewer",
                    "url": "https://github.com/owner/code-reviewer",
                },
            }
        ]
    }
    encoded = json.dumps(marketplace_data).encode()

    mock_resp = MagicMock()
    mock_resp.__enter__ = lambda s: s
    mock_resp.__exit__ = MagicMock(return_value=False)
    mock_resp.read.return_value = encoded

    idx = PluginIndex(cache_dir=tmp_path)
    with patch("urllib.request.urlopen", return_value=mock_resp):
        plugins = idx._fetch_marketplace()

    assert len(plugins) == 1
    p = plugins[0]
    assert p.name == "code-reviewer"
    assert p.repo == "owner/code-reviewer"
    assert p.description == "Automated code review"
    assert p.version == "2.1.0"
    assert p.category == "quality"
    assert p.keywords == ["review", "ci"]
    assert p.homepage == "https://example.com/reviewer"
    assert p.source_url == "https://github.com/owner/code-reviewer"


def test_marketplace_fetch_returns_empty_on_network_error(tmp_path: Path) -> None:
    """_fetch_marketplace() returns [] on any network error."""
    from urllib.error import URLError

    idx = PluginIndex(cache_dir=tmp_path)
    with patch("urllib.request.urlopen", side_effect=URLError("timeout")):
        plugins = idx._fetch_marketplace()

    assert plugins == []


# ---------------------------------------------------------------------------
# _score_plugin helper
# ---------------------------------------------------------------------------


def test_score_plugin_no_match() -> None:
    p = _make_plugin(name="foo", repo="o/foo", description="bar", keywords=["baz"])
    assert _score_plugin(p, "zzz") == 0


def test_score_plugin_multiple_matches() -> None:
    p = _make_plugin(
        name="deploy-tool",
        repo="o/deploy-tool",
        description="automates deploy steps",
        keywords=["deploy"],
        category="devops",
    )
    score = _score_plugin(p, "deploy")
    # name(10) + description(5) + keyword(7) = 22
    assert score == 22
