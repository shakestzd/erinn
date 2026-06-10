package pluginbuild

import (
	"fmt"
	"os"
	"path/filepath"
)

func init() { Register(geminiAdapter{}) }

// geminiAdapter emits the Gemini CLI extension tree. Layout:
//
//	<outDir>/gemini-extension.json
//	<outDir>/GEMINI.md                  (copied from repoRoot, if target.ContextFile is set)
//	<outDir>/commands/<namespace>/*.toml
//	<outDir>/agents/*.md
//	<outDir>/skills/<name>/SKILL.md
//	<outDir>/hooks/hooks.json
//
// Phase 0 scope: skeleton + manifest only. Assets and hooks land in later
// phases (see track trk-83603ac7). A skeleton emission is enough for
// `gemini extensions link <dir>` to succeed.
type geminiAdapter struct{}

func (geminiAdapter) Name() string { return "gemini" }

// geminiOwnedSubtrees lists the subdirectory names under the gemini outDir that
// build-ports fully regenerates. Hand-maintained files (README.md, etc.) live
// outside these subtrees and are never touched by stale-file cleanup.
var geminiOwnedSubtrees = []string{"commands", "agents", "skills", "templates", "static", "config", "hooks"}

func (g geminiAdapter) Emit(m *Manifest, repoRoot, outDir string) error {
	target, ok := m.Targets[g.Name()]
	if !ok {
		return fmt.Errorf("manifest has no target %q", g.Name())
	}

	// Pre-clean owned subtrees so renamed/deleted source files don't leave
	// stale output files behind. Non-owned files (README, gemini-extension.json,
	// GEMINI.md, etc.) at the outDir root are untouched.
	if err := cleanOwnedSubtrees(outDir, geminiOwnedSubtrees); err != nil {
		return fmt.Errorf("gemini pre-clean: %w", err)
	}

	if err := writeGeminiManifest(m, target, filepath.Join(outDir, target.ManifestPath)); err != nil {
		return err
	}
	if err := ensureGeminiSkeletonDirs(outDir); err != nil {
		return err
	}
	// Sub-emitters populate the skeleton across phases (assets, commands,
	// hooks). Each lives in its own file and registers via init() so phases
	// land without sharing edits to this function. Order follows filename
	// collation across gemini_*.go files — deterministic by Go semantics.
	for _, emit := range geminiSubEmitters {
		if err := emit(m, repoRoot, outDir, target); err != nil {
			return err
		}
	}
	return nil
}

// GeminiSubEmitter is the signature every phase uses to extend the Gemini
// adapter. Phase files (gemini_assets.go, gemini_commands.go, gemini_hooks.go)
// append to geminiSubEmitters in init().
type GeminiSubEmitter func(m *Manifest, repoRoot, outDir string, target Target) error

var geminiSubEmitters []GeminiSubEmitter

// geminiExtensionJSON is the Gemini extension manifest schema. Only the
// fields wipnote currently uses are modeled; Gemini tolerates omitted
// optional keys (excludeTools, settings, themes, mcpServers, migratedTo, plan).
type geminiExtensionJSON struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	Description     string `json:"description"`
	ContextFileName string `json:"contextFileName,omitempty"`
}

func writeGeminiManifest(m *Manifest, t Target, path string) error {
	manifest := geminiExtensionJSON{
		Name:        m.Name,
		Version:     m.Version,
		Description: m.Description,
	}
	if t.ContextFile != "" {
		manifest.ContextFileName = filepath.Base(t.ContextFile)
	}
	return writeJSON(path, manifest)
}

// ensureGeminiSkeletonDirs creates the empty subtree Gemini expects. Later
// phases (1–3) fill these in. Creating them up front means `gemini
// extensions link` succeeds against the skeleton and later additions don't
// racedecide which directory exists.
func ensureGeminiSkeletonDirs(outDir string) error {
	for _, dir := range []string{"commands", "agents", "skills", "hooks"} {
		if err := os.MkdirAll(filepath.Join(outDir, dir), 0o755); err != nil {
			return err
		}
	}
	return nil
}
