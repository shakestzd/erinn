package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/internal/recap"
	"github.com/shakestzd/wipnote/recap/recaptmpl"
	"github.com/spf13/cobra"
)

// recapCmd returns the `wipnote recap` cobra command.
func recapCmd() *cobra.Command {
	var rangeSpec string
	var sessionID string

	cmd := &cobra.Command{
		Use:   "recap [id]",
		Short: "Generate a grounded recap artifact for a work item, git range, or session",
		Long: `Generate a self-contained HTML recap artifact and commit it to .wipnote/recaps/.

Input modes:
  recap feat-<id>               work-item recap (idempotent — overwrites in place)
  recap bug-<id>                work-item recap
  recap spk-<id>                spike recap
  recap --range main..HEAD      bare git range (recap-r-<12-char-hash>)
  recap --session sess-<id>     session recap (recap-s-<session-short-id>)

The artifact is committed via git -C <repoRoot> so it lands in the main
repository even when running from inside a linked worktree.

A lineage edge (relates_to) is wired from the recap to the work item when the
input is a work-item id.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRecap(args, rangeSpec, sessionID)
		},
	}
	cmd.Flags().StringVar(&rangeSpec, "range", "", "git revision range (e.g. main..HEAD)")
	cmd.Flags().StringVar(&sessionID, "session", "", "session id (e.g. sess-abc123)")
	return cmd
}

// runRecap resolves the input, collects RecapData, renders the recap HTML,
// writes the artifact, commits it, and wires the lineage edge.
func runRecap(args []string, rangeSpec, sessionID string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	input, recapID, err := resolveRecapInput(args, rangeSpec, sessionID)
	if err != nil {
		return err
	}

	repoRoot := filepath.Dir(wipnoteDir)

	db, dbErr := openReadOnlyDB(wipnoteDir)
	if dbErr != nil {
		// Non-fatal: proceed with an empty DB; the collector degrades gracefully.
		fmt.Fprintf(os.Stderr, "recap: open read index (proceeding without): %v\n", dbErr)
		db = nil
	}
	if db != nil {
		defer db.Close()
	}

	opts := recap.Options{
		Input:      input,
		ProjectDir: repoRoot,
		Depth:      5,
	}
	data, collectErr := recap.Collect(db, opts)
	if collectErr != nil {
		// For work-item inputs, a collect error is non-fatal — render an empty
		// recap so the artifact is still written and committed.
		fmt.Fprintf(os.Stderr, "recap: collect (proceeding with empty): %v\n", collectErr)
		data = &recap.RecapData{
			Outcome: input,
			Provenance: recap.Provenance{
				Kind:  recap.InputKind(resolveInputKindStr(input, rangeSpec, sessionID)),
				Input: input,
			},
		}
	}

	page := recaptmpl.Build(*data)
	var buf bytes.Buffer
	if renderErr := page.Render(&buf); renderErr != nil {
		return fmt.Errorf("recap render: %w", renderErr)
	}

	// Write artifact.
	recapsDir := filepath.Join(wipnoteDir, "recaps")
	if mkErr := os.MkdirAll(recapsDir, 0o755); mkErr != nil {
		return fmt.Errorf("recap: create recaps dir: %w", mkErr)
	}
	artifactPath := filepath.Join(recapsDir, recapID+".html")
	if writeErr := os.WriteFile(artifactPath, buf.Bytes(), 0o644); writeErr != nil {
		return fmt.Errorf("recap: write artifact: %w", writeErr)
	}

	// Commit artifact.
	if commitErr := commitRecapArtifact(wipnoteDir, recapID); commitErr != nil {
		fmt.Fprintf(os.Stderr, "recap: commit artifact: %v\n", commitErr)
	}

	// Wire lineage edge from recap to work item when input is a work-item id.
	if isWorkItemID(input) {
		if edgeErr := addRecapLineageEdge(wipnoteDir, recapID, input); edgeErr != nil {
			// Non-fatal: lineage is a value-add.
			fmt.Fprintf(os.Stderr, "recap: add lineage edge (non-fatal): %v\n", edgeErr)
		}
	}

	fmt.Printf("Recap written: .wipnote/recaps/%s.html\n", recapID)
	return nil
}

// resolveRecapInput determines the input string and recap ID from the three
// possible invocation modes (work-item positional, --range, --session).
func resolveRecapInput(args []string, rangeSpec, sessionID string) (input, recapID string, err error) {
	nModes := 0
	if len(args) == 1 {
		nModes++
	}
	if rangeSpec != "" {
		nModes++
	}
	if sessionID != "" {
		nModes++
	}
	if nModes == 0 {
		return "", "", fmt.Errorf("recap requires one of: a work-item id, --range, or --session")
	}
	if nModes > 1 {
		return "", "", fmt.Errorf("recap: only one of work-item id, --range, or --session may be given")
	}

	switch {
	case len(args) == 1:
		id := args[0]
		if !isWorkItemID(id) {
			return "", "", fmt.Errorf("recap: %q does not look like a work-item id (expected feat-/bug-/spk-); use --range for git ranges", id)
		}
		return id, "recap-" + id, nil

	case rangeSpec != "":
		sum := sha256.Sum256([]byte(rangeSpec))
		hex12 := fmt.Sprintf("%x", sum)[:12]
		return rangeSpec, "recap-r-" + hex12, nil

	default: // sessionID
		short := sessionShortID(sessionID)
		return sessionID, "recap-s-" + short, nil
	}
}

// sessionShortID returns the shortest useful unambiguous session suffix.
// sess-abc123ef → "abc123ef"; already-short ids are returned as-is.
func sessionShortID(id string) string {
	id = strings.TrimPrefix(id, "sess-")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// resolveInputKindStr returns the recap.InputKind string for an input that
// could not be collected (used when building the fallback RecapData).
func resolveInputKindStr(input, rangeSpec, sessionID string) string {
	switch {
	case isWorkItemID(input):
		return string(recap.InputWorkItem)
	case rangeSpec != "":
		return string(recap.InputRange)
	case sessionID != "":
		return string(recap.InputSession)
	default:
		return string(recap.InputRange)
	}
}

// addRecapLineageEdge writes a relates_to edge from the recap HTML artifact
// into the work item's edge list so `wipnote lineage <workItemID>` surfaces
// the recap as a forward descendant.
func addRecapLineageEdge(wipnoteDir, recapID, workItemID string) error {
	p, err := workitem.Open(wipnoteDir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := resolveCollection(p, workItemID)
	if col == nil {
		return fmt.Errorf("cannot resolve collection for %q", workItemID)
	}

	edge := models.Edge{
		TargetID:     recapID,
		Relationship: models.RelRelatesTo,
		Title:        "recap",
		Since:        time.Now().UTC(),
	}
	_, err = col.AddEdge(workItemID, edge)
	return err
}
