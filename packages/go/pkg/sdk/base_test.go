package sdk_test

import (
	"errors"
	"testing"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/pkg/sdk"
)

// stubIndex is a test double for ReadIndex that records calls.
type stubIndex struct {
	insertCalls      int
	updateCalls      int
	closeCalls       int
	insertErr        error
	updateErr        error
	lastInsertedID   string
	lastUpdatedID    string
	lastUpdatedState string
}

func (s *stubIndex) InsertFeature(f *dbpkg.Feature) error {
	s.insertCalls++
	s.lastInsertedID = f.ID
	return s.insertErr
}

func (s *stubIndex) UpdateFeatureStatus(id, status string) error {
	s.updateCalls++
	s.lastUpdatedID = id
	s.lastUpdatedState = status
	return s.updateErr
}

func (s *stubIndex) Close() error {
	s.closeCalls++
	return nil
}

// ---------------------------------------------------------------------------
// Base construction and accessors
// ---------------------------------------------------------------------------

func TestNewBase(t *testing.T) {
	idx := &stubIndex{}
	b := sdk.NewBase("/tmp/hg", "test-agent", idx)

	assertEqual(t, "ProjectDir", b.ProjectDir, "/tmp/hg")
	assertEqual(t, "Agent", b.Agent, "test-agent")

	if b.Index() != idx {
		t.Error("Index() should return the provided ReadIndex")
	}
}

func TestBaseNilIndex(t *testing.T) {
	b := sdk.NewBase("/tmp/hg", "agent", nil)

	if b.Index() != nil {
		t.Error("Index() should be nil when no ReadIndex provided")
	}

	// Close should be a no-op
	if err := b.Close(); err != nil {
		t.Errorf("Close with nil index: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Directory helpers
// ---------------------------------------------------------------------------

func TestBaseHtmlDir(t *testing.T) {
	b := sdk.NewBase("/project/.htmlgraph", "agent", nil)

	assertEqual(t, "FeaturesDir", b.FeaturesDir(), "/project/.htmlgraph/features")
	assertEqual(t, "BugsDir", b.BugsDir(), "/project/.htmlgraph/bugs")
	assertEqual(t, "SpikesDir", b.SpikesDir(), "/project/.htmlgraph/spikes")
	assertEqual(t, "TracksDir", b.TracksDir(), "/project/.htmlgraph/tracks")
	assertEqual(t, "HtmlDir(sessions)", b.HtmlDir("sessions"), "/project/.htmlgraph/sessions")
}

// ---------------------------------------------------------------------------
// DualWriteNode
// ---------------------------------------------------------------------------

func TestDualWriteNodeSuccess(t *testing.T) {
	idx := &stubIndex{}
	b := sdk.NewBase("/tmp/hg", "agent", idx)

	feat := &dbpkg.Feature{ID: "feat-abc", Title: "Test"}
	path, err := b.DualWriteNode(
		func() (string, error) { return "/tmp/hg/features/feat-abc.html", nil },
		feat,
	)

	if err != nil {
		t.Fatalf("DualWriteNode: %v", err)
	}
	assertEqual(t, "path", path, "/tmp/hg/features/feat-abc.html")

	if idx.insertCalls != 1 {
		t.Errorf("InsertFeature calls: got %d, want 1", idx.insertCalls)
	}
	assertEqual(t, "inserted ID", idx.lastInsertedID, "feat-abc")
}

func TestDualWriteNodeHTMLError(t *testing.T) {
	idx := &stubIndex{}
	b := sdk.NewBase("/tmp/hg", "agent", idx)

	_, err := b.DualWriteNode(
		func() (string, error) { return "", errors.New("disk full") },
		&dbpkg.Feature{ID: "feat-abc"},
	)

	if err == nil {
		t.Fatal("expected error from HTML write failure")
	}
	// SQLite should NOT be called when HTML fails
	if idx.insertCalls != 0 {
		t.Errorf("InsertFeature should not be called on HTML error")
	}
}

func TestDualWriteNodeSQLiteErrorIgnored(t *testing.T) {
	idx := &stubIndex{insertErr: errors.New("UNIQUE constraint")}
	b := sdk.NewBase("/tmp/hg", "agent", idx)

	path, err := b.DualWriteNode(
		func() (string, error) { return "/tmp/hg/features/feat-abc.html", nil },
		&dbpkg.Feature{ID: "feat-abc"},
	)

	// HTML succeeded, so no error returned despite SQLite failure
	if err != nil {
		t.Fatalf("DualWriteNode should succeed despite SQLite error: %v", err)
	}
	assertEqual(t, "path", path, "/tmp/hg/features/feat-abc.html")
}

func TestDualWriteNodeNilIndex(t *testing.T) {
	b := sdk.NewBase("/tmp/hg", "agent", nil)

	path, err := b.DualWriteNode(
		func() (string, error) { return "/ok.html", nil },
		&dbpkg.Feature{ID: "feat-abc"},
	)

	if err != nil {
		t.Fatalf("DualWriteNode with nil index: %v", err)
	}
	assertEqual(t, "path", path, "/ok.html")
}

func TestDualWriteNodeNilDbFeat(t *testing.T) {
	idx := &stubIndex{}
	b := sdk.NewBase("/tmp/hg", "agent", idx)

	_, err := b.DualWriteNode(
		func() (string, error) { return "/ok.html", nil },
		nil,
	)

	if err != nil {
		t.Fatalf("DualWriteNode with nil dbFeat: %v", err)
	}
	if idx.insertCalls != 0 {
		t.Errorf("InsertFeature should not be called with nil dbFeat")
	}
}

// ---------------------------------------------------------------------------
// DualWriteStatus
// ---------------------------------------------------------------------------

func TestDualWriteStatus(t *testing.T) {
	idx := &stubIndex{}
	b := sdk.NewBase("/tmp/hg", "agent", idx)

	b.DualWriteStatus("feat-abc", "in-progress")

	if idx.updateCalls != 1 {
		t.Errorf("UpdateFeatureStatus calls: got %d, want 1", idx.updateCalls)
	}
	assertEqual(t, "updated ID", idx.lastUpdatedID, "feat-abc")
	assertEqual(t, "updated status", idx.lastUpdatedState, "in-progress")
}

func TestDualWriteStatusNilIndex(t *testing.T) {
	b := sdk.NewBase("/tmp/hg", "agent", nil)

	// Should be a no-op, not panic
	b.DualWriteStatus("feat-abc", "done")
}

func TestDualWriteStatusErrorIgnored(t *testing.T) {
	idx := &stubIndex{updateErr: errors.New("table not found")}
	b := sdk.NewBase("/tmp/hg", "agent", idx)

	// Should not panic despite error
	b.DualWriteStatus("feat-abc", "done")

	if idx.updateCalls != 1 {
		t.Errorf("UpdateFeatureStatus should still be called: got %d", idx.updateCalls)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestBaseClose(t *testing.T) {
	idx := &stubIndex{}
	b := sdk.NewBase("/tmp/hg", "agent", idx)

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if idx.closeCalls != 1 {
		t.Errorf("Close calls: got %d, want 1", idx.closeCalls)
	}
}
