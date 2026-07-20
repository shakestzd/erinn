package commitqueue

import "time"

// Committer performs the actual git commit for one intent. It returns nil on
// success (including the idempotent "nothing to commit" no-op) and a non-nil
// error on a real failure (locked index, hook rejection, etc.). Production
// wires this to a func that calls runGitMutation under the repo-scoped advisory
// lock; tests inject a stub so they never touch real git.
//
// The committer MUST be idempotent: because flush rewrites the outbox only
// AFTER a commit is confirmed, an interrupted flush re-runs the committer for
// intents whose commit already landed. The underlying artifact commit treats an
// already-committed file as a "nothing to commit" no-op, so re-running is safe.
type Committer func(Intent) error

// Clock returns the current time. Injectable so tests get deterministic timing
// without a real clock.
type Clock func() time.Time

// FlushResult summarizes one drain pass.
type FlushResult struct {
	Committed       int // intents whose commit succeeded and were removed
	Failed          int // intents that failed this pass but stay queued (under MaxAttempts)
	DeadLettered    int // intents moved to the dead-letter log this pass
	RemainingDepth  int // pending intents still queued after this pass
	DeadLetterDepth int // total dead-lettered intents after this pass
}

// Flush drains the outbox in FIFO order, committing each intent via commit.
//
// Ordering & atomicity:
//   - Intents are processed oldest-first.
//   - An intent is removed from the pending file ONLY after its commit is
//     confirmed (commit returned nil) — so a crash before the rewrite re-flushes
//     it, relying on commit idempotency to avoid double-commit harm.
//   - On commit failure the intent's Attempts is incremented and it stays
//     queued. Once Attempts reaches maxAttempts it is moved to the dead-letter
//     log and removed from pending, so one poison intent never freezes the
//     ordered queue — subsequent intents are still attempted in the same pass.
//
// The whole pending file is rewritten once at the end with exactly the intents
// that should remain (failed-but-under-threshold, in original order). This
// keeps the rewrite a single atomic rename rather than one-per-intent.
//
// maxAttempts <= 0 falls back to the package default MaxAttempts.
func (o *Outbox) Flush(commit Committer, maxAttempts int) (FlushResult, error) {
	if maxAttempts <= 0 {
		maxAttempts = MaxAttempts
	}
	// Hold the cross-operation lock across the ENTIRE snapshot-process-rewrite
	// cycle so a concurrent Append (or a second Flush) cannot have its intent
	// dropped by the stale-snapshot rewrite (roborev finding on feat-76504033).
	var res FlushResult
	err := o.withLock(func() error { return o.flushLocked(commit, maxAttempts, &res) })
	return res, err
}

// flushLocked is the drain body; callers MUST hold o.withLock. It is split out
// of Flush only so the locking is expressed once at the boundary.
func (o *Outbox) flushLocked(commit Committer, maxAttempts int, res *FlushResult) error {
	pending, err := o.Pending()
	if err != nil {
		return err
	}

	var remaining []Intent

	for idx, intent := range pending {
		if err := intent.Validate(); err != nil {
			intent.Attempts = maxAttempts
			intent.Reason = err.Error()
			intent.DeadLetteredAt = time.Now().UTC()
			if dlErr := o.appendDeadLetter(intent); dlErr != nil {
				remaining = append(remaining, intent)
				remaining = append(remaining, pending[idx+1:]...)
				_ = o.rewrite(remaining)
				return dlErr
			}
			res.DeadLettered++
			continue
		}
		commitErr := commit(intent)
		if commitErr == nil {
			res.Committed++
			continue
		}
		// Commit failed: count the attempt.
		intent.Attempts++
		if intent.Attempts >= maxAttempts {
			// Capture why the commit kept failing so dead-letter list has
			// something more useful than a bare count (GH#155).
			intent.Reason = commitErr.Error()
			intent.DeadLetteredAt = time.Now().UTC()
			if dlErr := o.appendDeadLetter(intent); dlErr != nil {
				// Could not dead-letter: don't lose data. Keep this intent and
				// every intent we have not yet processed, persist, and abort so
				// the operator sees the underlying error.
				remaining = append(remaining, intent)
				remaining = append(remaining, pending[idx+1:]...)
				_ = o.rewrite(remaining)
				return dlErr
			}
			res.DeadLettered++
			continue // dropped from pending; queue keeps draining
		}
		res.Failed++
		remaining = append(remaining, intent)
	}

	if err := o.rewrite(remaining); err != nil {
		return err
	}

	res.RemainingDepth = len(remaining)
	if dlDepth, dErr := o.DeadLetterDepth(); dErr == nil {
		res.DeadLetterDepth = dlDepth
	}
	return nil
}
