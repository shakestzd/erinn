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
			{ID: "feat-2570725c", Type: "feature", Title: "Recap renderer", EdgeType: "", Depth: 0},
			{ID: "plan-93b8eba0", Type: "plan", Title: "Visual recap", EdgeType: "planned_in", Depth: 1, Parent: "feat-2570725c"},
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
		"plan/blocks/blocks.go",                        // file tree
		"internal/recap/types.go",
		`class="block block-file-tree"`,         // shared block reuse
		`class="recap-zone recap-zone-diff"`,    // annotated diff zone
		`class="recap-zone recap-zone-lineage"`, // lineage zone present when grounded
		"feat-2570725c",                         // lineage node
		"plan-93b8eba0",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("recap page missing %q", want)
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
	if !strings.Contains(html, "x.go") {
		t.Error("expected file zone in ungrounded recap")
	}
	if strings.Contains(html, "recap-zone-lineage") {
		t.Error("lineage zone must be OMITTED when ungrounded (LineageChain nil)")
	}
}

func TestRecapPage_Render_DegradesWithoutPrism(t *testing.T) {
	// The standalone artifact must not inline the Prism bundle; diff code
	// must sit in <pre><code class="language-..."> so it renders as plain
	// text when Prism is absent.
	page := recaptmpl.Build(groundedData())
	var buf bytes.Buffer
	if err := page.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	html := buf.String()
	if strings.Contains(html, "prism.min.js") || strings.Contains(html, "Prism.highlightAll") {
		t.Error("standalone recap must NOT inline the Prism bundle")
	}
	if !strings.Contains(html, "language-") {
		t.Error("expected Prism-compatible language- class on code blocks")
	}
	if !strings.Contains(html, "<pre") || !strings.Contains(html, "<code") {
		t.Error("expected <pre><code> degrade-safe diff markup")
	}
}
