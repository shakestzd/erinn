// Package workitem provides internal work item operations for wipnote.
//
// It manages collections for features, bugs, spikes, tracks, and sessions
// with functional options for creation. HTML files and ledgers are the
// canonical store; there is no embedded database read-index.
package workitem

import (
	"fmt"
	"os"
	"path/filepath"
)

// --- Base types --------------------------------------------------------------

// Base holds the shared context needed by all collection operations.
type Base struct {
	// ProjectDir is the path to the .wipnote/ directory.
	ProjectDir string

	// Agent is the identifier of the agent using this package (e.g. "claude-code").
	Agent string

	// AgentID is the unique agent identity for per-agent claim attribution.
	// Empty string means orchestrator (main session). Subagents have a
	// non-empty ID set via WIPNOTE_AGENT_ID.
	AgentID string
}

// --- Project -----------------------------------------------------------------

// Project is the main entry point for interacting with an wipnote project.
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

// Open creates a new Project instance and initialises all collection accessors.
//
// projectDir must point to a .wipnote/ directory.
// agent identifies the calling agent for work attribution.
func Open(projectDir, agent string) (*Project, error) {
	if projectDir == "" {
		return nil, fmt.Errorf("projectDir must not be empty")
	}
	if agent == "" {
		return nil, fmt.Errorf("agent must not be empty")
	}

	base := &Base{
		ProjectDir: projectDir,
		Agent:      agent,
		AgentID:    os.Getenv("WIPNOTE_AGENT_ID"), // "" for orchestrator
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

// Close is retained for caller symmetry. Project no longer owns external
// resources.
func (p *Project) Close() error {
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
