package planyaml

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestValidate_BlocksOptional confirms an OLD plan (no blocks field on any
// slice) still passes Validate unchanged — the additive-optional contract.
func TestValidate_BlocksOptional(t *testing.T) {
	plan := validPlan() // no Blocks set on any slice
	if errs := Validate(plan); len(errs) != 0 {
		t.Fatalf("plan without blocks should validate clean, got: %v", errs)
	}

	// A slice carrying a well-formed block should also validate clean.
	plan.Slices[0].Blocks = []SliceBlock{
		{
			Type:  "data-model",
			Title: "User",
			Fields: map[string]string{"name": "User"},
			Rows: []map[string]string{
				{"name": "id", "type": "uuid"},
				{"name": "email", "type": "string"},
			},
		},
		{
			Type:    "file-tree",
			Entries: []string{"internal/user/user.go", "internal/user/store.go"},
		},
	}
	if errs := Validate(plan); len(errs) != 0 {
		t.Fatalf("plan with valid blocks should validate clean, got: %v", errs)
	}
}

// TestValidate_BlockShapes confirms malformed blocks (missing required fields,
// unknown type, missing rows/entries, raw colors in wireframe) are rejected,
// while valid blocks of every catalog type pass.
func TestValidate_BlockShapes(t *testing.T) {
	cases := []struct {
		name      string
		block     SliceBlock
		wantError bool
		errSubstr string
	}{
		{
			name:      "unknown type rejected",
			block:     SliceBlock{Type: "diagram"},
			wantError: true,
			errSubstr: "is unknown",
		},
		{
			name:      "data-model missing name field",
			block:     SliceBlock{Type: "data-model", Rows: []map[string]string{{"name": "id", "type": "uuid"}}},
			wantError: true,
			errSubstr: "fields.name is required",
		},
		{
			name:      "data-model missing rows",
			block:     SliceBlock{Type: "data-model", Fields: map[string]string{"name": "User"}},
			wantError: true,
			errSubstr: "rows must have at least 1 entry",
		},
		{
			name: "data-model row missing type key",
			block: SliceBlock{
				Type:   "data-model",
				Fields: map[string]string{"name": "User"},
				Rows:   []map[string]string{{"name": "id"}},
			},
			wantError: true,
			errSubstr: "rows[0].type is required",
		},
		{
			name:      "valid data-model",
			block:     SliceBlock{Type: "data-model", Fields: map[string]string{"name": "User"}, Rows: []map[string]string{{"name": "id", "type": "uuid"}}},
			wantError: false,
		},
		{
			name:      "api-endpoint missing method",
			block:     SliceBlock{Type: "api-endpoint", Fields: map[string]string{"path": "/users"}},
			wantError: true,
			errSubstr: "fields.method is required",
		},
		{
			name:      "valid api-endpoint without rows",
			block:     SliceBlock{Type: "api-endpoint", Fields: map[string]string{"method": "GET", "path": "/users"}},
			wantError: false,
		},
		{
			name:      "file-tree missing entries",
			block:     SliceBlock{Type: "file-tree"},
			wantError: true,
			errSubstr: "entries must have at least 1 entry",
		},
		{
			name:      "valid file-tree",
			block:     SliceBlock{Type: "file-tree", Entries: []string{"main.go"}},
			wantError: false,
		},
		{
			name:      "wireframe missing html",
			block:     SliceBlock{Type: "wireframe"},
			wantError: true,
			errSubstr: "fields.html is required",
		},
		{
			name:      "wireframe with raw hex color rejected",
			block:     SliceBlock{Type: "wireframe", Fields: map[string]string{"html": `<div style="color:#ff0000">x</div>`}},
			wantError: true,
			errSubstr: "design tokens",
		},
		{
			name:      "wireframe with rgb color rejected",
			block:     SliceBlock{Type: "wireframe", Fields: map[string]string{"html": `<div style="color:rgb(1,2,3)">x</div>`}},
			wantError: true,
			errSubstr: "design tokens",
		},
		{
			name:      "valid wireframe with design tokens",
			block:     SliceBlock{Type: "wireframe", Fields: map[string]string{"html": `<div style="color:var(--color-fg)">x</div>`}},
			wantError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := validPlan()
			plan.Slices[0].Blocks = []SliceBlock{tc.block}
			errs := Validate(plan)
			var blockErrs []string
			for _, e := range errs {
				if strings.Contains(e, ".blocks[") {
					blockErrs = append(blockErrs, e)
				}
			}
			if tc.wantError {
				if len(blockErrs) == 0 {
					t.Fatalf("expected a block error containing %q, got none in: %v", tc.errSubstr, errs)
				}
				found := false
				for _, e := range blockErrs {
					if tc.errSubstr == "" || strings.Contains(e, tc.errSubstr) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected a block error containing %q, got: %v", tc.errSubstr, blockErrs)
				}
			} else if len(blockErrs) != 0 {
				t.Fatalf("expected no block errors, got: %v", blockErrs)
			}
		})
	}
}

