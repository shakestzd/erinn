package hooks

import "database/sql"

// Lifecycle injection points (feat-331927fb).
//
// These package-level function vars are the seam that lets the core hook
// handlers stay free of telemetry (otel) and plugin-build tooling. Core hooks
// call them only when non-nil; the otel/pluginbuild-backed implementations live
// in the non-core sibling package internal/hooks/observe, which registers them
// in init(). The binary wires them in by blank-importing that package.
//
// A nil var means "feature not wired" — the core handler degrades to a no-op,
// which matches the best-effort, never-block semantics of these side concerns
// (disk retention, telemetry rollup, plugin-port drift). This is what keeps the
// core hook surface importable from core/ once the package is split out.
var (
	// RetentionSweepFn runs the fire-and-forget disk-retention pass at session
	// start (log rotation + ndjson archival for inactive sessions).
	RetentionSweepFn func(projectDir, activeSessionID string)

	// SessionMaterializeFn rolls up a session's OTel signals into the index at
	// session end. Registered directly from otel/materialize.Materialize.
	SessionMaterializeFn func(database *sql.DB, projectDir, sessionID string) error

	// PortDriftPathsFn returns the generated-plugin-tree paths that have drifted
	// from the manifest for the repo rooted at repoRoot, or nil when in sync,
	// not a plugin-core repo, or the checker is unavailable. Shared by the
	// session-exit reconcile and the pre-commit port-drift guard.
	PortDriftPathsFn func(repoRoot string) []string
)
