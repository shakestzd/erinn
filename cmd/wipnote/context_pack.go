package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	dbpkg "github.com/shakestzd/wipnote/core/db"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/shakestzd/wipnote/internal/blame"
	"github.com/shakestzd/wipnote/plan/planyaml"
	"github.com/spf13/cobra"
)

const contextPackCommitLimit = 8
const contextPackFileLimit = 20

// contextPackCmd returns the cobra command for `wipnote context-pack <id>`.
func contextPackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context-pack <work-item-id>",
		Short: "Emit a markdown briefing for a dispatched agent picking up a work item",
		Long: `Produces a structured GFM briefing covering all context a subagent
needs to pick up a work item: claim command, branch-sync state, description,
code surface, recent commits, and open plan questions.

Accepts partial IDs (e.g. feat-55d3 resolves to feat-55d39535).

Example:
  wipnote context-pack feat-55d39535
  wipnote context-pack feat-55d3`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContextPack(cmd.Context(), args[0])
		},
	}
}

// runContextPack is the main entry point: resolves the ID, loads all data,
// and emits the briefing to stdout.
func runContextPack(ctx context.Context, rawID string) error {
	dir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	fullID, err := workitem.ResolvePartialID(dir, rawID)
	if err != nil {
		return err
	}

	proj, err := workitem.Open(dir, "wipnote-cli")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer proj.Close()

	node, err := resolveNode(proj, fullID)
	if err != nil {
		return err
	}

	// WalkAreas is expensive (git ls-files + blame per file). Call ONCE.
	var trackArea *blame.TrackArea
	if node.TrackID != "" {
		root := filepath.Dir(dir)
		fa := false
		res, walkErr := blame.WalkAreas(ctx, nil, root, blame.WalkOptions{
			ByFile:           false,
			IncludeUntracked: &fa,
		})
		if walkErr == nil {
			for i := range res.ByTrack {
				if res.ByTrack[i].TrackID == node.TrackID {
					trackArea = &res.ByTrack[i]
					break
				}
			}
		}
	}

	commits, err := commitsByTrack(proj, node.TrackID)
	if err != nil {
		commits = nil
	}

	questions, err := openQuestionsForTrack(dir, proj, node.TrackID)
	if err != nil {
		questions = nil
	}

	repoRoot := filepath.Dir(dir)
	branch, _ := gitCurrentBranch()
	ahead, behind, _ := gitAheadBehind(repoRoot)

	fmt.Print(renderContextPack(node, branch, ahead, behind, trackArea, commits, questions))
	return nil
}

// resolveNode loads a node from the appropriate collection based on ID prefix.
func resolveNode(proj *workitem.Project, id string) (*models.Node, error) {
	switch {
	case strings.HasPrefix(id, "feat-"):
		return proj.Features.Collection.Get(id)
	case strings.HasPrefix(id, "bug-"):
		return proj.Bugs.Collection.Get(id)
	case strings.HasPrefix(id, "spk-"):
		return proj.Spikes.Collection.Get(id)
	default:
		return nil, fmt.Errorf("unsupported work item type for ID %q", id)
	}
}

// commitsByTrack collects and deduplicates commits across all features in the
// given track, sorted by timestamp descending, capped at contextPackCommitLimit.
// Uses both database-derived track membership (GetFeatureIDsByTrack) and canonical-HTML
// direct lookup (proj.Features with WithTrackID) to ensure no memberships are missed
// due to stale local caches.
func commitsByTrack(proj *workitem.Project, trackID string) ([]models.GitCommit, error) {
	if trackID == "" {
		return nil, nil
	}

	// Collect feature IDs from two sources: SQLite index and canonical HTML.
	dbIDs, err := dbpkg.GetFeatureIDsByTrack(nil, trackID)
	if err != nil {
		return nil, err
	}

	// Also load directly from canonical HTML to catch any stale-DB misses.
	htmlIDs, err := proj.Features.Collection.List(workitem.WithTrackID(trackID))
	if err != nil {
		return nil, err
	}

	// Merge and dedupe feature IDs.
	idSet := make(map[string]struct{})
	for _, id := range dbIDs {
		idSet[id] = struct{}{}
	}
	for _, node := range htmlIDs {
		idSet[node.ID] = struct{}{}
	}

	// Load commits for each feature.
	seen := make(map[string]bool)
	var all []models.GitCommit
	for fid := range idSet {
		cs, err := dbpkg.GetCommitsByFeature(nil, fid)
		if err != nil {
			continue
		}
		for _, c := range cs {
			if !seen[c.CommitHash] {
				seen[c.CommitHash] = true
				all = append(all, c)
			}
		}
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.After(all[j].Timestamp)
	})
	if len(all) > contextPackCommitLimit {
		all = all[:contextPackCommitLimit]
	}
	return all, nil
}

