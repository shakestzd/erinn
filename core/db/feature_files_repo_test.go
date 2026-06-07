package db

import (
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/models"
)

func TestResolveFileOwner_ReturnsMostFrequent(t *testing.T) {
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	// Insert track row (FK target for feat-a).
	if err := UpsertTrack(database, &Track{
		ID: "trk-test", Type: "track", Title: "Test Track",
		Status: "todo", Priority: "medium",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert track: %v", err)
	}
	// Insert feature rows.
	if err := UpsertFeature(database, &Feature{
		ID: "feat-a", Type: "feature", Title: "Feature A",
		Status: "todo", Priority: "medium", TrackID: "trk-test",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert feat-a: %v", err)
	}
	if err := UpsertFeature(database, &Feature{
		ID: "feat-b", Type: "feature", Title: "Feature B",
		Status: "todo", Priority: "medium",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert feat-b: %v", err)
	}

	// Verify feat-a exists in the features table.
	var checkTitle, checkTrack string
	database.QueryRow("SELECT title, COALESCE(track_id, '') FROM features WHERE id = 'feat-a'").Scan(&checkTitle, &checkTrack)
	t.Logf("features table: title=%q track=%q", checkTitle, checkTrack)

	// feat-a touches 3 files, feat-b touches 1 file. The target file "cmd/file.go"
	// is touched by feat-a only.
	UpsertFeatureFile(database, &models.FeatureFile{
		ID: "a-0", FeatureID: "feat-a",
		FilePath: "cmd/file.go", Operation: "commit",
	})
	UpsertFeatureFile(database, &models.FeatureFile{
		ID: "a-1", FeatureID: "feat-a",
		FilePath: "cmd/other.go", Operation: "commit",
	})
	UpsertFeatureFile(database, &models.FeatureFile{
		ID: "b-0", FeatureID: "feat-b",
		FilePath: "cmd/another.go", Operation: "commit",
	})

	owner := ResolveFileOwner(database, "cmd/file.go")
	if owner == nil {
		t.Fatal("expected owner, got nil")
	}
	if owner.FeatureID != "feat-a" {
		t.Errorf("expected feat-a, got %s", owner.FeatureID)
	}
	if owner.TrackID != "trk-test" {
		t.Errorf("expected trk-test, got %s", owner.TrackID)
	}
	if owner.Title != "Feature A" {
		t.Errorf("expected 'Feature A', got %s", owner.Title)
	}
}

func TestResolveFileOwner_ReturnsNilForUnknownFile(t *testing.T) {
	database, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	owner := ResolveFileOwner(database, "nonexistent.go")
	if owner != nil {
		t.Errorf("expected nil for unknown file, got %+v", owner)
	}
}
