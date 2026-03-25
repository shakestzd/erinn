"""Tests for skill_scout.project_analyzer and skill_scout.work_patterns."""

from __future__ import annotations

import json
import sqlite3
from pathlib import Path

import pytest
from htmlgraph.skill_scout.project_analyzer import ProjectAnalyzer
from htmlgraph.skill_scout.work_patterns import (
    ToolUsage,
    WorkPatternAnalyzer,
)

# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture()
def tmp_project(tmp_path: Path) -> Path:
    """Return a temporary directory representing a project root."""
    return tmp_path


# ---------------------------------------------------------------------------
# ProjectAnalyzer tests
# ---------------------------------------------------------------------------


class TestProjectAnalyzerLanguageDetection:
    def test_detects_python_from_pyproject_toml(self, tmp_project: Path) -> None:
        (tmp_project / "pyproject.toml").write_text('[project]\nname = "demo"\n')
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert "python" in analysis.languages

    def test_detects_javascript_from_package_json(self, tmp_project: Path) -> None:
        (tmp_project / "package.json").write_text('{"name": "demo"}')
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert "javascript" in analysis.languages

    def test_detects_elixir_from_mix_exs(self, tmp_project: Path) -> None:
        (tmp_project / "mix.exs").write_text("defmodule Demo.MixProject do\nend\n")
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert "elixir" in analysis.languages

    def test_no_languages_for_empty_project(self, tmp_project: Path) -> None:
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert analysis.languages == []

    def test_primary_language_returns_first(self, tmp_project: Path) -> None:
        (tmp_project / "pyproject.toml").write_text("")
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert analysis.primary_language() == "python"

    def test_primary_language_returns_none_when_empty(self, tmp_project: Path) -> None:
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert analysis.primary_language() is None


class TestProjectAnalyzerFrameworkDetection:
    def test_detects_pytest_from_pyproject_toml(self, tmp_project: Path) -> None:
        (tmp_project / "pyproject.toml").write_text(
            '[project.optional-dependencies]\ntest = ["pytest"]\n'
        )
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert "pytest" in analysis.frameworks

    def test_detects_react_from_package_json(self, tmp_project: Path) -> None:
        pkg = {"dependencies": {"react": "^18.0.0"}}
        (tmp_project / "package.json").write_text(json.dumps(pkg))
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert "React" in analysis.frameworks

    def test_detects_typescript_from_package_json(self, tmp_project: Path) -> None:
        pkg = {"devDependencies": {"typescript": "^5.0.0"}}
        (tmp_project / "package.json").write_text(json.dumps(pkg))
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert "TypeScript" in analysis.frameworks

    def test_no_frameworks_for_bare_project(self, tmp_project: Path) -> None:
        (tmp_project / "pyproject.toml").write_text('[project]\nname = "bare"\n')
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert analysis.frameworks == []


class TestProjectAnalyzerStructuralSignals:
    def test_detects_tests_directory(self, tmp_project: Path) -> None:
        (tmp_project / "tests").mkdir()
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert analysis.has_tests is True

    def test_detects_ci_from_github_workflows(self, tmp_project: Path) -> None:
        workflows = tmp_project / ".github" / "workflows"
        workflows.mkdir(parents=True)
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert analysis.has_ci is True

    def test_detects_docker(self, tmp_project: Path) -> None:
        (tmp_project / "Dockerfile").write_text("FROM python:3.12\n")
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert analysis.has_docker is True

    def test_detects_htmlgraph(self, tmp_project: Path) -> None:
        (tmp_project / ".htmlgraph").mkdir()
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert analysis.has_htmlgraph is True

    def test_no_signals_for_empty_project(self, tmp_project: Path) -> None:
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert analysis.has_tests is False
        assert analysis.has_ci is False
        assert analysis.has_docker is False
        assert analysis.has_htmlgraph is False

    def test_manifest_files_recorded(self, tmp_project: Path) -> None:
        (tmp_project / "pyproject.toml").write_text("")
        (tmp_project / "package.json").write_text("{}")
        analysis = ProjectAnalyzer(tmp_project).analyze()
        assert "pyproject.toml" in analysis.manifest_files
        assert "package.json" in analysis.manifest_files


# ---------------------------------------------------------------------------
# WorkPatternAnalyzer tests
# ---------------------------------------------------------------------------


