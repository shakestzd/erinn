"""
Tests for Phase 3.2: Step Dependencies Within Features.

Covers:
- Step.depends_on field
- Step.to_html() emits data-depends-on
- Step.to_context() includes depends_on info
- Parser extracts data-depends-on
- Node.get_ready_steps() dependency resolution
- Node.next_step respects dependencies
- event_tracker.resolve_active_step uses ready steps
- Circular dependency handling (no infinite loop)
"""

from __future__ import annotations

import textwrap
from pathlib import Path
from unittest.mock import patch

from htmlgraph.models import Node, Step
from htmlgraph.parser import HtmlParser

# ---------------------------------------------------------------------------
# Step model tests
# ---------------------------------------------------------------------------


def test_step_depends_on_field_default():
    step = Step(description="Do something")
    assert step.depends_on == []


def test_step_depends_on_field():
    step = Step(description="Do B", step_id="step-feat-1-1", depends_on=["step-feat-1-0"])
    assert step.depends_on == ["step-feat-1-0"]


def test_step_to_html_with_depends_on():
    step = Step(
        description="Install deps",
        step_id="step-1",
        depends_on=["step-0"],
    )
    html = step.to_html()
    assert 'data-depends-on="step-0"' in html
    assert 'data-step-id="step-1"' in html


def test_step_to_html_with_multiple_depends_on():
    step = Step(
        description="Final step",
        step_id="step-3",
        depends_on=["step-1", "step-2"],
    )
    html = step.to_html()
    assert 'data-depends-on="step-1,step-2"' in html


def test_step_to_html_without_depends_on():
    """Backward compat: no data-depends-on attribute when depends_on is empty."""
    step = Step(description="Simple step", step_id="step-0")
    html = step.to_html()
    assert "data-depends-on" not in html


def test_step_to_context_with_depends_on():
    step = Step(
        description="Run tests",
        step_id="step-2",
        depends_on=["step-0", "step-1"],
    )
    ctx = step.to_context()
    assert "depends_on" in ctx
    assert "step-0" in ctx
    assert "step-1" in ctx


def test_step_to_context_without_depends_on():
    step = Step(description="Write code", step_id="step-0")
    ctx = step.to_context()
    assert "depends_on" not in ctx


# ---------------------------------------------------------------------------
# Parser tests
# ---------------------------------------------------------------------------


def _make_feature_html(steps_html: str) -> str:
    return textwrap.dedent(f"""\
        <!DOCTYPE html>
        <html lang="en">
        <head><title>Test Feature</title></head>
        <body>
          <article id="feat-test" data-type="feature" data-status="in-progress"
                   data-priority="medium" data-created="2025-01-01T00:00:00"
                   data-updated="2025-01-01T00:00:00">
            <header><h1>Test Feature</h1></header>
            <section data-steps>
              <h3>Implementation Steps</h3>
              <ol>
                {steps_html}
              </ol>
            </section>
          </article>
        </body>
        </html>
    """)


def test_parser_extracts_depends_on():
    html = _make_feature_html(
        '<li data-completed="false" data-step-id="step-0">⏳ First</li>'
        '<li data-completed="false" data-step-id="step-1" data-depends-on="step-0">⏳ Second</li>'
    )
    parser = HtmlParser.from_string(html)
    steps = parser.get_steps()
    assert len(steps) == 2
    assert steps[0].get("depends_on", []) == []
    assert steps[1]["depends_on"] == ["step-0"]


def test_parser_extracts_multiple_depends_on():
    html = _make_feature_html(
        '<li data-completed="false" data-step-id="step-0">⏳ A</li>'
        '<li data-completed="false" data-step-id="step-1">⏳ B</li>'
        '<li data-completed="false" data-step-id="step-2" data-depends-on="step-0,step-1">⏳ C</li>'
    )
    parser = HtmlParser.from_string(html)
    steps = parser.get_steps()
    assert steps[2]["depends_on"] == ["step-0", "step-1"]


def test_parser_no_depends_on_attribute():
    html = _make_feature_html(
        '<li data-completed="false" data-step-id="step-0">⏳ Solo</li>'
    )
    parser = HtmlParser.from_string(html)
    steps = parser.get_steps()
    assert steps[0].get("depends_on", []) == []


# ---------------------------------------------------------------------------
# Node.get_ready_steps() tests
# ---------------------------------------------------------------------------


def _make_node(steps: list[Step]) -> Node:
    return Node(id="feat-test", title="Test Feature", steps=steps)


def test_node_get_ready_steps_no_deps():
    """All incomplete steps are ready when no dependency info exists."""
    steps = [
        Step(description="A", step_id="s0", completed=False),
        Step(description="B", step_id="s1", completed=False),
    ]
    node = _make_node(steps)
    ready = node.get_ready_steps()
    assert len(ready) == 2
    assert ready[0].step_id == "s0"
    assert ready[1].step_id == "s1"


def test_node_get_ready_steps_with_deps_unmet():
    """Step with unmet dependency is NOT in ready list."""
    steps = [
        Step(description="A", step_id="s0", completed=False),
        Step(description="B", step_id="s1", completed=False, depends_on=["s0"]),
    ]
    node = _make_node(steps)
    ready = node.get_ready_steps()
    assert len(ready) == 1
    assert ready[0].step_id == "s0"


def test_node_get_ready_steps_with_deps_met():
    """Step with met dependency appears in ready list."""
    steps = [
        Step(description="A", step_id="s0", completed=True),
        Step(description="B", step_id="s1", completed=False, depends_on=["s0"]),
    ]
    node = _make_node(steps)
    ready = node.get_ready_steps()
    assert len(ready) == 1
    assert ready[0].step_id == "s1"


