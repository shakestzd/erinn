package sdk

import (
	"database/sql"
	"path/filepath"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
)

// ReadIndex abstracts SQLite read-index operations for testability.
// The production implementation wraps *sql.DB; tests can supply a stub.
type ReadIndex interface {
	// InsertFeature inserts a work item row into the features table.
	InsertFeature(f *dbpkg.Feature) error

	// UpdateFeatureStatus changes the status (and updated_at) of a feature row.
	UpdateFeatureStatus(id, status string) error

	// Close releases the underlying database connection.
	Close() error
}

// sqliteIndex is the production ReadIndex backed by *sql.DB.
type sqliteIndex struct {
	db *sql.DB
}

func (s *sqliteIndex) InsertFeature(f *dbpkg.Feature) error {
	return dbpkg.InsertFeature(s.db, f)
}

func (s *sqliteIndex) UpdateFeatureStatus(id, status string) error {
	return dbpkg.UpdateFeatureStatus(s.db, id, status)
}

func (s *sqliteIndex) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// Base holds the shared infrastructure every SDK component needs:
// project directory, agent identity, and optional SQLite read-index.
//
// It is designed to be embedded by SDK and stored by Collection,
// breaking the tight coupling between collections and the full SDK.
type Base struct {
	// ProjectDir is the path to the .htmlgraph/ directory.
	ProjectDir string

	// Agent is the identifier of the agent using this SDK.
	Agent string

	// idx is the optional SQLite read-index.
	idx ReadIndex
}

// NewBase creates a Base with the given project directory, agent name,
// and optional ReadIndex. If idx is nil, dual-writes to SQLite are skipped.
func NewBase(projectDir, agent string, idx ReadIndex) *Base {
	return &Base{
		ProjectDir: projectDir,
		Agent:      agent,
		idx:        idx,
	}
}

// Index returns the ReadIndex (may be nil).
func (b *Base) Index() ReadIndex { return b.idx }

// Close releases the ReadIndex connection.
func (b *Base) Close() error {
	if b.idx != nil {
		return b.idx.Close()
	}
	return nil
}

// HtmlDir returns the absolute path to a named subdirectory
// (e.g. "features", "bugs") under the project directory.
func (b *Base) HtmlDir(sub string) string {
	return filepath.Join(b.ProjectDir, sub)
}

// FeaturesDir returns the path to the features subdirectory.
func (b *Base) FeaturesDir() string { return b.HtmlDir("features") }

// BugsDir returns the path to the bugs subdirectory.
func (b *Base) BugsDir() string { return b.HtmlDir("bugs") }

// SpikesDir returns the path to the spikes subdirectory.
func (b *Base) SpikesDir() string { return b.HtmlDir("spikes") }

// TracksDir returns the path to the tracks subdirectory.
func (b *Base) TracksDir() string { return b.HtmlDir("tracks") }

// DualWriteNode writes an HTML node to disk (via writeHTML callback)
// and best-effort inserts into the SQLite read-index.
// HTML is canonical; SQLite errors are silently ignored.
func (b *Base) DualWriteNode(writeHTML func() (string, error), dbFeat *dbpkg.Feature) (string, error) {
	path, err := writeHTML()
	if err != nil {
		return "", err
	}

	// Best-effort SQLite insert; HTML is canonical.
	if b.idx != nil && dbFeat != nil {
		_ = b.idx.InsertFeature(dbFeat)
	}

	return path, nil
}

// DualWriteStatus best-effort updates the SQLite read-index status.
// The caller is responsible for updating the HTML file first.
func (b *Base) DualWriteStatus(id, status string) {
	if b.idx != nil {
		_ = b.idx.UpdateFeatureStatus(id, status)
	}
}
