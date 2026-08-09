// Package readsrv serves the daemon read protocol from CANONICAL work-item
// state — the `.wipnote/**/*.html` files — rather than from the derived SQLite
// index.
//
// It lives outside core/daemon on purpose: core/daemon is transport, and the
// HTML parser and its dependencies have no business being linked into every
// process that merely dials the socket. core/daemon exposes a Reader function
// type; this package supplies one.
package readsrv

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shakestzd/wipnote/core/daemon"
	"github.com/shakestzd/wipnote/core/htmlparse"
	"github.com/shakestzd/wipnote/core/models"
)

// INVALIDATION DESIGN (feat-f6759e37).
//
// The daemon holds parsed canonical state while agents rewrite work items
// constantly — every `feature start`, every `complete`. Serving a stale status
// to a guard is the one failure this must not introduce, so the invalidation
// story is the design, not a detail of it.
//
// THE CHOICE: revalidate by stat on every read, per file, with a
// racily-clean window. Not file watching, and not invalidation driven by the
// write ops the daemon already sees.
//
// WHY NOT WRITE-PATH INVALIDATION. It is structurally incomplete here. The
// daemon deliberately does NOT see canonical file writes — that is a standing
// architectural decision (arch card
// `do-not-route-file-writes-through-the-daemon`): the write socket carries
// derived-index ops, and canonical HTML is written directly under a file lock.
// So the daemon's write stream is a lagging, partial shadow of canonical
// change. It would miss an agent editing the HTML with its own Edit tool, a
// `git checkout` that rewrites hundreds of items at once, and a sibling
// worktree completing an item. Building invalidation on a signal that misses
// the majority case would produce a cache that is fast and wrong.
//
// WHY NOT FILE WATCHING. It buys nothing the stat already provides, and costs
// a dependency, a platform matrix, a debounce policy, and a race whose failure
// direction is stale-serve: a watcher event that lands after a read has already
// been answered is a stale answer that nothing corrects. Its only advantage is
// avoiding the stat, and the stat is ~1-2ms against a parse of ~100ms.
//
// WHY STAT IS ENOUGH, AND WHERE IT ISN'T. An entry is served from memory only
// when the file's (mtime, size) still match what it was parsed from. Path
// resolution is deterministic from the ID, so a point lookup costs ONE stat,
// not a directory scan. A list costs one ReadDir plus one stat per entry, which
// also catches files added and removed — the cases a per-file check alone
// cannot see.
//
// The hole in naive (mtime, size) checking is the classic same-tick rewrite:
// a file rewritten within the filesystem's timestamp granularity of the
// observation can present an unchanged mtime, and a status flip can leave the
// size unchanged too ("todo" and "done" are both four bytes). Modern
// filesystems record nanoseconds, but correctness must not rest on that.
//
// So an entry parsed while its own mtime is still within racyWindow of now is
// marked UNSTABLE and is never served from memory — it is re-parsed on the next
// read, and stays unstable until the tick it was written in has demonstrably
// closed. This is the same "racily clean" rule git applies to its index, and it
// closes the window without any assumption about timestamp resolution. The cost
// is re-parsing ONE file for a couple of seconds after it changes, which is
// exactly the moment a guard is most likely to ask about it.

// racyWindow is how long after a file's mtime its parse is treated as
// unstable. Two seconds covers a one-second-granularity filesystem with margin.
const racyWindow = 2 * time.Second

// entry is one parsed work item plus the file identity it was parsed from.
type entry struct {
	item daemon.WorkItem
	// createdAt backs the deterministic list ordering. Kept out of
	// daemon.WorkItem so the wire projection stays narrow.
	createdAt time.Time

	// path, mtime and size are the revalidation basis. All three must still
	// match for the entry to be served from memory: path guards the flat-vs-
	// subdirectory layouts resolving differently between reads.
	path  string
	mtime time.Time
	size  int64

	// dir is the collection directory this entry was found in, so a list scan
	// can prune entries whose files have disappeared without touching entries
	// from directories it did not scan.
	dir string

	// unstable marks an entry parsed within racyWindow of its own mtime. Such
	// an entry is never served from memory. See the invalidation design above.
	unstable bool
}

// Cache holds parsed canonical work items keyed by ID.
//
// It is safe for concurrent use: reads arrive on per-connection goroutines.
type Cache struct {
	wipnoteDir string

	// now is injectable so tests can drive the racily-clean window
	// deterministically instead of sleeping.
	now func() time.Time

	mu   sync.Mutex
	byID map[string]*entry
}