def test_node_get_ready_steps_all_complete():
    """No ready steps when everything is done."""
    steps = [
        Step(description="A", step_id="s0", completed=True),
        Step(description="B", step_id="s1", completed=True, depends_on=["s0"]),
    ]
    node = _make_node(steps)
    assert node.get_ready_steps() == []


def test_node_get_ready_steps_multi_deps():
    """Step requiring two deps only ready when both done."""
    steps = [
        Step(description="A", step_id="s0", completed=True),
        Step(description="B", step_id="s1", completed=False),
        Step(description="C", step_id="s2", completed=False, depends_on=["s0", "s1"]),
    ]
    node = _make_node(steps)
    ready = node.get_ready_steps()
    # s1 is ready (no deps); s2 is blocked on s1
    assert {s.step_id for s in ready} == {"s1"}


# ---------------------------------------------------------------------------
# Node.next_step tests
# ---------------------------------------------------------------------------


def test_node_next_step_respects_deps():
    """next_step returns first READY step, not first incomplete."""
    steps = [
        # s0 is incomplete but depends on s1 (unusual ordering, but tests the logic)
        Step(description="A", step_id="s0", completed=False, depends_on=["s1"]),
        Step(description="B", step_id="s1", completed=False),
    ]
    node = _make_node(steps)
    # s0 depends on s1 (unmet), so next_step should be s1
    assert node.next_step is not None
    assert node.next_step.step_id == "s1"


def test_node_next_step_no_deps_returns_first_incomplete():
    """When no deps, next_step returns first incomplete (backward compat)."""
    steps = [
        Step(description="A", step_id="s0", completed=True),
        Step(description="B", step_id="s1", completed=False),
        Step(description="C", step_id="s2", completed=False),
    ]
    node = _make_node(steps)
    assert node.next_step is not None
    assert node.next_step.step_id == "s1"


def test_node_next_step_none_when_all_complete():
    steps = [
        Step(description="A", step_id="s0", completed=True),
    ]
    node = _make_node(steps)
    assert node.next_step is None


# ---------------------------------------------------------------------------
# event_tracker.resolve_active_step tests
# ---------------------------------------------------------------------------


def test_resolve_active_step_no_feature_id():
    from htmlgraph.hooks.event_tracker import resolve_active_step

    assert resolve_active_step(None) is None


def test_resolve_active_step_missing_file():
    from htmlgraph.hooks.event_tracker import resolve_active_step

    with patch("htmlgraph.hooks.event_tracker.Path") as mock_path:
        mock_path.cwd.return_value = Path("/nonexistent")
        # Should return None gracefully (directory not found)
        result = resolve_active_step("feat-does-not-exist")
        assert result is None


def test_resolve_active_step_with_deps(tmp_path: Path):
    """resolve_active_step picks the first ready step when deps are present."""
    from htmlgraph.hooks.event_tracker import resolve_active_step

    features_dir = tmp_path / ".htmlgraph" / "features"
    features_dir.mkdir(parents=True)

    html = _make_feature_html(
        '<li data-completed="true" data-step-id="s0">✅ Done</li>'
        '<li data-completed="false" data-step-id="s1" data-depends-on="s0">⏳ Ready</li>'
        '<li data-completed="false" data-step-id="s2" data-depends-on="s1">⏳ Blocked</li>'
    )
    feature_file = features_dir / "feat-deptest.html"
    feature_file.write_text(html)

    with patch(
        "htmlgraph.hooks.event_tracker.Path"
    ) as mock_path_cls:
        mock_path_cls.cwd.return_value = tmp_path
        # Make Path(...) calls for feature_file work normally
        mock_path_cls.side_effect = lambda *a, **k: Path(*a, **k)
        mock_path_cls.cwd.return_value = tmp_path

        result = resolve_active_step("feat-deptest")

    assert result == "s1"


def test_resolve_active_step_without_deps(tmp_path: Path):
    """resolve_active_step falls back to first incomplete when no deps."""
    from htmlgraph.hooks.event_tracker import resolve_active_step

    features_dir = tmp_path / ".htmlgraph" / "features"
    features_dir.mkdir(parents=True)

    html = _make_feature_html(
        '<li data-completed="true" data-step-id="s0">✅ Done</li>'
        '<li data-completed="false" data-step-id="s1">⏳ Next</li>'
    )
    feature_file = features_dir / "feat-nodeps.html"
    feature_file.write_text(html)

    with patch(
        "htmlgraph.hooks.event_tracker.Path"
    ) as mock_path_cls:
        mock_path_cls.side_effect = lambda *a, **k: Path(*a, **k)
        mock_path_cls.cwd.return_value = tmp_path

        result = resolve_active_step("feat-nodeps")

    assert result == "s1"


# ---------------------------------------------------------------------------
# Circular dependency handling
# ---------------------------------------------------------------------------


def test_circular_dependency_handling():
    """Circular deps must not cause infinite loop; no step is ready."""
    steps = [
        Step(description="A", step_id="s0", completed=False, depends_on=["s1"]),
        Step(description="B", step_id="s1", completed=False, depends_on=["s0"]),
    ]
    node = _make_node(steps)
    # get_ready_steps() iterates once — no recursion, no loop
    ready = node.get_ready_steps()
    # Neither step can be ready since each depends on the other
    assert ready == []
    # next_step falls back to first incomplete step
    ns = node.next_step
    assert ns is not None
    assert ns.step_id == "s0"
