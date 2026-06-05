package guardprofile

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ResolveGuards loads the committed guard profile under repoRoot and, when it
// is APPROVED, returns the guards configured for the named phase. The returned
// guards honor applies_when{paths} filtering (a guard is dropped when its path
// globs match no repo-relative file under repoRoot).
//
// Trust boundary: an absent profile, an UNAPPROVED profile, or an edited
// profile whose recorded approval signature no longer matches its content all
// resolve to (nil, false, nil) so callers fall back to manifest autodetection.
// A malformed profile (Validate fails) is also treated as absent — it is a
// configuration error that must never silently gate completions, so callers
// keep their existing behaviour. An unreadable/unparseable file IS returned as
// an error so the caller can surface it.
//
// usedProfile is true ONLY when an approved, valid profile supplied the guards
// (even if the phase has zero guards) — that signals the caller to trust the
// profile and skip autodetection.
func ResolveGuards(repoRoot, phase string) (guards []Guard, usedProfile bool, err error) {
	p, err := Load(repoRoot)
	if err != nil {
		return nil, false, err
	}
	if p == nil {
		return nil, false, nil
	}
	if err := Validate(p); err != nil {
		// Malformed committed profile: do not honor it, do not error out the
		// gate. Fall back to autodetection (caller emits the setup hint).
		return nil, false, nil
	}
	if !IsApproved(p) {
		return nil, false, nil
	}

	selected := make([]Guard, 0, len(p.Guards[phase]))
	for _, g := range p.Guards[phase] {
		ok, filterErr := guardAppliesTo(repoRoot, g)
		if filterErr != nil {
			return nil, false, filterErr
		}
		if ok {
			selected = append(selected, g)
		}
	}
	return selected, true, nil
}

// guardAppliesTo reports whether a guard's applies_when{paths} filter matches
// at least one repo-relative file under repoRoot. A guard with no applies_when
// (or no paths) always applies.
func guardAppliesTo(repoRoot string, g Guard) (bool, error) {
	if g.AppliesWhen == nil || len(g.AppliesWhen.Paths) == 0 {
		return true, nil
	}
	for _, raw := range g.AppliesWhen.Paths {
		matched, err := anyRepoFileMatches(repoRoot, raw)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

// anyRepoFileMatches walks repoRoot and reports whether any repo-relative path
// matches the forward-slash glob. The glob is matched against the cleaned,
// forward-slash repo-relative path of each file using path.Match semantics.
func anyRepoFileMatches(repoRoot, glob string) (bool, error) {
	clean := path.Clean(strings.ReplaceAll(glob, "\\", "/"))
	found := false
	walkErr := filepath.WalkDir(repoRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			// Skip unreadable entries rather than failing the whole gate.
			return nil //nolint:nilerr
		}
		if found {
			return filepath.SkipDir
		}
		if d.IsDir() {
			// Skip VCS and dependency noise to keep the walk bounded.
			name := d.Name()
			if p != repoRoot && (name == ".git" || name == "node_modules" || name == "vendor" || name == "target") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(repoRoot, p)
		if relErr != nil {
			return nil //nolint:nilerr
		}
		rel = filepath.ToSlash(rel)
		if matchGlob(clean, rel) {
			found = true
		}
		return nil
	})
	if walkErr != nil {
		return false, fmt.Errorf("guardprofile: scan repo for %q: %w", glob, walkErr)
	}
	return found, nil
}

// matchGlob matches a forward-slash glob against a repo-relative path with
// doublestar semantics: a "**" segment matches zero or more path segments, and
// any other segment is matched with path.Match (so "*" does not cross "/").
// E.g. "internal/**/*.go" matches both "internal/foo.go" and
// "internal/pkg/sub/foo.go". (roborev #3684: the prior "collapse **" approach
// dropped the intermediate segments and never matched nested files.)
func matchGlob(glob, rel string) bool {
	return matchSegments(strings.Split(glob, "/"), strings.Split(rel, "/"))
}

// matchSegments matches pattern segments against name segments, treating "**"
// as a wildcard over zero or more whole segments.
func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true // trailing "**" matches any remainder (incl. none)
			}
			// "**" matches zero or more leading segments of name; try each split.
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}
