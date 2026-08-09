package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shakestzd/wipnote/internal/commitqueue"
)

// commitOutboxPath returns the absolute path to the per-repo commit outbox.
// It lives in wipnote's per-user cache directory — the SAME directory that
// holds the SQLite read-index and the git-mutation lock — NOT inside .wipnote/
// (an outbox inside .wipnote/ would itself need committing: recursion). It is a
// package-level seam so tests can redirect it without touching the real cache.
var commitOutboxPath = func(repoRoot string) (string, error) {
	return projectRuntimeCachePath(repoRoot, "commit-outbox", "commit-outbox.ndjson")
}

// openCommitOutbox resolves the outbox for repoRoot.
func openCommitOutbox(repoRoot string) (*commitqueue.Outbox, error) {
	path, err := commitOutboxPath(repoRoot)
	if err != nil {
		return nil, err
	}
	return commitqueue.NewOutbox(path), nil
}

// recordCommitIntent appends a pending artifact-commit intent to the outbox
// AFTER the canonical .wipnote write has already completed. This is the
// producer half of the outbox model: it never touches git. Callers that want
// the durable alternative to direct autocommit record an intent here; the
// serialized `wipnote commit-queue flush` later drains it.
func recordCommitIntent(repoRoot string, relPaths []string, message, workItemID, action string) error {
	ob, err := openCommitOutbox(repoRoot)
	if err != nil {
		return err
	}
	return ob.AppendCoalescingByRelPath(commitqueue.Intent{
		RepoRoot:   repoRoot,
		RelPaths:   relPaths,
		Message:    message,
		WorkItemID: workItemID,
		Action:     action,
	})
}

// outboxCommitter is the production Committer: it stages and commits the
// intent's artifact paths under the repo-scoped advisory lock via
// runGitMutation, so the serialized flush never collides with any other wipnote
// git writer. It is idempotent — an already-committed artifact yields "nothing
// to commit", which is treated as success so an interrupted flush re-runs
// safely.
func outboxCommitter(i commitqueue.Intent) error {
	if err := i.Validate(); err != nil {
		return err
	}
	if !isGitRepo(i.RepoRoot) {
		// Non-git project: nothing to commit. Treat as success so the intent
		// drains rather than poisoning the queue.
		return nil
	}
	absPaths := make([]string, 0, len(i.RelPaths))
	for _, rel := range i.RelPaths {
		absPaths = append(absPaths, filepath.Join(i.RepoRoot, rel))
	}

	addArgs := append([]string{"add", "--"}, absPaths...)
	commitArgs := append([]string{"commit", "-m", i.Message, "--"}, absPaths...)

	// Run add+commit under ONE advisory-lock acquisition so no other wipnote git
	// mutation can interleave between staging and committing this artifact
	// (roborev finding on feat-76504033). On error, out is the failing command's
	// combined output; an interrupted flush that re-runs an already-committed
	// intent surfaces "nothing to commit" here and is treated as success.
	out, err := runGitMutationBatch(i.RepoRoot, addArgs, commitArgs)
	if err != nil {
		if isNothingToCommit(string(out)) {
			return nil // idempotent no-op (e.g. artifact already committed)
		}
		return fmt.Errorf("commit-queue: git add+commit: %s: %w", string(out), err)
	}
	return nil
}

func isNothingToCommit(out string) bool {
	return strings.Contains(out, "nothing to commit") || strings.Contains(out, "no changes added")
}

// commitQueueCmd is the "wipnote commit-queue" command group. The MVP exposes
// flush (drain) and status (depths) subcommands. A daemon/hook driver is an
// explicit follow-up, not part of this MVP.
func commitQueueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "commit-queue",
		Short: "Serialized outbox for wipnote artifact commits",
		Long: `Durable, FIFO outbox of pending wipnote artifact commits.

Canonical .wipnote writes complete first; a commit intent is then recorded in a
per-user cache file (never inside .wipnote/). A single serialized committer
drains the outbox under the repo-scoped advisory git lock, so no agent is on the
git hot path. Failed intents are retried; an intent that fails repeatedly is
moved to a dead-letter log so one poison commit cannot freeze the queue.`,
	}
	cmd.AddCommand(commitQueueFlushCmd())
	cmd.AddCommand(commitQueueStatusCmd())
	cmd.AddCommand(commitQueueDeadLetterCmd())
	return cmd
}

// deadLetterWarningLine formats the loud, actionable line printed by flush
// and status whenever the dead-letter depth is non-zero (GH#155) — a bare
// count previously gave no indication anything was remediable.
func deadLetterWarningLine(dlDepth int) string {
	return fmt.Sprintf(
		"WARNING: %d commit-queue intent(s) stuck in dead-letter — inspect with "+
			"'wipnote commit-queue dead-letter list', then 'retry' or 'clear'.\n",
		dlDepth)
}

func commitQueueFlushCmd() *cobra.Command {
	var maxAttempts int
	cmd := &cobra.Command{
		Use:   "flush",
		Short: "Drain the commit outbox in FIFO order",
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, err := resolveProjectRoot()
			if err != nil {
				return err
			}
			ob, err := openCommitOutbox(repoRoot)
			if err != nil {
				return err
			}
			res, err := ob.Flush(outboxCommitter, maxAttempts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"commit-queue flush: committed=%d failed=%d dead-lettered=%d remaining=%d dead-letter-depth=%d\n",
				res.Committed, res.Failed, res.DeadLettered, res.RemainingDepth, res.DeadLetterDepth)
			if res.DeadLetterDepth > 0 {
				fmt.Fprint(cmd.OutOrStdout(), deadLetterWarningLine(res.DeadLetterDepth))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&maxAttempts, "max-attempts", commitqueue.MaxAttempts,
		"consecutive failures before an intent is dead-lettered")
	return cmd
}

func commitQueueStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show outbox and dead-letter depths",
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, err := resolveProjectRoot()
			if err != nil {
				return err
			}
			ob, err := openCommitOutbox(repoRoot)
			if err != nil {
				return err
			}
			depth, err := ob.Depth()
			if err != nil {
				return err
			}
			dlDepth, err := ob.DeadLetterDepth()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"commit-queue: pending=%d dead-letter=%d\n  outbox: %s\n",
				depth, dlDepth, ob.Path())
			if dlDepth > 0 {
				fmt.Fprint(cmd.OutOrStdout(), deadLetterWarningLine(dlDepth))
			}
			return nil
		},
	}
}
