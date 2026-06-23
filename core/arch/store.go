package arch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	importYAMLCardExt = ".yaml"
	legacyMDCardExt   = ".md"
)

// Store manages import-compatible arch cards on disk under <wipnoteDir>/arch/.
type Store struct {
	dir        string // absolute path to .wipnote/arch/
	ledgerPath string // absolute path to .wipnote/architecture.html
}

// NewStore returns a Store rooted at the arch subdirectory of wipnoteDir.
// It creates the directory if it does not exist.
func NewStore(wipnoteDir string) (*Store, error) {
	dir := filepath.Join(wipnoteDir, "arch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create arch dir: %w", err)
	}
	return &Store{
		dir:        dir,
		ledgerPath: LedgerPath(wipnoteDir),
	}, nil
}

// Dir returns the absolute path to the arch directory.
func (s *Store) Dir() string { return s.dir }

// cardPath returns the import-compatible YAML file path for a card slug.
func (s *Store) cardPath(slug string) string {
	return filepath.Join(s.dir, slug+importYAMLCardExt)
}

func isCardFile(name string) bool {
	ext := filepath.Ext(name)
	return ext == importYAMLCardExt || ext == legacyMDCardExt
}

func cardSlugFromName(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// resolveCardPath prefers import-compatible YAML cards but falls back to
// legacy .md cards.
func (s *Store) resolveCardPath(slug string) (string, error) {
	yamlPath := filepath.Join(s.dir, slug+importYAMLCardExt)
	if _, err := os.Stat(yamlPath); err == nil {
		return yamlPath, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("stat card: %w", err)
	}
	mdPath := filepath.Join(s.dir, slug+legacyMDCardExt)
	if _, err := os.Stat(mdPath); err == nil {
		return mdPath, nil
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("stat card: %w", err)
	}
	return "", fmt.Errorf("%w: %s", ErrNotFound, slug)
}

// Get reads and parses the card with the given slug.
// Returns ErrNotFound when the file does not exist.
// Returns an error when slug is not a valid slug (path traversal prevention).
func (s *Store) Get(slug string) (*Card, error) {
	if !isValidSlug(slug) {
		return nil, fmt.Errorf("invalid slug %q: must contain only lowercase letters, digits, and hyphens", slug)
	}
	if card, ok, err := s.getLedgerCard(slug); err != nil {
		return nil, err
	} else if ok {
		return card, nil
	}
	path, err := s.resolveCardPath(slug)
	if err != nil {
		return nil, err
	}
	return ParseFile(path)
}

// List returns all cards in the store.
// When includeRetired is false, superseded/retired cards are omitted.
func (s *Store) List(includeRetired bool) ([]*Card, error) {
	cardsBySlug := make(map[string]*Card)
	if ledgerCards, err := ReadLedger(s.ledgerPath); err == nil {
		for _, card := range ledgerCards {
			cardsBySlug[card.Name] = card
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read architecture ledger: %w", err)
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return filterRetired(cardsBySlug, includeRetired), nil
		}
		return nil, fmt.Errorf("read arch dir: %w", err)
	}

	pathsBySlug := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() || !isCardFile(e.Name()) {
			continue
		}
		slug := cardSlugFromName(e.Name())
		path := filepath.Join(s.dir, e.Name())
		existing, ok := pathsBySlug[slug]
		if ok && filepath.Ext(existing) == importYAMLCardExt {
			continue
		}
		if filepath.Ext(path) == importYAMLCardExt || !ok {
			pathsBySlug[slug] = path
		}
	}

	for _, path := range pathsBySlug {
		card, err := ParseFile(path)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
		}
		if _, exists := cardsBySlug[card.Name]; !exists {
			cardsBySlug[card.Name] = card
		}
	}
	return filterRetired(cardsBySlug, includeRetired), nil
}

