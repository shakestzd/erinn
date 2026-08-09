package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shakestzd/wipnote/core/agent"
	"github.com/shakestzd/wipnote/core/sessionledger"
)

func TestEnsureSessionWritesLedgerAndActiveSession(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	t.Setenv("WIPNOTE_SESSION_ID", "019fe7af-9ba5-7e60-b288-656d6fe72e6f")

	got, err := agent.EnsureSession(nil, projectDir)
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if got != "019fe7af-9ba5-7e60-b288-656d6fe72e6f" {
		t.Fatalf("session id = %q", got)
	}

	store := sessionledger.NewStore(filepath.Join(projectDir, ".wipnote"))
	if _, ok, err := store.Get(got); err != nil || !ok {
		t.Fatalf("ledger Get = ok %v err %v, want recorded session", ok, err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".wipnote", ".active-session")); err != nil {
		t.Fatalf(".active-session not written: %v", err)
	}
}
