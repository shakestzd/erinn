package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/storage"
	"github.com/spf13/cobra"
)

// recapJSON is the machine-readable shape emitted by `recap list` and
// `recap show`. It mirrors the recaps read-index row plus the on-disk path.
type recapJSON struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Input      string `json:"input"`
	GitRange   string `json:"git_range,omitempty"`
	Grounded   bool   `json:"grounded"`
	Title      string `json:"title"`
	Outcome    string `json:"outcome,omitempty"`
	WorkItemID string `json:"work_item_id,omitempty"`
	Path       string `json:"path"`
	CreatedAt  string `json:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// recapListCmd lists committed recap artifacts from the SQLite read index.
func recapListCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List committed recap artifacts (SQLite-backed)",
		Long: `List recaps recorded in the read index. The index is refreshed from
.wipnote/recaps/*.html before listing so the view is always current.

Headless: --format json emits a machine-readable array; exit code is 0 even
when no recaps exist (an empty array is printed).`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runRecapList(format)
		},
	}
	defaultFmt := "text"
	if !isTerminal() {
		defaultFmt = "json"
	}
	cmd.Flags().StringVar(&format, "format", defaultFmt, "Output format: json or text")
	return cmd
}

// recapShowCmd prints a single recap's metadata by id.
func recapShowCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show one recap artifact's metadata by id",
		Long: `Show metadata for a single recap. Exit code 1 (with a stderr message) when
the id is unknown, so scripts can branch on presence.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runRecapShow(args[0], format)
		},
	}
	defaultFmt := "text"
	if !isTerminal() {
		defaultFmt = "json"
	}
	cmd.Flags().StringVar(&format, "format", defaultFmt, "Output format: json or text")
	return cmd
}

// recapDeleteCmd removes a recap artifact (HTML file + read-index row).
func recapDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a recap artifact (HTML file and read-index row)",
		Long: `Delete the recap HTML under .wipnote/recaps/ and remove its read-index row.
Exit code 1 when the id is unknown. The deletion is not auto-committed — the
HTML file is removed from the working tree; commit the removal yourself.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runRecapDelete(args[0])
		},
	}
	return cmd
}

// openRecapsIndex opens the read index and refreshes the recaps table from the
// canonical HTML files so list/show/delete always operate on current data.
func openRecapsIndex(wipnoteDir string) (*sql.DB, error) {
	projectDir := filepath.Dir(wipnoteDir)
	dbPath, err := storage.CanonicalDBPath(projectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return nil, fmt.Errorf("ensure db dir: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	reindexRecaps(database, wipnoteDir, projectDir, false)
	return database, nil
}

// recapJSONFromRow converts a read-index row into the output shape.
func recapJSONFromRow(r *dbpkg.RecapRow) recapJSON {
	out := recapJSON{
		ID:         r.ID,
		Kind:       r.Kind,
		Input:      r.Input,
		GitRange:   r.GitRange,
		Grounded:   r.Grounded,
		Title:      r.Title,
		Outcome:    r.Outcome,
		WorkItemID: r.WorkItemID,
		Path:       filepath.Join(".wipnote", "recaps", r.ID+".html"),
	}
	if r.CreatedAt != nil {
		out.CreatedAt = r.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	if r.UpdatedAt != nil {
		out.UpdatedAt = r.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
	}
	return out
}

func runRecapList(format string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	database, err := openRecapsIndex(wipnoteDir)
	if err != nil {
		return err
	}
	defer database.Close()

	rows, err := dbpkg.ListRecaps(database)
	if err != nil {
		return err
	}
	out := make([]recapJSON, 0, len(rows))
	for _, r := range rows {
		out = append(out, recapJSONFromRow(r))
	}

	if format == "json" {
		data, mErr := json.MarshalIndent(out, "", "  ")
		if mErr != nil {
			return fmt.Errorf("marshal json: %w", mErr)
		}
		fmt.Println(string(data))
		return nil
	}
	printRecapListText(out)
	return nil
}

func printRecapListText(out []recapJSON) {
	if len(out) == 0 {
		fmt.Println("No recaps found.")
		return
	}
	fmt.Printf("%-30s  %-10s  %-9s  %s\n", "ID", "KIND", "GROUNDED", "TITLE")
	for _, r := range out {
		grounded := "no"
		if r.Grounded {
			grounded = "yes"
		}
		fmt.Printf("%-30s  %-10s  %-9s  %s\n", r.ID, r.Kind, grounded, truncate(r.Title, 40))
	}
	fmt.Printf("\n%d recap(s)\n", len(out))
}

func runRecapShow(id, format string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	database, err := openRecapsIndex(wipnoteDir)
	if err != nil {
		return err
	}
	defer database.Close()

	row, err := dbpkg.GetRecap(database, id)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("recap %q not found", id)
	}
	out := recapJSONFromRow(row)

	if format == "json" {
		data, mErr := json.MarshalIndent(out, "", "  ")
		if mErr != nil {
			return fmt.Errorf("marshal json: %w", mErr)
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("ID:        %s\n", out.ID)
	fmt.Printf("Kind:      %s\n", out.Kind)
	fmt.Printf("Input:     %s\n", out.Input)
	if out.GitRange != "" {
		fmt.Printf("GitRange:  %s\n", out.GitRange)
	}
	fmt.Printf("Grounded:  %t\n", out.Grounded)
	fmt.Printf("Title:     %s\n", out.Title)
	if out.WorkItemID != "" {
		fmt.Printf("WorkItem:  %s\n", out.WorkItemID)
	}
	fmt.Printf("Path:      %s\n", out.Path)
	return nil
}

func runRecapDelete(id string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	database, err := openRecapsIndex(wipnoteDir)
	if err != nil {
		return err
	}
	defer database.Close()

	row, err := dbpkg.GetRecap(database, id)
	if err != nil {
		return err
	}
	artifactPath := filepath.Join(wipnoteDir, "recaps", id+".html")
	_, statErr := os.Stat(artifactPath)
	if row == nil && statErr != nil {
		return fmt.Errorf("recap %q not found", id)
	}

	if statErr == nil {
		if rmErr := os.Remove(artifactPath); rmErr != nil {
			return fmt.Errorf("remove recap artifact: %w", rmErr)
		}
	}
	if delErr := dbpkg.DeleteRecap(database, id); delErr != nil {
		return fmt.Errorf("delete recap row: %w", delErr)
	}
	fmt.Printf("Deleted recap: %s\n", id)
	return nil
}