// NewCache returns a Cache reading canonical state from wipnoteDir (the
// `.wipnote` directory itself, not the project root).
func NewCache(wipnoteDir string) *Cache {
	return &Cache{
		wipnoteDir: wipnoteDir,
		now:        time.Now,
		byID:       make(map[string]*entry),
	}
}

// collectionForID maps a work-item ID prefix to its canonical collection
// directory. An unknown prefix returns "" — the caller reports not-found
// rather than guessing a directory.
func collectionForID(id string) string {
	switch {
	case strings.HasPrefix(id, "feat-"):
		return "features"
	case strings.HasPrefix(id, "bug-"):
		return "bugs"
	case strings.HasPrefix(id, "spk-"):
		return "spikes"
	case strings.HasPrefix(id, "trk-"):
		return "tracks"
	default:
		return ""
	}
}

// allCollections is the set a list scan covers when no type filter narrows it.
var allCollections = []string{"features", "bugs", "spikes", "tracks"}

// collectionForType maps a wire type filter ("feature", "bug", …) to its
// directory.
func collectionForType(t string) string {
	switch t {
	case "feature":
		return "features"
	case "bug":
		return "bugs"
	case "spike":
		return "spikes"
	case "track":
		return "tracks"
	default:
		return ""
	}
}

// resolvePath returns the canonical file path for id, supporting both the flat
// (`<id>.html`) and subdirectory (`<id>/index.html`) layouts that
// graph.LoadDir accepts. ok is false when neither exists.
func (c *Cache) resolvePath(id string) (path string, info os.FileInfo, ok bool) {
	coll := collectionForID(id)
	if coll == "" {
		return "", nil, false
	}
	dir := filepath.Join(c.wipnoteDir, coll)
	flat := filepath.Join(dir, id+".html")
	if fi, err := os.Stat(flat); err == nil && !fi.IsDir() {
		return flat, fi, true
	}
	nested := filepath.Join(dir, id, "index.html")
	if fi, err := os.Stat(nested); err == nil && !fi.IsDir() {
		return nested, fi, true
	}
	return "", nil, false
}

// Get resolves one work item from canonical state. hit reports whether the
// answer came from memory (true) or from a fresh parse (false); it is
// diagnostic and lets invalidation be tested without inspecting internals.
//
// A missing file DROPS any cached entry for that ID and reports not-found: a
// deleted or archived item must never keep answering from memory.
func (c *Cache) Get(id string) (item daemon.WorkItem, found bool, hit bool) {
	if id == "" {
		return daemon.WorkItem{}, false, false
	}
	path, info, ok := c.resolvePath(id)
	if !ok {
		c.mu.Lock()
		delete(c.byID, id)
		c.mu.Unlock()
		return daemon.WorkItem{}, false, false
	}

	c.mu.Lock()
	cached, have := c.byID[id]
	if have && c.entryValid(cached, path, info) {
		out := cached.item
		c.mu.Unlock()
		return out, true, true
	}
	c.mu.Unlock()

	e, err := c.parse(path, info, filepath.Dir(path))
	if err != nil {
		// Unparseable canonical file: drop any cached entry and report
		// not-found. Serving the previous parse of a file that no longer
		// parses would be exactly the stale answer this design forbids.
		c.mu.Lock()
		delete(c.byID, id)
		c.mu.Unlock()
		return daemon.WorkItem{}, false, false
	}

	c.mu.Lock()
	c.byID[e.item.ID] = e
	c.mu.Unlock()
	return e.item, true, false
}

// entryValid reports whether a cached entry may be served for the file
// described by path/info. Caller holds c.mu.
func (c *Cache) entryValid(e *entry, path string, info os.FileInfo) bool {
	if e.unstable {
		return false
	}
	if e.path != path || e.size != info.Size() {
		return false
	}
	return e.mtime.Equal(info.ModTime())
}

// parse reads and parses one canonical file into an entry, stamping the
// revalidation basis and the racily-clean flag.
func (c *Cache) parse(path string, info os.FileInfo, dir string) (*entry, error) {
	node, err := htmlparse.ParseFile(path)
	if err != nil {
		return nil, err
	}
	mtime := info.ModTime()
	return &entry{
		item:      projectItem(node),
		createdAt: node.CreatedAt,
		path:      path,
		mtime:     mtime,
		size:      info.Size(),
		dir:       dir,
		unstable:  c.now().Sub(mtime) < racyWindow,
	}, nil
}

// projectItem narrows a parsed node to the wire projection. Keeping this in one
// place is what stops the read protocol from drifting into a second copy of the
// index.
func projectItem(n *models.Node) daemon.WorkItem {
	return daemon.WorkItem{
		ID:      n.ID,
		Type:    n.Type,
		Status:  string(n.Status),
		Title:   n.Title,
		TrackID: n.TrackID,
	}
}

