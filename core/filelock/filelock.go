// Package filelock holds the canonical wipnote guard for writers to a single
// canonical file on disk.
//
// The guard is two layers:
//
//  1. an in-process mutex keyed by the file path, so goroutines inside one
//     wipnote process serialise, and
//  2. a BLOCKING exclusive advisory lock on a sidecar "<path>.lock" file, so
//     separate wipnote processes on the same repository serialise too.
//
// On Unix layer 2 subsumes layer 1 — flock is per open-file-description, and
// each Guard opens its own descriptor, so it already excludes goroutines within
// a process. Layer 1 is not redundant, though: it is the ONLY exclusion that
// exists on Windows, where layer 2 is a no-op (bug-68f3593b), and it is also
// the fallback whenever the sidecar cannot be opened or locked.
//
// The sidecar is separate from the data file on purpose: canonical writers
// replace the data file via temp-then-rename, and a lock held on the data
// file's inode would not survive the swap. The sidecar's inode is stable.
//
// This was extracted from core/arch, which had the only copy. Every canonical
// HTML artifact that needs a cross-process-safe write path should call Guard
// rather than growing a second copy of the same three lines.
package filelock

import "sync"

// LockSuffix is appended to the guarded path to name the sidecar lock file.
// The managed .wipnote/.gitignore excludes "**/*.lock", so sidecars created
// inside .wipnote/ are never committed.
const LockSuffix = ".lock"

// guards maps an absolute file path to the in-process mutex for that path.
var guards sync.Map // string -> *sync.Mutex

// Guard blocks until the caller has exclusive write access to path, and
// returns the release function. Callers must always call release (defer it).
//
// On platforms where CrossProcess is false the returned guard is IN-PROCESS
// ONLY — see the CrossProcess doc comment. Callers whose correctness depends
// on cross-process exclusion must consult CrossProcess rather than assume it.
func Guard(path string) (release func()) {
	muVal, _ := guards.LoadOrStore(path, &sync.Mutex{})
	mu := muVal.(*sync.Mutex)
	mu.Lock()
	return lockSidecar(path, mu.Unlock)
}
