// Package workitem provides internal work item operations for HtmlGraph.
//
// It manages collections for features, bugs, spikes, tracks, and sessions
// with functional options for creation and a dual-write strategy
// (HTML canonical, SQLite read-index).
package workitem

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
	"github.com/shakestzd/htmlgraph/internal/storage"
)

// --- Base types --------------------------------------------------------------

// Base holds the shared context needed by all collection operations.
type Base struct {
	// ProjectDir is the path to the .htmlgraph/ directory.
	ProjectDir string

	// Agent is the identifier of the agent using this package (e.g. "claude-code").
	Agent string

	// AgentID is the unique agent identity for per-agent claim attribution.
	// Empty string means orchestrator (main session). Subagents have a
	// non-empty ID set via HTMLGRAPH_AGENT_ID.
	AgentID string

	// DB is the optional SQLite database (read index).
	DB *sql.DB
}

// --- Project -----------------------------------------------------------------

// Project is the main entry point for interacting with an HtmlGraph project.
type Project struct {
	*Base

	// Collection accessors
	Features *FeatureCollection
	Bugs     *BugCollection
	Spikes   *SpikeCollection
	Tracks   *TrackCollection
	Sessions *SessionCollection
	Plans    *PlanCollection
	Specs    *SpecCollection
}

// Open creates a new Project instance, opens the SQLite database, and
// initialises all collection accessors.
//
// projectDir must point to a .htmlgraph/ directory.
// agent identifies the calling agent for work attribution.
func Open(projectDir, agent string) (*Project, error) {
	if projectDir == "" {
		return nil, fmt.Errorf("projectDir must not be empty")
	}
	if agent == "" {
		return nil, fmt.Errorf("agent must not be empty")
	}

	// Note: ProjectDir field is the .htmlgraph directory (caller convention),
	// but storage.CanonicalDBPath wants the actual project root.
	dbPath, err := storage.CanonicalDBPath(filepath.Dir(projectDir))
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}
	if err := storage.EnsureDBDir(dbPath); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	base := &Base{
		ProjectDir: projectDir,
		Agent:      agent,
		AgentID:    os.Getenv("HTMLGRAPH_AGENT_ID"), // "" for orchestrator
		DB:         database,
	}

	p := &Project{Base: base}

	p.Features = NewFeatureCollection(base)
	p.Bugs = NewBugCollection(base)
	p.Spikes = NewSpikeCollection(base)
	p.Tracks = NewTrackCollection(base)
	p.Sessions = NewSessionCollection(base)
	p.Plans = NewPlanCollection(base)
	p.Specs = NewSpecCollection(base)

	return p, nil
}

// Close releases the SQLite database connection.
func (p *Project) Close() error {
	if p.DB != nil {
		return p.DB.Close()
	}
	return nil
}

// FeaturesDir returns the path to the features subdirectory.
func (p *Project) FeaturesDir() string {
	return filepath.Join(p.ProjectDir, "features")
}

// BugsDir returns the path to the bugs subdirectory.
func (p *Project) BugsDir() string {
	return filepath.Join(p.ProjectDir, "bugs")
}

// SpikesDir returns the path to the spikes subdirectory.
func (p *Project) SpikesDir() string {
	return filepath.Join(p.ProjectDir, "spikes")
}

// TracksDir returns the path to the tracks subdirectory.
func (p *Project) TracksDir() string {
	return filepath.Join(p.ProjectDir, "tracks")
}

// PlansDir returns the path to the plans subdirectory.
func (p *Project) PlansDir() string {
	return filepath.Join(p.ProjectDir, "plans")
}

// SpecsDir returns the path to the specs subdirectory.
func (p *Project) SpecsDir() string {
	return filepath.Join(p.ProjectDir, "specs")
}
