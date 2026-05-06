package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

func buildCmd() *cobra.Command {
	var dist bool

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Rebuild the erinn binary",
		Long:  "Rebuild the erinn Go binary using the build script in the plugin directory.",
		RunE: func(cmd *cobra.Command, args []string) error {
			buildScript, err := resolveBuildScript()
			if err != nil {
				return err
			}

			var c *exec.Cmd
			if dist {
				c = exec.Command("bash", buildScript, "--dist")
			} else {
				c = exec.Command("bash", buildScript)
			}
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			return c.Run()
		},
	}
	cmd.Flags().BoolVar(&dist, "dist", false, "Build for distribution (bootstrap entry point + binary)")
	return cmd
}

// resolveBuildScript finds build.sh using a priority-ordered strategy.
//
// The build script lives at plugin/build.sh.  Its location
// depends on which binary is running:
//
//   - Dev mode: binary at plugin/hooks/bin/htmlgraph → two levels up
//   - Standalone CLI: binary at ~/.local/bin/htmlgraph → walk-up fails;
//     fall back to plugin dir from CLAUDE_PLUGIN_ROOT / ERINN_PLUGIN_DIR /
//     project-root detection
//   - Marketplace install: binary is a bootstrap script, not the real binary;
//     CLAUDE_PLUGIN_ROOT points to the real plugin tree
//
// Search order:
//  1. CLAUDE_PLUGIN_ROOT env var (always set in hook/plugin context)
//  2. ERINN_PLUGIN_DIR env var (explicit user override)
//  3. project-root detection (find .erinn/, look for plugin/ next to it)
//  4. os.Executable() walk-up (dev mode: binary inside plugin tree)
func resolveBuildScript() (string, error) {
	// Helper: probe whether a plugin dir has build.sh.
	tryPluginDir := func(dir string) (string, bool) {
		if dir == "" {
			return "", false
		}
		candidate := filepath.Join(dir, "build.sh")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		return "", false
	}

	// 1. CLAUDE_PLUGIN_ROOT (hook/plugin context).
	if s, ok := tryPluginDir(os.Getenv("CLAUDE_PLUGIN_ROOT")); ok {
		return s, nil
	}

	// 2. Explicit user override.
	if s, ok := tryPluginDir(os.Getenv("ERINN_PLUGIN_DIR")); ok {
		return s, nil
	}

	// 3. Project-root detection: find the .erinn/ directory walking up from
	//    CWD, then look for plugin/ adjacent to .erinn/.
	//    This works when the user runs `htmlgraph build` from anywhere inside
	//    the project tree (standalone CLI case).
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for {
			if _, err := os.Stat(filepath.Join(dir, ".erinn")); err == nil {
				candidate := filepath.Join(dir, "plugin", "build.sh")
				if _, err := os.Stat(candidate); err == nil {
					return candidate, nil
				}
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	// 4. os.Executable() walk-up — works for dev mode where the binary lives
	//    at plugin/hooks/bin/htmlgraph.
	binPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(binPath); err == nil {
		binPath = resolved
	}
	binDir := filepath.Dir(binPath)
	script := filepath.Join(binDir, "..", "..", "build.sh")
	abs, err := filepath.Abs(script)
	if err != nil {
		return "", fmt.Errorf("resolving build script path: %w", err)
	}
	if _, err := os.Stat(abs); os.IsNotExist(err) {
		return "", fmt.Errorf(
			"build script not found — tried CLAUDE_PLUGIN_ROOT, ERINN_PLUGIN_DIR, "+
				"project-root walk-up, and binary walk-up (last path: %s).\n"+
				"Run from the project root or set ERINN_PLUGIN_DIR=<path-to-go-plugin>.", abs)
	}
	return abs, nil
}
