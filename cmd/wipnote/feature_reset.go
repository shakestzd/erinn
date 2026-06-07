package main

import (
	"fmt"
	"os"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

func featureResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reset <feature-id>",
		Short: "Reset an in-progress feature back to todo",
		Long: `Reset an in-progress feature by setting its status back to 'todo'.
This is a non-destructive operation — all history, steps, edges, and description
are preserved. The agent assignment is cleared.

Errors if the feature is not currently in-progress.

Example:
  wipnote feature reset feat-a1b2c3d4`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			title, err := executeReset("feature", args[0])
			if err != nil {
				return err
			}
			fmt.Printf("Reset: %s  %s\n", args[0], title)
			return nil
		},
	}
}

// executeReset sets an in-progress work item back to todo.
// Returns the item title on success.
func executeReset(typeName, id string) (string, error) {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return "", err
	}

	id, err = resolveID(wipnoteDir, id)
	if err != nil {
		return "", err
	}

	p, err := workitem.Open(wipnoteDir, "claude-code")
	if err != nil {
		return "", fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := collectionFor(p, typeName)

	node, err := col.Get(id)
	if err != nil {
		return "", fmt.Errorf("get %s %s: %w", typeName, id, err)
	}

	if node.Status != models.StatusInProgress {
		return "", fmt.Errorf("%s is not in-progress (status: %s) — nothing to reset", id, node.Status)
	}

	if err := col.Edit(id).SetStatus(string(models.StatusTodo)).SetAgent("").Save(); err != nil {
		return "", fmt.Errorf("reset %s %s: %w", typeName, id, err)
	}

	if p.DB != nil {
		_ = dbpkg.UpdateFeatureStatus(p.DB, id, string(models.StatusTodo))
	}

	// Auto-commit the reset HTML so the state transition is durable
	// (feat-712f9194 / roborev #1678). Non-fatal — failures log to stderr.
	if shouldAutocommitWorkitemArtifact(typeName) {
		if commitErr := commitWipnoteArtifact(wipnoteDir, typeName, id, "reset"); commitErr != nil {
			fmt.Fprintf(os.Stderr, "autocommit warning: %v\n", commitErr)
		}
	}

	return node.Title, nil
}
