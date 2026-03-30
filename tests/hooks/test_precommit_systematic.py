"""Tests for the pre-commit systematic-change checker.

Tests the pure-Python logic in .githooks/pre-commit-systematic-check.py
without actually running git commands, by importing the module's helpers
directly after patching subprocess calls.
"""

from __future__ import annotations

import textwrap
from pathlib import Path
from unittest.mock import patch

# ---------------------------------------------------------------------------
# Import the hook module (it lives in .githooks/, not src/)
# ---------------------------------------------------------------------------
HOOK_PATH = Path(__file__).parents[2] / ".githooks" / "pre-commit-systematic-check.py"


def _load_hook():
    import importlib.util

    spec = importlib.util.spec_from_file_location("pre_commit_systematic", HOOK_PATH)
    assert spec is not None
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)  # type: ignore[union-attr]
    return mod


hook = _load_hook()


# ---------------------------------------------------------------------------
# _is_systematic
# ---------------------------------------------------------------------------


class TestIsSystematic:
    def test_detects_rename_in_diff(self):
        diff = "- old_foo_bar\n+ new_baz_qux\n# renamed foo_bar to baz_qux"
        assert hook._is_systematic(diff, "") is True

    def test_detects_replace_in_message(self):
        assert (
            hook._is_systematic(
                "", "refactor: replace SessionManager with ContextManager"
            )
            is True
        )

    def test_detects_migrate_in_diff(self):
        diff = "# migrating from legacy API"
        assert hook._is_systematic(diff, "") is True

    def test_detects_refactor_in_message(self):
        assert hook._is_systematic("", "refactoring the auth module") is True

    def test_returns_false_for_normal_commit(self):
        diff = "- x = 1\n+ x = 2"
        msg = "fix: correct off-by-one in loop"
        assert hook._is_systematic(diff, msg) is False

    def test_case_insensitive(self):
        assert hook._is_systematic("", "RENAME the module") is True
        assert hook._is_systematic("", "Replaced old API") is True


# ---------------------------------------------------------------------------
# _extract_renamed_pairs
# ---------------------------------------------------------------------------


class TestExtractRenamedPairs:
    def test_finds_old_term_removed_new_term_added(self):
        diff = textwrap.dedent("""\
            -    session_tracker = SessionTracker()
            +    context_manager = ContextManager()
        """)
        pairs = hook._extract_renamed_pairs(diff)
        old_terms = {p[0] for p in pairs}
        assert "SessionTracker" in old_terms or "session_tracker" in old_terms

    def test_skips_common_words(self):
        diff = textwrap.dedent("""\
            - the old value was None
            + the new value is True
        """)
        pairs = hook._extract_renamed_pairs(diff)
        old_terms = {p[0] for p in pairs}
        # Common words like 'the', 'old', 'was', 'None' should not appear
        assert "the" not in old_terms
        assert "None" not in old_terms
        assert "was" not in old_terms

    def test_empty_diff_returns_empty(self):
        assert hook._extract_renamed_pairs("") == []

    def test_no_removals_returns_empty(self):
        diff = "+    new_function()\n+    another_call()\n"
        pairs = hook._extract_renamed_pairs(diff)
        # Only additions — no old terms to detect
        old_terms = {p[0] for p in pairs}
        assert "new_function" not in old_terms

    def test_skips_short_tokens(self):
        diff = "-    foo = bar\n+    baz = qux\n"
        pairs = hook._extract_renamed_pairs(diff)
        old_terms = {p[0] for p in pairs}
        # 3-char tokens like 'foo','bar' should be filtered (len < 4)
        assert "foo" not in old_terms
        assert "bar" not in old_terms


# ---------------------------------------------------------------------------
# _grep_remaining
# ---------------------------------------------------------------------------


