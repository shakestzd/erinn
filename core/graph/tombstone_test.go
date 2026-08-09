package graph

import (
	"reflect"
	"testing"
)

// The tombstone half of the target-validity gate hangs entirely on this shape
// check: match, and a dangling reference is preserved forever as a tombstone;
// miss, and a pruned session's provenance is erased. Both halves are pinned.
func TestIsSessionShapedID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"aaaabbbb-cccc-dddd-eeee-ffff00001111", true},
		{"D846B50D-9CE4-45C1-8AD2-0F84DA537EFD", true}, // uppercase hex
		{"sess-aaaabbbb-cccc-dddd-eeee-ffff00001111", true},

		// The 28-hex undashed form. Real ids taken from this repo's
		// .wipnote/sessions/ — most live session records use this shape, so
		// omitting it would leave the current format losing provenance.
		{"019f424e188c60f444c8eaca668b", true},
		{"019de4e6c69edd254a6613fd17d2", true},
		{"sess-019f424e188c60f444c8eaca668b", true},

		// Work-item ids must never tombstone — an edge to a work item that is
		// not in the index is a genuine dangling reference.
		{"feat-d1439606", false},
		{"bug-10e186d8", false},
		{"spk-e6e82b5a", false},
		{"trk-2c55adea", false},
		{"plan-3b0d5133", false},
		{"spec-1234abcd", false},
		{"arch:some-card-slug", false},

		// Near misses. A loose pattern would readmit the dangling-reference
		// class the gate exists to refuse.
		{"", false},
		{"aaaabbbb-cccc-dddd-eeee-ffff0000111", false},   // 11 in last group
		{"aaaabbbb-cccc-dddd-eeee-ffff000011112", false}, // 13 in last group
		{"aaaabbbbccccddddeeeeffff00001111", false},      // no dashes
		{"gggggggg-cccc-dddd-eeee-ffff00001111", false},  // non-hex
		{" aaaabbbb-cccc-dddd-eeee-ffff00001111", false}, // leading space
		{"aaaabbbb-cccc-dddd-eeee-ffff00001111 ", false}, // trailing space
		{"x-aaaabbbb-cccc-dddd-eeee-ffff00001111", false},
		{"3f2504e0-4f89-11d3-9a0c-0305e82c3301.html", false},

		// Undashed-hex near misses. `c4efb206` is a real declared target in
		// this repo and is deliberately refused: 8 bare hex chars is also an
		// abbreviated commit SHA, so admitting it would readmit the
		// dangling-reference class the gate exists to refuse.
		{"c4efb206", false},
		{"019f424e188c60f444c8eaca668", false},      // 27
		{"019f424e188c60f444c8eaca668bb", false},    // 29
		{"019f424e188c60f444c8eaca668g", false},     // 28 chars, non-hex
		{"0123456789abcdef0123456789abcdef", false}, // 32 — not a shape we emit
	} {
		if got := IsSessionShapedID(tc.id); got != tc.want {
			t.Errorf("IsSessionShapedID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestClassifyEdgeTarget(t *testing.T) {
	validIDs := map[string]bool{
		"feat-live0001":                        true,
		"aaaabbbb-cccc-dddd-eeee-ffff00001111": true,
	}
	for _, tc := range []struct {
		name   string
		target string
		want   EdgeTargetDisposition
	}{
		{"work item in the index", "feat-live0001", EdgeTargetLive},
		{"session in the index", "aaaabbbb-cccc-dddd-eeee-ffff00001111", EdgeTargetLive},
		{"pruned session", "99999999-8888-7777-6666-555544443333", EdgeTargetTombstoned},
		{"nonexistent work item", "feat-deadbeef", EdgeTargetDangling},
		{"nonexistent bug", "bug-deadbeef", EdgeTargetDangling},
		{"garbage", "not-an-id-at-all", EdgeTargetDangling},
	} {
		if got := ClassifyEdgeTarget(tc.target, validIDs); got != tc.want {
			t.Errorf("%s: ClassifyEdgeTarget(%q) = %d, want %d", tc.name, tc.target, got, tc.want)
		}
	}
}

// MarkEdgeTombstoned must not mutate the caller's map: it belongs to the parsed
// HTML node, which the reindex passes reuse.
func TestMarkEdgeTombstoned_CopiesAndPreservesProps(t *testing.T) {
	declared := map[string]string{"tag": "needs-triage-dup", "similarity_score": "0.842"}
	marked := MarkEdgeTombstoned(declared)

	want := map[string]string{
		"tag":              "needs-triage-dup",
		"similarity_score": "0.842",
		EdgeMetaTombstoned: EdgeTombstoneSession,
	}
	if !reflect.DeepEqual(marked, want) {
		t.Errorf("marked props = %#v, want %#v", marked, want)
	}
	if _, mutated := declared[EdgeMetaTombstoned]; mutated {
		t.Errorf("MarkEdgeTombstoned mutated the caller's map: %#v", declared)
	}
}

func TestMarkEdgeTombstoned_NilProps(t *testing.T) {
	marked := MarkEdgeTombstoned(nil)
	if marked[EdgeMetaTombstoned] != EdgeTombstoneSession {
		t.Errorf("marked nil props = %#v, want the tombstone marker", marked)
	}
}
