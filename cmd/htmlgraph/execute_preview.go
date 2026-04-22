package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shakestzd/htmlgraph/internal/htmlparse"
	"github.com/shakestzd/htmlgraph/internal/models"
	"github.com/spf13/cobra"
)

// executePreviewCmd returns the cobra command for execute-preview.
func executePreviewCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "execute-preview <trk-id>",
		Short: "Assemble one-call execution context for a track",
		Long: `Aggregate track metadata, linked features, bugs, plans, and git state
into a single JSON envelope. Designed for orchestrators that need execution
context without making many individual CLI calls.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runExecutePreview(args[0], format)
		},
	}
	cmd.Flags().StringVar(&format, "format", "json", "Output format: json or text")
	return cmd
}

// executePreviewEnvelope is the top-level JSON output for execute-preview.
type executePreviewEnvelope struct {
	Track    *executePreviewTrack    `json:"track"`
	Features []executePreviewItem    `json:"features"`
	Bugs     []executePreviewItem    `json:"bugs"`
	Plans    []executePreviewPlan    `json:"plans"`
	Git      executePreviewGit       `json:"git"`
}

// executePreviewTrack holds core track metadata.
type executePreviewTrack struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Description string `json:"description,omitempty"`
	Priority    string `json:"priority,omitempty"`
}

// executePreviewItem holds a condensed view of a linked feature or bug.
type executePreviewItem struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority,omitempty"`
}

// executePreviewPlan holds a condensed plan with its slice count.
type executePreviewPlan struct {
	ID     string               `json:"id"`
	Title  string               `json:"title"`
	Status string               `json:"status"`
	Slices []executePreviewSlice `json:"slices,omitempty"`
}

// executePreviewSlice is a minimal slice summary (step description).
type executePreviewSlice struct {
	Description string `json:"description"`
	Completed   bool   `json:"completed"`
}

// executePreviewGit holds the git state for the track's worktree.
type executePreviewGit struct {
	Branch             string `json:"branch"`
	CommitsAheadMain   int    `json:"commits_ahead_main"`
	CommitsBehindMain  int    `json:"commits_behind_main"`
	WorktreePath       string `json:"worktree_path,omitempty"`
	HeadSHA            string `json:"head_sha"`
}

// runExecutePreview is the testable core that assembles the execution preview.
func runExecutePreview(trkID, format string) error {
	dir, err := findHtmlgraphDir()
	if err != nil {
		return err
	}

	// Resolve partial/full track ID.
	resolved, err := resolveID(dir, trkID)
	if err != nil {
		return err
	}

	// Load the track node.
	path := filepath.Join(dir, "tracks", resolved+".html")
	trackNode, err := htmlparse.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parse track %s: %w", resolved, err)
	}

	// Build track section.
	track := &executePreviewTrack{
		ID:          trackNode.ID,
		Title:       trackNode.Title,
		Status:      string(trackNode.Status),
		Description: trackNode.Content,
		Priority:    string(trackNode.Priority),
	}

	// Collect linked features, bugs, and plans.
	features := collectItems(loadLinkedByType(dir, "features", resolved))
	bugs := collectItems(loadLinkedByType(dir, "bugs", resolved))
	plans := collectPlans(loadLinkedByType(dir, "plans", resolved))

	// Gather git state.
	gitInfo := gatherGitState(resolved, dir)

	env := &executePreviewEnvelope{
		Track:    track,
		Features: features,
		Bugs:     bugs,
		Plans:    plans,
		Git:      gitInfo,
	}

	if format == "text" {
		return printExecutePreviewText(env)
	}

	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal execute-preview: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

// collectItems converts a slice of models.Node into executePreviewItem records.
func collectItems(nodes []*models.Node) []executePreviewItem {
	items := make([]executePreviewItem, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, executePreviewItem{
			ID:       n.ID,
			Title:    n.Title,
			Status:   string(n.Status),
			Priority: string(n.Priority),
		})
	}
	return items
}

// collectPlans converts plan nodes into executePreviewPlan records,
// mapping Steps to Slices for plan nodes.
func collectPlans(nodes []*models.Node) []executePreviewPlan {
	plans := make([]executePreviewPlan, 0, len(nodes))
	for _, n := range nodes {
		slices := make([]executePreviewSlice, 0, len(n.Steps))
		for _, s := range n.Steps {
			slices = append(slices, executePreviewSlice{
				Description: s.Description,
				Completed:   s.Completed,
			})
		}
		plans = append(plans, executePreviewPlan{
			ID:     n.ID,
			Title:  n.Title,
			Status: string(n.Status),
			Slices: slices,
		})
	}
	return plans
}

// gatherGitState collects branch, commits ahead/behind main, worktree path, and HEAD SHA.
// Non-fatal: missing git information results in zero values.
func gatherGitState(trackID, htmlgraphDir string) executePreviewGit {
	var info executePreviewGit

	// Current branch.
	branch, err := runGitIn("", "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil {
		info.Branch = strings.TrimSpace(branch)
	}

	// HEAD SHA.
	sha, err := runGitIn("", "rev-parse", "HEAD")
	if err == nil {
		info.HeadSHA = strings.TrimSpace(sha)
	}

	// Commits ahead/behind main for the track branch.
	trackBranch := trackID
	aheadBehind, err := runGitIn("", "rev-list", "--left-right", "--count", "main..."+trackBranch)
	if err == nil {
		parts := strings.Fields(strings.TrimSpace(aheadBehind))
		if len(parts) == 2 {
			behind, _ := strconv.Atoi(parts[0])
			ahead, _ := strconv.Atoi(parts[1])
			info.CommitsBehindMain = behind
			info.CommitsAheadMain = ahead
		}
	}

	// Worktree path — check the canonical worktree location.
	repoRoot := filepath.Dir(htmlgraphDir)
	worktreePath := filepath.Join(repoRoot, ".claude", "worktrees", trackID)
	info.WorktreePath = worktreePath

	return info
}

// runGitIn runs git with args in the given working directory (empty = inherit).
func runGitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// printExecutePreviewText renders a human-readable summary of the envelope.
func printExecutePreviewText(env *executePreviewEnvelope) error {
	sep := strings.Repeat("─", 60)
	fmt.Println(sep)
	if env.Track != nil {
		fmt.Printf("  Track: %s  %s  [%s]\n", env.Track.ID, env.Track.Title, env.Track.Status)
	}
	fmt.Println(sep)

	fmt.Printf("\nFeatures (%d):\n", len(env.Features))
	for _, f := range env.Features {
		fmt.Printf("  %-22s  %-11s  %s\n", f.ID, f.Status, truncate(f.Title, 38))
	}

	fmt.Printf("\nBugs (%d):\n", len(env.Bugs))
	for _, b := range env.Bugs {
		fmt.Printf("  %-22s  %-11s  %s\n", b.ID, b.Status, truncate(b.Title, 38))
	}

	fmt.Printf("\nPlans (%d):\n", len(env.Plans))
	for _, p := range env.Plans {
		fmt.Printf("  %-22s  %-11s  %s  (%d slices)\n", p.ID, p.Status, truncate(p.Title, 30), len(p.Slices))
	}

	fmt.Printf("\nGit:\n")
	fmt.Printf("  Branch:  %s\n", env.Git.Branch)
	fmt.Printf("  HEAD:    %s\n", env.Git.HeadSHA)
	fmt.Printf("  Ahead:   %d  Behind: %d\n", env.Git.CommitsAheadMain, env.Git.CommitsBehindMain)
	if env.Git.WorktreePath != "" {
		fmt.Printf("  Worktree: %s\n", env.Git.WorktreePath)
	}

	return nil
}
