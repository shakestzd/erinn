package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// sessionFamilyIndex is the JSON structure stored in
// .wipnote/session-families.json. It maps session_id -> session_family_id for
// all active/recent sessions in a project.
//
// This replaces the last-writer-wins .active-session for projects running
// parallel root sessions: each entry coexists without clobbering the others.
type sessionFamilyIndex struct {
	Families map[string]string `json:"families"` // session_id -> family_id
	// Order records (session_id, registration unix-nanos) in append order so a
	// "resume last" launch can recover the most-recently-registered session's
	// family deterministically instead of relying on Go map iteration order
	// (which is randomized and would attach a resumed session to an arbitrary
	// family when parallel roots populate the index).
	Order []familyOrderEntry `json:"order,omitempty"`
}

// familyOrderEntry is one timestamped registration in the family index.
type familyOrderEntry struct {
	SessionID string `json:"session_id"`
	FamilyID  string `json:"family_id"`
	TS        int64  `json:"ts"` // unix nanoseconds at registration
}

// sessionFamilyMu serializes writes to session-families.json within a process.
// Cross-process safety is provided by an OS advisory file lock (flock) held
// across the whole read-modify-write — the atomic temp+rename only prevents
// torn files, not the lost-update race between concurrent launcher processes.
var sessionFamilyMu sync.Mutex

// familyIndexPath returns the path to the project's session family index file.
func familyIndexPath(projectDir string) string {
	return filepath.Join(projectDir, ".wipnote", "session-families.json")
}

// familyLockPath returns the path to the interprocess lock file guarding the
// session family index. It is a sibling of the index so it shares the same
// directory lifetime; it is never read for content, only flock()'d.
func familyLockPath(projectDir string) string {
	return filepath.Join(projectDir, ".wipnote", ".session-families.lock")
}

// withFamilyFileLock runs fn while holding an exclusive OS advisory lock on the
// project's family lock file, serializing the full read-modify-write across
// concurrent launcher processes. The in-process mutex is also held so goroutines
// in one process do not race on the same fd.
//
// Degradation: if the lock file cannot be created or locked (e.g. read-only or
// missing .wipnote dir, unusual filesystem), fn still runs under the in-process
// mutex only. This preserves single-process correctness and never hard-fails a
// launcher just because the lock dir is unavailable — the same best-effort
// contract the callers already rely on.
func withFamilyFileLock(projectDir string, fn func() error) error {
	sessionFamilyMu.Lock()
	defer sessionFamilyMu.Unlock()

	// Best-effort: ensure the .wipnote dir exists so the lock file can live
	// next to the index. Ignore errors — fn degrades to mutex-only below.
	_ = os.MkdirAll(filepath.Join(projectDir, ".wipnote"), 0o755)

	lf, err := os.OpenFile(familyLockPath(projectDir), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		// Lock dir unavailable — degrade to in-process serialization only.
		return fn()
	}
	defer lf.Close()

	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		// Could not acquire the OS lock — degrade rather than block forever.
		return fn()
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck

	return fn()
}

// RegisterSessionFamily records sessionID -> familyID in the project's family
// index. Multiple sessions may share the same familyID (resumed continuations).
// The write is atomic (temp+rename) so concurrent processes cannot corrupt it.
func RegisterSessionFamily(projectDir, sessionID, familyID string) error {
	return withFamilyFileLock(projectDir, func() error {
		idx := readFamilyIndexLocked(projectDir)
		if idx.Families == nil {
			idx.Families = make(map[string]string)
		}
		idx.Families[sessionID] = familyID
		// Append a timestamped order entry. Drop any prior entry for the same
		// session so "most recent" reflects the latest registration and the
		// slice does not grow unbounded across re-registrations.
		filtered := idx.Order[:0]
		for _, e := range idx.Order {
			if e.SessionID != sessionID {
				filtered = append(filtered, e)
			}
		}
		idx.Order = append(filtered, familyOrderEntry{
			SessionID: sessionID,
			FamilyID:  familyID,
			TS:        time.Now().UnixNano(),
		})
		return writeFamilyIndexLocked(projectDir, idx)
	})
}

// SessionFamilyFor returns the family ID registered for an exact session ID,
// or "" when that session has no recorded family. This is the precise lookup
// used when the launcher knows the concrete resumed session ID — it avoids
// inheriting an arbitrary family from an unrelated parallel root.
func SessionFamilyFor(projectDir, sessionID string) string {
	if projectDir == "" || sessionID == "" {
		return ""
	}
	// Per-session state is the most authoritative single-session record.
	if st, err := ReadSessionState(projectDir, sessionID); err == nil && st != nil && st.SessionFamilyID != "" {
		return st.SessionFamilyID
	}
	idx := readFamilyIndexLocked(projectDir)
	return idx.Families[sessionID]
}

