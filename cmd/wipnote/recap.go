package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
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
	cmd.AddCommand(recapListCmd(), recapShowCmd(), recapDeleteCmd())
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
		if rangeSpec != "" || sessionID != "" {
			return collectErr
		}
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
	if indexErr := upsertRecapArtifact(wipnoteDir, repoRoot, recapID); indexErr != nil {
		fmt.Fprintf(os.Stderr, "recap: update read index (non-fatal): %v\n", indexErr)
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

// RunPlanRollupRecap generates a consolidated rollup recap for a finalized plan.
// It resolves the git range anchored at the first commit of the plan's YAML file
// (<first-commit>..HEAD), runs recap.Collect in range mode, renders and writes
// .wipnote/recaps/recap-pln-<planID>.html, commits via commitRecapArtifact, and
// wires a relates_to lineage edge from the recap to the plan.
//
// ALL errors are non-fatal by design: callers wrap this in a non-fatal block.
// The function returns an error only when a step that can reasonably be reported
// fails; it never calls os.Exit. Callers should print returned errors to stderr.
func RunPlanRollupRecap(wipnoteDir, planID string) error {
	recapID := "recap-pln-" + planID
	repoRoot := filepath.Dir(wipnoteDir)

	// Resolve the plan YAML path and find its first commit.
	planYAMLRelPath := filepath.Join(".wipnote", "plans", planID+".yaml")
	planYAMLAbsPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")

	firstSHA, err := planFirstCommitSHA(repoRoot, planYAMLAbsPath)
	if err != nil {
		return fmt.Errorf("cannot resolve first commit for %s (skipping recap): %w", planYAMLRelPath, err)
	}
	if firstSHA == "" {
		return fmt.Errorf("cannot resolve first commit for %s (untracked, skipping recap)", planYAMLRelPath)
	}

	gitRange := firstSHA + "..HEAD"

	db, dbErr := openReadOnlyDB(wipnoteDir)
	if dbErr != nil {
		fmt.Fprintf(os.Stderr, "recap (non-fatal): open read index (proceeding without): %v\n", dbErr)
		db = nil
	}
	if db != nil {
		defer db.Close()
	}

	opts := recap.Options{
		Input:      gitRange,
		ProjectDir: repoRoot,
		Depth:      5,
	}
	data, collectErr := recap.Collect(db, opts)
	if collectErr != nil {
		fmt.Fprintf(os.Stderr, "recap (non-fatal): collect (proceeding with empty): %v\n", collectErr)
		data = &recap.RecapData{
			Outcome: fmt.Sprintf("Plan %s rollup", planID),
			Provenance: recap.Provenance{
				Kind:  recap.InputRange,
				Input: gitRange,
			},
		}
	}

	page := recaptmpl.Build(*data)
	var buf bytes.Buffer
	if renderErr := page.Render(&buf); renderErr != nil {
		return fmt.Errorf("recap render: %w", renderErr)
	}

	recapsDir := filepath.Join(wipnoteDir, "recaps")
	if mkErr := os.MkdirAll(recapsDir, 0o755); mkErr != nil {
		return fmt.Errorf("recap: create recaps dir: %w", mkErr)
	}
	artifactPath := filepath.Join(recapsDir, recapID+".html")
	if writeErr := os.WriteFile(artifactPath, buf.Bytes(), 0o644); writeErr != nil {
		return fmt.Errorf("recap: write artifact: %w", writeErr)
	}

	// Update read index so a running dashboard sees the recap immediately (non-fatal).
	if indexErr := upsertRecapArtifact(wipnoteDir, repoRoot, recapID); indexErr != nil {
		fmt.Fprintf(os.Stderr, "recap (non-fatal): update read index: %v\n", indexErr)
	}

	if commitErr := commitRecapArtifact(wipnoteDir, recapID); commitErr != nil {
		fmt.Fprintf(os.Stderr, "recap (non-fatal): commit artifact: %v\n", commitErr)
	}

	// Wire relates_to lineage edge from recap to plan (non-fatal).
	if edgeErr := addPlanRecapLineageEdge(wipnoteDir, recapID, planID); edgeErr != nil {
		fmt.Fprintf(os.Stderr, "recap (non-fatal): add lineage edge: %v\n", edgeErr)
	}

	// Commit the plan HTML after the lineage edge mutates it (non-fatal).
	// addPlanRecapLineageEdge writes into the plan HTML; without this commit
	// the edge is only on disk and the plan HTML is left dirty in git.
	// commitPlanChange takes the YAML path and derives the HTML path from it.
	planYAMLCommitPath := filepath.Join(wipnoteDir, "plans", planID+".yaml")
	commitMsg := fmt.Sprintf("plan(%s): add recap lineage edge", planID)
	if planCommitErr := commitPlanChange(planYAMLCommitPath, commitMsg); planCommitErr != nil {
		fmt.Fprintf(os.Stderr, "recap (non-fatal): commit plan HTML: %v\n", planCommitErr)
	}

	// Wire a relates_to edge from the plan to its introducing commit SHA so
	// lineage queries can surface the plan's origin commit. firstSHA is the
	// oldest commit that added the plan YAML (resolved via planFirstCommitSHA).
	if addIntroErr := addPlanIntroducingCommitEdge(wipnoteDir, planID, firstSHA); addIntroErr != nil {
		fmt.Fprintf(os.Stderr, "recap (non-fatal): add introducing commit edge: %v\n", addIntroErr)
	}

	shortStart := firstSHA
	if len(shortStart) > 7 {
		shortStart = shortStart[:7]
	}
	fmt.Printf("  ✓ %s.html generated (range: %s..HEAD)\n", recapID, shortStart)
	fmt.Printf("  Dashboard: wipnote serve then open http://127.0.0.1:8080 (or http://127.0.0.1:8088 in devcontainer)\n")
	return nil
}

// planFirstCommitSHA returns the SHA of the first commit that introduced
// planYAMLAbsPath in the repository at repoRoot. Returns "" when the file
// is not tracked. Uses --diff-filter=A --follow to follow renames.
func planFirstCommitSHA(repoRoot, planYAMLAbsPath string) (string, error) {
	out, err := exec.Command(
		"git", "-C", repoRoot,
		"log", "--diff-filter=A", "--follow", "--format=%H", "--", planYAMLAbsPath,
	).Output()
	if err != nil {
		return "", fmt.Errorf("git log --diff-filter=A: %w", err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return "", fmt.Errorf("plan YAML %s has no git history (untracked)", planYAMLAbsPath)
	}
	// git log outputs newest-first; we want the oldest (last line).
	lines := strings.Split(raw, "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

// addPlanRecapLineageEdge wires a relates_to edge from the recap artifact to
// the plan work item so lineage queries surface the recap as a descendant.
func addPlanRecapLineageEdge(wipnoteDir, recapID, planID string) error {
	p, err := workitem.Open(wipnoteDir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	edge := models.Edge{
		TargetID:     recapID,
		Relationship: models.RelRelatesTo,
		Title:        "recap",
		Since:        time.Now().UTC(),
	}
	_, err = p.Plans.AddEdge(planID, edge)
	return err
}

// addPlanIntroducingCommitEdge wires a relates_to edge from the plan HTML
// artifact to its introducing commit SHA. This lets lineage queries surface
// the commit that first added the plan YAML as the plan's origin point.
// introducingSHA is the full commit hash returned by planFirstCommitSHA.
func addPlanIntroducingCommitEdge(wipnoteDir, planID, introducingSHA string) error {
	if introducingSHA == "" {
		return nil
	}
	p, err := workitem.Open(wipnoteDir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	edge := models.Edge{
		TargetID:     introducingSHA,
		Relationship: models.RelRelatesTo,
		Title:        "introducing-commit",
		Since:        time.Now().UTC(),
	}
	_, err = p.Plans.AddEdge(planID, edge)
	return err
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