def _make_db(path: Path, rows: list[tuple[str, int | None]]) -> None:
    """Create a minimal events table and insert (tool_name, is_error) rows."""
    with sqlite3.connect(str(path)) as conn:
        conn.execute(
            """
            CREATE TABLE events (
                id INTEGER PRIMARY KEY,
                tool_name TEXT,
                is_error INTEGER DEFAULT 0,
                error TEXT
            )
            """
        )
        conn.executemany("INSERT INTO events (tool_name, is_error) VALUES (?, ?)", rows)
        conn.commit()


class TestWorkPatternAnalyzerBasic:
    def test_returns_empty_summary_when_db_missing(self, tmp_path: Path) -> None:
        analyzer = WorkPatternAnalyzer(tmp_path / "nonexistent.db")
        summary = analyzer.analyze()
        assert summary.total_tool_calls == 0
        assert summary.tool_usage == []

    def test_counts_tool_calls(self, tmp_path: Path) -> None:
        db = tmp_path / "test.db"
        _make_db(db, [("Bash", 0), ("Bash", 0), ("Read", 0)])
        summary = WorkPatternAnalyzer(db).analyze()
        assert summary.total_tool_calls == 3
        bash_usage = next(u for u in summary.tool_usage if u.tool_name == "Bash")
        assert bash_usage.call_count == 2

    def test_most_used_tools_ordered(self, tmp_path: Path) -> None:
        db = tmp_path / "test.db"
        _make_db(
            db,
            [("Read", 0)] * 5 + [("Bash", 0)] * 3 + [("Write", 0)] * 1,
        )
        summary = WorkPatternAnalyzer(db).analyze()
        top = summary.most_used_tools(2)
        assert top[0].tool_name == "Read"
        assert top[1].tool_name == "Bash"

    def test_least_used_tools_ordered(self, tmp_path: Path) -> None:
        db = tmp_path / "test.db"
        _make_db(db, [("Read", 0)] * 10 + [("Write", 0)] * 1)
        summary = WorkPatternAnalyzer(db).analyze()
        bottom = summary.least_used_tools(1)
        assert bottom[0].tool_name == "Write"


class TestWorkPatternDelegationRate:
    def test_zero_when_no_delegation_tools(self, tmp_path: Path) -> None:
        db = tmp_path / "test.db"
        _make_db(db, [("Bash", 0), ("Read", 0)])
        summary = WorkPatternAnalyzer(db).analyze()
        assert summary.delegation_rate == 0.0

    def test_nonzero_with_task_tool(self, tmp_path: Path) -> None:
        db = tmp_path / "test.db"
        _make_db(db, [("Bash", 0)] * 4 + [("Task", 0)] * 1)
        summary = WorkPatternAnalyzer(db).analyze()
        assert summary.delegation_rate == pytest.approx(0.2)


class TestWorkPatternCapabilityGaps:
    def test_flags_delegation_gap_when_task_never_used(self, tmp_path: Path) -> None:
        db = tmp_path / "test.db"
        _make_db(db, [("Bash", 0), ("Read", 0)])
        summary = WorkPatternAnalyzer(db).analyze()
        assert "delegation" in summary.capability_gaps

    def test_no_delegation_gap_when_task_used(self, tmp_path: Path) -> None:
        db = tmp_path / "test.db"
        _make_db(db, [("Bash", 0), ("Task", 0)])
        summary = WorkPatternAnalyzer(db).analyze()
        assert "delegation" not in summary.capability_gaps

    def test_flags_web_gap_when_no_web_tools(self, tmp_path: Path) -> None:
        db = tmp_path / "test.db"
        _make_db(db, [("Bash", 0)])
        summary = WorkPatternAnalyzer(db).analyze()
        assert "web_browsing" in summary.capability_gaps

    def test_no_web_gap_when_webfetch_used(self, tmp_path: Path) -> None:
        db = tmp_path / "test.db"
        _make_db(db, [("Bash", 0), ("WebFetch", 0)])
        summary = WorkPatternAnalyzer(db).analyze()
        assert "web_browsing" not in summary.capability_gaps


class TestToolUsageDataclass:
    def test_error_rate_zero_when_no_errors(self) -> None:
        u = ToolUsage(tool_name="Bash", call_count=10, error_count=0)
        assert u.error_rate == 0.0

    def test_error_rate_calculated_correctly(self) -> None:
        u = ToolUsage(tool_name="Bash", call_count=10, error_count=2)
        assert u.error_rate == pytest.approx(0.2)

    def test_error_rate_zero_when_no_calls(self) -> None:
        u = ToolUsage(tool_name="Bash", call_count=0, error_count=0)
        assert u.error_rate == 0.0
