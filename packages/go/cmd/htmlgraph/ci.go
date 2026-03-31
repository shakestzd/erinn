package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func ciCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "CI/CD workflow management",
	}
	cmd.AddCommand(ciInitCmd())
	return cmd
}

func ciInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Scaffold GitHub Actions CI workflow",
		Long:  "Generate .github/workflows/ci.yml with quality gates (build, vet, test) for pull requests.",
		RunE:  runCIInit,
	}
}

func runCIInit(_ *cobra.Command, _ []string) error {
	// ci init works on any project, not just HtmlGraph-initialized ones.
	// Prefer --project-dir flag, then fall through to CWD.
	projectDir := projectDirFlag
	if projectDir == "" {
		var err error
		projectDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
	}

	workflowDir := filepath.Join(projectDir, ".github", "workflows")
	workflowPath := filepath.Join(workflowDir, "ci.yml")

	if _, err := os.Stat(workflowPath); err == nil {
		fmt.Fprintf(os.Stderr, "ci.yml already exists at %s\n", workflowPath)
		return nil
	}

	goDir, err := detectGoDir(projectDir)
	if err != nil {
		return err
	}

	workflow := generateGoWorkflow(goDir)

	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", workflowDir, err)
	}

	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", workflowPath, err)
	}

	fmt.Printf("Created %s\n", workflowPath)
	return nil
}

// detectGoDir searches for go.mod under packages/go/ first, then at the
// project root, and returns the directory path relative to projectDir.
func detectGoDir(projectDir string) (string, error) {
	candidates := []string{
		filepath.Join(projectDir, "packages", "go"),
		projectDir,
	}
	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			rel, err := filepath.Rel(projectDir, dir)
			if err != nil {
				return "", fmt.Errorf("resolve go dir: %w", err)
			}
			return rel, nil
		}
	}
	return "", fmt.Errorf("no go.mod found — only Go projects are supported currently")
}

// generateGoWorkflow returns a GitHub Actions CI workflow YAML for a Go
// project located at goDir (relative to the repository root).
func generateGoWorkflow(goDir string) string {
	return fmt.Sprintf(`name: CI

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]

jobs:
  quality-gates:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: %s/go.mod
          cache-dependency-path: %s/go.sum
      - name: Build
        run: cd %s && go build ./...
      - name: Vet
        run: cd %s && go vet ./...
      - name: Test
        run: cd %s && go test ./...
`, goDir, goDir, goDir, goDir, goDir)
}
