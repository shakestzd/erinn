package hooks

import (
	"os/exec"
	"strings"
	"testing"
)

// TestHooksPackageDoesNotImportWorkitem enforces the spike creation policy:
// hook handlers must never create spikes directly. Spike creation is reserved
// for CLI commands and orchestrator actions only (see feat-84052b5e).
//
// The workitem package is the only path to SpikeCollection.Create. If hooks
// ever imported workitem, this test would catch the regression at CI time.
func TestHooksPackageDoesNotImportWorkitem(t *testing.T) {
	out, err := exec.Command(
		"go", "list", "-f", `{{join .Imports "\n"}}`,
		"github.com/shakestzd/wipnote/core/hooks",
	).Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}

	for _, imp := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.Contains(imp, "workitem") {
			t.Errorf("hooks package must not import workitem (spike creation policy violation): found import %q", imp)
		}
	}
}
