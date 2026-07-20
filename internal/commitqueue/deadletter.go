package commitqueue

import "time"

// deadletter.go implements the remediation half of the dead-letter log
// (GH#155): `flush`/`status` already surfaced a bare dead-letter depth, but
// there was no way to see WHY an intent gave up, nor any path back to
// committed besides hand-editing the NDJSON file. RetryDeadLetter and
// ClearDeadLetter give operators (and `wipnote commit-queue dead-letter
// retry|clear`) that path.

// RetryDeadLetter re-enqueues dead-lettered intents matching workItemID back
// onto the pending queue for another flush attempt. An empty workItemID
// matches every dead-lettered intent. Attempts, Reason, and DeadLetteredAt
// are reset so the retried intent gets a full fresh run of maxAttempts on
// the next flush. Returns the number of intents re-enqueued (0 if nothing
// matched — not an error).
func (o *Outbox) RetryDeadLetter(workItemID string) (int, error) {
	var n int
	err := o.withLock(func() error {
		dl, err := readIntents(o.dlPath)
		if err != nil {
			return err
		}
		var keptDL, toRetry []Intent
		for _, i := range dl {
			if workItemID != "" && i.WorkItemID != workItemID {
				keptDL = append(keptDL, i)
				continue
			}
			i.Attempts = 0
			i.Reason = ""
			i.DeadLetteredAt = time.Time{}
			toRetry = append(toRetry, i)
		}
		n = len(toRetry)
		if n == 0 {
			return nil
		}
		pending, err := readIntents(o.path)
		if err != nil {
			return err
		}
		if err := o.rewrite(append(pending, toRetry...)); err != nil {
			return err
		}
		return atomicWriteIntents(o.dlPath, keptDL)
	})
	return n, err
}

// ClearDeadLetter permanently drops dead-lettered intents matching
// workItemID (or every dead-lettered intent when workItemID is ""). This is
// destructive and unrecoverable — callers (the CLI) are responsible for
// confirming with the operator before calling it. Returns the number of
// intents dropped (0 if nothing matched — not an error).
func (o *Outbox) ClearDeadLetter(workItemID string) (int, error) {
	var n int
	err := o.withLock(func() error {
		dl, err := readIntents(o.dlPath)
		if err != nil {
			return err
		}
		var keptDL []Intent
		for _, i := range dl {
			if workItemID != "" && i.WorkItemID != workItemID {
				keptDL = append(keptDL, i)
				continue
			}
			n++
		}
		if n == 0 {
			return nil
		}
		return atomicWriteIntents(o.dlPath, keptDL)
	})
	return n, err
}

// CountDeadLetterMatches reports how many currently dead-lettered intents
// match workItemID (or the total when workItemID is "") without mutating
// anything. Used by the CLI to size a confirmation prompt before ClearDeadLetter.
func (o *Outbox) CountDeadLetterMatches(workItemID string) (int, error) {
	dl, err := o.DeadLettered()
	if err != nil {
		return 0, err
	}
	if workItemID == "" {
		return len(dl), nil
	}
	n := 0
	for _, i := range dl {
		if i.WorkItemID == workItemID {
			n++
		}
	}
	return n, nil
}
