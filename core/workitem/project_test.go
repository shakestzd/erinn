package workitem_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
)

// newTestProject creates a Project rooted in a temp dir with the required subdirectories.
func newTestProject(t *testing.T) *workitem.Project {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"features", "bugs", "spikes", "tracks", "sessions", "plans", "specs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}
	p, err := workitem.Open(dir, "test-agent")
	if err != nil {
		t.Fatalf("workitem.Open: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// assertEqual is a shared test helper used across all workitem test files.
func assertEqual(t *testing.T, field, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %q, want %q", field, got, want)
	}
}

// ---------------------------------------------------------------------------
// Collection.Filter
// ---------------------------------------------------------------------------

func TestFeatureFilter(t *testing.T) {
	p := newTestProject(t)

	_, _ = p.Features.Create("AAA Feature")
	_, _ = p.Features.Create("BBB Feature")
	_, _ = p.Features.Create("AAA Other")

	filtered, err := p.Features.Filter(func(n *models.Node) bool {
		return strings.Contains(n.Title, "AAA")
	})
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(filtered) != 2 {
		t.Errorf("Filter AAA: got %d, want 2", len(filtered))
	}
}

// ---------------------------------------------------------------------------
// ID Generation
// ---------------------------------------------------------------------------

func TestIDGeneration(t *testing.T) {
	p := newTestProject(t)

	f1, _ := p.Features.Create("Feature One")
	f2, _ := p.Features.Create("Feature Two")

	if f1.ID == f2.ID {
		t.Error("two features should have different IDs")
	}
	if !strings.HasPrefix(f1.ID, "feat-") {
		t.Errorf("f1 ID prefix: got %q", f1.ID)
	}
	if !strings.HasPrefix(f2.ID, "feat-") {
		t.Errorf("f2 ID prefix: got %q", f2.ID)
	}
	// IDs should be prefix + 8 hex chars
	parts := strings.SplitN(f1.ID, "-", 2)
	if len(parts) != 2 || len(parts[1]) != 8 {
		t.Errorf("ID format: got %q, want feat-XXXXXXXX", f1.ID)
	}
}

// ---------------------------------------------------------------------------
// Project init validation
// ---------------------------------------------------------------------------

func TestOpenRequiresAgent(t *testing.T) {
	dir := t.TempDir()
	_, err := workitem.Open(dir, "")
	if err == nil {
		t.Error("expected error for empty agent")
	}
}

func TestOpenRequiresProjectDir(t *testing.T) {
	_, err := workitem.Open("", "agent")
	if err == nil {
		t.Error("expected error for empty projectDir")
	}
}
