package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	dbpkg "github.com/shakestzd/wipnote/internal/db"
	"github.com/shakestzd/wipnote/internal/models"
	"github.com/shakestzd/wipnote/internal/storage"
	"github.com/spf13/cobra"
)

// linkCommitCmd returns a cobra.Command for manually linking a git commit to a
// work item. Registered as a subcommand of feature, bug, and spike.
func linkCommitCmd(typeName string) *cobra.Command {
	return &cobra.Command{
		Use:   "link-commit <id> <sha>",
		Short: "Link a git commit to a " + typeName,
		Long: `Manually insert a git commit into the provenance ledger for a work item.
Accepts short or full SHA. Idempotent: silently skips if the commit is already linked.`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runLinkCommit(typeName, args[0], args[1])
		},
	}
}

// runLinkCommit resolves the work item and commit, then inserts a git_commits row.
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
	if resolveNodePath(wipnoteDir, resolvedID) == "" {
		kind := kindFromPrefix(resolvedID)
		return fmt.Errorf("%s %s not found", kind, resolvedID)
	}

	repoRoot := filepath.Dir(wipnoteDir)

	// Resolve the full SHA and extract commit metadata from git.
	fullHash, msg, ts, err := resolveCommitFromRepo(repoRoot, sha)
	if err != nil {
		return fmt.Errorf("resolve commit %s: %w", sha, err)
	}

	dbPath, err := storage.CanonicalDBPath(repoRoot)
	if err != nil {
		return fmt.Errorf("resolve db path: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	commit := &models.GitCommit{
		CommitHash: fullHash,
		SessionID:  "manual",
		FeatureID:  resolvedID,
		Message:    msg,
		Timestamp:  ts,
	}

	n, insertErr := dbpkg.InsertGitCommitResult(database, commit)
	if insertErr != nil {
		return fmt.Errorf("insert commit %s: %w", truncate(fullHash, 10), insertErr)
	}

	if n == 0 {
		fmt.Printf("Already linked: %s → %s (skipped)\n", truncate(fullHash, 12), resolvedID)
		return nil
	}

	fmt.Printf("Linked: %s → %s\n  message: %s\n", truncate(fullHash, 12), resolvedID, msg)
	return nil
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
