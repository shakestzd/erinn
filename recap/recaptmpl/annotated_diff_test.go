package recaptmpl_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/recap"
	"github.com/shakestzd/wipnote/recap/recaptmpl"
)

func diffFixture() recap.FileChange {
	return recap.FileChange{
		Path:   "internal/recap/collect.go",
		Change: recap.ChangeModify,
		Hunks: []recap.Hunk{
			{
				OldStart: 5, OldLines: 2, NewStart: 5, NewLines: 3,
				Header: "@@ -5,2 +5,3 @@ func Collect()",
				Before: []string{"a := 1", "b := 2"},
				After:  []string{"a := 1", "b := 3", "c := 4"},
			},
		},
	}
}

func TestAnnotatedDiff_SplitVsUnified(t *testing.T) {
	fc := diffFixture()

	split := &recaptmpl.AnnotatedDiff{File: fc, Mode: recaptmpl.DiffSplit, Language: "go"}
	var sb bytes.Buffer
	if err := split.Render(&sb); err != nil {
		t.Fatalf("split Render: %v", err)
	}
	splitHTML := sb.String()

	unified := &recaptmpl.AnnotatedDiff{File: fc, Mode: recaptmpl.DiffUnified, Language: "go"}
	var ub bytes.Buffer
	if err := unified.Render(&ub); err != nil {
		t.Fatalf("unified Render: %v", err)
	}
	unifiedHTML := ub.String()

	// Both modes: Prism-compatible markup + per-hunk summary + the hunk header.
	for name, html := range map[string]string{"split": splitHTML, "unified": unifiedHTML} {
		if !strings.Contains(html, "language-go") {
			t.Errorf("%s: missing Prism language class", name)
		}
		if !strings.Contains(html, "<pre") || !strings.Contains(html, "<code") {
			t.Errorf("%s: missing degrade-safe <pre><code>", name)
		}
		if !strings.Contains(html, "hunk-summary") {
			t.Errorf("%s: missing per-hunk summary", name)
		}
		if !strings.Contains(html, "func Collect()") {
			t.Errorf("%s: missing hunk header context", name)
		}
	}

	// Split mode carries side-by-side structure; unified does not.
	if !strings.Contains(splitHTML, "diff-split") {
		t.Error("split mode must mark itself as diff-split")
	}
	if !strings.Contains(unifiedHTML, "diff-unified") {
		t.Error("unified mode must mark itself as diff-unified")
	}
	if strings.Contains(unifiedHTML, "diff-split") {
		t.Error("unified mode must NOT emit split structure")
	}
}

func TestAnnotatedDiff_ContextNotCountedOrMarked(t *testing.T) {
	// A hunk with 2 context lines + 1 add + 1 del: counts must reflect only the
	// real change (1 added, 1 removed), and context lines must render as ctx, not
	// as add/del (the prior bug counted and marked context on both sides).
	fc := recap.FileChange{
		Path:   "x.go",
		Change: recap.ChangeModify,
		Hunks: []recap.Hunk{{
			Header: "@@ -1,3 +1,3 @@",
			Lines: []recap.DiffLine{
				{Kind: recap.DiffContext, Text: "ctxA"},
				{Kind: recap.DiffDel, Text: "old"},
				{Kind: recap.DiffAdd, Text: "new"},
				{Kind: recap.DiffContext, Text: "ctxB"},
			},
		}},
	}
	d := &recaptmpl.AnnotatedDiff{File: fc, Mode: recaptmpl.DiffUnified, Language: "go"}
	var b bytes.Buffer
	if err := d.Render(&b); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := b.String()
	if !strings.Contains(html, "1 added, 1 removed") {
		t.Errorf("summary must count only real changes (1 added, 1 removed), got:\n%s", html)
	}
	if !strings.Contains(html, `class="dl dl-ctx"`) {
		t.Errorf("context lines must render with the ctx class:\n%s", html)
	}
	// The context text must not be wrapped as add or del.
	if strings.Contains(html, `dl-add">+ ctxA`) || strings.Contains(html, `dl-del">- ctxA`) {
		t.Errorf("context line wrongly marked as a change:\n%s", html)
	}
}

func TestAnnotatedDiff_Collapsible(t *testing.T) {
	d := &recaptmpl.AnnotatedDiff{File: diffFixture(), Mode: recaptmpl.DiffUnified, Language: "go"}
	var b bytes.Buffer
	if err := d.Render(&b); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := b.String()
	// Each file's diff is wrapped in a native <details>/<summary> so it can be
	// collapsed with no JS (works in the standalone artifact and the dashboard).
	if !strings.Contains(html, "<details class=\"diff-file\"") {
		t.Errorf("diff file is not wrapped in <details>:\n%s", html)
	}
	if !strings.Contains(html, "<summary class=\"diff-file-header\"") {
		t.Errorf("diff file header is not a <summary>:\n%s", html)
	}
	// Defaults to open so content is visible without a click, but is collapsible.
	if !strings.Contains(html, "<details class=\"diff-file\" open>") {
		t.Errorf("diff file should default to open:\n%s", html)
	}
	if !strings.Contains(html, "diff-hunk-count") {
		t.Errorf("collapsed summary should show a hunk count:\n%s", html)
	}
}

func TestAnnotatedDiff_PerHunkSummaryCounts(t *testing.T) {
	fc := diffFixture() // 2 before lines, 3 after lines
	d := &recaptmpl.AnnotatedDiff{File: fc, Mode: recaptmpl.DiffUnified, Language: "go"}
	var buf bytes.Buffer
	if err := d.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	// Summary should reflect added/removed counts (1 net add, etc.).
	if !strings.Contains(html, "added") || !strings.Contains(html, "removed") {
		t.Errorf("expected add/remove counts in hunk summary:\n%s", html)
	}
}

func TestAnnotatedDiff_EscapesContent(t *testing.T) {
	fc := recap.FileChange{
		Path:   "x.go",
		Change: recap.ChangeModify,
		Hunks: []recap.Hunk{{
			Header: "@@ -1 +1 @@",
			Before: []string{"<script>alert(1)</script>"},
			After:  []string{"safe"},
		}},
	}
	d := &recaptmpl.AnnotatedDiff{File: fc, Mode: recaptmpl.DiffUnified, Language: "go"}
	var buf bytes.Buffer
	if err := d.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), "<script>alert(1)</script>") {
		t.Errorf("diff content must be HTML-escaped:\n%s", buf.String())
	}
}
