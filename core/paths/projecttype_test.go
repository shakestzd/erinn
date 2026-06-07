package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectProjectType_EmptyDir(t *testing.T) {
	if got := DetectProjectType(""); got != ProjectTypeUnknown {
		t.Errorf("empty projectDir: got %q, want %q", got, ProjectTypeUnknown)
	}
}

func TestDetectProjectType_NonExistentDir(t *testing.T) {
	if got := DetectProjectType("/nonexistent/path/that/does/not/exist"); got != ProjectTypeUnknown {
		t.Errorf("nonexistent dir: got %q, want %q", got, ProjectTypeUnknown)
	}
}

func TestDetectProjectType_NoMarkers(t *testing.T) {
	dir := t.TempDir()
	if got := DetectProjectType(dir); got != ProjectTypeUnknown {
		t.Errorf("dir with no markers: got %q, want %q", got, ProjectTypeUnknown)
	}
}

func TestDetectProjectType_Go(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/foo\n")
	if got := DetectProjectType(dir); got != ProjectTypeGo {
		t.Errorf("go.mod: got %q, want %q", got, ProjectTypeGo)
	}
}

func TestDetectProjectType_PythonPyproject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pyproject.toml"), "[project]\nname=\"foo\"\n")
	if got := DetectProjectType(dir); got != ProjectTypePython {
		t.Errorf("pyproject.toml: got %q, want %q", got, ProjectTypePython)
	}
}

func TestDetectProjectType_PythonRequirements(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "requirements.txt"), "pytest>=8.0\n")
	if got := DetectProjectType(dir); got != ProjectTypePython {
		t.Errorf("requirements.txt: got %q, want %q", got, ProjectTypePython)
	}
}

func TestDetectProjectType_Node(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name":"foo"}`)
	if got := DetectProjectType(dir); got != ProjectTypeNode {
		t.Errorf("package.json: got %q, want %q", got, ProjectTypeNode)
	}
}

func TestDetectProjectType_Rust(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "Cargo.toml"), "[package]\nname=\"foo\"\n")
	if got := DetectProjectType(dir); got != ProjectTypeRust {
		t.Errorf("Cargo.toml: got %q, want %q", got, ProjectTypeRust)
	}
}

func TestDetectProjectType_PriorityGoBeatsPython(t *testing.T) {
	// When both manifests exist, the priority order should pick Go first
	// because it appears first in projectTypeMarkers. This documents the
	// existing behavior so a future re-ordering is a deliberate choice.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/mixed\n")
	writeFile(t, filepath.Join(dir, "pyproject.toml"), "[project]\nname=\"mixed\"\n")
	if got := DetectProjectType(dir); got != ProjectTypeGo {
		t.Errorf("go+python: got %q, want %q (priority order)", got, ProjectTypeGo)
	}
}

func TestDetectProjectType_MonorepoPackagesSubdir(t *testing.T) {
	// Marker is in packages/api/go.mod, not at the root.
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "packages", "api")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(pkgDir, "go.mod"), "module example.com/api\n")
	if got := DetectProjectType(dir); got != ProjectTypeGo {
		t.Errorf("monorepo packages/api/go.mod: got %q, want %q", got, ProjectTypeGo)
	}
}

func TestDetectProjectType_MonorepoSrcSubdir(t *testing.T) {
	// Marker is in src/myapp/Cargo.toml, not at the root.
	dir := t.TempDir()
	subDir := filepath.Join(dir, "src", "myapp")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(subDir, "Cargo.toml"), "[package]\nname=\"myapp\"\n")
	if got := DetectProjectType(dir); got != ProjectTypeRust {
		t.Errorf("monorepo src/myapp/Cargo.toml: got %q, want %q", got, ProjectTypeRust)
	}
}

func TestTestCommandFor(t *testing.T) {
	cases := []struct {
		typ  ProjectType
		want string
	}{
		{ProjectTypeGo, "go test ./..."},
		{ProjectTypePython, "uv run pytest"},
		{ProjectTypeNode, "npm test"},
		{ProjectTypeRust, "cargo test"},
		{ProjectTypeUnknown, ""},
		{ProjectType("garbage"), ""},
	}
	for _, c := range cases {
		if got := TestCommandFor(c.typ); got != c.want {
			t.Errorf("TestCommandFor(%q): got %q, want %q", c.typ, got, c.want)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
