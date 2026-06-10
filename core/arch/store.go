package arch

import (
	"fmt"
	"os"
	"path/filepath"
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
func (s *Store) Get(slug string) (*Card, error) {
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
func (s *Store) Create(card *Card) error {
	if err := Validate(card); err != nil {
		return err
	}
	path := s.cardPath(card.Name)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrDuplicateSlug, card.Name)
	}
	now := time.Now().UTC()
	card.CreatedAt = now
	card.UpdatedAt = now
	return s.write(card)
}

// Update validates and overwrites an existing card.
// Returns ErrNotFound when the card does not exist.
func (s *Store) Update(card *Card) error {
	if err := Validate(card); err != nil {
		return err
	}
	path := s.cardPath(card.Name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrNotFound, card.Name)
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
