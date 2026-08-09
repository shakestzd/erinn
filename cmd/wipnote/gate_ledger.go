package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/shakestzd/wipnote/core/gateledger"
)

// initGateLedgerCommitSeam installs the commit producer for canonical gate
// ledger writes. core/gateledger cannot import internal/commitqueue (core must
// not depend on internal), but every writer — `wipnote check --gate`, the
// completion re-check, and the hook handlers alike — runs inside this binary, so
// wiring the seam once at startup covers them all.
func initGateLedgerCommitSeam() {
	gateledger.OnCommit = persistGateLedgerWrite
}

// persistGateLedgerWrite records the gate ledger for commit.
//
// COMMIT BATCHING: the ledger is one file that every gate run in the repo appends
// to, so committing each run directly would produce a commit per `check --gate`.
// The deferred artifact commit-queue already solves this and needs no changes:
// AppendCoalescingByRelPath drops older pending intents naming the same
// repo-relative path before appending the new one, so a whole session's gate runs
// collapse into a SINGLE pending intent that commits the file's final state once,
// whenever `wipnote commit-queue flush` next drains.
//
// The DEFERRAL IS ONLY THE GIT COMMIT. gateledger.Append fsyncs the row before
// this is ever called, which is what makes the write-then-read case sound: a gate
// run recorded by `wipnote check --gate` is readable by the `wipnote feature
// complete` that follows, in a different process, with the queue still unflushed.
//
// Under the legacy "separate" policy the write is committed directly, matching
// how work-item artifacts and the claim and sessions ledgers behave under that
// same opt-in.
func persistGateLedgerWrite(wipnoteDir, relPath, action string) {
	msg := "wipnote: record " + action
	switch workitemArtifactCommitPolicyForEnv() {
	case workitemArtifactCommitPolicyDefer:
		if err := enqueueGateLedgerCommitIntent(wipnoteDir, relPath, msg); err != nil && os.Getenv("WIPNOTE_DEBUG") == "1" {
			fmt.Fprintf(stderr, "gate ledger commit defer: %v\n", err)
		}
	default:
		if err := commitWipnotePath(wipnoteDir, relPath, msg); err != nil && os.Getenv("WIPNOTE_DEBUG") == "1" {
			fmt.Fprintf(stderr, "gate ledger commit: %v\n", err)
		}
	}
}

func enqueueGateLedgerCommitIntent(wipnoteDir, relPath, msg string) error {
	repoRoot := filepath.Dir(wipnoteDir)
	if skipWipnoteGitMutation(wipnoteDir, "gate ledger commit defer") {
		return nil
	}
	return recordCommitIntent(repoRoot, []string{relPath}, msg, "", "gate")
}
