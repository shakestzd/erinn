package ndjson

// Overflow strategy note:
//
// Every WriteBatch acquires syscall.Flock(LOCK_EX) for the duration of the
// write, regardless of payload size. This means the "oversize payload"
// problem — two concurrent writers racing to append — is already solved by
// the flock itself: only one writer holds the lock at a time, so lines are
// always atomically appended in their entirety.
//
// No additional overflow handling (chunking, temp files, etc.) is needed for
// the NDJSON sink. The flock pattern, borrowed from session_html.go:147 and
// materialize.go:241, is the single mechanism that keeps events.ndjson
// consistent under concurrent writers.
//
// If very large batches (>1 MB) become a concern in a future slice, split
// them into sub-batches before calling WriteBatch — the flock overhead per
// call is negligible on local filesystems.
