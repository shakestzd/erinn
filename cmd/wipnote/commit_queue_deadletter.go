// commit_queue_deadletter.go — `wipnote commit-queue dead-letter {list,retry,clear}`.
//
// GH#155: with defer policy, `wipnote commit-queue flush` reported a steady
// non-zero dead-letter-depth with no surfaced reason and no remediation path.
// This file is the inspect/retry/clear surface for that log.
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shakestzd/wipnote/internal/commitqueue"
)

// deadLetterOutbox resolves the commit-queue outbox for the current project
// root — the shared entry point for all three dead-letter subcommands below.
func deadLetterOutbox() (*commitqueue.Outbox, error) {
	repoRoot, err := resolveProjectRoot()
	if err != nil {
		return nil, err
	}
	return openCommitOutbox(repoRoot)
}

// commitQueueDeadLetterCmd is `wipnote commit-queue dead-letter` — the
// remediation surface for the dead-letter log. flush/status only ever
// surfaced a bare depth; this group gives an operator the inspect/retry/
// clear path that a bare count is missing.
func commitQueueDeadLetterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "dead-letter",
		Aliases: []string{"deadletter", "dlq"},
		Short:   "Inspect and remediate dead-lettered commit intents",
		Long: `Intents that fail to commit MaxAttempts times in a row move from the
pending outbox to the dead-letter log instead of retrying forever. This group
answers "why is dead-letter-depth non-zero" and provides a remediation path:

  wipnote commit-queue dead-letter list             Inspect what's stuck and why
  wipnote commit-queue dead-letter retry <id>|--all  Re-enqueue for another flush
  wipnote commit-queue dead-letter clear <id>|--all  Permanently drop (confirms)`,
	}
	cmd.AddCommand(commitQueueDeadLetterListCmd())
	cmd.AddCommand(commitQueueDeadLetterRetryCmd())
	cmd.AddCommand(commitQueueDeadLetterClearCmd())
	return cmd
}

func commitQueueDeadLetterListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"inspect", "ls"},
		Short:   "List dead-lettered commit intents with failure reason",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ob, err := deadLetterOutbox()
			if err != nil {
				return err
			}
			dl, err := ob.DeadLettered()
			if err != nil {
				return err
			}
			printDeadLetterList(cmd, dl)
			return nil
		},
	}
}

func printDeadLetterList(cmd *cobra.Command, dl []commitqueue.Intent) {
	out := cmd.OutOrStdout()
	if len(dl) == 0 {
		fmt.Fprintln(out, "commit-queue dead-letter: empty")
		return
	}
	fmt.Fprintf(out, "commit-queue dead-letter: %d intent(s)\n\n", len(dl))
	for _, i := range dl {
		fmt.Fprintf(out, "work_item=%s action=%s attempts=%d\n",
			orDash(i.WorkItemID), orDash(i.Action), i.Attempts)
		fmt.Fprintf(out, "  paths:            %s\n", strings.Join(i.RelPaths, ", "))
		fmt.Fprintf(out, "  reason:           %s\n",
			orDash2(i.Reason, "(unknown — dead-lettered before reason tracking)"))
		fmt.Fprintf(out, "  enqueued_at:      %s\n", i.EnqueuedAt.Format(time.RFC3339))
		if !i.DeadLetteredAt.IsZero() {
			fmt.Fprintf(out, "  dead_lettered_at: %s\n", i.DeadLetteredAt.Format(time.RFC3339))
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "Remediate: wipnote commit-queue dead-letter retry <work-item-id>|--all\n"+
		"Drop:      wipnote commit-queue dead-letter clear <work-item-id>|--all\n")
}

func orDash(s string) string { return orDash2(s, "-") }

func orDash2(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func commitQueueDeadLetterRetryCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "retry [work-item-id]",
		Short: "Re-enqueue dead-lettered intent(s) onto the pending queue",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := deadLetterTargetID(args, all)
			if err != nil {
				return err
			}
			ob, err := deadLetterOutbox()
			if err != nil {
				return err
			}
			n, err := ob.RetryDeadLetter(id)
			if err != nil {
				return err
			}
			if n == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "commit-queue dead-letter retry: no matching dead-lettered intent")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "commit-queue dead-letter retry: re-enqueued %d intent(s)\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Retry every dead-lettered intent")
	return cmd
}

func commitQueueDeadLetterClearCmd() *cobra.Command {
	var all, yes bool
	cmd := &cobra.Command{
		Use:   "clear [work-item-id]",
		Short: "Permanently drop dead-lettered intent(s)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := deadLetterTargetID(args, all)
			if err != nil {
				return err
			}
			ob, err := deadLetterOutbox()
			if err != nil {
				return err
			}
			matchCount, err := ob.CountDeadLetterMatches(id)
			if err != nil {
				return err
			}
			if matchCount == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "commit-queue dead-letter clear: no matching dead-lettered intent")
				return nil
			}
			question := fmt.Sprintf("Permanently drop %d dead-lettered intent(s)? This cannot be undone.", matchCount)
			if !promptYesNo(question, yes) {
				fmt.Fprintln(cmd.OutOrStdout(), "commit-queue dead-letter clear: aborted")
				return nil
			}
			n, err := ob.ClearDeadLetter(id)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "commit-queue dead-letter clear: dropped %d intent(s)\n", n)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Clear every dead-lettered intent")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompt")
	return cmd
}

// deadLetterTargetID resolves the retry/clear target from either a single
// positional work-item-id or --all, rejecting the ambiguous/underspecified
// combinations (both, or neither).
func deadLetterTargetID(args []string, all bool) (string, error) {
	if all {
		if len(args) > 0 {
			return "", fmt.Errorf("commit-queue dead-letter: pass either --all or a work-item-id, not both")
		}
		return "", nil
	}
	if len(args) == 0 {
		return "", fmt.Errorf("commit-queue dead-letter: pass a work-item-id or --all")
	}
	return args[0], nil
}
