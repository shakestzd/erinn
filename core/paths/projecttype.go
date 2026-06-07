package paths

import (
	"os"
	"path/filepath"
)

// ProjectType identifies the language ecosystem a project uses.
// Detected from well-known manifest files at the project root or one
// level of monorepo subdirectories.
type ProjectType string

const (
	ProjectTypeUnknown ProjectType = "unknown"
	ProjectTypeGo      ProjectType = "go"
	ProjectTypePython  ProjectType = "python"
	ProjectTypeNode    ProjectType = "node"
	ProjectTypeRust    ProjectType = "rust"
)

// projectTypeMarkers is the priority-ordered list of manifest files used
// to identify a project's language. The first match wins, scanned across
// projectDir and one level of monorepo subdirectories.
var projectTypeMarkers = []struct {
	file string
	typ  ProjectType
}{
	{"go.mod", ProjectTypeGo},
	{"pyproject.toml", ProjectTypePython},
	{"requirements.txt", ProjectTypePython},
	{"package.json", ProjectTypeNode},
	{"Cargo.toml", ProjectTypeRust},
}

// monorepoSubdirs is the list of conventional monorepo container
// directories scanned for nested manifests when no top-level marker
// is found in projectDir itself.
var monorepoSubdirs = []string{"packages", "src"}

// DetectProjectType returns the language ecosystem of projectDir by
// looking for well-known manifest files. It checks projectDir itself
// plus one level of monorepo subdirectories (packages/*, src/*).
// Returns ProjectTypeUnknown if projectDir is empty or no marker is
// found in any candidate directory.
func DetectProjectType(projectDir string) ProjectType {
	if projectDir == "" {
		return ProjectTypeUnknown
	}

	dirs := []string{projectDir}
	for _, sub := range monorepoSubdirs {
		entries, err := os.ReadDir(filepath.Join(projectDir, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(projectDir, sub, e.Name()))
			}
		}
	}

	for _, m := range projectTypeMarkers {
		for _, dir := range dirs {
			if _, err := os.Stat(filepath.Join(dir, m.file)); err == nil {
				return m.typ
			}
		}
	}
	return ProjectTypeUnknown
}

// TestCommandFor returns the canonical test command for a project type,
// or an empty string for ProjectTypeUnknown. Callers that surface this
// to the user should provide their own fallback text when the result
// is empty so the user is never blocked with no actionable suggestion.
func TestCommandFor(t ProjectType) string {
	switch t {
	case ProjectTypeGo:
		return "go test ./..."
	case ProjectTypePython:
		return "uv run pytest"
	case ProjectTypeNode:
		return "npm test"
	case ProjectTypeRust:
		return "cargo test"
	default:
		return ""
	}
}
