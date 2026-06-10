package pluginbuild

import (
	"fmt"
	"os"
	"path/filepath"
)

// copyAssets is the shared asset-copy helper used by every target adapter.
// Markdown surfaces (commands, agents, skills) and static files are copied
// verbatim — the formats are compatible across Claude Code and Codex CLI.
func copyAssets(m *Manifest, repoRoot, outDir string) error {
	pairs := []struct{ src, dst string }{
		{m.AssetSources.Commands, "commands"},
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
		if err := copyAssetTree(src, dst); err != nil {
			return err
		}
	}
	if err := emitClaudeAgents(m, repoRoot, outDir); err != nil {
		return err
	}
	return nil
}

func emitClaudeAgents(m *Manifest, repoRoot, outDir string) error {
	if m.AssetSources.Agents == "" {
		return nil
	}
	srcDir := filepath.Join(repoRoot, m.AssetSources.Agents)
	info, err := os.Stat(srcDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat agents source %s: %w", srcDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("agents source %s is not a directory", srcDir)
	}

	dstDir := filepath.Join(outDir, "agents")
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

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read agents source %s: %w", srcDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(srcDir, e.Name())
		dst := filepath.Join(dstDir, e.Name())
		if filepath.Ext(e.Name()) != ".md" {
			if err := copyFile(src, dst); err != nil {
				return err
			}
			continue
		}
		raw, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read agent %s: %w", e.Name(), err)
		}
		translated, err := translateClaudeAgentFrontmatter(e.Name(), raw)
		if err != nil {
			return fmt.Errorf("translate agent %s: %w", e.Name(), err)
		}
		if err := os.WriteFile(dst, translated, 0o644); err != nil {
			return fmt.Errorf("write translated agent %s: %w", dst, err)
		}
	}
	return nil
}
