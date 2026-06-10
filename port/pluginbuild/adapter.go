package pluginbuild

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// cleanOwnedSubtrees removes entire owned subtrees before emit so that renamed
// or deleted source files do not leave stale output files behind. Only the
// listed subtrees are removed — hand-maintained files at the outDir root (README,
// .gitignore, etc.) and any other unowned directories are left untouched.
//
// Missing subtrees are silently skipped (clean no-op on first run).
func cleanOwnedSubtrees(outDir string, ownedSubtrees []string) error {
	for _, sub := range ownedSubtrees {
		dir := filepath.Join(outDir, sub)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("clean owned subtree %s: %w", dir, err)
		}
	}
	return nil
}

// Adapter emits a target-specific plugin tree from the shared manifest. Each
// target (Claude Code, Codex CLI, …) registers one.
type Adapter interface {
	// Name identifies the target in CLI flags and manifest.targets keys.
	Name() string

	// Emit writes the generated plugin tree under outDir. repoRoot is the
	// absolute path to the repository root — adapters resolve manifest asset
	// sources relative to it.
	Emit(m *Manifest, repoRoot, outDir string) error
}

// Registry holds all known adapters by name.
var registry = map[string]Adapter{}

// Register adds an adapter to the registry. Duplicate registrations for the
// same name panic at init time so conflicts surface immediately.
func Register(a Adapter) {
	if _, dup := registry[a.Name()]; dup {
		panic(fmt.Sprintf("pluginbuild: adapter %q already registered", a.Name()))
	}
	registry[a.Name()] = a
}

// Get returns the adapter for name or an error if none is registered.
func Get(name string) (Adapter, error) {
	a, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown plugin-build target %q (registered: %v)", name, Names())
	}
	return a, nil
}

// Names lists all registered target names in stable order.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	// sort for determinism — stdlib sort avoids importing another dep
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	return names
}

// copyAssetTree copies srcDir into dstDir recursively. Missing sources are
// silently skipped — every target is expected to accept a superset of assets
// and missing ones simply mean that the parent chose not to ship that surface.
//
// When srcDir and dstDir resolve to the same directory (common for the Claude
// target where assetSources live under plugin/ and outDir is plugin/) the copy
// is a no-op: walking a directory while writing over its files truncates them
// to zero bytes.
func copyAssetTree(srcDir, dstDir string) error {
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
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

// samePath reports whether a and b refer to the same filesystem location after
// resolving symlinks and relative components.
func samePath(a, b string) (bool, error) {
	aAbs, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	bAbs, err := filepath.Abs(b)
	if err != nil {
		return false, err
	}
	if aAbs == bAbs {
		return true, nil
	}
	aReal, errA := filepath.EvalSymlinks(aAbs)
	bReal, errB := filepath.EvalSymlinks(bAbs)
	if errA == nil && errB == nil {
		return aReal == bReal, nil
	}
	return false, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// writeJSON marshals v to path with stable two-space indent and a trailing newline.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeJSONTo(f, v)
}
