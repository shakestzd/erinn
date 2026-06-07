package observe

import (
	"testing"

	"github.com/shakestzd/wipnote/internal/hooks"
)

// TestInitRegistersLifecycleFns verifies that importing this package wires all
// three core-hook lifecycle injection points (feat-331927fb).
func TestInitRegistersLifecycleFns(t *testing.T) {
	if hooks.RetentionSweepFn == nil {
		t.Error("RetentionSweepFn not registered by observe.init()")
	}
	if hooks.SessionMaterializeFn == nil {
		t.Error("SessionMaterializeFn not registered by observe.init()")
	}
	if hooks.PortDriftPathsFn == nil {
		t.Error("PortDriftPathsFn not registered by observe.init()")
	}
}

// TestPortDriftPaths_NotPluginCoreRepo_ReturnsNil verifies the manifest-presence
// gate: a directory without packages/plugin-core/manifest.json (and the empty
// root) report no drift rather than erroring.
func TestPortDriftPaths_NotPluginCoreRepo_ReturnsNil(t *testing.T) {
	if got := portDriftPaths(t.TempDir()); got != nil {
		t.Errorf("non-plugin-core dir: want nil, got %v", got)
	}
	if got := portDriftPaths(""); got != nil {
		t.Errorf("empty repoRoot: want nil, got %v", got)
	}
}
