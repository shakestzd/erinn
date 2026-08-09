package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/sessionledger"
)

// TestSessionShowListUseCanonicalLedger verifies the read-only session commands
// use the authoritative session ledger and never create/open the project DB as a
// schema bootstrap fallback.
func TestSessionShowListUseCanonicalLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("drives session command integration flow")
	}
	tmpDir := t.TempDir()
	wipnoteDir := filepath.Join(tmpDir, ".wipnote")
	if err := os.MkdirAll(wipnoteDir, 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	dbPath := filepath.Join(tmpDir, "must-not-exist.db")
	t.Setenv("WIPNOTE_DB_PATH", dbPath)

	origProjectDir := projectDirFlag
	projectDirFlag = tmpDir
	t.Cleanup(func() { projectDirFlag = origProjectDir })

	store := sessionledger.NewStore(wipnoteDir)
	start := time.Date(2026, 8, 9, 10, 0, 0, 0, time.UTC)
	if _, err := store.Open(sessionledger.Record{SessionID: "sess-aaaabbbb-cccc-dddd-eeee-ffff00001111", Harness: "codex", StartedAt: start}); err != nil {
		t.Fatalf("open ledger session: %v", err)
	}
	if _, err := store.Open(sessionledger.Record{SessionID: "sess-bbbbaaaa-cccc-dddd-eeee-ffff00001111", Harness: "codex", StartedAt: start.Add(-time.Hour)}); err != nil {
		t.Fatalf("open closed ledger session: %v", err)
	}
	if err := store.Close("sess-bbbbaaaa-cccc-dddd-eeee-ffff00001111", start.Add(-30*time.Minute)); err != nil {
		t.Fatalf("close ledger session: %v", err)
	}

	tests := []struct {
		name         string
		runFunc      func() error
		wantErrorMsg string // non-empty: substring expected in error; empty: expect nil
	}{
		{
			name: "runSessionList from ledger",
			runFunc: func() error {
				return runSessionList(false, 10)
			},
			wantErrorMsg: "",
		},
		{
			name: "runSessionList active-only from ledger",
			runFunc: func() error {
				return runSessionList(true, 5)
			},
			wantErrorMsg: "",
		},
		{
			name: "runSessionShow known session from ledger",
			runFunc: func() error {
				return runSessionShow("sess-aaaabbbb-cccc-dddd-eeee-ffff00001111")
			},
			wantErrorMsg: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.runFunc()

			if err != nil && strings.Contains(err.Error(), "no such table") {
				t.Fatalf("schema error (project DB fallback detected): %v", err)
			}
			if tt.wantErrorMsg == "" {
				if err != nil {
					t.Fatalf("expected nil error but got: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q but got nil", tt.wantErrorMsg)
				}
				if !strings.Contains(err.Error(), tt.wantErrorMsg) {
					t.Fatalf("expected error substring %q, got: %v", tt.wantErrorMsg, err)
				}
			}
		})
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("session read command created/opened project DB %s: %v", dbPath, err)
	}
}