// unansweredQuestion is a flat representation of a question that lacks an answer.
type unansweredQuestion struct {
	Source string // e.g. "plan pln-xxx" or "slice 2: My Slice"
	Text   string
}

// openQuestionsForTrack loads all plans whose TrackID matches and collects
// unanswered plan-level and slice-level questions.
func openQuestionsForTrack(wipnoteDir string, proj *workitem.Project, trackID string) ([]unansweredQuestion, error) {
	if trackID == "" {
		return nil, nil
	}
	plans, err := proj.Plans.Collection.List(workitem.WithTrackID(trackID))
	if err != nil {
		return nil, err
	}

	var out []unansweredQuestion
	for _, planNode := range plans {
		yamlPath := filepath.Join(wipnoteDir, "plans", planNode.ID+".yaml")
		py, err := planyaml.Load(yamlPath)
		if err != nil {
			continue
		}
		for _, q := range py.Questions {
			if q.Answer == nil {
				out = append(out, unansweredQuestion{
					Source: fmt.Sprintf("plan %s", planNode.ID),
					Text:   q.Text,
				})
			}
		}
		for _, sl := range py.Slices {
			for _, sq := range sl.Questions {
				if sq.Answer == "" {
					out = append(out, unansweredQuestion{
						Source: fmt.Sprintf("slice %d: %s", sl.Num, sl.Title),
						Text:   sq.Text,
					})
				}
			}
		}
	}
	return out, nil
}

// gitAheadBehind returns how many commits HEAD is ahead/behind origin/main.
func gitAheadBehind(repoRoot string) (ahead, behind int, err error) {
	cmd := exec.Command("git", "-C", repoRoot, "rev-list", "--left-right", "--count", "origin/main...HEAD")
	var out bytes.Buffer
	cmd.Stdout = &out
	if runErr := cmd.Run(); runErr != nil {
		return 0, 0, runErr
	}
	parts := strings.Fields(strings.TrimSpace(out.String()))
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output: %q", out.String())
	}
	behind, _ = strconv.Atoi(parts[0])
	ahead, _ = strconv.Atoi(parts[1])
	return ahead, behind, nil
}

