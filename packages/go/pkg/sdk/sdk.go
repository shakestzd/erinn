// Package sdk provides the public HtmlGraph SDK for Go consumers.
//
// It mirrors the Python SDK from htmlgraph.sdk, offering collections
// for features, bugs, spikes, tracks, and sessions with functional
// options for creation and a dual-write strategy (HTML canonical,
// SQLite read-index).
//
// Usage:
//
//	s, err := sdk.New("/path/to/.htmlgraph", "my-agent")
//	if err != nil { log.Fatal(err) }
//	defer s.Close()
//
//	feat, err := s.Features.Create("My Feature",
//	    sdk.FeatWithPriority("high"),
//	    sdk.FeatWithTrack("trk-abc"),
//	    sdk.FeatWithSteps("Step 1", "Step 2"),
//	)
package sdk

import (
	"fmt"
	"path/filepath"

	dbpkg "github.com/shakestzd/htmlgraph/internal/db"
)

// SDK is the main entry point for interacting with an HtmlGraph project.
// It embeds *Base for shared infrastructure (directory paths, agent identity,
// database connection) and exposes typed collection accessors.
type SDK struct {
	*Base

	// Collection accessors
	Features *FeatureCollection
	Bugs     *BugCollection
	Spikes   *SpikeCollection
	Tracks   *TrackCollection
	Sessions *SessionCollection
}

// New creates a new SDK instance, opens the SQLite database, and
// initialises all collection accessors.
//
// projectDir must point to a .htmlgraph/ directory.
// agent identifies the calling agent for work attribution.
func New(projectDir, agent string) (*SDK, error) {
	if projectDir == "" {
		return nil, fmt.Errorf("projectDir must not be empty")
	}
	if agent == "" {
		return nil, fmt.Errorf("agent must not be empty")
	}

	dbPath := filepath.Join(projectDir, "htmlgraph.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	base := NewBase(projectDir, agent, &sqliteIndex{db: database})

	s := &SDK{
		Base: base,
	}

	s.Features = NewFeatureCollection(s)
	s.Bugs = NewBugCollection(s)
	s.Spikes = NewSpikeCollection(s)
	s.Tracks = NewTrackCollection(s)
	s.Sessions = NewSessionCollection(s)

	return s, nil
}
