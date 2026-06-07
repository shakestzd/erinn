package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// executePreview is the JSON envelope returned by `wipnote execute-preview`.
// It aggregates everything an orchestrator needs to start dispatching work on a
// track: track metadata, linked work items grouped by kind, and current git state.
type executePreview struct {
	Track    *models.Node    `json:"track"`
	Features []*models.Node  `json:"features,omitempty"`
	Bugs     []*models.Node  `json:"bugs,omitempty"`
	Plans    []*models.Node  `json:"plans,omitempty"`
	Spikes   []*models.Node  `json:"spikes,omitempty"`
	Git      executeGitState `json:"git"`
}

type executeGitState struct {
	Branch            string `json:"branch"`
	HeadSHA           string `json:"head_sha"`
	CommitsAheadMain  int    `json:"commits_ahead_main"`
	CommitsBehindMain int    `json:"commits_behind_main"`
	WorktreePath      string `json:"worktree_path"`
}

func executePreviewCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "execute-preview <trk-id>",
		Short: "Return everything /wipnote:execute needs to start dispatching — one call",
		Long: "Aggregates track metadata, linked features/bugs/plans, and current git state " +
			"into a single structured payload. Collapses the ~10-call discovery sequence " +
			"that orchestrators previously needed before first dispatch.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runExecutePreview(args[0], format)
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format: json or text")
	return cmd
}

func runExecutePreview(id, format string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}
	resolved, err := resolveID(dir, id)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resolved, "trk-") {
		return fmt.Errorf("execute-preview: expected a track id, got %q", resolved)
	}

	preview, err := buildExecutePreview(dir, resolved)
	if err != nil {
		return err
	}

	switch format {
	case "json":
		data, err := json.MarshalIndent(preview, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json: %w", err)
		}
		fmt.Println(string(data))
	default:
		printExecutePreviewText(preview)
	}
	return nil
}

func buildExecutePreview(dir, trackID string) (*executePreview, error) {
	trackPath := resolveTrackPath(dir, trackID)
	if trackPath == "" {
		return nil, workitem.ErrNotFoundOnDisk("track", trackID)
	}
	trackNode, err := htmlparse.ParseFile(trackPath)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", trackPath, err)
	}

	preview := &executePreview{Track: trackNode}
	seen := make(map[string]bool)

	// 1. Direct edges from the track — features, bugs, spikes, plans.
	for _, edges := range trackNode.Edges {
		for _, edge := range edges {
			addLinkedNode(dir, edge.TargetID, preview, seen)
		}
	}

	// 2. Indirect plans — plans commonly link back to a feature or track via
	//    data-feature-id, or a feature's planned_in edges point to a plan.
	//    Walk linked features' planned_in targets and scan plans/ for any
	//    plan whose data-feature-id matches the track or a linked feature.
	for _, feat := range preview.Features {
		for _, edges := range feat.Edges {
			for _, edge := range edges {
				if strings.HasPrefix(edge.TargetID, "plan-") {
					addLinkedNode(dir, edge.TargetID, preview, seen)
				}
			}
		}
	}
	discoverPlansByFeatureID(dir, trackID, preview, seen)
	for _, feat := range preview.Features {
		discoverPlansByFeatureID(dir, feat.ID, preview, seen)
	}

	sort.Slice(preview.Features, func(i, j int) bool { return preview.Features[i].ID < preview.Features[j].ID })
	sort.Slice(preview.Bugs, func(i, j int) bool { return preview.Bugs[i].ID < preview.Bugs[j].ID })
	sort.Slice(preview.Plans, func(i, j int) bool { return preview.Plans[i].ID < preview.Plans[j].ID })
	sort.Slice(preview.Spikes, func(i, j int) bool { return preview.Spikes[i].ID < preview.Spikes[j].ID })

	preview.Git = currentGitState(dir)
	return preview, nil
}

// resolveTrackPath mirrors runTrackShowWithFormat's track-path lookup: flat
// tracks/<id>.html first, then the directory-backed tracks/<id>/index.html
// fallback. resolveNodePath only handles the flat form, so execute-preview
// needs its own resolver to avoid regressing directory-backed tracks.
func resolveTrackPath(wipnoteDir, id string) string {
	flat := filepath.Join(wipnoteDir, "tracks", id+".html")
	if _, err := os.Stat(flat); err == nil {
		return flat
	}
	indexed := filepath.Join(wipnoteDir, "tracks", id, "index.html")
	if _, err := os.Stat(indexed); err == nil {
		return indexed
	}
	return ""
}