// renderContextPack assembles the full briefing as a GFM string.
func renderContextPack(
	node *models.Node,
	branch string,
	ahead, behind int,
	trackArea *blame.TrackArea,
	commits []models.GitCommit,
	questions []unansweredQuestion,
) string {
	var buf bytes.Buffer

	fmt.Fprintf(&buf, "# Context Pack: %s\n\n", node.ID)

	// Section 1: Claim command
	fmt.Fprintln(&buf, "## 1. Claim Command")
	fmt.Fprintf(&buf, "\n```\nwipnote %s start %s\n```\n\n", node.Type, node.ID)

	// Section 2: Branch-sync state
	fmt.Fprintln(&buf, "## 2. Branch-Sync State")
	fmt.Fprintf(&buf, "\n- **Branch:** %s\n", branch)
	fmt.Fprintf(&buf, "- **Ahead origin/main:** %d\n", ahead)
	fmt.Fprintf(&buf, "- **Behind origin/main:** %d\n\n", behind)

	// Section 3: Work-item description + steps
	fmt.Fprintln(&buf, "## 3. Work Item Description")
	fmt.Fprintf(&buf, "\n**Title:** %s\n\n", node.Title)
	if node.Content != "" {
		fmt.Fprintln(&buf, node.Content)
		fmt.Fprintln(&buf)
	}
	if len(node.Steps) > 0 {
		fmt.Fprintf(&buf, "**Steps:**\n\n")
		for _, s := range node.Steps {
			check := " "
			if s.Completed {
				check = "x"
			}
			fmt.Fprintf(&buf, "- [%s] %s\n", check, s.Description)
		}
		fmt.Fprintln(&buf)
	}

	// Section 4: Code-Surface Helpers
	fmt.Fprintln(&buf, "## 4. Code-Surface Helpers")
	if node.TrackID == "" {
		fmt.Fprintln(&buf, "\n(no track attribution)")
	} else {
		fmt.Fprintln(&buf, "\nUse `wipnote blame <file>` to identify file owners.")
		fmt.Fprintf(&buf, "Use `wipnote code-areas --track %s` for the full file list.\n\n", node.TrackID)
	}

	// Section 5: File Paths with Package Qualifiers
	fmt.Fprintln(&buf, "## 5. File Paths with Package Qualifiers")
	if node.TrackID == "" || trackArea == nil {
		fmt.Fprintln(&buf, "\n(no track attribution)")
	} else {
		fmt.Fprintf(&buf, "\nTrack: **%s** (%s)\n\n", trackArea.TrackTitle, trackArea.TrackID)
		fmt.Fprintln(&buf, "| File | Package | Features | Touches |")
		fmt.Fprintln(&buf, "|------|---------|----------|---------|")
		files := trackArea.Files
		truncated := len(files) - contextPackFileLimit
		if truncated > 0 {
			files = files[:contextPackFileLimit]
		}
		for _, f := range files {
			pkg := goPackageQualifier(f.Path)
			pkgCol := pkg
			if pkgCol == "" {
				pkgCol = "—"
			}
			fmt.Fprintf(&buf, "| %s | %s | %d | %d |\n", f.Path, pkgCol, f.Features, f.Touches)
		}
		if truncated > 0 {
			fmt.Fprintf(&buf, "\n… and %d more files in this track (use `wipnote code-areas --track %s` to see all).\n", truncated, trackArea.TrackID)
		}
		fmt.Fprintln(&buf)
	}

	// Section 6: Recent same-track commits
	fmt.Fprintln(&buf, "## 6. Recent Same-Track Commits")
	if node.TrackID == "" {
		fmt.Fprintln(&buf, "\n(no track attribution)")
	} else if len(commits) == 0 {
		fmt.Fprintln(&buf, "\n(no commits yet)")
	} else {
		fmt.Fprintln(&buf, "")
		fmt.Fprintln(&buf, "| Hash | Feature | Timestamp | Message |")
		fmt.Fprintln(&buf, "|------|---------|-----------|---------|")
		for _, c := range commits {
			ts := c.Timestamp.UTC().Format("2006-01-02T15:04Z")
			hash := c.CommitHash
			if len(hash) > 8 {
				hash = hash[:8]
			}
			msg := strings.ReplaceAll(c.Message, "|", "\\|")
			if len(msg) > 72 {
				msg = msg[:72] + "…"
			}
			fmt.Fprintf(&buf, "| %s | %s | %s | %s |\n", hash, c.FeatureID, ts, msg)
		}
		fmt.Fprintln(&buf)
	}

	// Section 7: Open plan-slice questions
	fmt.Fprintln(&buf, "## 7. Open Plan-Slice Questions")
	if node.TrackID == "" {
		fmt.Fprintln(&buf, "\n(no track attribution)")
	} else if len(questions) == 0 {
		fmt.Fprintln(&buf, "\n(none)")
	} else {
		fmt.Fprintln(&buf, "")
		for _, q := range questions {
			fmt.Fprintf(&buf, "- **[%s]** %s\n", q.Source, q.Text)
		}
		fmt.Fprintln(&buf)
	}

	return buf.String()
}

// goPackageQualifier returns "package <dir>" for .go files; empty string otherwise.
// The package name is inferred from the immediate parent directory, matching Go
// convention (internal/foo/bar.go → "package foo"). Callers should render ""
// as "—" in table columns.
func goPackageQualifier(path string) string {
	if !strings.HasSuffix(path, ".go") {
		return ""
	}
	dir := filepath.Dir(path)
	if dir == "." || dir == "" {
		return "package main"
	}
	return "package " + filepath.Base(dir)
}
