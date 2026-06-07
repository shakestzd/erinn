// cli_route.go — MVP-4 (feat-075c110d): bounded, fallback-safe daemon routing
// for the two highest-contention CLI derived-index writes (work-item status
// transitions and session insert/status). This mirrors the committed MVP-3 hook
// path in internal/hooks/dbgate.go (routeDerivedEventViaDaemon): build a typed
// DerivedOp, ship it to the per-project writer daemon under a hard deadline,
// auto-spawning the daemon when absent, and report whether the op was applied.
//
// LIVE-SESSION SAFETY: these run inside foreground CLI commands that the user
// is waiting on (feature/bug/spike start+complete, session start+end). The
// attempt MUST be strictly bounded — on ErrWriterUnavailable / any error /
// deadline / error-ack the CALLER falls back to its existing direct-open +
// RetryOnBusy path. This helper NEVER blocks beyond CLISubmitBudget and never
// surfaces an error: a false return means "fall back to direct write".
//
// Canonical .html is written FIRST by the command; only this derived-index
// (SQLite) write is delegated. Reads stay direct.
package apply

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/models"
)

// CLISubmitBudget is the TOTAL wall-clock budget for a daemon-routed CLI
// derived-write attempt (dial + auto-spawn + readiness-wait + submit). It
// matches the hook tree's daemonSubmitBudget — the failure boundary is the
// same: exceed it and the caller falls back to the direct path. Foreground CLI
// commands must never hang on a wedged or absent daemon.
const CLISubmitBudget = 2 * time.Second

// cliOpID derives a deterministic, bounded dedup key for a CLI op from its
// op_type and the natural identity of the row it touches (feature/session id
// plus the target status). Replaying the same transition dedups against its
// first application within the daemon's dedup window; a DISTINCT transition
// (e.g. in-progress → done) yields a distinct key so it is not swallowed.
func cliOpID(opType, id, status string) string {
	h := sha256.Sum256([]byte(opType + ":" + id + ":" + status))
	return hex.EncodeToString(h[:16])
}

// routeViaDaemon performs the bounded daemon round-trip for an already-built
// DerivedOp. opID is the dedup key; projectRoot is the project root (the
// PARENT of .wipnote/). It returns true ONLY when the writer applied (or
// deduped) the op. Empty projectRoot, encode failure, unavailability, ctx
// deadline, or an error ack all return false so the caller falls back.
func routeViaDaemon(projectRoot, opID, opType string, op DerivedOp) bool {
	if projectRoot == "" {
		return false
	}
	payload, err := Encode(op)
	if err != nil {
		return false
	}
	env := daemon.Envelope{
		OpID:    opID,
		OpType:  opType,
		Payload: payload,
	}

	ctx, cancel := context.WithTimeout(context.Background(), CLISubmitBudget)
	defer cancel()

	selfExe, _ := os.Executable()
	client := daemon.NewWriterClient(projectRoot)
	ack, err := client.SubmitOrSpawn(ctx, projectRoot, selfExe, env)
	if err != nil {
		return false // ErrWriterUnavailable / ctx deadline → fall back
	}
	switch ack.Status {
	case daemon.AckApplied, daemon.AckDuplicate:
		return true
	default:
		return false // error ack → fall back to direct-open
	}
}

// RouteFeatureStatus routes a work-item status transition (db.UpdateFeatureStatus)
// through the writer daemon. Returns true when applied/deduped; false → caller
// must perform the direct write. Bounded by CLISubmitBudget.
func RouteFeatureStatus(projectRoot, featureID, status string) bool {
	return routeViaDaemon(
		projectRoot,
		cliOpID(OpTypeFeatureStatus, featureID, status),
		OpTypeFeatureStatus,
		DerivedOp{Type: OpTypeFeatureStatus, FeatureID: featureID, Status: status},
	)
}

// RouteSessionStatus routes a session status transition (db.UpdateSessionStatus)
// through the writer daemon. Bounded by CLISubmitBudget.
func RouteSessionStatus(projectRoot, sessionID, status string) bool {
	return routeViaDaemon(
		projectRoot,
		cliOpID(OpTypeSessionStatus, sessionID, status),
		OpTypeSessionStatus,
		DerivedOp{Type: OpTypeSessionStatus, SessionID: sessionID, Status: status},
	)
}

// RouteSessionInsert routes a session-row insert (db.InsertSession) through the
// writer daemon. The op_id is keyed on the unique session_id so a replay
// dedups. Bounded by CLISubmitBudget. Returns true on applied/dedup; false →
// caller performs the direct insert (and surfaces its error exactly as today).
func RouteSessionInsert(projectRoot string, s *models.Session) bool {
	if s == nil {
		return false
	}
	return routeViaDaemon(
		projectRoot,
		cliOpID(OpTypeSessionInsert, s.SessionID, s.Status),
		OpTypeSessionInsert,
		DerivedOp{Type: OpTypeSessionInsert, Session: s},
	)
}
