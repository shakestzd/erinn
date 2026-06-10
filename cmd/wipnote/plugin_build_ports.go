package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shakestzd/wipnote/port/pluginbuild"
	"github.com/spf13/cobra"
)

// findRepoRoot walks up from dir until it finds a directory containing go.mod,
// which is treated as the repository root. Returns an error if no go.mod is
// found before reaching the filesystem root. This is preferred over stripping a
// fixed number of path components from the manifest path, which breaks when the
// manifest is not at the canonical packages/plugin-core/manifest.json location.
func findRepoRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("could not locate repo root: no go.mod found walking up from %s", dir)
		}
		d = parent
	}
}

// pluginBuildContext holds the resolved inputs shared by `build-ports` and
// `check-ports`: the parsed manifest, the inferred repo root, and the ordered
// list of targets to act on. Both commands resolve these the same way — the
// only difference is whether they emit into the repo or into a tempdir.
type pluginBuildContext struct {
	manifest *pluginbuild.Manifest
	repoRoot string
	targets  []string
}

// resolvePluginBuildContext loads the manifest (autodetected from the working
// directory unless manifestFlag overrides it), infers the repo root by walking
// up to go.mod, and resolves the target list. Extracted so build-ports and
// check-ports share one code path.
func resolvePluginBuildContext(manifestFlag, targetFlag string) (*pluginBuildContext, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	manifestPath := manifestFlag
	if manifestPath == "" {
		manifestPath, err = pluginbuild.FindManifest(wd)
		if err != nil {
			return nil, err
		}
	}
	// Walk up from the manifest's directory to find go.mod — this correctly
	// handles any manifest path, not just the canonical
	// packages/plugin-core/manifest.json location.
	repoRoot, err := findRepoRoot(filepath.Dir(manifestPath))
	if err != nil {
		return nil, err
	}

	m, err := pluginbuild.Load(manifestPath)
	if err != nil {
		return nil, err
	}

	targets, err := resolveTargets(m, targetFlag)
	if err != nil {
		return nil, err
	}
	return &pluginBuildContext{manifest: m, repoRoot: repoRoot, targets: targets}, nil
}

// pluginBuildPortsCmd is `wipnote plugin build-ports`. It regenerates every
// target plugin tree from packages/plugin-core/manifest.json — the single
// source of truth for the wipnote CLI companion plugin across Claude Code,
// Codex CLI, and future targets. See port/pluginbuild for the adapter
// interface and per-target emitters.
func pluginBuildPortsCmd() *cobra.Command {
	var (
		targetFlag   string
		manifestFlag string
		outFlag      string
	)
	cmd := &cobra.Command{
		Use:   "build-ports",
		Short: "Generate Claude Code and Codex CLI plugin trees from plugin-core",
		Long: "Regenerate the target plugin trees (plugin/ for Claude Code, " +
			"packages/codex-marketplace/ for Codex CLI, " +
			"packages/gemini-extension/ for Gemini CLI) from the shared manifest " +
			"at packages/plugin-core/manifest.json. Use --target to limit output " +
			"to a single target.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, err := resolvePluginBuildContext(manifestFlag, targetFlag)
			if err != nil {
				return err
			}
			m := ctx.manifest
			repoRoot := ctx.repoRoot
			targets := ctx.targets

			for _, name := range targets {
				adapter, err := pluginbuild.Get(name)
				if err != nil {
					return err
				}
				outDir := outFlag
				if outDir == "" {
					outDir = filepath.Join(repoRoot, m.Targets[name].OutDir)
				}
				if err := adapter.Emit(m, repoRoot, outDir); err != nil {
					return fmt.Errorf("emit %s: %w", name, err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  wrote %s plugin → %s\n", name, outDir)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&targetFlag, "target", "all",
		"target to emit: all | "+strings.Join(pluginbuild.Names(), " | "))
	cmd.Flags().StringVar(&manifestFlag, "manifest", "",
		"path to plugin-core manifest (default: autodetect packages/plugin-core/manifest.json); "+
			"repo root is inferred by walking up from the manifest's directory until go.mod is found")
	cmd.Flags().StringVar(&outFlag, "out", "",
		"override output directory (only meaningful with a single --target)")
	return cmd
}

func resolveTargets(m *pluginbuild.Manifest, flag string) ([]string, error) {
	if flag == "" || flag == "all" {
		names := make([]string, 0, len(m.Targets))
		for n := range m.Targets {
			names = append(names, n)
		}
		// deterministic order
		for i := 1; i < len(names); i++ {
			for j := i; j > 0 && names[j-1] > names[j]; j-- {
				names[j-1], names[j] = names[j], names[j-1]
			}
		}
		return names, nil
	}
	if _, ok := m.Targets[flag]; !ok {
		return nil, fmt.Errorf("target %q not declared in manifest.targets", flag)
	}
	return []string{flag}, nil
}
