package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/spf13/cobra"
)

func sessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Session query helpers",
	}
	cmd.AddCommand(sessionsResumableCmd())
	return cmd
}

func sessionsResumableCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "resumable",
		Short: "List resumable work items ranked by recent session activity",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runSessionsResumable(jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit structured JSON output")
	return cmd
}

func runSessionsResumable(jsonOut bool) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	db, err := openReadOnlyDB(dir)
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := dbpkg.ListResumableSessions(db, dbpkg.LivenessStalenessThreshold(parentDir(dir)))
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	renderResumableSessionsTable(os.Stdout, rows)
	return nil
}

func renderResumableSessionsTable(w io.Writer, rows []dbpkg.ResumableSession) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORK ITEM\tTYPE\tHARNESS\tLIVE\tLAST ACTIVITY\tSESSION\tBRANCH\tWORKTREE\tTITLE")
	for _, row := range rows {
		live := "no"
		if row.Live {
			live = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.WorkItemID,
			row.Type,
			row.Harness,
			live,
			row.LastActivity,
			truncate(row.LastSessionID, 14),
			row.Branch,
			emptyDash(row.ExecWorktreePath),
			row.Title,
		)
	}
	_ = tw.Flush()
}

func parentDir(wipnoteDir string) string {
	return filepath.Dir(wipnoteDir)
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
