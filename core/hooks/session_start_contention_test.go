package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shakestzd/wipnote/core/db"
)

// TestSessionStartFailsFastUnderContention verifies that the session-start
// hook's writable handle, opened via OpenHookDBWithBusyTimeout with a short
// busy_timeout, does NOT stall for the full default 5s busy_timeout when another
// connection holds the write lock. SessionStart's derived-index writes are all
// best-effort (logged + swallowed via the canonical-first fallback), so a busy
// error just means a skipped derived write — the hook must return fast and
// without error so the launcher's post-selection path is not gated on lock
// contention. Regression test for bug-504095f2 (Driver B).
func TestSessionStartFailsFastUnderContention(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}
	dbPath := filepath.Join(projectDir, ".wipnote", "wipnote.db")

	// Bootstrap the schema so the bounded open takes the warm (zero-DDL) path.
	boot, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("bootstrap open: %v", err)
	}
	boot.Close()

	// Open the hook handle via the SHORT busy_timeout path under test.
	hookDB, reason := OpenHookDBWithBusyTimeout("session-start", "sess-contention", dbPath, 300*time.Millisecond)
	if hookDB == nil {
		t.Fatalf("OpenHookDBWithBusyTimeout returned nil handle (reason=%s)", reason)
	}
	defer hookDB.Close()

	// A second connection holds the RESERVED write lock for the whole test.
	holderDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	defer holderDB.Close()
	holder, err := holderDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("acquire holder conn: %v", err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("acquire write lock: %v", err)
	}
	defer holder.ExecContext(context.Background(), "ROLLBACK") //nolint:errcheck

	// Avoid nested-session env leakage that would change the resolved session ID
	// or mark this as a subagent (see MEMORY: nested-session-hooks-test).
	t.Setenv("CLAUDE_SESSION_ID", "")
	t.Setenv("WIPNOTE_PARENT_SESSION", "")
	t.Setenv("WIPNOTE_NESTING_DEPTH", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("WIPNOTE_SESSION_FAMILY_ID", "")

	event := &CloudEvent{SessionID: "sess-contention", CWD: projectDir}

	start := time.Now()
	_, hookErr := SessionStart(event, hookDB, projectDir)
	elapsed := time.Since(start)

	// SessionStart performs SEVERAL writes; each must fail fast (~300ms) rather
	// than stall ~5s. Allow generous slack for slow CI, but anything near 5s
	// means the bound was not applied. (A few writes × 300ms is still well under
	// 2s.)
	if elapsed > 2*time.Second {
		t.Fatalf("SessionStart blocked %v under contention; expected fast-fail with the bounded busy_timeout", elapsed)
	}
	// The hook MUST NOT surface an error even when its derived writes are
	// blocked — canonical-first fallback swallows busy errors so the launcher
	// never sees a hook error.
	if hookErr != nil {
		t.Fatalf("SessionStart returned an error under contention (must swallow busy via canonical-first): %v", hookErr)
	}
}
