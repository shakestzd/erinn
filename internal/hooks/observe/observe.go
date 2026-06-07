// Package observe holds the otel- and pluginbuild-backed implementations of the
// core hooks lifecycle injection points (feat-331927fb).
//
// It is intentionally NOT part of the core hooks surface: it imports telemetry
// (internal/otel/*) and plugin-build tooling (internal/pluginbuild), neither of
// which may live in core. Blank-importing this package from the binary
// registers these implementations into core hooks at init time, so lifecycle
// hooks invoke them without taking a direct dependency on otel/pluginbuild.
package observe

import (
	"os"
	"path/filepath"

	"github.com/shakestzd/wipnote/internal/hooks"
	"github.com/shakestzd/wipnote/internal/otel/materialize"
	"github.com/shakestzd/wipnote/internal/otel/retention"
	"github.com/shakestzd/wipnote/internal/pluginbuild"
)

func init() {
	hooks.RetentionSweepFn = runRetentionSweep
	hooks.SessionMaterializeFn = materialize.Materialize
	hooks.PortDriftPathsFn = portDriftPaths
}

// runRetentionSweep performs a disk-retention pass for the project: rotate
// oversized logs and archive+prune raw events.ndjson for inactive,
// fully-ingested sessions. Invoked fire-and-forget from session-start so disk
// reclamation happens at a natural lifecycle point even when `wipnote serve`
// is not running.
//
// The DB handle is intentionally nil: the ndjson coverage sweep relies on the
// per-session .index-offset checkpoint, not the DB, and log rotation needs no
// DB. All errors are swallowed — retention must never affect session start.
func runRetentionSweep(projectDir, activeSessionID string) {
	if projectDir == "" {
		return
	}
	wipnoteDir := filepath.Join(projectDir, ".wipnote")
	_, _ = retention.Sweep(nil, wipnoteDir, activeSessionID, false)
}

// portDriftPaths reuses the generator drift gate (pluginbuild.CheckPorts) to
// return the drifted generated-port paths for the plugin-core repo at repoRoot,
// or nil when in sync, not a plugin-core repo, or on any error. The generator
// is the single source of truth; this never reimplements port diffing.
func portDriftPaths(repoRoot string) []string {
	if repoRoot == "" {
		return nil
	}
	manifestPath := filepath.Join(repoRoot, "packages", "plugin-core", "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		// Not a plugin-core repo (e.g. a downstream project dogfooding wipnote).
		return nil
	}
	m, err := pluginbuild.Load(manifestPath)
	if err != nil {
		return nil
	}
	drifts, err := pluginbuild.CheckPorts(m, repoRoot, pluginbuild.Names())
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(drifts))
	for _, d := range drifts {
		out = append(out, d.Path)
	}
	return out
}
