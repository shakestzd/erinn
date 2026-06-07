package workitem

import (
	"strings"
	"testing"
)

func TestErrNotFound(t *testing.T) {
	tests := []struct {
		kind, id string
		wantSub  []string // substrings that should be in the error
	}{
		{
			kind: "feature",
			id:   "feat-abc123",
			wantSub: []string{
				"work item",
				"feat-abc123",
				"wipnote feature list",
			},
		},
		{
			kind: "bug",
			id:   "bug-xyz789",
			wantSub: []string{
				"work item",
				"bug-xyz789",
				"wipnote bug list",
			},
		},
		{
			kind: "track",
			id:   "trk-def456",
			wantSub: []string{
				"work item",
				"trk-def456",
				"wipnote track list",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			err := ErrNotFound(tt.kind, tt.id)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			errStr := err.Error()
			for _, sub := range tt.wantSub {
				if !strings.Contains(errStr, sub) {
					t.Errorf("error missing substring %q\ngot: %s", sub, errStr)
				}
			}
			// Verify it's a two-line error (has a newline)
			if !strings.Contains(errStr, "\n") {
				t.Errorf("expected two-line format, got: %s", errStr)
			}
		})
	}
}

func TestErrNotFoundOnDisk(t *testing.T) {
	err := ErrNotFoundOnDisk("feature", "feat-abc123")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errStr := err.Error()
	wantSubs := []string{
		"work item",
		"feat-abc123",
		"not found on disk",
		"reindex",
	}
	for _, sub := range wantSubs {
		if !strings.Contains(errStr, sub) {
			t.Errorf("error missing substring %q\ngot: %s", sub, errStr)
		}
	}
	// Verify it's a two-line error (has a newline)
	if !strings.Contains(errStr, "\n") {
		t.Errorf("expected two-line format, got: %s", errStr)
	}
}

func TestErrNoActive(t *testing.T) {
	err := ErrNoActive("features", "wipnote feature create 'title'")
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errStr := err.Error()
	wantSubs := []string{
		"no active",
		"features",
		"wipnote feature create",
	}
	for _, sub := range wantSubs {
		if !strings.Contains(errStr, sub) {
			t.Errorf("error missing substring %q\ngot: %s", sub, errStr)
		}
	}
	// Verify it's a two-line error (has a newline)
	if !strings.Contains(errStr, "\n") {
		t.Errorf("expected two-line format, got: %s", errStr)
	}
}

func TestErrUnknownValue(t *testing.T) {
	valid := []string{"feature", "bug", "spike", "track"}
	err := ErrUnknownValue("type", "unknown", valid)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	errStr := err.Error()
	wantSubs := []string{
		"unknown type",
		"unknown",
	}
	for _, sub := range wantSubs {
		if !strings.Contains(errStr, sub) {
			t.Errorf("error missing substring %q\ngot: %s", sub, errStr)
		}
	}
	// Should list all valid options
	for _, opt := range valid {
		if !strings.Contains(errStr, opt) {
			t.Errorf("error missing valid option %q\ngot: %s", opt, errStr)
		}
	}
}
