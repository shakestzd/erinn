package recaptmpl_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/internal/lineage"
	"github.com/shakestzd/wipnote/internal/recap"
	"github.com/shakestzd/wipnote/recap/recaptmpl"
)

func groundedData() recap.RecapData {
	return recap.RecapData{
		Outcome: "Add recap renderer + shared block components",
		Files: []recap.FileChange{
			{
				Path:   "plan/blocks/blocks.go",
				Change: recap.ChangeAdd,
				Hunks: []recap.Hunk{
					{
						OldStart: 0, OldLines: 0, NewStart: 1, NewLines: 2,
						Header: "@@ -0,0 +1,2 @@",
						Before: nil,
						After:  []string{"package blocks", "// leaf package"},
					},
				},
			},
			{
				Path:   "internal/recap/types.go",
				Change: recap.ChangeModify,
				Hunks: []recap.Hunk{
					{
						OldStart: 10, OldLines: 1, NewStart: 10, NewLines: 1,
						Header: "@@ -10,1 +10,1 @@ func foo()",
						Before: []string{"old line"},
						After:  []string{"new line"},
					},
				},
			},
		},
		Commits: []recap.Commit{
			{Hash: "abc1234", Message: "feat: blocks", FeatureID: "feat-2570725c"},
		},
		LineageChain: []lineage.Node{
			{ID: "plan-93b8eba0", Type: "plan", Title: "Visual recap", EdgeType: "implemented_in", Depth: 1, Direction: "ancestor"},
			{ID: "trk-a951e3c0", Type: "track", Title: "Visual planning track", EdgeType: "part_of", Depth: 1, Direction: "ancestor"},
			{ID: "recap-feat-2570725c", Type: "recap", EdgeType: "relates_to", Depth: 1, Direction: "descendant", Parent: "feat-2570725c"},
		},
		Provenance: recap.Provenance{
			Kind: recap.InputWorkItem, Input: "feat-2570725c",
			GitRange: "main..HEAD", Grounded: true,
		},
	}
}

func ungroundedData() recap.RecapData {
	return recap.RecapData{
		Outcome: "Range main..HEAD",
		Files: []recap.FileChange{
			{
				Path: "x.go", Change: recap.ChangeModify,
				Hunks: []recap.Hunk{{Header: "@@ -1 +1 @@", Before: []string{"a"}, After: []string{"b"}}},
			},
		},
		Commits:      []recap.Commit{{Hash: "deadbee", Message: "wip"}},
		LineageChain: nil,
		Provenance:   recap.Provenance{Kind: recap.InputRange, Input: "main..HEAD", Grounded: false},
	}
}

func TestRecapPage_Render(t *testing.T) {
	page := recaptmpl.Build(groundedData())
	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		"<!DOCTYPE html>",
		"</html>",
		"Add recap renderer + shared block components", // outcome
		`class="recap-zone recap-zone-lineage"`,        // unified spine zone
		`class="lin-tree"`,                             // the causal tree
		`class="lin-branch`,                            // nested indentation
		`lin-node pivot`,                               // pivot present when grounded
		"feat-2570725c",                                // pivot id
		"plan-93b8eba0",                                // ancestry node
		"recap-feat-2570725c",                          // direct downstream node
		"abc1234",                                      // produced commit
		"plan/blocks/blocks.go",                        // produced file (station)
		"internal/recap/types.go",
		`class="lin-file"`,        // file station is collapsible
		`annotated-diff embedded`, // diff embedded inside the file station
		"package blocks",          // embedded diff content (added line)
	} {
		if !strings.Contains(html, want) {
			t.Errorf("recap page missing %q", want)
		}
	}
	// The old separate file-tree and diff zones must be gone (unified into spine).
	for _, gone := range []string{`recap-zone-diff`, `recap-zone-files`, `lineage-table`} {
		if strings.Contains(html, gone) {
			t.Errorf("obsolete zone %q should be removed (unified into the spine)", gone)
		}
	}
}

