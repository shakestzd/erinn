package paths

import "path/filepath"

// dependencyManifestBasenames is the canonical set of dependency-manifest
// filenames across the ecosystems wipnote supports. Modifying one of these is a
// high-signal marker of external-technology work (adding, removing, or
// version-bumping a third-party library/SDK). It is the single source of truth
// shared by the PreToolUse research guards (core/hooks) and the CLI completion
// research gate (cmd/wipnote), so the pre-edit, pre-commit, and completion gates
// stay in lockstep. It is broader than projectTypeMarkers, which is a
// priority-ordered subset used only for language detection.
var dependencyManifestBasenames = map[string]bool{
	"go.mod":            true,
	"go.sum":            true,
	"go.work":           true,
	"go.work.sum":       true,
	"package.json":      true,
	"package-lock.json": true,
	"pnpm-lock.yaml":    true,
	"yarn.lock":         true,
	"requirements.txt":  true,
	"pyproject.toml":    true,
	"poetry.lock":       true,
	"Pipfile":           true,
	"Pipfile.lock":      true,
	"Cargo.toml":        true,
	"Cargo.lock":        true,
	"Gemfile":           true,
	"Gemfile.lock":      true,
}

// IsDependencyManifest reports whether path's basename is a dependency manifest.
// Detection is keyed on the basename (not the full path) so it matches a
// manifest in any directory of a monorepo.
func IsDependencyManifest(path string) bool {
	if path == "" {
		return false
	}
	return dependencyManifestBasenames[filepath.Base(path)]
}