// TestBlocks_RoundTrip confirms blocks survive a YAML marshal/unmarshal
// round-trip, and that omitempty drops the key entirely when absent.
func TestBlocks_RoundTrip(t *testing.T) {
	original := PlanSlice{
		Num:    1,
		Title:  "Slice with blocks",
		Effort: "S",
		Risk:   "Low",
		Blocks: []SliceBlock{
			{
				Type:   "data-model",
				Title:  "User",
				Fields: map[string]string{"name": "User"},
				Rows:   []map[string]string{{"name": "id", "type": "uuid"}},
			},
			{
				Type:    "file-tree",
				Entries: []string{"a.go", "b.go"},
			},
		},
	}

	data, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var loaded PlanSlice
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if len(loaded.Blocks) != 2 {
		t.Fatalf("Blocks len = %d, want 2", len(loaded.Blocks))
	}
	if loaded.Blocks[0].Type != "data-model" || loaded.Blocks[0].Fields["name"] != "User" {
		t.Errorf("block[0] not round-tripped: %+v", loaded.Blocks[0])
	}
	if len(loaded.Blocks[0].Rows) != 1 || loaded.Blocks[0].Rows[0]["type"] != "uuid" {
		t.Errorf("block[0].rows not round-tripped: %+v", loaded.Blocks[0].Rows)
	}
	if len(loaded.Blocks[1].Entries) != 2 || loaded.Blocks[1].Entries[1] != "b.go" {
		t.Errorf("block[1].entries not round-tripped: %+v", loaded.Blocks[1].Entries)
	}
}

// TestBlocks_OmitEmptyWhenAbsent confirms the blocks key is omitted from YAML
// output when no blocks are set (back-compat: existing plans don't gain a key).
func TestBlocks_OmitEmptyWhenAbsent(t *testing.T) {
	s := PlanSlice{Num: 1, Title: "No blocks", Effort: "S", Risk: "Low"}
	data, err := yaml.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if strings.Contains(string(data), "blocks:") {
		t.Errorf("expected blocks key omitted, got:\n%s", string(data))
	}
}

// TestBlockCatalog_CoversVocabulary confirms the catalog enumerates the four
// supported block types with stable descriptions.
func TestBlockCatalog_CoversVocabulary(t *testing.T) {
	want := map[string]bool{"data-model": false, "api-endpoint": false, "file-tree": false, "wireframe": false}
	for _, spec := range BlockCatalog() {
		if _, ok := want[spec.Type]; !ok {
			t.Errorf("unexpected catalog type %q", spec.Type)
		}
		want[spec.Type] = true
		if spec.Description == "" {
			t.Errorf("catalog type %q has empty description", spec.Type)
		}
	}
	for typ, seen := range want {
		if !seen {
			t.Errorf("catalog missing expected type %q", typ)
		}
	}
}
