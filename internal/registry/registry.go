// Package registry manages a JSON-backed catalog of wipnote projects on the
// local machine.
//
// # File format
//
// The registry is stored as a JSON array of Entry values at DefaultPath()
// (~/.local/share/wipnote/projects.json).  A missing file is treated as an
// empty registry; Load never returns an error for a missing file.
//
// # Atomic writes
//
// Save writes to a sibling <path>.tmp file and then calls os.Rename to atomically
// replace the registry file.  This guarantees that readers never observe a
// partially-written file.  flock-based mutual exclusion is out of scope for the
// MVP; concurrent writers on the same machine should be rare enough that the
// last-write-wins behaviour of os.Rename is acceptable.
//
// # Read-only SQLite access
//
// OpenReadOnly opens a foreign project's SQLite database in read-only mode
// (?mode=ro URI flag) so the registry can query project metadata without
// running migrations or acquiring write locks on databases it does not own.
package registry

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DefaultRegistryTTL is the maximum age of a registry entry before passive
// cleanup removes it. Three days reflects the dashboard's purpose: active
// work. If you haven't touched a project in three days it shouldn't pollute
// the landing view; re-running wipnote in the project re-registers it instantly.
const DefaultRegistryTTL = 3 * 24 * time.Hour

// Entry represents a single registered wipnote project.
type Entry struct {
	// ID is the first 8 hex characters of SHA256(ProjectDir).
	// It is computed on first Upsert and never changes for a given directory.
	ID string `json:"id"`

	// ProjectDir is the absolute path to the project root (the directory that
	// contains .wipnote/).
	ProjectDir string `json:"project_dir"`

	// Name is the human-readable project name (typically the directory basename
	// or the value supplied by the caller).
	Name string `json:"name"`

	// GitRemoteURL is the git remote origin URL, or empty if unavailable.
	GitRemoteURL string `json:"git_remote_url,omitempty"`

	// LastSeen is an RFC 3339 UTC timestamp updated on every Upsert call.
	LastSeen string `json:"last_seen"`
}

// Registry is an in-memory view of the JSON registry file.  Mutating methods
// (Upsert, Prune) update the in-memory slice; call Save to persist changes.
type Registry struct {
	path    string
	entries []Entry

	// migrated is set when Load resolved this Registry's contents from the
	// legacy ~/.local/share/wipnote/projects.json instead of the supplied
	// canonical path. Callers can query MigrationPending() to learn whether
	// a Save is needed solely to materialise the migration into the
	// canonical XDG location, even when the in-memory slice is unchanged.
	migrated bool
}

// Load reads the registry from path.  If the file does not exist an empty
// Registry is returned with no error.  Any other I/O error is propagated.
//
// Legacy migration: when path is the canonical XDG-aware DefaultPath() and
// it does not yet exist, Load also probes the legacy
// ~/.local/share/wipnote/projects.json. If that legacy file exists, its
// contents are returned and the in-memory Registry retains the canonical
// path — the next Save persists to the canonical location and the legacy
// file is left untouched. This avoids "all my projects vanished" reports
// from users who set XDG_DATA_HOME after first run (PR #62 review).
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			entries, found, lerr := loadLegacyForCanonical(path)
			if lerr != nil {
				return nil, fmt.Errorf("registry.Load: legacy fallback: %w", lerr)
			}
			if found {
				return &Registry{path: path, entries: entries, migrated: true}, nil
			}
			return &Registry{path: path}, nil
		}
		return nil, fmt.Errorf("registry.Load: %w", err)
	}

	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("registry.Load: malformed JSON in %s: %w", path, err)
	}
	return &Registry{path: path, entries: entries}, nil
}

// loadLegacyForCanonical reads the legacy registry file when path is the
// current canonical DefaultPath. It returns:
//
//   - (entries, true, nil)   — legacy file exists, was readable, parsed cleanly
//   - (nil,     false, nil)  — legacy file genuinely missing, OR path is not
//     the canonical default (no fallback applies)
//   - (nil,     false, err)  — legacy file exists but read or JSON parse failed
//
// The third case is critical (review #55 F3): without it, a corrupt or
// unreadable legacy file would be silently masked as "no legacy registry,"
// the caller would fall through to an empty Registry, and the next Save
// would overwrite the canonical path with `[]` — destroying the user's
// project list. Propagating the error forces a hard stop until the
// human sorts the file out by hand.
func loadLegacyForCanonical(path string) ([]Entry, bool, error) {
	canonical := canonicalDefaultPath()
	legacy := legacyDefaultPath()
	if path != canonical || canonical == legacy {
		return nil, false, nil
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read legacy %s: %w", legacy, err)
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, false, fmt.Errorf("parse legacy %s: %w", legacy, err)
	}
	return entries, true, nil
}

