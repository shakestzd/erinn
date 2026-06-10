package pluginbuild

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Drift describes a single way a committed generated tree diverges from a fresh
// regeneration. Path is repo-root-relative for stable, copy-pasteable output.
type Drift struct {
	// Path is the repo-root-relative path that drifted.
	Path string
	// Kind is one of "modified" (content differs), "missing" (generator
	// produced a file the committed tree lacks), or "extra" (committed tree
	// has a generated-tree file the fresh regen no longer produces).
	Kind string
}

func (d Drift) String() string { return fmt.Sprintf("%s: %s", d.Kind, d.Path) }

// CheckPorts regenerates every requested target into a private tempdir and
// diffs each generated tree against the committed tree under repoRoot. It
// returns the list of drifted paths (empty when in sync).
//
// The diff is intentionally a full regenerate-and-compare with no checksum or
// manifest cache: the generator is the authority, and any cache is one more
// thing that can silently disagree with reality. Comparison is bidirectional
// within each target's generated subtree:
//
//   - every file the generator produces must exist byte-identically in the
//     committed tree (else "missing" or "modified");
//   - every file under the committed target's owned subtrees that the fresh
//     regen no longer produces is reported "extra".
//
// Hand-maintained files outside the generated/owned subtrees are never
// compared, so editing README.md or a source asset never trips the gate
// unless it changes generated output.
func CheckPorts(m *Manifest, repoRoot string, targets []string) ([]Drift, error) {
	tmpRoot, err := os.MkdirTemp("", "wipnote-check-ports-")
	if err != nil {
		return nil, fmt.Errorf("create tempdir: %w", err)
	}
	defer os.RemoveAll(tmpRoot)

	var drifts []Drift
	for _, name := range targets {
		target, ok := m.Targets[name]
		if !ok {
			return nil, fmt.Errorf("manifest has no target %q", name)
		}
		adapter, err := Get(name)
		if err != nil {
			return nil, err
		}

		genOut := filepath.Join(tmpRoot, target.OutDir)
		// Emit reads asset sources from repoRoot and writes the target tree
		// into genOut — identical to `build-ports`, only redirected.
		if err := adapter.Emit(m, repoRoot, genOut); err != nil {
			return nil, fmt.Errorf("regenerate %s into tempdir: %w", name, err)
		}

		committedOut := filepath.Join(repoRoot, target.OutDir)
		d, err := diffTrees(repoRoot, genOut, committedOut, target)
		if err != nil {
			return nil, err
		}
		drifts = append(drifts, d...)
	}

	sort.Slice(drifts, func(i, j int) bool {
		if drifts[i].Path == drifts[j].Path {
			return drifts[i].Kind < drifts[j].Kind
		}
		return drifts[i].Path < drifts[j].Path
	})
	return drifts, nil
}

// generatedArtifacts returns the explicit files an adapter writes itself
// (manifest, hooks, mcp). For an in-place target like Claude — whose outDir IS
// the repo's plugin/ tree and whose asset sources are copied/translated onto
// themselves — these are the ONLY paths a clean repo can be diffed against:
// everything else under plugin/ is either hand-maintained (bootstrap scripts,
// marketplace.json) or a copy-in-place asset where committed == source by
// design. Returns nil for dedicated-tree targets (codex/gemini) so they use
// the full owned-subtree diff instead.
func generatedArtifacts(target Target) []string {
	if isInPlaceTarget(target) {
		paths := []string{target.ManifestPath, target.HooksPath}
		if target.MCPPath != "" {
			paths = append(paths, target.MCPPath)
		}
		return paths
	}
	return nil
}

// isInPlaceTarget reports whether the target emits into the repo's own plugin/
// tree (Claude) rather than a dedicated generated tree under packages/. Claude
// is the only such target: it has no marketplace plugin subdir, no Gemini
// command namespace, and no context file.
func isInPlaceTarget(target Target) bool {
	return target.PluginSubdir == "" &&
		target.CommandNamespace == "" &&
		target.ContextFile == ""
}

