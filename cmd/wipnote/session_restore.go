package main

import (
	"fmt"
	"path/filepath"

	"github.com/shakestzd/wipnote/observe/otel/retention"
	"github.com/spf13/cobra"
)

// sessionRestoreCmd returns a cobra.Command that extracts an archived session
// from .wipnote/archive/ back into .wipnote/sessions/ so the indexer can
// pick it up on next replay.
func sessionRestoreCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restore <session-id>",
		Short: "Restore an archived session for re-indexing",
		Long: `Extracts a previously-archived session (.wipnote/archive/<yyyy-mm>/<sid>.tar.gz)
back into .wipnote/sessions/<sid>/ so the NDJSON indexer picks it up on
its next replay cycle. The session must have been archived by the retention
job (wipnote serve runs this automatically every 24h).`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runSessionRestore(args[0])
		},
	}
}

func runSessionRestore(sessionID string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	wipnoteDir := filepath.Clean(dir)

	if err := retention.ExtractArchive(wipnoteDir, sessionID); err != nil {
		return fmt.Errorf("restore session %s: %w", sessionID, err)
	}

	fmt.Printf("Restored session %s to .wipnote/sessions/%s/\n", sessionID, sessionID)
	fmt.Println("The indexer will pick up events.ndjson on its next replay cycle.")
	return nil
}
