package recap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLineageWalkIsShared asserts the BFS traversal lives in exactly one place.
// cmd/wipnote/lineage.go must NOT carry a private copy of the walk (it should
// delegate to internal/lineage), and internal/recap must consume internal/lineage.
func TestLineageWalkIsShared(t *testing.T) {
	root := repoRoot(t)

	cmdLineage := readFile(t, filepath.Join(root, "cmd", "wipnote", "lineage.go"))
	for _, banned := range []string{
		"func bfsWalk(",
		"func forwardWalk(",
		"func backwardWalk(",
		"func annotateTimestamps(",
	} {
		if strings.Contains(cmdLineage, banned) {
			t.Errorf("cmd/wipnote/lineage.go retains a private BFS copy: %q", banned)
		}
	}
	if !strings.Contains(cmdLineage, "internal/lineage") {
		t.Errorf("cmd/wipnote/lineage.go does not import internal/lineage")
	}

	collect := readFile(t, filepath.Join(root, "internal", "recap", "collect.go"))
	if !strings.Contains(collect, "internal/lineage") {
		t.Errorf("internal/recap/collect.go does not consume internal/lineage")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found from working dir")
		}
		dir = parent
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
