package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func buildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build",
		Short: "Rebuild the wipnote binary",
		Long:  "Compile the wipnote Go binary with version stamping and install it to ~/.local/bin/.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuild()
		},
	}
}

func runBuild() error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return err
	}

	version := resolveBuildVersion(projectRoot)

	if err := syncNotebookFiles(projectRoot); err != nil {
		return fmt.Errorf("sync notebook files: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	installDir := filepath.Join(home, ".local", "bin")
	metaDir := filepath.Join(home, ".local", "share", "wipnote")
	binaryPath := filepath.Join(installDir, "wipnote")
	aliasPath := filepath.Join(installDir, "wn")
	versionFile := filepath.Join(metaDir, ".binary-version")

	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", installDir, err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", metaDir, err)
	}

	fmt.Printf("Building wipnote (version: %s)...\n", version)
	// Build to a temp path in the same directory, then atomically rename into
	// place. A direct `go build -o binaryPath` (or remove-then-build) leaves a
	// multi-second window where ~/.local/bin/wipnote is missing or partially
	// written — a concurrently-running agy/Claude statusline or hook hits
	// "exit 127: not found" mid-rebuild (bug-8fd0fac2). os.Rename is atomic on
	// the same filesystem and gives the new binary a fresh inode, so macOS does
	// not reuse a cached code signature (the reason the old code removed first).
	tmpBinary := binaryPath + ".tmp-build"
	_ = os.Remove(tmpBinary)
	goBuild := exec.Command("go", "build",
		"-ldflags", fmt.Sprintf("-s -w -X main.version=%s", version),
		"-o", tmpBinary,
		"./cmd/wipnote/",
	)
	goBuild.Dir = projectRoot
	goBuild.Stdout = os.Stdout
	goBuild.Stderr = os.Stderr
	if err := goBuild.Run(); err != nil {
		_ = os.Remove(tmpBinary)
		return fmt.Errorf("go build: %w", err)
	}
	if err := os.Chmod(tmpBinary, 0o755); err != nil {
		_ = os.Remove(tmpBinary)
		return fmt.Errorf("chmod %s: %w", tmpBinary, err)
	}
	if err := os.Rename(tmpBinary, binaryPath); err != nil {
		_ = os.Remove(tmpBinary)
		return fmt.Errorf("install %s: %w", binaryPath, err)
	}

	if err := os.Remove(aliasPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", aliasPath, err)
	}
	if err := os.Symlink(binaryPath, aliasPath); err != nil {
		return fmt.Errorf("symlink %s: %w", aliasPath, err)
	}

	if err := os.WriteFile(versionFile, []byte(version+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", versionFile, err)
	}

	// Mirror the source plugin tree into ~/.local/share/wipnote/plugin so a
	// dev-built binary lays out the same bundle as a release-installed one.
	// This is what Phase B will read from when `wipnote claude` (no --dev)
	// flips to loading the bundled tree via --plugin-dir. `--dev` mode keeps
	// using $(pwd)/plugin/ directly — unchanged.
	srcPlugin := filepath.Join(projectRoot, "plugin")
	destPlugin := filepath.Join(metaDir, "plugin")
	if err := mirrorPluginTree(srcPlugin, destPlugin); err != nil {
		return fmt.Errorf("mirror plugin tree: %w", err)
	}

	// Mirror the Codex CLI marketplace tree alongside the plugin tree. Phase B
	// will flip `wipnote codex` to load from this bundled path; until then it
	// just keeps dev and release layouts in sync.
	srcCodex := filepath.Join(projectRoot, "port", "packages", "codex-marketplace")
	destCodex := filepath.Join(metaDir, "codex-marketplace")
	if err := mirrorPluginTree(srcCodex, destCodex); err != nil {
		return fmt.Errorf("mirror codex-marketplace tree: %w", err)
	}

	// Mirror the Gemini CLI extension tree alongside the plugin tree. Phase B
	// will flip `wipnote gemini` to load from this bundled path; until then it
	// just keeps dev and release layouts in sync.
	srcGemini := filepath.Join(projectRoot, "port", "packages", "gemini-extension")
	destGemini := filepath.Join(metaDir, "gemini-extension")
	if err := mirrorPluginTree(srcGemini, destGemini); err != nil {
		return fmt.Errorf("mirror gemini-extension tree: %w", err)
	}

	// Mirror the Antigravity CLI extension tree alongside the plugin tree. Phase B
	// will flip `wipnote antigravity` to load from this bundled path; until then it
	// just keeps dev and release layouts in sync.
	srcAntigravity := filepath.Join(projectRoot, "port", "packages", "antigravity-extension")
	destAntigravity := filepath.Join(metaDir, "antigravity-extension")
	if err := mirrorPluginTree(srcAntigravity, destAntigravity); err != nil {
		return fmt.Errorf("mirror antigravity-extension tree: %w", err)
	}

	fmt.Printf("Installed: %s (v%s)\n", binaryPath, version)
	fmt.Printf("Alias:     %s -> wipnote\n", aliasPath)
	fmt.Printf("Plugin:    %s\n", destPlugin)
	fmt.Printf("Codex:     %s\n", destCodex)
	fmt.Printf("Gemini:    %s\n", destGemini)
	fmt.Printf("Antigravity: %s\n", destAntigravity)
	return nil
}

// mirrorPluginTree replaces dst with a fresh copy of src. Uses `cp -a` so
// file modes (e.g. executable hooks/bin/*.sh) are preserved without us
// reimplementing permission handling. Idempotent: removes dst first.
func mirrorPluginTree(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		// Source plugin/ directory missing — surface a clear error rather
		// than silently leaving the dest stale. This should never happen
		// in a healthy worktree.
		return fmt.Errorf("plugin source %s not found: %w", src, err)
	}
	if err := os.RemoveAll(dst); err != nil {
		return fmt.Errorf("remove %s: %w", dst, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	cp := exec.Command("cp", "-a", src, dst)
	cp.Stdout = os.Stdout
	cp.Stderr = os.Stderr
	if err := cp.Run(); err != nil {
		return fmt.Errorf("cp -a %s %s: %w", src, dst, err)
	}
	return nil
}

// findProjectRoot walks up from CWD looking for go.mod. Honors WIPNOTE_PROJECT_ROOT.
func findProjectRoot() (string, error) {
	if env := os.Getenv("WIPNOTE_PROJECT_ROOT"); env != "" {
		if _, err := os.Stat(filepath.Join(env, "go.mod")); err == nil {
			return env, nil
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf(
				"wipnote project root not found — no go.mod walking up from %s.\n"+
					"Run from within the project tree, or set WIPNOTE_PROJECT_ROOT=<repo-path>",
				cwd,
			)
		}
		dir = parent
	}
}

func resolveBuildVersion(projectRoot string) string {
	cmd := exec.Command("git", "describe", "--tags", "--always")
	cmd.Dir = projectRoot
	out, err := cmd.Output()
	if err != nil {
		return "dev"
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
}

// syncNotebookFiles mirrors prototypes/*.py into internal/notebook/files/ so
// //go:embed picks up the latest source. Idempotent; preserves destination-only
// files (plan_notebook.py, plan_persistence.py, plan_ui.py live only in the
// destination and must not be deleted).
func syncNotebookFiles(projectRoot string) error {
	srcDir := filepath.Join(projectRoot, "prototypes")
	dstDir := filepath.Join(projectRoot, "internal", "notebook", "files")

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
			continue
		}
		src := filepath.Join(srcDir, e.Name())
		dst := filepath.Join(dstDir, e.Name())
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if existing, err := os.ReadFile(dst); err == nil && string(existing) == string(data) {
			continue
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
