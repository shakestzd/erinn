package blocks_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/blocks"
	"github.com/shakestzd/wipnote/plan/planyaml"
)

func render(t *testing.T, c blocks.Block) string {
	t.Helper()
	var buf bytes.Buffer
	if err := c.Render(&buf); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String()
}

func TestBlocks_DataModel(t *testing.T) {
	dm := &blocks.DataModel{
		Name: "RecapData",
		Columns: []blocks.Column{
			{Name: "Outcome", Type: "string"},
			{Name: "Files", Type: "[]FileChange", Note: "sorted by path"},
		},
	}
	html := render(t, dm)
	for _, want := range []string{
		`class="block block-data-model"`,
		"RecapData",
		"Outcome",
		"string",
		"[]FileChange",
		"sorted by path",
		"<table",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("data-model output missing %q\n%s", want, html)
		}
	}
}

func TestBlocks_DataModel_EscapesInput(t *testing.T) {
	dm := &blocks.DataModel{
		Name:    "<script>x</script>",
		Columns: []blocks.Column{{Name: "<b>", Type: "y"}},
	}
	html := render(t, dm)
	if strings.Contains(html, "<script>x</script>") {
		t.Errorf("expected name to be HTML-escaped, got raw script tag:\n%s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("expected escaped name, got:\n%s", html)
	}
}

func TestBlocks_FileTree(t *testing.T) {
	ft := &blocks.FileTree{
		Entries: []blocks.FileNode{
			{Path: "recap/recaptmpl/recaptmpl.go", Change: "add"},
			{Path: "plan/blocks/blocks.go", Change: "add"},
			{Path: "internal/recap/types.go", Change: "modify"},
		},
	}
	html := render(t, ft)
	for _, want := range []string{
		`class="block block-file-tree"`,
		"recap/recaptmpl/recaptmpl.go",
		"plan/blocks/blocks.go",
		"internal/recap/types.go",
		`class="file-change file-change-add"`,
		`class="file-change file-change-modify"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("file-tree output missing %q\n%s", want, html)
		}
	}
}

func TestBlocks_FileTree_NoChangeBadge(t *testing.T) {
	// A plain file-tree (plantmpl planning use) has no Change set; it must
	// still render the path and must not emit a change badge.
	ft := &blocks.FileTree{
		Entries: []blocks.FileNode{{Path: "cmd/wipnote/main.go"}},
	}
	html := render(t, ft)
	if !strings.Contains(html, "cmd/wipnote/main.go") {
		t.Errorf("expected path in output:\n%s", html)
	}
	if strings.Contains(html, "file-change-") {
		t.Errorf("expected no change badge when Change empty:\n%s", html)
	}
}

func TestBlocks_FileTree_EscapesPath(t *testing.T) {
	ft := &blocks.FileTree{Entries: []blocks.FileNode{{Path: "<x>/a.go"}}}
	html := render(t, ft)
	if strings.Contains(html, "<x>/a.go") {
		t.Errorf("expected path to be escaped:\n%s", html)
	}
}

// fileTreeGutterWidthRe extracts the width attribute of each per-row depth
// gutter SVG (see blocks.FileTree.Rows), in document order.
var fileTreeGutterWidthRe = regexp.MustCompile(`<svg[^>]*width="([0-9.]+)"[^>]*class="file-tree-gutter"`)

// TestFileTree_Indents is the slice-9 (feat-47793a68) contract: paths at
// differing depths must get differing x offsets. A previously-flat corpus
// sample (mixed shallow/nested paths) must now show a gutter that grows with
// depth, and a genuinely flat entry must render with NO gutter at all — the
// small-corpus (~2.5 entries) common case must stay visually unchanged.
func TestFileTree_Indents(t *testing.T) {
	ft := &blocks.FileTree{
		Entries: []blocks.FileNode{
			{Path: "a.go"},                   // depth 0 — top-level, no gutter
			{Path: "cmd/main.go"},            // depth 1
			{Path: "plan/blocks/blocks.go"},  // depth 2
			{Path: "plan/blocks/svg/svg.go"}, // depth 3
		},
	}
	html := render(t, ft)

	if !strings.Contains(html, "<line") {
		t.Errorf("expected drawn connector lines in nested rows:\n%s", html)
	}

	matches := fileTreeGutterWidthRe.FindAllStringSubmatch(html, -1)
	if len(matches) != 3 {
		t.Fatalf("expected 3 gutters (one per nested entry, none for the flat entry), got %d:\n%s", len(matches), html)
	}
	widths := make([]string, len(matches))
	for i, m := range matches {
		widths[i] = m[1]
	}
	want := []string{"14", "28", "42"}
	for i, w := range want {
		if widths[i] != w {
			t.Errorf("gutter %d: want width %q (depth-scaled x offset), got %q\nall widths: %v\n%s", i, w, widths[i], widths, html)
		}
	}
}

// TestBlockAnchors_Unchanged guards the slice-8 dashboard annotation contract
// (slice-<num>-block-<type>-<idx>). That anchor id is stamped externally by
// plan/plantmpl's blocks-zone wrapper around each block's rendered HTML — it
// is not part of this package — but the wrapper's join key is the block's own
// top-level "block block-<type>" class, which this locks byte-for-byte so
// feat-47793a68's file-tree/diagram rendering changes cannot silently break
// it.
func TestBlockAnchors_Unchanged(t *testing.T) {
	ft := render(t, &blocks.FileTree{Entries: []blocks.FileNode{{Path: "a/b.go"}}})
	if !strings.Contains(ft, `class="block block-file-tree"`) {
		t.Errorf("file-tree must keep its block-file-tree wrapper class (anchor join key):\n%s", ft)
	}

	d := render(t, &blocks.Diagram{Steps: []string{"A", "B"}})
	if !strings.Contains(d, `class="block block-diagram"`) {
		t.Errorf("diagram must keep its block-diagram wrapper class (anchor join key):\n%s", d)
	}
}

func TestBlocks_Render_AllImplementBlock(t *testing.T) {
	// Compile-time + run-time guarantee that all renderers satisfy Block.
	bs := []blocks.Block{
		&blocks.DataModel{Name: "X"},
		&blocks.FileTree{Entries: []blocks.FileNode{{Path: "a"}}},
	}
	for _, b := range bs {
		if got := render(t, b); got == "" {
			t.Errorf("%T rendered empty", b)
		}
	}
}

func TestBlockCatalog_NoAPIEndpoint(t *testing.T) {
	// Verify that api-endpoint is no longer in the block catalog.
	catalog := planyaml.BlockCatalog()

	// Check that api-endpoint is not in the catalog.
	for _, spec := range catalog {
		if spec.Type == "api-endpoint" {
			t.Errorf("api-endpoint should not be in BlockCatalog, but found: %+v", spec)
		}
	}

	// Verify the catalog has the expected remaining types.
	expectedTypes := map[string]bool{
		"data-model": false,
		"file-tree":  false,
		"wireframe":  false,
		"diagram":    false,
		"tabs":       false,
	}
	for _, spec := range catalog {
		if _, ok := expectedTypes[spec.Type]; ok {
			expectedTypes[spec.Type] = true
		}
	}
	for typeName, found := range expectedTypes {
		if !found {
			t.Errorf("expected type %q not found in BlockCatalog", typeName)
		}
	}
}
