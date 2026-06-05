package guardprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"sort"
	"strings"
)

// canonicalPhaseOrder fixes the order in which phases are encoded into the
// canonical byte string. This order is part of the signature contract and must
// never change.
var canonicalPhaseOrder = []string{PhaseQuality, PhaseCompletion, PhaseYolo}

// Canonicalization delimiters. These are control characters that cannot appear
// in normal guard text, so they unambiguously separate fields/records.
const (
	fieldSep = "\x00" // within a guard tuple (name\0cmd\0cwd\0appliesWhen)
	pathSep  = "\x1f" // between paths inside applies_when
	guardSep = "\x1e" // between guards within a phase
	phaseSep = "\x1d" // between phases
)

// canonical builds the deterministic byte string that the signature hashes.
//
// Algorithm (MUST stay exact and stable across platforms and YAML formatting):
//
//  1. Phases are emitted in the FIXED order: quality, completion, yolo.
//     Unknown/extra phase keys are ignored here (Validate rejects them).
//  2. Within each phase, guards are sorted by Name (lexicographic, byte order).
//  3. Each guard is encoded as the NUL(\0)-joined tuple:
//     name \0 cmd \0 cwd \0 appliesWhenNormalized
//  4. appliesWhenNormalized: applies_when.paths are forward-slash normalized
//     (path.Clean over the slash form) and sorted, then joined with pathSep
//     (\x1f). A nil/omitted applies_when yields the empty string — identical to
//     an applies_when with no paths, since both mean "always applies".
//  5. Guards within a phase are joined with guardSep (\x1e); phases are joined
//     with phaseSep (\x1d).
//  6. The approved: block is EXCLUDED entirely — the signature signs guard
//     content, not its own approval marker.
//
// Because the model is map-backed and we iterate phases/guards in a fully
// sorted, fixed order, the result is independent of YAML key order, guard list
// order, comments, and whitespace.
func canonical(p *Profile) []byte {
	var phaseParts []string
	for _, phase := range canonicalPhaseOrder {
		guards := append([]Guard(nil), p.Guards[phase]...)
		var guardParts []string
		for _, g := range guards {
			tuple := strings.Join([]string{
				g.Name,
				g.Cmd,
				g.Cwd,
				normalizeAppliesWhen(g.AppliesWhen),
			}, fieldSep)
			guardParts = append(guardParts, tuple)
		}
		// Sort by the FULL canonical tuple, not just Name. Validate does not
		// require guard names to be unique, so two same-name guards with
		// different cmds must still sort deterministically — sorting by Name
		// alone would let their YAML order leak into the signature, breaking
		// the order-independence contract (roborev finding on c021945).
		sort.Strings(guardParts)
		phaseParts = append(phaseParts, strings.Join(guardParts, guardSep))
	}
	return []byte(strings.Join(phaseParts, phaseSep))
}

// normalizeAppliesWhen renders applies_when into a stable string: paths are
// forward-slash cleaned and sorted, then joined with pathSep. Nil yields "".
func normalizeAppliesWhen(aw *AppliesWhen) string {
	if aw == nil || len(aw.Paths) == 0 {
		return ""
	}
	norm := make([]string, len(aw.Paths))
	for i, raw := range aw.Paths {
		norm[i] = path.Clean(strings.ReplaceAll(raw, "\\", "/"))
	}
	sort.Strings(norm)
	return strings.Join(norm, pathSep)
}

// Signature returns "sha256:" + hex(sha256(canonical(p))). A nil profile
// signs the empty canonical form.
func Signature(p *Profile) string {
	if p == nil {
		p = &Profile{}
	}
	sum := sha256.Sum256(canonical(p))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// IsApproved reports whether the recorded approved.signature matches the
// current content signature. A profile with no recorded signature is never
// approved.
func IsApproved(p *Profile) bool {
	if p == nil {
		return false
	}
	if p.Approved.Signature == "" {
		return false
	}
	// An INVALID profile is never "approved". Canonicalization ignores unknown
	// phase keys, so a typo like `completions:` could otherwise keep the old
	// signature matching while the gates reject the profile and fall back to
	// autodetection — leaving IsApproved (and the `guard init` no-op path) saying
	// "approved" while gates disagree (roborev #3708). Requiring validity keeps
	// the two consistent.
	if Validate(p) != nil {
		return false
	}
	return Signature(p) == p.Approved.Signature
}
