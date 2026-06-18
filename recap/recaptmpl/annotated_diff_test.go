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
