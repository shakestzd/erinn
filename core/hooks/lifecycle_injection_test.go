package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReconcilePortDrift_Injection proves the core reconcile path delegates to
// the injected PortDriftPathsFn (feat-331927fb) and resolves the repo root
// before calling it — without importing pluginbuild.
func TestReconcilePortDrift_Injection(t *testing.T) {
	prev := PortDriftPathsFn
	t.Cleanup(func() { PortDriftPathsFn = prev })

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".wipnote"), 0o755); err != nil {
		t.Fatalf("mkdir .wipnote: %v", err)
	}

	// No checker wired → no drift, never blocks.
	PortDriftPathsFn = nil
	if got := reconcilePortDrift(dir); got != nil {
		t.Fatalf("nil PortDriftPathsFn: want nil, got %v", got)
	}

	// Injected checker → its result, invoked with the resolved repo root.
	var gotRoot string
	PortDriftPathsFn = func(repoRoot string) []string {
		gotRoot = repoRoot
		return []string{"plugin/commands/x.md"}
	}
	got := reconcilePortDrift(dir)
	if len(got) != 1 || got[0] != "plugin/commands/x.md" {
		t.Fatalf("injected checker: want [plugin/commands/x.md], got %v", got)
	}
	if filepath.Clean(gotRoot) != filepath.Clean(dir) {
		t.Fatalf("checker called with repoRoot %q, want %q (dir holding .wipnote)", gotRoot, dir)
	}
}
