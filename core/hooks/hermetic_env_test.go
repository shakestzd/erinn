package hooks

import (
	"os"
	"testing"
)

// TestMain makes the whole hooks test suite hermetic against session-runtime
// environment variables leaked from the live wipnote/Claude session that is
// running the tests (this project dogfoods itself, so `go test` inherits the
// developer session's env).
//
// Why this is required — the FOREIGN KEY trap:
// SessionStart copies WIPNOTE_PARENT_SESSION → sessions.parent_session_id,
// WIPNOTE_PARENT_EVENT → sessions.parent_event_id, and WIPNOTE_CONTINUED_FROM →
// sessions.continued_from. Each of those columns carries a FOREIGN KEY back to
// sessions(session_id) / agent_events(event_id). When one of these vars is set
// in the ambient environment it names a row from the REAL project DB that is
// absent from a test's fresh per-case DB, so the session upsert violates the FK.
// Crucially, `INSERT OR IGNORE` does NOT suppress FOREIGN KEY violations (SQLite
// only ignores UNIQUE / NOT NULL / CHECK / PRIMARY KEY conflicts), so the whole
// insert is rejected — every subsequent read-back then fails with
// "sql: no rows in result set". On a clean CI/Linux runner these vars are unset,
// so the suite passes there and only fails when run inside a live session; unset
// them once here so behaviour is identical in both environments. Individual
// tests that legitimately exercise these fields still set them explicitly via
// t.Setenv (which saves/restores around each test), so this baseline never
// fights a test that opts in.
//
// The session-identity vars (CLAUDE_SESSION_ID / CLAUDE_CODE_SESSION_ID /
// WIPNOTE_SESSION_ID) and the subagent/family markers are cleared for the same
// hermeticity reason the per-test helpers already clear them: a leaked value can
// change the resolved session ID or flag a hook as a subagent, landing writes
// under the wrong ID. Centralising them here is a safety net for tests that
// forget one of the clears.
func TestMain(m *testing.M) {
	for _, key := range []string{
		// FK-column vars — the direct cause of the FK-reject "no rows" failures.
		"WIPNOTE_PARENT_SESSION",
		"WIPNOTE_PARENT_EVENT",
		"WIPNOTE_CONTINUED_FROM",
		// Session identity — a leaked value would resolve writes under the wrong ID.
		"CLAUDE_SESSION_ID",
		"CLAUDE_CODE_SESSION_ID",
		"WIPNOTE_SESSION_ID",
		// Subagent / family / lineage markers.
		"WIPNOTE_NESTING_DEPTH",
		"WIPNOTE_PARENT_AGENT",
		"WIPNOTE_SESSION_FAMILY_ID",
		// Env-file redirect — never write into the developer session's env file.
		"CLAUDE_ENV_FILE",
	} {
		_ = os.Unsetenv(key)
	}
	os.Exit(m.Run())
}
