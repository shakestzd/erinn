package pluginbuild

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Phase 1 registration: append the asset sub-emitter at init time so
// geminiAdapter.Emit walks it without needing edits to gemini.go. Phases 2 and
// 3 register their own sub-emitters from separate files for the same reason.
func init() {
	geminiSubEmitters = append(geminiSubEmitters, emitGeminiAssets)
}

// emitGeminiAssets copies the verbatim-reusable asset trees (skills,
// templates, static, config) plus the repo-root context file (GEMINI.md) into
// the generated Gemini extension tree.
//
// Commands are deliberately NOT copied here — Gemini uses TOML command
// definitions and a different on-disk layout (commands/<namespace>/*.toml).
// Phase 2 owns the .md → .toml translation and lives in its own file.
//
// Agents are deliberately NOT copied here — gemini_agents.go translates agent
// frontmatter from Claude conventions to Gemini conventions and owns the
// agents/ subtree. Copying verbatim here would overwrite the translated files.
//
// Missing sources are tolerated (copyAssetTree treats them as no-ops), which
// mirrors how copyAssets behaves for the Claude and Codex targets.
func emitGeminiAssets(m *Manifest, repoRoot, outDir string, t Target) error {
	knownRoles := codexKnownAgentRoles(m, repoRoot)
	pairs := []struct{ src, dst string }{
		{m.AssetSources.Skills, "skills"},
		{m.AssetSources.Templates, "templates"},
		{m.AssetSources.Static, "static"},
		{m.AssetSources.Config, "config"},
	}
	for _, p := range pairs {
		if p.src == "" {
			continue
		}
		src := filepath.Join(repoRoot, p.src)
		dst := filepath.Join(outDir, p.dst)
		if err := copyAssetTreeGemini(src, dst, knownRoles); err != nil {
			return fmt.Errorf("gemini copy %s -> %s: %w", p.src, p.dst, err)
		}
	}
	// Gemini picks up the extension's "context file" (e.g. GEMINI.md) from the
	// extension root. The manifest target declares the repo-relative source via
	// ContextFile; we copy it verbatim to <outDir>/<basename>. When ContextFile
	// is empty, skip — targets that don't declare one (Claude, Codex) opt out.
	if t.ContextFile != "" {
		src := filepath.Join(repoRoot, t.ContextFile)
		dst := filepath.Join(outDir, filepath.Base(t.ContextFile))
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("gemini copy contextFile %s: %w", t.ContextFile, err)
		}
	}
	return nil
}

func copyAssetTreeGemini(srcDir, dstDir string, knownRoles map[string]struct{}) error {
	info, err := os.Stat(srcDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("asset source %s is not a directory", srcDir)
	}
	same, err := samePath(srcDir, dstDir)
	if err != nil {
		return err
	}
	if same {
		return nil
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && isCodexOverrideFile(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFileGemini(path, target, knownRoles)
	})
}

func copyFileGemini(src, dst string, knownRoles map[string]struct{}) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	probe := data
	if len(probe) > 512 {
		probe = probe[:512]
	}
	if bytes.IndexByte(probe, 0) >= 0 {
		return os.WriteFile(dst, data, 0o644)
	}
	translated := rewriteGeminiAgentIDs(rewriteGeminiDelegationSyntax(string(data), knownRoles), knownRoles)
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(translated), info.Mode().Perm())
}

func rewriteGeminiAgentIDs(content string, knownRoles map[string]struct{}) string {
	const prefix = "wipnote:"
	if !strings.Contains(content, prefix) {
		return content
	}
	var buf strings.Builder
	buf.Grow(len(content))
	rest := content
	for {
		idx := strings.Index(rest, prefix)
		if idx < 0 {
			buf.WriteString(rest)
			break
		}
		buf.WriteString(rest[:idx])
		rest = rest[idx+len(prefix):]
		roleEnd := 0
		for roleEnd < len(rest) {
			c := rest[roleEnd]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '-' || c == '_' {
				roleEnd++
			} else {
				break
			}
		}
		role := rest[:roleEnd]
		rest = rest[roleEnd:]
		if _, known := knownRoles[role]; known {
			buf.WriteString(role)
		} else {
			buf.WriteString(prefix)
			buf.WriteString(role)
		}
	}
	return buf.String()
}
