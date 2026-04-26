// Package registry manages a JSON-backed catalog of HtmlGraph projects on the
// local machine.
//
// # File format
//
// The registry is stored as a JSON array of Entry values at DefaultPath()
// (~/.local/share/htmlgraph/projects.json).  A missing file is treated as an
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
	"time"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	_ "modernc.org/sqlite"
)

// Entry represents a single registered HtmlGraph project.
type Entry struct {
	// ID is the first 8 hex characters of SHA256(ProjectDir).
	// It is computed on first Upsert and never changes for a given directory.
	ID string `json:"id"`

	// ProjectDir is the absolute path to the project root (the directory that
	// contains .htmlgraph/).
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
}

// Load reads the registry from path.  If the file does not exist an empty
// Registry is returned with no error.  Any other I/O error is propagated.
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
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

// Save persists the registry to disk using a tempfile + os.Rename so the
// write is atomic from the reader's perspective.
func (r *Registry) Save() error {
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("registry.Save: mkdir %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(r.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("registry.Save: marshal: %w", err)
	}
	data = append(data, '\n')

	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("registry.Save: write tmp: %w", err)
	}
	if err := os.Rename(tmp, r.path); err != nil {
		// Best-effort cleanup of the tmp file on rename failure.
		_ = os.Remove(tmp)
		return fmt.Errorf("registry.Save: rename: %w", err)
	}
	return nil
}

// Upsert inserts or updates the entry for dir.  If an entry with the same
// cleaned absolute path already exists, its LastSeen (and optionally Name /
// GitRemoteURL) is updated and the original ID is preserved.  Otherwise a new
// entry is appended with a freshly computed ID.
func (r *Registry) Upsert(dir, name, remoteURL string) {
	dir = filepath.Clean(dir)
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
// .htmlgraph subdirectory.  It returns the ProjectDir values of the removed
// entries.
func (r *Registry) Prune() []string {
	var pruned []string
	kept := r.entries[:0]
	for _, e := range r.entries {
		if _, err := os.Stat(filepath.Join(e.ProjectDir, ".htmlgraph")); err == nil {
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

// DefaultPath returns the canonical registry file path:
// ~/.local/share/htmlgraph/projects.json
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fallback that will be visible to the caller.
		return filepath.Join(".local", "share", "htmlgraph", "projects.json")
	}
	return filepath.Join(home, ".local", "share", "htmlgraph", "projects.json")
}

// OpenReadOnly opens the SQLite database at dbPath in read-only mode using the
// ?mode=ro URI flag, then applies the same connection-level pragmas as db.Open
// (synchronous, temp_store, mmap_size, cache_size) so reads coexist with the
// per-session otel-collect indexer's writer without hitting SQLITE_BUSY.
//
// busy_timeout is embedded in the DSN so it takes effect on the very first
// connection, before any query runs — mirroring db.Open. journal_mode and
// foreign_keys are intentionally skipped: both are file/writer-owned concerns
// and would error on a mode=ro connection.
//
// The caller is responsible for closing the returned *sql.DB.
func OpenReadOnly(dbPath string) (*sql.DB, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("registry.OpenReadOnly: resolve path: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(5000)", abs)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("registry.OpenReadOnly: open: %w", err)
	}

	pragmas := dbpkg.BuildPragmas(dbPath)
	delete(pragmas, "journal_mode")
	delete(pragmas, "foreign_keys")
	if err := dbpkg.ApplyPragmas(db, pragmas); err != nil {
		db.Close()
		return nil, fmt.Errorf("registry.OpenReadOnly: apply pragmas: %w", err)
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