// MigrationPending reports whether this Registry was populated via the
// legacy-path fallback (i.e. the canonical XDG path did not exist, but
// the legacy path did, and Load read the legacy contents). When true,
// callers SHOULD invoke Save at a convenient point so the canonical
// path is materialised — otherwise a clean migration with no stale
// entries would persist legacy-only forever.
func (r *Registry) MigrationPending() bool {
	return r != nil && r.migrated
}

// Save persists the registry to disk using a tempfile + os.Rename so the
// write is atomic from the reader's perspective.
//
// SIDE EFFECT: Save also calls Prune() before writing — entries whose
// project directory no longer contains a .wipnote/ subdirectory are
// dropped from the in-memory slice and never written. Callers expecting
// "save exactly what I have in memory" semantics will be surprised; if a
// project dir was temporarily unmounted or symlinked away at save time,
// its entry disappears with no log line. If you need pure save-without-
// pruning behaviour, write the JSON yourself or copy this method without
// the r.Prune() call. Renaming this to SaveAndPrune was considered but
// deferred to keep the call-site churn small; this godoc is the contract.
func (r *Registry) Save() error {
	r.Prune()
	return r.SaveExact()
}

// SaveExact persists the in-memory registry verbatim using the same atomic
// tempfile + rename path as Save, but without an implicit structural prune.
// Callers that have already curated the entry set (for example, TTL cleanup)
// should prefer this helper so they don't accidentally drop otherwise-kept
// entries whose .wipnote directory is temporarily unavailable.
func (r *Registry) SaveExact() error {
	if err := writeEntriesAtomic(r.path, r.entries); err != nil {
		return err
	}
	// Migration is now materialised into the canonical path. Clearing the
	// flag prevents a subsequent caller from re-saving on a stable Registry
	// that already lives in canonical.
	r.migrated = false
	return nil
}

// writeEntriesAtomic is the shared atomic write used by Save and the
// test-only WriteEntriesForTest helper. Keeping the on-disk format in one
// place ensures the test helper cannot silently drift from production.
//
// The temp file is created via os.CreateTemp with a unique randomised
// suffix in the same directory as the target so concurrent Save calls do
// not stomp each other (review #55 F2 — the previous fixed `<path>.tmp`
// allowed two writers to overwrite each other's tempfile and rename a
// half-written file into place). os.Rename within the same directory is
// atomic on POSIX. Any tempfile left behind on a partial failure is
// cleaned up; the persistent file is never modified except by Rename.
func writeEntriesAtomic(path string, entries []Entry) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("registry: mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("registry: marshal: %w", err)
	}
	data = append(data, '\n')

	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, base+".*.tmp")
	if err != nil {
		return fmt.Errorf("registry: create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("registry: write tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("registry: close tmp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("registry: chmod tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("registry: rename: %w", err)
	}
	return nil
}

// WriteEntriesForTest writes raw entries to path using the same JSON format
// Save uses. Tests need this to seed registry files with entries that would
// be rejected by Upsert (e.g. tempdirs that fail looksLikeRealProject) — and
// without it, tests hand-rolled the JSON format and risked silently drifting
// from Save when the schema evolved (PR #62 review issue #7).
//
// Production code must go through Upsert+Save. Do NOT call this outside tests.
func WriteEntriesForTest(path string, entries []Entry) error {
	return writeEntriesAtomic(path, entries)
}

// ShouldSkipRegistration reports whether dir should be silently skipped at
// registration time, preventing test temp directories from polluting the
// project registry.
//
// Two conditions trigger a skip:
//
//  1. WIPNOTE_SKIP_REGISTER=1 env var — explicit opt-out for tests.
//  2. The path is inside a temp directory (os.TempDir() or GOTMPDIR) AND a
//     component of the path under that dir starts with "Test" (Go's
//     t.TempDir() naming convention: /tmp/TestFooNNNN/sub).
//
// Production paths (e.g. /workspaces/wipnote) are never skipped because they
// do not live under the OS temp directory.
func ShouldSkipRegistration(projectDir string) bool {
	if os.Getenv("WIPNOTE_SKIP_REGISTER") == "1" {
		return true
	}
	return isGoTestTempDirPath(projectDir)
}

// effectiveTempDirs returns the set of candidate temp-root directories to
// check when deciding whether a path is a Go test temp dir. It always includes
// os.TempDir() (which honours TMPDIR on Unix), and also GOTMPDIR when set —
// that environment variable redirects where 'go test' places t.TempDir()
// subtrees (e.g. /home/vscode/.gotest-tmp in devcontainer setups).
func effectiveTempDirs() []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(raw string) {
		if raw == "" {
			return
		}
		resolved, err := filepath.EvalSymlinks(raw)
		if err != nil {
			resolved = raw
		}
		resolved = filepath.Clean(resolved)
		if !seen[resolved] {
			seen[resolved] = true
			dirs = append(dirs, resolved)
		}
	}
	add(os.TempDir())
	add(os.Getenv("GOTMPDIR"))
	return dirs
}

