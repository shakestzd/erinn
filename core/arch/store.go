package arch

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store manages arch cards on disk under <wipnoteDir>/arch/.
type Store struct {
	dir string // absolute path to .wipnote/arch/
}

// NewStore returns a Store rooted at the arch subdirectory of wipnoteDir.
// It creates the directory if it does not exist.
func NewStore(wipnoteDir string) (*Store, error) {
	dir := filepath.Join(wipnoteDir, "arch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create arch dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the absolute path to the arch directory.
func (s *Store) Dir() string { return s.dir }

// cardPath returns the file path for a card slug.
func (s *Store) cardPath(slug string) string {
	return filepath.Join(s.dir, slug+".md")
}

// Get reads and parses the card with the given slug.
// Returns ErrNotFound when the file does not exist.
// Returns an error when slug is not a valid slug (path traversal prevention).
func (s *Store) Get(slug string) (*Card, error) {
	if !isValidSlug(slug) {
		return nil, fmt.Errorf("invalid slug %q: must contain only lowercase letters, digits, and hyphens", slug)
	}
	return ParseFile(s.cardPath(slug))
}

// List returns all cards in the store.
// When includeRetired is false, superseded/retired cards are omitted.
func (s *Store) List(includeRetired bool) ([]*Card, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read arch dir: %w", err)
	}

	var cards []*Card
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		card, err := ParseFile(path)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if !includeRetired && card.IsRetired() {
			continue
		}
		cards = append(cards, card)
	}
	return cards, nil
}

// Create validates and writes a new card.
// Returns ErrDuplicateSlug when a card with the same name already exists.
// Returns ErrDuplicateGlobSet when an existing active card has the exact same
// set of non-empty path globs (order-insensitive). Empty glob sets are exempt.
func (s *Store) Create(card *Card) error {
	if err := Validate(card); err != nil {
		return err
	}
	path := s.cardPath(card.Name)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrDuplicateSlug, card.Name)
	}
	if len(card.Paths) > 0 {
		if dup, err := s.findGlobSetDuplicate(card); err != nil {
			return err
		} else if dup != "" {
			return fmt.Errorf("%w: same paths as %q", ErrDuplicateGlobSet, dup)
		}
	}
	now := time.Now().UTC()
	card.CreatedAt = now
	card.UpdatedAt = now
	return s.write(card)
}

// findGlobSetDuplicate returns the slug of an existing active card whose path
// glob set is equal (order-insensitive) to card.Paths, or "" if none.
func (s *Store) findGlobSetDuplicate(card *Card) (string, error) {
	existing, err := s.List(false) // active cards only
	if err != nil {
		return "", err
	}
	target := normalizeGlobSet(card.Paths)
	for _, c := range existing {
		if len(c.Paths) == 0 {
			continue
		}
		if c.Name == card.Name {
			continue
		}
		if normalizeGlobSet(c.Paths) == target {
			return c.Name, nil
		}
	}
	return "", nil
}

// normalizeGlobSet sorts and joins a glob slice into a canonical string for
// order-insensitive equality comparison.
func normalizeGlobSet(paths []string) string {
	cp := make([]string, len(paths))
	copy(cp, paths)
	sort.Strings(cp)
	return strings.Join(cp, "\x00")
}

// Update validates and overwrites an existing card.
// Returns ErrNotFound when the card does not exist.
// Returns ErrDuplicateGlobSet when another active card has the exact same
// set of non-empty path globs (order-insensitive). Empty glob sets are exempt.
func (s *Store) Update(card *Card) error {
	if err := Validate(card); err != nil {
		return err
	}
	path := s.cardPath(card.Name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrNotFound, card.Name)
	}
	if len(card.Paths) > 0 {
		if dup, err := s.findGlobSetDuplicate(card); err != nil {
			return err
		} else if dup != "" {
			return fmt.Errorf("%w: same paths as %q", ErrDuplicateGlobSet, dup)
		}
	}
	card.UpdatedAt = time.Now().UTC()
	return s.write(card)
}

// Deprecate retires the named card.
// When supersededBy is non-empty the card's superseded_by field is set.
// When supersededBy is empty the card is retired outright (retired: true).
func (s *Store) Deprecate(slug, supersededBy string) error {
	card, err := s.Get(slug)
	if err != nil {
		return err
	}
	if supersededBy != "" && !isValidSlug(supersededBy) {
		return fmt.Errorf("superseded_by %q is not a valid slug", supersededBy)
	}
	if supersededBy == slug {
		return fmt.Errorf("superseded_by must differ from the card slug %q", slug)
	}
	if supersededBy != "" {
		card.SupersededBy = supersededBy
	} else {
		card.Retired = true
	}
	card.UpdatedAt = time.Now().UTC()
	return s.write(card)
}

// write marshals a card and writes it to disk atomically (write temp + rename).
func (s *Store) write(card *Card) error {
	data, err := Marshal(card)
	if err != nil {
		return err
	}
	path := s.cardPath(card.Name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write card: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename card: %w", err)
	}
	return nil
}

// ValidateAll parses and validates every card in the store.
// Returns a map from slug to validation error for any card that fails.
func (s *Store) ValidateAll() (map[string]error, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read arch dir: %w", err)
	}

	errs := make(map[string]error)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".md")
		path := filepath.Join(s.dir, e.Name())
		card, parseErr := ParseFile(path)
		if parseErr != nil {
			errs[slug] = parseErr
			continue
		}
		if valErr := Validate(card); valErr != nil {
			errs[slug] = valErr
		}
	}
	return errs, nil
}