func TestRecapPage_Render_Ungrounded(t *testing.T) {
	page := recaptmpl.Build(ungroundedData())
	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render (ungrounded): %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, "<!DOCTYPE html>") {
		t.Error("expected valid HTML document")
	}
	// Produced files still render (the spine's "produced" half works without grounding).
	if !strings.Contains(html, "x.go") {
		t.Error("expected produced files in ungrounded recap")
	}
	if !strings.Contains(html, "spine-produced-only") {
		t.Error("ungrounded spine should be marked produced-only")
	}
	// But there is no pivot or ancestry when ungrounded.
	if strings.Contains(html, "lin-node pivot") {
		t.Error("ungrounded recap must NOT render a pivot (no work-item grounding)")
	}
}

func TestRecapPage_Render_GroundedWithoutLineageKeepsPivot(t *testing.T) {
	// A grounded work-item recap with commits/files but NO graph neighbors must
	// still anchor on its pivot — not degrade to a produced-only range view.
	data := groundedData()
	data.LineageChain = nil
	html := mustRender(t, data)
	if !strings.Contains(html, "lin-node pivot") {
		t.Error("grounded recap without lineage neighbors must still render the pivot")
	}
	if !strings.Contains(html, "feat-2570725c") {
		t.Error("expected the work-item id on the pivot")
	}
	if strings.Contains(html, "spine-produced-only") {
		t.Error("grounded recap must not render as produced-only")
	}
}

func TestRecapPage_Render_FilesUnderChangeset(t *testing.T) {
	// Files are the union change set, so they hang off an explicit "changed files"
	// node — not nested under whichever commit rendered last.
	html := mustRender(t, groundedData())
	if !strings.Contains(html, "lin-node down changeset") {
		t.Error("expected an aggregate 'changed files' node above the file list")
	}
	if !strings.Contains(html, "changed files") {
		t.Error("expected the 'changed files' label")
	}
}

func TestRecapPage_Render_UngroundedCommitOnly(t *testing.T) {
	// A commit-only ungrounded recap must NOT emit an empty file branch or a
	// changeset node.
	data := recap.RecapData{
		Outcome:    "Range with no file diffs",
		Commits:    []recap.Commit{{Hash: "abc1234", Message: "wip"}},
		Provenance: recap.Provenance{Kind: recap.InputRange, Input: "main..HEAD", Grounded: false},
	}
	html := mustRender(t, data)
	if !strings.Contains(html, "abc1234") {
		t.Error("expected the commit to render")
	}
	if strings.Contains(html, "lin-node down file-node") {
		t.Error("commit-only recap must not emit any file nodes")
	}
	if strings.Contains(html, "changeset") {
		t.Error("commit-only recap must not emit a changed-files node")
	}
}

func mustRender(t *testing.T, data recap.RecapData) string {
	t.Helper()
	var buf bytes.Buffer
	if err := recaptmpl.Build(data).Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String()
}

func TestRecapPage_Render_DegradesWithoutPrism(t *testing.T) {
	// The standalone artifact must not inline the Prism bundle; diff code sits in
	// <pre><code> so it renders as plain text when Prism is absent. The unified
	// diff opts out of Prism (diff-code, no language- class) so its per-line dl-*
	// coloring survives Prism on the dashboard.
	page := recaptmpl.Build(groundedData())
	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if strings.Contains(html, "prism.min.js") || strings.Contains(html, "Prism.highlightAll") {
		t.Error("standalone recap must NOT inline the Prism bundle")
	}
	if !strings.Contains(html, "<pre") || !strings.Contains(html, "<code") {
		t.Error("expected <pre><code> degrade-safe diff markup")
	}
	if !strings.Contains(html, `class="diff-code"`) || !strings.Contains(html, "dl-") {
		t.Error("expected unified diff to use diff-code + dl-* line spans (Prism-safe)")
	}
}
