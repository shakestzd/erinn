package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCIInit_CreatesWorkflow(t *testing.T) {
	dir := t.TempDir()

	goDir := filepath.Join(dir, "packages", "go")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goDir, "go.mod"), []byte("module test\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Point the flag at our temp dir so runCIInit resolves correctly.
	origFlag := projectDirFlag
	projectDirFlag = dir
	t.Cleanup(func() { projectDirFlag = origFlag })

	cmd := ciInitCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	workflowPath := filepath.Join(dir, ".github", "workflows", "ci.yml")
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("expected ci.yml to exist: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "packages/go/go.mod") {
		t.Errorf("expected 'packages/go/go.mod' in workflow, got:\n%s", content)
	}
	if !strings.Contains(content, "go build ./...") {
		t.Errorf("expected 'go build ./...' in workflow, got:\n%s", content)
	}
	if !strings.Contains(content, "go vet ./...") {
		t.Errorf("expected 'go vet ./...' in workflow, got:\n%s", content)
	}
	if !strings.Contains(content, "go test ./...") {
		t.Errorf("expected 'go test ./...' in workflow, got:\n%s", content)
	}
}

func TestCIInit_DoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()

	goDir := filepath.Join(dir, "packages", "go")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goDir, "go.mod"), []byte("module test\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-create ci.yml with sentinel content.
	workflowDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("# sentinel\n")
	workflowPath := filepath.Join(workflowDir, "ci.yml")
	if err := os.WriteFile(workflowPath, sentinel, 0o644); err != nil {
		t.Fatal(err)
	}

	origFlag := projectDirFlag
	projectDirFlag = dir
	t.Cleanup(func() { projectDirFlag = origFlag })

	cmd := ciInitCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(sentinel) {
		t.Errorf("existing ci.yml was overwritten; got:\n%s", data)
	}
}

func TestCIInit_NoGoMod_ReturnsError(t *testing.T) {
	dir := t.TempDir()

	origFlag := projectDirFlag
	projectDirFlag = dir
	t.Cleanup(func() { projectDirFlag = origFlag })

	cmd := ciInitCmd()
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected error when no go.mod found")
	}
	if !strings.Contains(err.Error(), "no go.mod found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestDetectGoDir_NestedPackage(t *testing.T) {
	dir := t.TempDir()

	goDir := filepath.Join(dir, "packages", "go")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goDir, "go.mod"), []byte("module test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := detectGoDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "packages/go" {
		t.Errorf("got %q, want %q", got, "packages/go")
	}
}

func TestDetectGoDir_RootLevel(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := detectGoDir(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "." {
		t.Errorf("got %q, want %q", got, ".")
	}
}

func TestGenerateGoWorkflow_ContainsDir(t *testing.T) {
	workflow := generateGoWorkflow("packages/go")

	checks := []string{
		"packages/go/go.mod",
		"packages/go/go.sum",
		"cd packages/go && go build ./...",
		"cd packages/go && go vet ./...",
		"cd packages/go && go test ./...",
	}
	for _, want := range checks {
		if !strings.Contains(workflow, want) {
			t.Errorf("expected %q in workflow output", want)
		}
	}
}