// addLinkedNode resolves a node path, parses the HTML, and appends it to the
// correct preview bucket based on the canonical id prefix. Spikes use spk-
// (matching internal/models/enums.go and history.go). Idempotent via seen.
func addLinkedNode(wipnoteDir, id string, p *executePreview, seen map[string]bool) {
	if id == "" || seen[id] {
		return
	}
	seen[id] = true
	path := resolveNodePath(wipnoteDir, id)
	if path == "" {
		if strings.HasPrefix(id, "trk-") {
			path = resolveTrackPath(wipnoteDir, id)
		}
		if path == "" {
			return
		}
	}
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		return
	}
	switch {
	case strings.HasPrefix(id, "feat-"):
		p.Features = append(p.Features, node)
	case strings.HasPrefix(id, "bug-"):
		p.Bugs = append(p.Bugs, node)
	case strings.HasPrefix(id, "plan-"):
		p.Plans = append(p.Plans, node)
	case strings.HasPrefix(id, "spk-"):
		p.Spikes = append(p.Spikes, node)
	}
}

// discoverPlansByFeatureID scans .wipnote/plans/*.html for any plan whose
// data-feature-id attribute matches sourceID, and adds matching plans to the
// preview. Mirrors findExistingPlanForSource in plan_cmds.go but collects
// all hits rather than returning the first.
func discoverPlansByFeatureID(wipnoteDir, sourceID string, p *executePreview, seen map[string]bool) {
	plansDir := filepath.Join(wipnoteDir, "plans")
	entries, err := os.ReadDir(plansDir)
	if err != nil {
		return
	}
	needle := []byte(fmt.Sprintf(`data-feature-id="%s"`, sourceID))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		planID := strings.TrimSuffix(e.Name(), ".html")
		if seen[planID] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(plansDir, e.Name()))
		if err != nil {
			continue
		}
		if !bytes.Contains(data, needle) {
			continue
		}
		addLinkedNode(wipnoteDir, planID, p, seen)
	}
}

// currentGitState resolves git state for the caller's current working directory
// when it belongs to the same repository as the wipnote project. If the
// caller is inside a nested/submodule repo (different git-common-dir), probes
// run against the wipnote project root instead so the preview never reports
// branch/ahead/behind for an unrelated repository. Mirrors the same guard as
// `wipnote history` (see resolveHistoryRoot in history.go).
func currentGitState(wipnoteDir string) executeGitState {
	repoRoot := resolveGitProbeRoot(wipnoteDir)
	state := executeGitState{WorktreePath: repoRoot}

	if branch, err := gitOutputIn(repoRoot, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		state.Branch = branch
	}
	if sha, err := gitOutputIn(repoRoot, "rev-parse", "HEAD"); err == nil {
		state.HeadSHA = sha
	}
	// --left-right main...HEAD with --count emits "<behind>\t<ahead>".
	if counts, err := gitOutputIn(repoRoot, "rev-list", "--left-right", "--count", "main...HEAD"); err == nil {
		parts := strings.Fields(counts)
		if len(parts) == 2 {
			if behind, err := strconv.Atoi(parts[0]); err == nil {
				state.CommitsBehindMain = behind
			}
			if ahead, err := strconv.Atoi(parts[1]); err == nil {
				state.CommitsAheadMain = ahead
			}
		}
	}
	return state
}

// resolveGitProbeRoot picks the repo root for git probes. Prefers the caller's
// cwd when it shares a git-common-dir with the wipnote project (linked
// worktree — branch-local state is correct). Otherwise falls back to the
// project owner so nested/submodule CWDs don't leak unrelated repo state.
// Reuses resolveHistoryRoot which already implements this check.
//
// On error from resolveHistoryRoot (permission error, missing git, etc.) we
// silently fall back to the project owner rather than aborting — execute-
// preview must stay non-fatal — but emit a stderr diagnostic so confused
// ahead/behind counts have a visible trail for debugging.
func resolveGitProbeRoot(wipnoteDir string) string {
	owner := filepath.Dir(wipnoteDir)
	root, err := resolveHistoryRoot(owner)
	if err != nil {
		fmt.Fprintf(os.Stderr, "execute-preview: resolveHistoryRoot(%s) failed, falling back to project owner: %v\n", owner, err)
		return owner
	}
	return root
}

// gitOutputIn runs a git sub-command with cwd set to repoRoot, returning
// trimmed stdout. Distinct from gitOutput in review.go (which runs in the
// current working directory) because execute-preview needs to resolve paths
// relative to the discovered wipnote project root, not the caller's cwd.
func gitOutputIn(repoRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func printExecutePreviewText(p *executePreview) {
	fmt.Printf("Track: %s  %s  [%s]\n", p.Track.ID, p.Track.Title, p.Track.Status)
	fmt.Printf("Git:   branch=%s  ahead=%d  behind=%d  head=%s\n",
		p.Git.Branch, p.Git.CommitsAheadMain, p.Git.CommitsBehindMain, firstN(p.Git.HeadSHA, 8))
	printNodeGroup("Features", p.Features)
	printNodeGroup("Bugs", p.Bugs)
	printNodeGroup("Plans", p.Plans)
	printNodeGroup("Spikes", p.Spikes)
}

func printNodeGroup(label string, nodes []*models.Node) {
	if len(nodes) == 0 {
		return
	}
	fmt.Printf("\n%s (%d):\n", label, len(nodes))
	for _, n := range nodes {
		fmt.Printf("  %-20s  %-12s  %s\n", n.ID, n.Status, n.Title)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