// scanResult is the outcome of revalidating one collection directory.
type scanResult struct {
	items  []daemon.WorkItem
	hits   int
	misses int
}

// scanCollections revalidates every work item in the named collection
// directories and returns the current set.
//
// The directory read is what makes ADD and REMOVE visible: a per-file stat can
// only tell you about files you already know. Entries whose files have vanished
// from a scanned directory are pruned here, so a completed-and-archived item
// cannot linger in memory and answer a later point lookup.
func (c *Cache) scanCollections(colls []string) scanResult {
	var out scanResult
	// sortable carries createdAt alongside the wire item so ordering does not
	// need a second pass over the cache under lock.
	type sortable struct {
		item      daemon.WorkItem
		createdAt time.Time
	}
	var rows []sortable

	for _, coll := range colls {
		dir := filepath.Join(c.wipnoteDir, coll)
		entries, err := os.ReadDir(dir)
		if err != nil {
			// A missing collection directory is normal (a project with no
			// spikes yet), not an error.
			continue
		}
		seen := make(map[string]struct{}, len(entries))
		for _, de := range entries {
			path, info, ok := resolveDirEntry(dir, de)
			if !ok {
				continue
			}
			id := idForPath(dir, path)
			if id == "" {
				continue
			}
			seen[id] = struct{}{}

			c.mu.Lock()
			cached, have := c.byID[id]
			if have && c.entryValid(cached, path, info) {
				rows = append(rows, sortable{cached.item, cached.createdAt})
				c.mu.Unlock()
				out.hits++
				continue
			}
			c.mu.Unlock()

			e, err := c.parse(path, info, dir)
			if err != nil {
				c.mu.Lock()
				delete(c.byID, id)
				c.mu.Unlock()
				continue
			}
			out.misses++
			c.mu.Lock()
			c.byID[e.item.ID] = e
			c.mu.Unlock()
			rows = append(rows, sortable{e.item, e.createdAt})
		}
		c.pruneMissing(dir, seen)
	}

	sortRows := func(a, b sortable) int {
		// in-progress first, then by type, then newest first — the ordering
		// the index-backed queries produced, reproduced deterministically so
		// callers never depend on filesystem order.
		if r := statusRank(a.item.Status) - statusRank(b.item.Status); r != 0 {
			return r
		}
		if r := typeRank(a.item.Type) - typeRank(b.item.Type); r != 0 {
			return r
		}
		switch {
		case a.createdAt.After(b.createdAt):
			return -1
		case a.createdAt.Before(b.createdAt):
			return 1
		}
		return strings.Compare(a.item.ID, b.item.ID)
	}
	slices.SortStableFunc(rows, sortRows)

	out.items = make([]daemon.WorkItem, 0, len(rows))
	for _, r := range rows {
		out.items = append(out.items, r.item)
	}
	return out
}

// pruneMissing drops cached entries that belonged to dir but were not seen in
// the latest scan of it.
func (c *Cache) pruneMissing(dir string, seen map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, e := range c.byID {
		if e.dir != dir {
			continue
		}
		if _, ok := seen[id]; !ok {
			delete(c.byID, id)
		}
	}
}

// resolveDirEntry maps one directory entry to a canonical file path plus its
// stat, mirroring graph.LoadDir's flat-and-subdirectory acceptance.
func resolveDirEntry(dir string, de os.DirEntry) (string, os.FileInfo, bool) {
	if de.IsDir() {
		path := filepath.Join(dir, de.Name(), "index.html")
		fi, err := os.Stat(path)
		if err != nil || fi.IsDir() {
			return "", nil, false
		}
		return path, fi, true
	}
	if !strings.HasSuffix(de.Name(), ".html") {
		return "", nil, false
	}
	fi, err := de.Info()
	if err != nil {
		return "", nil, false
	}
	return filepath.Join(dir, de.Name()), fi, true
}

// idForPath derives the work-item ID a canonical path represents, for both
// layouts.
func idForPath(dir, path string) string {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return ""
	}
	if filepath.Base(rel) == "index.html" {
		return filepath.Dir(rel)
	}
	return strings.TrimSuffix(rel, ".html")
}

// statusRank orders in-progress ahead of everything else.
func statusRank(s string) int {
	if s == "in-progress" {
		return 0
	}
	return 1
}

// typeRank orders features, then bugs, then everything else.
func typeRank(t string) int {
	switch t {
	case "feature":
		return 0
	case "bug":
		return 1
	default:
		return 2
	}
}
