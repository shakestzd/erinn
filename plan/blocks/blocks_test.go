package blocks_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/plan/blocks"
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

func TestBlocks_APIEndpoint(t *testing.T) {
	ep := &blocks.APIEndpoint{
		Method: "POST",
		Path:   "/api/recaps/{id}/render",
		Params: []blocks.Column{
			{Name: "id", Type: "string", Note: "recap id"},
		},
	}
	html := render(t, ep)
	for _, want := range []string{
		`class="block block-api-endpoint"`,
		"POST",
		"/api/recaps/{id}/render",
		"recap id",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("api-endpoint output missing %q\n%s", want, html)
		}
	}
}

func TestBlocks_APIEndpoint_NoParams(t *testing.T) {
	ep := &blocks.APIEndpoint{Method: "GET", Path: "/health"}
	html := render(t, ep)
	if !strings.Contains(html, "/health") {
		t.Errorf("expected path in output:\n%s", html)
	}
	if strings.Contains(html, "<table") {
		t.Errorf("expected no params table when Params empty:\n%s", html)
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

func TestBlocks_Render_AllImplementBlock(t *testing.T) {
	// Compile-time + run-time guarantee that all renderers satisfy Block.
	bs := []blocks.Block{
		&blocks.DataModel{Name: "X"},
		&blocks.APIEndpoint{Method: "GET", Path: "/x"},
		&blocks.FileTree{Entries: []blocks.FileNode{{Path: "a"}}},
	}
	for _, b := range bs {
		if got := render(t, b); got == "" {
			t.Errorf("%T rendered empty", b)
		}
	}
}
