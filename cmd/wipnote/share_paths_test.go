package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeFakePluginTree creates a minimal directory layout that resolveSharedTreePath
// will accept for the given treeName. Returns the absolute path of the root.
func makeFakePluginTree(t *testing.T, root, treeName string) string {
	t.Helper()
	dir := filepath.Join(root, treeName)
	switch treeName {
	case "plugin":
		mustMkdirAll(t, filepath.Join(dir, ".claude-plugin"))
		mustWriteFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"wipnote"}`)
	case "codex-marketplace":
		mustMkdirAll(t, filepath.Join(dir, ".agents", "plugins"))
		mustWriteFile(t, filepath.Join(dir, ".agents", "plugins", "marketplace.json"), `{"name":"wipnote"}`)
	case "gemini-extension":
		mustMkdirAll(t, dir)
		mustWriteFile(t, filepath.Join(dir, "gemini-extension.json"), `{"name":"wipnote"}`)
	case "antigravity-extension":
		mustMkdirAll(t, dir)
		mustWriteFile(t, filepath.Join(dir, "plugin.json"), `{"name":"wipnote"}`)
	default:
		t.Fatalf("unknown tree %q", treeName)
	}
	return dir
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestResolveSharedTreePath_EnvOverride(t *testing.T) {
	tmp := t.TempDir()
	for _, tree := range []string{"plugin", "codex-marketplace", "gemini-extension", "antigravity-extension"} {
		t.Run(tree, func(t *testing.T) {
			got := makeFakePluginTree(t, tmp, tree)
			envVar, _, _ := sharedTreeMetadata(tree)
			t.Setenv(envVar, got)
			// Force everything else to miss.
			t.Setenv("HOME", filepath.Join(tmp, "nonexistent-home"))
			resolved, err := resolveSharedTreePath(tree)
			if err != nil {
				t.Fatalf("resolveSharedTreePath: %v", err)
			}
			if resolved != got {
				t.Fatalf("resolved %s, want %s", resolved, got)
			}
		})
	}
}

func TestResolveSharedTreePath_EnvOverrideRejectsInvalid(t *testing.T) {
	tmp := t.TempDir()
	bogus := filepath.Join(tmp, "bogus")
	if err := os.MkdirAll(bogus, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("WIPNOTE_PLUGIN_DIR", bogus)
	t.Setenv("HOME", filepath.Join(tmp, "nonexistent-home"))
	if _, err := resolveSharedTreePath("plugin"); err == nil {
		t.Fatalf("expected error for bogus env override, got nil")
	}
}

func TestResolveSharedTreePath_LocalShare(t *testing.T) {
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "home")
	shareWipnote := filepath.Join(fakeHome, ".local", "share", "wipnote")
	want := makeFakePluginTree(t, shareWipnote, "plugin")

	// Clear env override so step 2 wins.
	t.Setenv("WIPNOTE_PLUGIN_DIR", "")
	t.Setenv("HOME", fakeHome)

	resolved, err := resolveSharedTreePath("plugin")
	if err != nil {
		t.Fatalf("resolveSharedTreePath: %v", err)
	}
	if resolved != want {
		t.Fatalf("resolved %s, want %s", resolved, want)
	}
}

func TestResolveSharedTreePath_UnknownTreeName(t *testing.T) {
	if _, err := resolveSharedTreePath("not-a-real-tree"); err == nil {
		t.Fatal("expected error for unknown tree name")
	}
}

func TestIsValidHarnessTree(t *testing.T) {
	tmp := t.TempDir()
	cases := []struct {
		tree  string
		ok    bool
		setup func(string)
	}{
		{"plugin", true, func(dir string) {
			_ = os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755)
			_ = os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte("{}"), 0o644)
		}},
		{"plugin", false, func(dir string) {
			_ = os.MkdirAll(dir, 0o755)
		}},
		{"codex-marketplace", true, func(dir string) {
			_ = os.MkdirAll(filepath.Join(dir, ".agents", "plugins"), 0o755)
			_ = os.WriteFile(filepath.Join(dir, ".agents", "plugins", "marketplace.json"), []byte("{}"), 0o644)
		}},
		{"codex-marketplace", true, func(dir string) {
			// bundled tarball flat layout: marketplace.json at root (no .agents/plugins/)
			_ = os.MkdirAll(dir, 0o755)
			_ = os.WriteFile(filepath.Join(dir, "marketplace.json"), []byte("{}"), 0o644)
		}},
		{"gemini-extension", true, func(dir string) {
			_ = os.MkdirAll(dir, 0o755)
			_ = os.WriteFile(filepath.Join(dir, "gemini-extension.json"), []byte("{}"), 0o644)
		}},
		{"antigravity-extension", true, func(dir string) {
			_ = os.MkdirAll(dir, 0o755)
			_ = os.WriteFile(filepath.Join(dir, "plugin.json"), []byte("{}"), 0o644)
		}},
	}
	for i, c := range cases {
		dir := filepath.Join(tmp, c.tree+"-case-"+itoa(i))
		c.setup(dir)
		got := isValidHarnessTree(dir, c.tree)
		if got != c.ok {
			t.Errorf("case %d (%s): got %v, want %v", i, c.tree, got, c.ok)
		}
	}
}

func TestIsValidHarnessTree_CodexAcceptsBundledFlatLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marketplace.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !isValidHarnessTree(dir, "codex-marketplace") {
		t.Fatal("expected true for codex flat layout, got false")
	}
}

func TestIsValidHarnessTree_CodexAcceptsDevDeepLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agents", "plugins"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agents", "plugins", "marketplace.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !isValidHarnessTree(dir, "codex-marketplace") {
		t.Fatal("expected true for codex deep layout, got false")
	}
}

func TestIsValidHarnessTree_RejectsEmptyTree(t *testing.T) {
	for _, tree := range []string{"plugin", "codex-marketplace", "gemini-extension", "antigravity-extension"} {
		dir := t.TempDir()
		if isValidHarnessTree(dir, tree) {
			t.Errorf("expected false for empty tree %q, got true", tree)
		}
	}
}

func TestSharedTreeRefreshHint(t *testing.T) {
	originalVersion := version
	t.Cleanup(func() { version = originalVersion })

	version = "v0.62.1"
	if got := sharedTreeRefreshHint(); !strings.Contains(got, "wipnote upgrade --version 0.62.1") {
		t.Fatalf("release hint = %q", got)
	}

	for _, v := range []string{"dev", "0.62.1-5-gabc1234"} {
		version = v
		got := sharedTreeRefreshHint()
		if strings.Contains(got, "--version") {
			t.Fatalf("dev hint for %q should not include --version: %q", v, got)
		}
		if !strings.Contains(got, "wipnote upgrade") {
			t.Fatalf("dev hint for %q missing upgrade: %q", v, got)
		}
	}
}

func TestResolveSharedTreePath_MissingTreeMentionsUpgradeRefresh(t *testing.T) {
	t.Setenv("WIPNOTE_ANTIGRAVITY_DIR", "")
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Chdir(t.TempDir())
	originalVersion := version
	version = "0.62.1"
	t.Cleanup(func() { version = originalVersion })

	_, err := resolveSharedTreePath("antigravity-extension")
	if err == nil {
		t.Fatal("expected missing antigravity-extension error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "wipnote upgrade --version 0.62.1") {
		t.Fatalf("error missing upgrade refresh guidance:\n%s", msg)
	}
	if !strings.Contains(msg, "wipnote source checkout") {
		t.Fatalf("error missing source-checkout-only build guidance:\n%s", msg)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	n := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}
