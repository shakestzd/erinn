package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestPlanBlocks_TextLists confirms `wipnote plan blocks` prints every supported
// block type and its required fields in human-readable form.
func TestPlanBlocks_TextLists(t *testing.T) {
	var buf bytes.Buffer
	if err := runPlanBlocks(&buf, false); err != nil {
		t.Fatalf("runPlanBlocks failed: %v", err)
	}
	out := buf.String()

	for _, typ := range []string{"data-model", "api-endpoint", "file-tree", "wireframe"} {
		if !strings.Contains(out, typ) {
			t.Errorf("expected block type %q in output:\n%s", typ, out)
		}
	}
	// Required-field surfacing: data-model requires name; api-endpoint method/path.
	if !strings.Contains(out, "name") {
		t.Errorf("expected required field 'name' in output:\n%s", out)
	}
	if !strings.Contains(out, "method") || !strings.Contains(out, "path") {
		t.Errorf("expected api-endpoint required fields method/path in output:\n%s", out)
	}
	if !strings.Contains(out, "entries") {
		t.Errorf("expected file-tree 'entries' requirement in output:\n%s", out)
	}
}

// TestPlanBlocks_JSON confirms the --json form emits a parseable catalog with
// types and required fields.
func TestPlanBlocks_JSON(t *testing.T) {
	var buf bytes.Buffer
	if err := runPlanBlocks(&buf, true); err != nil {
		t.Fatalf("runPlanBlocks(json) failed: %v", err)
	}

	var entries []blockCatalogEntry
	if err := json.Unmarshal(buf.Bytes(), &entries); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(entries) != 6 {
		t.Fatalf("expected 6 catalog entries, got %d", len(entries))
	}

	byType := map[string]blockCatalogEntry{}
	for _, e := range entries {
		byType[e.Type] = e
	}
	dm, ok := byType["data-model"]
	if !ok {
		t.Fatal("data-model entry missing")
	}
	if !dm.RequiresRows {
		t.Error("data-model should require rows")
	}
	if len(dm.RequiredFields) == 0 || dm.RequiredFields[0] != "name" {
		t.Errorf("data-model required fields = %v, want [name]", dm.RequiredFields)
	}
	ft, ok := byType["file-tree"]
	if !ok || !ft.RequiresEntries {
		t.Errorf("file-tree should require entries: %+v", ft)
	}
}
