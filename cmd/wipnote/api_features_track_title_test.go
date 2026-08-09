package main

import (
	"testing"

	dbpkg "github.com/shakestzd/wipnote/core/db"
)

// The board's feature query resolves each row's track title by joining on
// features.track_id. That join used to target the features table itself,
// matching a trk- id against the work-item primary key. Work-item and track
// ids come from different namespaces and never collide, so it matched nothing
// for every row in production — and an empty title renders as nothing rather
// than erroring, which is why it survived (bug-0fe9485f).
//
// A join that cannot ever match is indistinguishable at runtime from one that
// has not matched yet, so the test seeds the one arrangement that tells them
// apart: a feature whose track exists only in the tracks table.
func TestFeaturesFromDB_ResolvesTrackTitleFromTracksTable(t *testing.T) {
	database := openGraphTestDB(t)

	if err := dbpkg.UpsertTrack(database, &dbpkg.Track{
		ID:     "trk-11111111",
		Title:  "Canonical Storage",
		Status: "in-progress",
	}); err != nil {
		t.Fatalf("seed track: %v", err)
	}
	if _, err := database.Exec(
		`INSERT INTO features (id, type, title, status, track_id)
		 VALUES ('feat-22222222', 'feature', 'Some feature', 'todo', 'trk-11111111')`,
	); err != nil {
		t.Fatalf("seed feature: %v", err)
	}

	features := featuresFromDB(database)

	for _, f := range features {
		if f["id"] != "feat-22222222" {
			continue
		}
		got, _ := f["track_title"].(string)
		if got != "Canonical Storage" {
			t.Fatalf("track_title = %q, want %q — the board is joining track_id "+
				"against a table that cannot contain track ids", got, "Canonical Storage")
		}
		return
	}
	t.Fatal("feat-22222222 missing from featuresFromDB result")
}