// isGoTestTempDirPath returns true when projectDir is inside any known temp
// root (os.TempDir() or GOTMPDIR) and matches Go's t.TempDir() naming
// convention: projectDir is DIRECTLY under (or IS ITSELF) a directory named
// TestXXX... (e.g. /tmp/TestFoo1234 or /tmp/TestFoo1234/sub, but not
// /tmp/TestFoo1234/realproject/sub).
//
// GOTMPDIR is checked alongside os.TempDir() to handle devcontainer setups
// where 'go test' writes t.TempDir() subtrees into a non-standard location
// (e.g. /home/vscode/.gotest-tmp) that differs from the system /tmp.
func isGoTestTempDirPath(projectDir string) bool {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return false
	}
	for _, tempDir := range effectiveTempDirs() {
		if matchesTestTempDirUnder(abs, tempDir) {
			return true
		}
	}
	return false
}

// matchesTestTempDirUnder returns true when abs is inside tempDir and matches
// Go's t.TempDir() path shape (Test* component directly under tempDir).
func matchesTestTempDirUnder(abs, tempDir string) bool {
	if !strings.HasPrefix(abs, tempDir+string(filepath.Separator)) {
		return false
	}

	// Check if abs itself or its direct parent is a Test* directory.
	// For <tempDir>/TestFoo1234, return true
	// For <tempDir>/TestFoo1234/sub, return true
	// For <tempDir>/TestFoo1234/sub/subsub, return false
	parent := filepath.Dir(abs)
	if parent == abs {
		return false
	}

	// Check direct parent
	parentBase := filepath.Base(parent)
	if strings.HasPrefix(parentBase, "Test") && parent != tempDir {
		return true
	}

	// Check if abs is itself a Test* dir (directly under tempDir)
	absBase := filepath.Base(abs)
	if strings.HasPrefix(absBase, "Test") {
		relPath, _ := filepath.Rel(tempDir, abs)
		return !strings.Contains(relPath, string(filepath.Separator))
	}

	return false
}

// IsGoTestTempDirPath reports whether projectDir points inside a known temp
// root (os.TempDir() or GOTMPDIR) and contains a Test* path component,
// matching Go's t.TempDir() naming convention.
func IsGoTestTempDirPath(projectDir string) bool {
	return isGoTestTempDirPath(projectDir)
}

