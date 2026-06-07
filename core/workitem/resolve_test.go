package workitem

import (
	"os"
	"path/filepath"
	"testing"
)

// makeTestDir creates a minimal .wipnote directory structure with stub HTML
// files for the given IDs. Returns the wipnoteDir path.
func makeTestDir(t *testing.T, ids ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range subdirs {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		sub := subForID(id)
		if sub == "" {
			t.Fatalf("cannot determine subdir for id %q", id)
		}
		path := filepath.Join(dir, sub, id+".html")
		if err := os.WriteFile(path, []byte("<html></html>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// subForID maps a test ID to its expected subdirectory.
func subForID(id string) string {
	switch {
	case len(id) > 5 && id[:5] == "feat-":
		return "features"
	case len(id) > 4 && id[:4] == "bug-":
		return "bugs"
	case len(id) > 4 && id[:4] == "spk-":
		return "spikes"
	case len(id) > 4 && id[:4] == "trk-":
		return "tracks"
	case len(id) > 5 && id[:5] == "plan-":
		return "plans"
	case len(id) > 5 && id[:5] == "spec-":
		return "specs"
	default:
		return ""
	}
}

func TestResolvePartialID_ExactMatch(t *testing.T) {
	dir := makeTestDir(t, "feat-43aea33f", "feat-43aeb001")
	got, err := ResolvePartialID(dir, "feat-43aea33f")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "feat-43aea33f" {
		t.Errorf("got %q, want %q", got, "feat-43aea33f")
	}
}

func TestResolvePartialID_UnambiguousPrefix(t *testing.T) {
	dir := makeTestDir(t, "feat-43aea33f")
	got, err := ResolvePartialID(dir, "feat-43a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "feat-43aea33f" {
		t.Errorf("got %q, want %q", got, "feat-43aea33f")
	}
}

func TestResolvePartialID_AmbiguousPrefix(t *testing.T) {
	dir := makeTestDir(t, "feat-43aea33f", "feat-43aeb001")
	_, err := ResolvePartialID(dir, "feat-43a")
	if err == nil {
		t.Fatal("expected ambiguous error, got nil")
	}
	// Error should mention both candidates.
	errStr := err.Error()
	for _, candidate := range []string{"feat-43aea33f", "feat-43aeb001"} {
		found := false
		for i := 0; i <= len(errStr)-len(candidate); i++ {
			if errStr[i:i+len(candidate)] == candidate {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ambiguous error missing candidate %q; error = %q", candidate, errStr)
		}
	}
}

func TestResolvePartialID_NotFound(t *testing.T) {
	dir := makeTestDir(t, "feat-43aea33f")
	_, err := ResolvePartialID(dir, "feat-ffffffff")
	if err == nil {
		t.Fatal("expected not-found error, got nil")
	}
}

func TestResolvePartialID_CrossTypePrefix(t *testing.T) {
	// A short prefix that matches items in different collection types.
	dir := makeTestDir(t, "feat-aabbcc11", "bug-aabbcc22")
	// "feat-aabbcc11" and "bug-aabbcc22" share only "aabbcc" in the hex part.
	// Use a prefix that uniquely identifies the feature.
	got, err := ResolvePartialID(dir, "feat-aabbcc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "feat-aabbcc11" {
		t.Errorf("got %q, want %q", got, "feat-aabbcc11")
	}
}

func TestResolvePartialID_EmptyDir(t *testing.T) {
	dir := makeTestDir(t) // no files
	_, err := ResolvePartialID(dir, "feat-abc")
	if err == nil {
		t.Fatal("expected not-found error for empty dir, got nil")
	}
}
