// Package guardprofile owns the committed project guard profile stored at
// .wipnote/guard-profile.yaml. It provides the schema, a loader, validation,
// and a deterministic content signature.
//
// This package is intentionally self-contained: it does NOT wire guards into
// any gate, perform discovery/launcher integration, or render docs. It only
// models the profile and computes its canonical signature so that later slices
// can decide whether the committed profile has been approved by a human.
package guardprofile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// RelPath is the repo-root-relative location of the committed guard profile.
const RelPath = ".wipnote/guard-profile.yaml"

// Phase names. The fixed canonical ordering of phases is enforced by
// canonicalPhaseOrder in signature.go.
const (
	PhaseQuality    = "quality"
	PhaseCompletion = "completion"
	PhaseYolo       = "yolo"
)

// validPhases is the set of phase keys allowed under guards:.
var validPhases = map[string]bool{
	PhaseQuality:    true,
	PhaseCompletion: true,
	PhaseYolo:       true,
}

// AppliesWhen narrows when a guard runs. v1 supports path globs only. An
// omitted AppliesWhen (nil) means the guard always applies.
type AppliesWhen struct {
	Paths []string `yaml:"paths,omitempty"`
}

// Guard is a single command to run during a phase.
type Guard struct {
	Name        string       `yaml:"name"`
	Cmd         string       `yaml:"cmd"`
	Cwd         string       `yaml:"cwd,omitempty"`
	AppliesWhen *AppliesWhen `yaml:"applies_when,omitempty"`
}

// Approval is the human-approval marker. It is EXCLUDED from the content
// signature: the signature signs the guard content, not its own approval.
type Approval struct {
	Signature string `yaml:"signature,omitempty"`
	By        string `yaml:"by,omitempty"`
	At        string `yaml:"at,omitempty"`
}

// Profile is the full guard-profile document.
//
// Guards is a phase-keyed map (rather than a fixed struct) so that unknown
// phase keys survive unmarshalling and can be rejected by Validate. Use the
// known Phase* constants as keys.
type Profile struct {
	Guards   map[string][]Guard `yaml:"guards"`
	Approved Approval           `yaml:"approved,omitempty"`
}

// Load reads .wipnote/guard-profile.yaml under repoRoot. A missing file
// returns (nil, nil) — absence of a profile is not an error. Any other read or
// parse failure is returned as an error.
func Load(repoRoot string) (*Profile, error) {
	path := filepath.Join(repoRoot, filepath.FromSlash(RelPath))
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read guard profile: %w", err)
	}
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse guard profile %s: %w", path, err)
	}
	return &p, nil
}

// Validate rejects malformed profiles: any unknown phase key under guards:, or
// any guard with an empty cmd. A nil profile is valid (no profile present).
func Validate(p *Profile) error {
	if p == nil {
		return nil
	}
	for phase, guards := range p.Guards {
		if !validPhases[phase] {
			return fmt.Errorf("guardprofile: unknown phase %q", phase)
		}
		for i, g := range guards {
			// name is required: gate output, provenance, and the canonical
			// signature's per-guard sort key all rely on it (roborev #3716).
			if strings.TrimSpace(g.Name) == "" {
				return fmt.Errorf("guardprofile: guard #%d in phase %q is missing name", i, phase)
			}
			if strings.TrimSpace(g.Cmd) == "" {
				return fmt.Errorf("guardprofile: guard %q in phase %q is missing cmd", g.Name, phase)
			}
		}
	}
	return nil
}