// pathInsideTempDir reports whether path lives under any known temp root
// (os.TempDir() or GOTMPDIR). Test suites that redirect the registry file
// into a temp location should still be able to exercise tempdir projects
// without polluting the user's persistent registry.
func pathInsideTempDir(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, tempDir := range effectiveTempDirs() {
		if abs == tempDir || strings.HasPrefix(abs, tempDir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// PruneStale removes entries whose LastSeen timestamp is older than ttl.
// It returns the number of removed entries. The in-memory registry is
// mutated; call Save to persist.
func PruneStale(reg *Registry, ttl time.Duration) int {
	cutoff := time.Now().Add(-ttl)
	var removed int
	kept := reg.entries[:0]
	for _, p := range reg.entries {
		t, err := time.Parse(time.RFC3339, p.LastSeen)
		if err != nil || t.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, p)
	}
	reg.entries = kept
	return removed
}

// PruneTempdirEntries removes entries whose ProjectDir is inside os.TempDir()
// and has a Test* component (i.e. paths that match Go test temp dirs).
// Returns the number removed.
func PruneTempdirEntries(reg *Registry) int {
	var removed int
	kept := reg.entries[:0]
	for _, e := range reg.entries {
		if IsGoTestTempDirPath(e.ProjectDir) {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	reg.entries = kept
	return removed
}

// looksLikeRealProject returns true when dir contains a .wipnote/
// subdirectory. That is the sole signal: wipnote projects are not
// required to be Git repositories, so a `.git` ancestor is NOT part of
// the gate (review #55 F1).
//
// Registration hardening lives in Upsert, not here: a real project is still
// defined solely by the presence of .wipnote/. The tempdir guard decides
// whether that real project should be written to the persistent registry.
func looksLikeRealProject(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".wipnote"))
	return err == nil
}

// Upsert inserts or updates the entry for dir.  If an entry with the same
// cleaned absolute path already exists, its LastSeen (and optionally Name /
// GitRemoteURL) is updated and the original ID is preserved.  Otherwise a new
// entry is appended with a freshly computed ID.
//
// Upsert silently skips:
//   - Directories that do not look like real projects (no .wipnote/ subdirectory).
//   - Directories that match test temp-dir patterns when the target registry is
//     persistent (test-local registries under os.TempDir() are allowed).
//
// Before saving, callers should also call Prune.
func (r *Registry) Upsert(dir, name, remoteURL string) {
	dir = filepath.Clean(dir)
	if os.Getenv("WIPNOTE_SKIP_REGISTER") == "1" {
		return
	}
	if isGoTestTempDirPath(dir) && !pathInsideTempDir(r.path) {
		return
	}
	if !looksLikeRealProject(dir) {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)

	for i := range r.entries {
		if r.entries[i].ProjectDir == dir {
			r.entries[i].Name = name
			r.entries[i].GitRemoteURL = remoteURL
			r.entries[i].LastSeen = now
			return
		}
	}

	r.entries = append(r.entries, Entry{
		ID:           computeID(dir),
		ProjectDir:   dir,
		Name:         name,
		GitRemoteURL: remoteURL,
		LastSeen:     now,
	})
}

// List returns a copy of the current entries.
func (r *Registry) List() []Entry {
	result := make([]Entry, len(r.entries))
	copy(result, r.entries)
	return result
}

// Prune removes entries whose project directory no longer contains a
// .wipnote subdirectory.  It returns the ProjectDir values of the removed
// entries.
func (r *Registry) Prune() []string {
	var pruned []string
	kept := r.entries[:0]
	for _, e := range r.entries {
		if _, err := os.Stat(filepath.Join(e.ProjectDir, ".wipnote")); err == nil {
			kept = append(kept, e)
		} else {
			pruned = append(pruned, e.ProjectDir)
		}
	}
	r.entries = kept
	return pruned
}

// DropLinkedWorktrees removes entries whose project directory is inside
// a git linked worktree (as determined by the supplied resolver, which
// mirrors paths.ResolveViaGitCommonDir — returns the main repo root when
// dir is a linked worktree, empty string otherwise). Linked worktrees
// are NOT standalone projects: they share their data with the main
// repo, and the multi-project doorway should show one card per real
// project, not one per worktree branch.
//
// The resolver is injected so internal/registry does not import
// internal/paths (reverse dependency would break the package layout).
// Callers should pass paths.ResolveViaGitCommonDir.
//
// Returns the ProjectDir values of removed entries.
func (r *Registry) DropLinkedWorktrees(resolveMain func(dir string) string) []string {
	if resolveMain == nil {
		return nil
	}
	var dropped []string
	kept := r.entries[:0]
	for _, e := range r.entries {
		mainRoot := resolveMain(e.ProjectDir)
		// Keep if: not a linked worktree, OR the resolver returned the
		// same path (edge case: main repo root where ResolveViaGitCommonDir
		// returns "" — kept automatically).
		if mainRoot == "" || filepath.Clean(mainRoot) == filepath.Clean(e.ProjectDir) {
			kept = append(kept, e)
			continue
		}
		dropped = append(dropped, e.ProjectDir)
	}
	r.entries = kept
	return dropped
}

// DefaultPath returns the canonical registry file path. It honors
// XDG_DATA_HOME when set, otherwise falls back to the historical
// ~/.local/share/wipnote/projects.json.
//
// Legacy migration is handled by Load(): when the canonical path is
// missing but the legacy file exists, Load reads from legacy and the
// next Save persists to the canonical path. DefaultPath itself always
// returns the canonical (write-target) path.
func DefaultPath() string {
	return canonicalDefaultPath()
}

// canonicalDefaultPath returns the XDG-aware path. When XDG_DATA_HOME is
// unset this collapses to the legacy path, which is correct: the legacy
// path IS the canonical default in that case.
func canonicalDefaultPath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "wipnote", "projects.json")
	}
	return legacyDefaultPath()
}

// legacyDefaultPath returns the historical pre-XDG path
// (~/.local/share/wipnote/projects.json), independent of XDG_DATA_HOME.
func legacyDefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".local", "share", "wipnote", "projects.json")
	}
	return filepath.Join(home, ".local", "share", "wipnote", "projects.json")
}

// OpenReadOnly opens the SQLite database at dbPath in read-only mode using the
// ?mode=ro URI flag.  No migrations or PRAGMAs are applied — the caller gets a
// raw *sql.DB suitable for SELECT queries only.
//
// The caller is responsible for closing the returned *sql.DB.
func OpenReadOnly(dbPath string) (*sql.DB, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("registry.OpenReadOnly: resolve path: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", abs)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("registry.OpenReadOnly: open: %w", err)
	}
	return db, nil
}

// computeID returns the first 8 hex characters of SHA256(dir).
func computeID(dir string) string {
	return ComputeID(dir)
}

// ComputeID returns the first 8 hex characters of SHA256(dir). It is the
// stable project identifier used by the registry and by the parent server
// to route per-project reverse-proxy traffic (/p/<id>/...).
func ComputeID(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return hex.EncodeToString(sum[:])[:8]
}
