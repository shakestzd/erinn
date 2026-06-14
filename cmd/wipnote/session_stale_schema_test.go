package main

import (
	"strings"
	"testing"
)

// TestSessionShowListStaleSchemaRegressions verifies that runSessionList and
// runSessionShow use OpenReadOnlyMigrated (bootstrap+migrate then read-only)
// rather than a bare OpenReadOnly or writable openDB, so they succeed against a
// fresh or stale-schema DB instead of surfacing "no such table" errors.
//
// Bug-af107c36: before this fix both commands called openDB (writable open, no
// RetryOnBusy), causing SQLITE_BUSY under contention.  The entire show/list
// path is read-only (pure SELECT), so the correct fix is openReadOnlyDB, which
// calls dbpkg.OpenReadOnlyMigrated.
//
// Each subtest gets its own fresh, schema-less DB (via setupStaleSchemaDB) to
// prevent cross-subtest schema pollution from OpenReadOnlyMigrated
// bootstrapping: if any callsite reverts to bare OpenReadOnly the schema won't
// be bootstrapped and the "no such table" guard turns RED.
//
// The tests drive the ACTUAL command paths (runSessionList / runSessionShow),
// not OpenReadOnlyMigrated directly — so a revert at the openReadOnlyDB call
// site makes the subtest FAIL.
func TestSessionShowListStaleSchemaRegressions(t *testing.T) {
	if testing.Short() {
		t.Skip("drives stale-schema session integration flow")
	}

	tests := []struct {
		name         string
		runFunc      func() error
		wantErrorMsg string // non-empty: substring expected in error; empty: expect nil
	}{
		{
			name: "runSessionList on fresh schema-less DB",
			runFunc: func() error {
				// runSessionList opens via openReadOnlyDB which calls
				// OpenReadOnlyMigrated.  Against a fresh, schema-bootstrapped-but-empty
				// DB the query returns zero rows and nil error.
				return runSessionList(false, 10)
			},
			wantErrorMsg: "", // schema bootstrapped; empty result set → nil
		},
		{
			name: "runSessionList active-only on fresh schema-less DB",
			runFunc: func() error {
				return runSessionList(true, 5)
			},
			wantErrorMsg: "", // same path, active-only filter; empty result set → nil
		},
		{
			name: "runSessionShow unknown session on fresh schema-less DB",
			runFunc: func() error {
				// runSessionShow opens via openReadOnlyDB then calls GetSession.
				// Against a schema-bootstrapped-but-empty DB there are no sessions,
				// so GetSession returns a "not found" error — NOT a schema error.
				return runSessionShow("sess-deadbeef")
			},
			// GetSession wraps sql.ErrNoRows as "get session sess-deadbeef: …"
			// which runSessionShow re-wraps as "session … not found".
			wantErrorMsg: "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each subtest gets its own fresh, schema-less DB.  setupStaleSchemaDB
			// (defined in trace_test.go) sets WIPNOTE_DB_PATH and projectDirFlag.
			setupStaleSchemaDB(t)

			err := tt.runFunc()

			// RED guard: "no such table" means OpenReadOnlyMigrated was NOT used.
			if err != nil && strings.Contains(err.Error(), "no such table") {
				t.Fatalf("schema error (bare OpenReadOnly or openDB detected, not openReadOnlyDB/OpenReadOnlyMigrated): %v", err)
			}

			// GREEN: assert the concrete expected outcome per case.
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
}