// Create validates and writes a new card.
// Returns ErrDuplicateSlug when a card with the same name already exists.
// Returns ErrDuplicateGlobSet when an existing active card has the exact same
// set of non-empty path globs (order-insensitive). Empty glob sets are exempt.
func (s *Store) Create(card *Card) error {
	if err := Validate(card); err != nil {
		return err
	}
	if _, ok, err := s.getLedgerCard(card.Name); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrDuplicateSlug, card.Name)
	}
	if _, err := s.resolveCardPath(card.Name); err == nil {
		return fmt.Errorf("%w: %s", ErrDuplicateSlug, card.Name)
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return err
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
	return s.write(card, "")
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
	existingPath := ""
	if _, ok, err := s.getLedgerCard(card.Name); err != nil {
		return err
	} else if !ok {
		var resolveErr error
		existingPath, resolveErr = s.resolveCardPath(card.Name)
		if resolveErr != nil {
			return resolveErr
		}
	}
	if len(card.Paths) > 0 {
		if dup, err := s.findGlobSetDuplicate(card); err != nil {
			return err
		} else if dup != "" {
			return fmt.Errorf("%w: same paths as %q", ErrDuplicateGlobSet, dup)
		}
	}
	card.UpdatedAt = time.Now().UTC()
	return s.write(card, existingPath)
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
	existingPath := ""
	if _, ok, getErr := s.getLedgerCard(card.Name); getErr != nil {
		return getErr
	} else if !ok {
		existingPath, err = s.resolveCardPath(card.Name)
		if err != nil {
			return err
		}
	}
	return s.write(card, existingPath)
}

// write upserts a card into the canonical HTML ledger and removes any migrated
// legacy/import file when previousPath points at one.
func (s *Store) write(card *Card, previousPath string) error {
	ledgerCards, err := s.ledgerCards()
	if err != nil {
		return err
	}
	ledgerCards[card.Name] = card
	if err := WriteLedger(s.ledgerPath, mapCardsToSlice(ledgerCards)); err != nil {
		return err
	}
	if previousPath != "" && previousPath != s.ledgerPath {
		if err := os.Remove(previousPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove legacy/import card: %w", err)
		}
	}
	return nil
}

// ValidateAll parses and validates every card in the store.
// Returns:
//   - errs: map from slug to error for cards that fail validation (error-class violations).
//   - warnings: map from slug to warning strings for cards with advisory path issues.
//   - err: a non-nil error only when the store directory cannot be read.
func (s *Store) ValidateAll() (errs map[string]error, warnings map[string][]string, err error) {
	errs = make(map[string]error)
	warnings = make(map[string][]string)

	cards, loadErr := s.List(true)
	if loadErr != nil {
		return nil, nil, loadErr
	}
	for _, card := range cards {
		if valErr := Validate(card); valErr != nil {
			errs[card.Name] = valErr
		}
		if ws := ValidatePaths(card.Paths); len(ws) > 0 {
			warnings[card.Name] = ws
		}
	}
	return errs, warnings, nil
}

func (s *Store) ledgerCards() (map[string]*Card, error) {
	cards := make(map[string]*Card)
	ledgerCards, err := ReadLedger(s.ledgerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cards, nil
		}
		return nil, fmt.Errorf("read architecture ledger: %w", err)
	}
	for _, card := range ledgerCards {
		cards[card.Name] = card
	}
	return cards, nil
}

func (s *Store) getLedgerCard(slug string) (*Card, bool, error) {
	cards, err := s.ledgerCards()
	if err != nil {
		return nil, false, err
	}
	card, ok := cards[slug]
	return card, ok, nil
}

func mapCardsToSlice(cards map[string]*Card) []*Card {
	out := make([]*Card, 0, len(cards))
	for _, card := range cards {
		out = append(out, card)
	}
	return out
}

func filterRetired(cards map[string]*Card, includeRetired bool) []*Card {
	list := make([]*Card, 0, len(cards))
	for _, card := range cards {
		if !includeRetired && card.IsRetired() {
			continue
		}
		list = append(list, card)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list
}