// diffTrees compares the freshly generated tree (genOut) against the committed
// tree (committedOut). repoRoot is used only to render drift paths relative to
// the repo root.
func diffTrees(repoRoot, genOut, committedOut string, target Target) ([]Drift, error) {
	// In-place target (Claude): diff ONLY the explicitly generated artifacts.
	// A full-tree walk would false-positive on translated agents and
	// hand-maintained files that live under the same plugin/ root.
	if artifacts := generatedArtifacts(target); artifacts != nil {
		var drifts []Drift
		for _, rel := range artifacts {
			genPath := filepath.Join(genOut, rel)
			committedPath := filepath.Join(committedOut, rel)
			repoRel := relTo(repoRoot, committedPath)

			generated, gerr := os.ReadFile(genPath)
			if os.IsNotExist(gerr) {
				// Adapter declared the path but did not emit it — not drift.
				continue
			}
			if gerr != nil {
				return nil, gerr
			}
			committed, cerr := os.ReadFile(committedPath)
			if os.IsNotExist(cerr) {
				drifts = append(drifts, Drift{Path: repoRel, Kind: "missing"})
				continue
			}
			if cerr != nil {
				return nil, cerr
			}
			if !bytes.Equal(generated, committed) {
				drifts = append(drifts, Drift{Path: repoRel, Kind: "modified"})
			}
		}
		return drifts, nil
	}

	var drifts []Drift

	// Forward pass: every generated file must match the committed file.
	genFiles := map[string]struct{}{}
	err := filepath.WalkDir(genOut, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(genOut, path)
		if err != nil {
			return err
		}
		genFiles[rel] = struct{}{}

		committedPath := filepath.Join(committedOut, rel)
		repoRel := relTo(repoRoot, committedPath)

		committed, cerr := os.ReadFile(committedPath)
		if os.IsNotExist(cerr) {
			drifts = append(drifts, Drift{Path: repoRel, Kind: "missing"})
			return nil
		}
		if cerr != nil {
			return cerr
		}
		generated, gerr := os.ReadFile(path)
		if gerr != nil {
			return gerr
		}
		if !bytes.Equal(generated, committed) {
			drifts = append(drifts, Drift{Path: repoRel, Kind: "modified"})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk generated tree %s: %w", genOut, err)
	}

	// Reverse pass: a committed file inside an owned/generated subtree that the
	// fresh regen no longer produces is stale ("extra"). Scoped to owned
	// subtrees so hand-maintained root files are never flagged.
	for _, sub := range ownedDiffSubtrees(target) {
		base := filepath.Join(committedOut, sub)
		if _, statErr := os.Stat(base); os.IsNotExist(statErr) {
			continue
		}
		walkErr := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(committedOut, path)
			if relErr != nil {
				return relErr
			}
			if _, produced := genFiles[rel]; !produced {
				drifts = append(drifts, Drift{Path: relTo(repoRoot, path), Kind: "extra"})
			}
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("walk committed subtree %s: %w", base, walkErr)
		}
	}

	return drifts, nil
}

// ownedDiffSubtrees returns the committed-tree subdirectories whose contents
// are fully generated for a dedicated-tree target (codex/gemini). A committed
// file under one of these that the fresh regen no longer produces is stale and
// reported as "extra". Only reached for non-in-place targets — Claude is
// handled by generatedArtifacts/diffTrees before this is consulted. The lists
// mirror codexOwnedSubtrees / geminiOwnedSubtrees in the adapters.
func ownedDiffSubtrees(target Target) []string {
	if target.PluginSubdir != "" {
		// Codex: everything under the plugin subdir is generated.
		return []string{filepath.Dir(target.PluginSubdir)}
	}
	// Gemini: mirrors geminiOwnedSubtrees.
	return []string{"commands", "agents", "skills", "templates", "static", "config", "hooks"}
}

// relTo renders p relative to root, falling back to p on error so output is
// always something printable.
func relTo(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return rel
}
