package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/storage"
	"github.com/spf13/cobra"
)

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize a new HtmlGraph project in the current directory",
		Long: `Creates the .htmlgraph/ directory structure, initializes the SQLite
database, and writes default configuration files.

Safe to run on an existing project — only missing pieces are created.`,
		RunE: runInit,
	}
}

func runInit(_ *cobra.Command, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	graphDir := filepath.Join(cwd, ".htmlgraph")

	if err := createSubdirs(graphDir); err != nil {
		return err
	}

	dbPath, err := storage.CanonicalDBPath(cwd)
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}
	if err := initDatabase(dbPath); err != nil {
		return err
	}

	if err := writeRefsJSON(graphDir); err != nil {
		return err
	}

	if err := writeStylesCSS(graphDir); err != nil {
		return err
	}

	fmt.Printf("Initialized HtmlGraph in %s\n", graphDir)
	fmt.Println()
	fmt.Println("  .htmlgraph/features/")
	fmt.Println("  .htmlgraph/bugs/")
	fmt.Println("  .htmlgraph/spikes/")
	fmt.Println("  .htmlgraph/tracks/")
	fmt.Println("  .htmlgraph/sessions/")
	fmt.Printf("  %s\n", dbPath)
	fmt.Println("  .htmlgraph/refs.json")
	fmt.Println("  .htmlgraph/styles.css")
	fmt.Println()
	fmt.Println("Run 'htmlgraph status' to verify.")
	return nil
}

var subdirs = []string{"features", "bugs", "spikes", "tracks", "sessions"}

func createSubdirs(graphDir string) error {
	for _, sub := range subdirs {
		path := filepath.Join(graphDir, sub)
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create directory %s: %w", path, err)
		}
	}
	return nil
}

func initDatabase(dbPath string) error {
	conn, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("initialize database: %w", err)
	}
	return conn.Close()
}

func writeRefsJSON(graphDir string) error {
	return writeFileIfAbsent(filepath.Join(graphDir, "refs.json"), []byte("{}\n"))
}

func writeStylesCSS(graphDir string) error {
	return writeFileIfAbsent(filepath.Join(graphDir, "styles.css"), defaultStylesCSS)
}

// writeFileIfAbsent writes content to path only when the file does not yet exist.
func writeFileIfAbsent(path string, content []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil // already exists — leave it alone
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// defaultStylesCSS is a minimal stylesheet for HtmlGraph HTML nodes.
var defaultStylesCSS = []byte(`/**
 * HtmlGraph Default Stylesheet
 *
 * Provides sensible defaults for HtmlGraph nodes.
 * Customize as needed for your project.
 */

:root {
    --color-primary: #2563eb;
    --color-success: #16a34a;
    --color-warning: #d97706;
    --color-danger:  #dc2626;

    --status-todo:        #6b7280;
    --status-in-progress: #2563eb;
    --status-blocked:     #dc2626;
    --status-done:        #16a34a;

    --priority-low:      #9ca3af;
    --priority-medium:   #3b82f6;
    --priority-high:     #f59e0b;
    --priority-critical: #dc2626;

    --color-bg:           #ffffff;
    --color-bg-secondary: #f9fafb;
    --color-border:       #e5e7eb;
    --color-text:         #1f2937;
    --color-text-secondary: #6b7280;

    --space-xs: 0.25rem;
    --space-sm: 0.5rem;
    --space-md: 1rem;
    --space-lg: 1.5rem;

    --font-sans: system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
    --font-mono: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
}

@media (prefers-color-scheme: dark) {
    :root {
        --color-bg:           #111827;
        --color-bg-secondary: #1f2937;
        --color-border:       #374151;
        --color-text:         #f9fafb;
        --color-text-secondary: #9ca3af;
    }
}

*, *::before, *::after { box-sizing: border-box; }

body {
    font-family: var(--font-sans);
    font-size: 16px;
    line-height: 1.6;
    color: var(--color-text);
    background: var(--color-bg);
    margin: 0;
    padding: var(--space-lg);
    max-width: 800px;
    margin-inline: auto;
}

article {
    background: var(--color-bg);
    border: 1px solid var(--color-border);
    border-radius: 8px;
    padding: var(--space-lg);
    margin-bottom: var(--space-lg);
}

.badge {
    display: inline-flex;
    align-items: center;
    padding: var(--space-xs) var(--space-sm);
    font-size: 0.75rem;
    font-weight: 500;
    border-radius: 9999px;
    text-transform: uppercase;
    letter-spacing: 0.025em;
}

.status-todo        { background: color-mix(in srgb, var(--status-todo) 15%, transparent);        color: var(--status-todo); }
.status-in-progress { background: color-mix(in srgb, var(--status-in-progress) 15%, transparent); color: var(--status-in-progress); }
.status-blocked     { background: color-mix(in srgb, var(--status-blocked) 15%, transparent);     color: var(--status-blocked); }
.status-done        { background: color-mix(in srgb, var(--status-done) 15%, transparent);        color: var(--status-done); }

.priority-low      { background: color-mix(in srgb, var(--priority-low) 15%, transparent);      color: var(--priority-low); }
.priority-medium   { background: color-mix(in srgb, var(--priority-medium) 15%, transparent);   color: var(--priority-medium); }
.priority-high     { background: color-mix(in srgb, var(--priority-high) 15%, transparent);     color: var(--priority-high); }
.priority-critical { background: color-mix(in srgb, var(--priority-critical) 15%, transparent); color: var(--priority-critical); }
`)