// MostRecentSessionFamily returns the family ID of the most-recently-registered
// session in the project (by registration timestamp), or "" when the index is
// empty or has no ordered entries. Used for "resume last" launches where no
// concrete session ID is known: the newest session's family is the correct
// continuation target, unlike arbitrary map iteration.
func MostRecentSessionFamily(projectDir string) string {
	if projectDir == "" {
		return ""
	}
	idx := readFamilyIndexLocked(projectDir)
	var best familyOrderEntry
	found := false
	for _, e := range idx.Order {
		if !found || e.TS > best.TS {
			best = e
			found = true
		}
	}
	if found {
		return best.FamilyID
	}
	return ""
}

// ReadSessionFamilyIndex reads the project's family index. Returns an empty
// index (not an error) when the file does not exist.
func ReadSessionFamilyIndex(projectDir string) (map[string]string, error) {
	idx := readFamilyIndexLocked(projectDir)
	if idx.Families == nil {
		return map[string]string{}, nil
	}
	return idx.Families, nil
}

// readFamilyIndexLocked reads the family index (no lock acquired — caller holds lock or reads once).
func readFamilyIndexLocked(projectDir string) sessionFamilyIndex {
	path := familyIndexPath(projectDir)
	b, err := os.ReadFile(path)
	if err != nil {
		return sessionFamilyIndex{}
	}
	var idx sessionFamilyIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return sessionFamilyIndex{}
	}
	return idx
}

// writeFamilyIndexLocked atomically writes the family index.
func writeFamilyIndexLocked(projectDir string, idx sessionFamilyIndex) error {
	b, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	dir := filepath.Join(projectDir, ".wipnote")
	tmp, err := os.CreateTemp(dir, ".session-families.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	_ = os.Chmod(tmpPath, 0o644)
	return os.Rename(tmpPath, familyIndexPath(projectDir))
}

// SessionStateFile is the per-session state stored in
// .wipnote/sessions/<session_id>/state.json. It contains the harness-neutral
// session identity and family linkage, allowing each session to have its own
// fallback without the last-writer-wins collisions of .active-session.
type SessionStateFile struct {
	SessionID       string `json:"session_id"`
	AgentID         string `json:"agent_id"`
	SessionFamilyID string `json:"session_family_id"`
	Timestamp       int64  `json:"timestamp"`
}

// sessionStatePath returns the per-session state file path.
func sessionStatePath(projectDir, sessionID string) string {
	return filepath.Join(projectDir, ".wipnote", "sessions", sessionID, "state.json")
}

// WriteSessionState writes per-session state to the session-scoped directory.
// Unlike .active-session, each session has its own file so parallel sessions
// cannot overwrite each other.
func WriteSessionState(projectDir, sessionID, agentID, familyID string) error {
	if projectDir == "" || sessionID == "" {
		return nil
	}
	sessDir := filepath.Join(projectDir, ".wipnote", "sessions", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return err
	}
	state := SessionStateFile{
		SessionID:       sessionID,
		AgentID:         agentID,
		SessionFamilyID: familyID,
		Timestamp:       time.Now().Unix(),
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	path := sessionStatePath(projectDir, sessionID)
	tmp, err := os.CreateTemp(sessDir, ".state.tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	_ = os.Chmod(tmpPath, 0o644)
	return os.Rename(tmpPath, path)
}

// ReadSessionState reads the per-session state for the given session. Returns
// (nil, nil) when the file does not exist (session predates per-session state).
func ReadSessionState(projectDir, sessionID string) (*SessionStateFile, error) {
	path := sessionStatePath(projectDir, sessionID)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state SessionStateFile
	if err := json.Unmarshal(b, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// ResolvePrimarySessionID returns the session ID for a project using the
// per-session state file when present (preferred), falling back to the legacy
// .active-session file for backward compatibility.
//
// Callers should prefer per-session state when a concrete sessionID is known;
// this function is for callers that need to discover the "current" session
// without an explicit ID (e.g. CLI commands running outside a hook).
func ResolvePrimarySessionID(projectDir, sessionID string) string {
	// Per-session state is primary when present.
	if sessionID != "" {
		if st, err := ReadSessionState(projectDir, sessionID); err == nil && st != nil {
			return st.SessionID
		}
	}
	// Fall back to legacy .active-session (backward compat).
	return readActiveSessionID(projectDir)
}