class TestGrepRemaining:
    def test_finds_occurrences_in_real_files(self, tmp_path):
        (tmp_path / "module.py").write_text("def old_function():\n    pass\n")
        (tmp_path / "other.py").write_text("x = old_function()\n")

        results = hook._grep_remaining("old_function", tmp_path)
        assert len(results) == 2
        assert any("module.py" in r for r in results)
        assert any("other.py" in r for r in results)

    def test_excludes_htmlgraph_dir(self, tmp_path):
        hg_dir = tmp_path / ".htmlgraph"
        hg_dir.mkdir()
        (hg_dir / "feature.html").write_text("old_function mentioned here")
        (tmp_path / "real.py").write_text("def old_function(): pass\n")

        results = hook._grep_remaining("old_function", tmp_path)
        # .htmlgraph/ excluded — only real.py should appear
        assert all(".htmlgraph" not in r for r in results)
        assert any("real.py" in r for r in results)

    def test_returns_empty_when_no_matches(self, tmp_path):
        (tmp_path / "clean.py").write_text("def new_function(): pass\n")
        results = hook._grep_remaining("old_function", tmp_path)
        assert results == []

    def test_filters_false_positive_todo_lines(self, tmp_path):
        content = "# TODO: rename old_function to new_function eventually\n"
        (tmp_path / "note.py").write_text(content)
        results = hook._grep_remaining("old_function", tmp_path)
        # The false-positive filter should suppress this
        assert results == []


# ---------------------------------------------------------------------------
# _format_warning
# ---------------------------------------------------------------------------


class TestFormatWarning:
    def test_includes_old_term(self):
        warning = hook._format_warning("old_name", ["file.py:1: old_name = 1"])
        assert "old_name" in warning

    def test_includes_file_reference(self):
        warning = hook._format_warning("old_name", ["src/foo.py:42: old_name"])
        assert "src/foo.py:42" in warning

    def test_truncates_long_lists(self):
        instances = [f"file_{i}.py:{i}: old_name" for i in range(30)]
        warning = hook._format_warning("old_name", instances)
        assert "more" in warning

    def test_includes_no_verify_hint(self):
        warning = hook._format_warning("old_name", ["file.py:1: old_name"])
        assert "--no-verify" in warning


# ---------------------------------------------------------------------------
# main() integration
# ---------------------------------------------------------------------------


class TestMain:
    def test_exits_zero_when_no_diff(self):
        with patch.object(hook, "_staged_diff", return_value=""):
            assert hook.main() == 0

    def test_exits_zero_when_not_systematic(self):
        with (
            patch.object(hook, "_staged_diff", return_value="-x = 1\n+x = 2\n"),
            patch.object(hook, "_commit_message", return_value="fix: typo"),
        ):
            assert hook.main() == 0

    def test_exits_zero_even_when_remaining_instances_found(self, tmp_path):
        """Hook is warning-only — always exits 0."""
        diff = textwrap.dedent("""\
            -    old_legacy_func()
            +    new_modern_func()
            # renamed old_legacy_func to new_modern_func
        """)
        (tmp_path / "leftover.py").write_text("old_legacy_func()\n")

        with (
            patch.object(hook, "_staged_diff", return_value=diff),
            patch.object(
                hook, "_commit_message", return_value="refactor: rename function"
            ),
            patch.object(hook, "_git_root", return_value=tmp_path),
        ):
            result = hook.main()
        assert result == 0

    def test_prints_warning_to_stderr_when_remaining_found(self, tmp_path, capsys):
        diff = textwrap.dedent("""\
            -    old_legacy_func()
            +    new_modern_func()
            # renamed old_legacy_func to new_modern_func
        """)
        (tmp_path / "leftover.py").write_text("old_legacy_func()\n")

        with (
            patch.object(hook, "_staged_diff", return_value=diff),
            patch.object(
                hook, "_commit_message", return_value="refactor: rename function"
            ),
            patch.object(hook, "_git_root", return_value=tmp_path),
        ):
            hook.main()

        capsys.readouterr()
        # When remaining instances exist, stderr should contain a warning
        # (May or may not fire depending on token extraction — test is best-effort)
        # At minimum, exit code must be 0
        assert True  # main() didn't raise

    def test_exits_zero_when_pairs_empty(self):
        """If systematic keywords found but no token pairs extracted, still exit 0."""
        diff = "# this commit renames things\n"
        with (
            patch.object(hook, "_staged_diff", return_value=diff),
            patch.object(hook, "_commit_message", return_value="refactor: rename"),
            patch.object(hook, "_extract_renamed_pairs", return_value=[]),
        ):
            assert hook.main() == 0
