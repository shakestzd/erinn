package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
	"github.com/shakestzd/wipnote/core/workitem"
	"github.com/spf13/cobra"
)

// RelCommittedIn is the relationship a work item declares towards a git commit
// that implemented it. It mirrors implemented_in (item → session) one level
// down: implemented_in names the session that did the work, committed_in names
// the commit the work landed in.
//
// It is deliberately NOT in models.ValidRelationshipTypes. That list gates what
// `wipnote link add` and `wipnote batch` accept as user input, and its members
// all take a work-item ID as the target. A commit target is a 40-char SHA, not
// a work-item ID, so it is reachable only through this command — which resolves
// and verifies the SHA against the repo first. Nothing on the read path filters
// on relationship name, so the edge parses, renders, and round-trips like any
// other.
const RelCommittedIn models.RelationshipType = "committed_in"

// linkCommitCmd returns a cobra.Command for manually linking a git commit to a
// work item. Registered as a subcommand of feature, bug, and spike.
func linkCommitCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "link-commit <id> <sha>",
		Short: "Link a git commit to a " + typeName,
		Long: `Record a git commit on a work item as a committed_in edge in its
canonical HTML. Accepts short or full SHA; the SHA is resolved and verified
against this repository before anything is written. Idempotent: re-linking the
same commit reports the existing edge and writes nothing.`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runLinkCommit(typeName, args[0], args[1])
		},
	}
}

// runLinkCommit resolves the work item and commit, then records the link as a
// committed_in edge on the work item's canonical HTML.
//
// It used to insert a git_commits row into the per-project SQLite file and do
// nothing else. That was already broken before the file went away: no read path
// anywhere populated git_commits from a manual link, so the command printed
// "Linked: …" while the link existed nowhere a user could see it. The edge is
// the fix — it lands in .wipnote/<collection>/<id>.html, which is the canonical
// store, is git-tracked, and is what `wipnote <type> show` prints.
func runLinkCommit(typeName, itemID, sha string) error {
	wipnoteDir, err := findWipnoteDir()
	if err != nil {
		return err
	}

	// Resolve partial IDs.
	resolvedID, err := resolveID(wipnoteDir, itemID)
	if err != nil {
		return err
	}

	// Verify the work item exists on disk.
	nodePath := resolveNodePath(wipnoteDir, resolvedID)
	if nodePath == "" {
		kind := kindFromPrefix(resolvedID)
		return fmt.Errorf("%s %s not found", kind, resolvedID)
	}

	repoRoot := filepath.Dir(wipnoteDir)

	// Resolve the full SHA and extract commit metadata from git.
	fullHash, msg, ts, err := resolveCommitFromRepo(repoRoot, sha)
	if err != nil {
		return fmt.Errorf("resolve commit %s: %w", sha, err)
	}

	// Idempotency check reads the canonical file directly: AddEdge appends
	// unconditionally, so without this a re-run would stack duplicate edges.
	node, err := htmlparse.ParseFile(nodePath)
	if err != nil {
		return fmt.Errorf("read %s: %w", resolvedID, err)
	}
	if hasCommitEdge(node, fullHash) {
		fmt.Printf("Already linked: %s → %s (skipped)\n", truncate(fullHash, 12), resolvedID)
		return nil
	}

	p, err := workitem.Open(wipnoteDir, "claude-code")
	if err != nil {
		return fmt.Errorf("open project: %w", err)
	}
	defer p.Close()

	col := resolveCollection(p, resolvedID)
	if col == nil {
		return fmt.Errorf("cannot determine collection for %s (%s)", resolvedID, typeName)
	}

	edge := models.Edge{
		TargetID:     fullHash,
		Relationship: RelCommittedIn,
		Title:        msg,
		Since:        ts,
	}
	if _, addErr := col.AddEdge(resolvedID, edge); addErr != nil {
		return fmt.Errorf("link commit %s to %s: %w", truncate(fullHash, 10), resolvedID, addErr)
	}

	fmt.Printf("Linked: %s → %s\n  message: %s\n", truncate(fullHash, 12), resolvedID, msg)
	return nil
}

// hasCommitEdge reports whether node already declares a committed_in edge to
// commitHash. This is what makes the command idempotent — AddEdge appends
// unconditionally, so re-running it would otherwise stack duplicate edges.
func hasCommitEdge(node *models.Node, commitHash string) bool {
	if node == nil {
		return false
	}
	for _, e := range node.Edges[string(RelCommittedIn)] {
		if e.TargetID == commitHash {
			return true
		}
	}
	return false
}

// resolveCommitFromRepo resolves a short or full commit SHA in the given repo
// and returns the full hash, subject line, and author timestamp. It uses
// git rev-parse to find the real repo root for worktree-aware resolution.
func resolveCommitFromRepo(repoRoot, sha string) (fullHash, msg string, ts time.Time, err error) {
	// Resolve to the common git dir so worktrees share the same object store.
	gitCommonDir, gitErr := exec.Command("git", "-C", repoRoot, "rev-parse", "--git-common-dir").Output()
	if gitErr == nil {
		commonDir := strings.TrimSpace(string(gitCommonDir))
		if commonDir != "" && commonDir != ".git" {
			// Resolve relative path against repoRoot.
			if !filepath.IsAbs(commonDir) {
				commonDir = filepath.Join(repoRoot, commonDir)
			}
			// Use the worktree root for git commands (git-common-dir parent).
			repoRoot = filepath.Dir(commonDir)
		}
	}

	// Resolve short SHA to full 40-char hash.
	revOut, revErr := exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", sha+"^{commit}").Output()
	if revErr != nil {
		return "", "", time.Time{}, fmt.Errorf("commit %q not found in repo %s", sha, repoRoot)
	}
	fullHash = strings.TrimSpace(string(revOut))

	// Extract subject line and author ISO timestamp.
	logOut, logErr := exec.Command("git", "-C", repoRoot,
		"log", "-1", "--format=%s|%aI", fullHash).Output()
	if logErr != nil {
		return "", "", time.Time{}, fmt.Errorf("git log for %s: %w", fullHash, logErr)
	}

	parts := strings.SplitN(strings.TrimSpace(string(logOut)), "|", 2)
	msg = parts[0]
	if len(parts) == 2 {
		ts, _ = time.Parse(time.RFC3339, parts[1])
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	return fullHash, msg, ts, nil
}
