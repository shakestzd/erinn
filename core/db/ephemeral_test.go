package db

import "testing"

func TestOpenEphemeralProjectionPragmas(t *testing.T) {
	database, err := OpenEphemeralProjection()
	if err != nil {
		t.Fatalf("OpenEphemeralProjection: %v", err)
	}
	defer database.Close()
	if err := VerifyEphemeralPragmas(database); err != nil {
		t.Fatalf("VerifyEphemeralPragmas: %v", err)
	}
	var n int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='features'`).Scan(&n); err != nil {
		t.Fatalf("query schema: %v", err)
	}
	if n != 1 {
		t.Fatalf("features table count = %d, want 1", n)
	}
}
