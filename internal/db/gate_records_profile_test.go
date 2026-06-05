package db

import (
	"testing"
)

// TestGateRecord_PersistsProfileSignatureAndGuards verifies the new
// profile_signature and guards_run columns survive an insert+scan round trip
// through the migrated schema, and that an autodetection record (empty profile
// signature) defaults cleanly.
func TestGateRecord_PersistsProfileSignatureAndGuards(t *testing.T) {
	database, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	rec := &GateRecord{
		SessionID:        "sess-1",
		WorkItemID:       "feat-x",
		Harness:          "claude-code",
		ProjectType:      "go",
		GateCommand:      "go build ./...",
		Status:           "pass",
		Source:           "check",
		OutputSummary:    "all commands passed",
		ProfileSignature: "sha256:deadbeef",
		GuardsRunJSON:    `["build","vet"]`,
	}
	if err := InsertGateRecord(database, rec); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := LatestGateRecordForSession(database, "sess-1")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if got == nil {
		t.Fatal("expected a record")
	}
	if got.ProfileSignature != "sha256:deadbeef" {
		t.Errorf("profile_signature: got %q want sha256:deadbeef", got.ProfileSignature)
	}
	if got.GuardsRunJSON != `["build","vet"]` {
		t.Errorf("guards_run: got %q", got.GuardsRunJSON)
	}
	if !got.SignatureValid() {
		t.Errorf("record-integrity signature should remain valid (new columns must not be in the MAC)")
	}

	// Autodetection record: empty profile signature defaults cleanly.
	auto := &GateRecord{
		SessionID:   "sess-2",
		ProjectType: "go",
		GateCommand: "go build ./...",
		Status:      "pass",
		Source:      "check",
	}
	if err := InsertGateRecord(database, auto); err != nil {
		t.Fatalf("insert auto: %v", err)
	}
	gotAuto, err := LatestGateRecordForSession(database, "sess-2")
	if err != nil {
		t.Fatalf("latest auto: %v", err)
	}
	if gotAuto.ProfileSignature != "" {
		t.Errorf("autodetection profile_signature should be empty, got %q", gotAuto.ProfileSignature)
	}
	if gotAuto.GuardsRunJSON != "[]" {
		t.Errorf("autodetection guards_run should default to [], got %q", gotAuto.GuardsRunJSON)
	}
}
