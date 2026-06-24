package arch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- helpers ----------------------------------------------------------------

func validCard() *Card {
	return &Card{
		Name:      "auth-subsystem",
		Kind:      KindSubsystemMap,
		Paths:     []string{"internal/auth/**"},
		CreatedBy: "agent",
		Body:      "The auth subsystem handles JWT validation and session management.",
	}
}

func validFrontmatter(extra ...string) string {
	lines := []string{
		"---",
		"name: auth-subsystem",
		"kind: subsystem-map",
		"paths:",
		"  - internal/auth/**",
		"created_by: agent",
	}
	lines = append(lines, extra...)
	lines = append(lines, "---", "Body text here.")
	return strings.Join(lines, "\n")
}

// ---- Parse tests -------------------------------------------------------------

func TestParse_ValidCard(t *testing.T) {
	data := []byte(validFrontmatter())
	card, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if card.Name != "auth-subsystem" {
		t.Errorf("name = %q, want %q", card.Name, "auth-subsystem")
	}
	if card.Kind != KindSubsystemMap {
		t.Errorf("kind = %q, want %q", card.Kind, KindSubsystemMap)
	}
	if card.Body != "Body text here." {
		t.Errorf("body = %q, want %q", card.Body, "Body text here.")
	}
}

func TestParse_ImportYAMLCard(t *testing.T) {
	data := []byte("name: auth-subsystem\nkind: subsystem-map\ncreated_by: agent\npaths:\n  - internal/auth/**\nbody: |\n  Body text here.\n")
	card, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if card.Name != "auth-subsystem" {
		t.Errorf("name = %q, want %q", card.Name, "auth-subsystem")
	}
	if card.Body != "Body text here." {
		t.Errorf("body = %q, want %q", card.Body, "Body text here.")
	}
}

func TestParse_MissingFrontmatterDelimiter(t *testing.T) {
	_, err := Parse([]byte("no frontmatter here"))
	if err == nil {
		t.Fatal("expected error for missing frontmatter delimiter")
	}
}

func TestParse_UnclosedFrontmatter(t *testing.T) {
	_, err := Parse([]byte("---\nname: x\nkind: hazard\n"))
	if err == nil {
		t.Fatal("expected error for unclosed frontmatter")
	}
}

func TestParse_EmptyBody(t *testing.T) {
	data := []byte("---\nname: x\nkind: hazard\ncreated_by: agent\n---\n")
	card, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if card.Body != "" {
		t.Errorf("expected empty body, got %q", card.Body)
	}
}

func TestParse_SupersededBy(t *testing.T) {
	data := []byte(validFrontmatter("superseded_by: new-card"))
	card, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if card.SupersededBy != "new-card" {
		t.Errorf("superseded_by = %q, want %q", card.SupersededBy, "new-card")
	}
	if !card.IsRetired() {
		t.Error("expected IsRetired() = true for card with superseded_by")
	}
}

// ---- Validate tests ----------------------------------------------------------

func TestValidate_ValidCard(t *testing.T) {
	if err := Validate(validCard()); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_MissingName(t *testing.T) {
	c := validCard()
	c.Name = ""
	if err := Validate(c); err == nil {
		t.Error("expected error for missing name")
	}
}

func TestValidate_InvalidSlug(t *testing.T) {
	cases := []string{"Auth Subsystem", "auth_subsystem", "CAPS", "-starts-hyphen", "ends-hyphen-"}
	for _, name := range cases {
		c := validCard()
		c.Name = name
		if err := Validate(c); err == nil {
			t.Errorf("expected slug error for name %q", name)
		}
	}
}

func TestValidate_ValidSlugs(t *testing.T) {
	cases := []string{"auth", "auth-subsystem", "auth2", "a1b2c3"}
	for _, name := range cases {
		c := validCard()
		c.Name = name
		if err := Validate(c); err != nil {
			t.Errorf("unexpected slug error for %q: %v", name, err)
		}
	}
}

func TestValidate_MissingKind(t *testing.T) {
	c := validCard()
	c.Kind = ""
	if err := Validate(c); err == nil {
		t.Error("expected error for missing kind")
	}
}

func TestValidate_InvalidKind(t *testing.T) {
	c := validCard()
	c.Kind = "unknown-kind"
	if err := Validate(c); err == nil {
		t.Error("expected error for invalid kind")
	}
}

func TestValidate_AllKindsValid(t *testing.T) {
	for _, k := range []Kind{KindSubsystemMap, KindInvariant, KindHazard, KindDecision} {
		c := validCard()
		c.Kind = k
		if err := Validate(c); err != nil {
			t.Errorf("unexpected error for kind %q: %v", k, err)
		}
	}
}

func TestValidate_MissingCreatedBy(t *testing.T) {
	c := validCard()
	c.CreatedBy = ""
	if err := Validate(c); err == nil {
		t.Error("expected error for missing created_by")
	}
}

func TestValidate_BodyAtWordLimit(t *testing.T) {
	c := validCard()
	c.Body = strings.Repeat("word ", MaxBodyWords)
	if err := Validate(c); err != nil {
		t.Errorf("unexpected error at exact word limit: %v", err)
	}
}

func TestValidate_BodyOverWordLimit(t *testing.T) {
	c := validCard()
	c.Body = strings.Repeat("word ", MaxBodyWords+1)
	err := Validate(c)
	if err == nil {
		t.Error("expected error for body exceeding word limit")
	}
	if !strings.Contains(err.Error(), "word") {
		t.Errorf("error should mention word limit, got: %v", err)
	}
}

func TestValidate_SupersededByInvalidSlug(t *testing.T) {
	c := validCard()
	c.SupersededBy = "Invalid Slug!"
	if err := Validate(c); err == nil {
		t.Error("expected error for invalid superseded_by slug")
	}
}

func TestValidate_SupersededByValidSlug(t *testing.T) {
	c := validCard()
	c.SupersededBy = "new-auth-card"
	if err := Validate(c); err != nil {
		t.Errorf("unexpected error for valid superseded_by: %v", err)
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	c := &Card{}
	err := Validate(c)
	if err == nil {
		t.Fatal("expected errors for empty card")
	}
	msg := err.Error()
	if !strings.Contains(msg, "name") {
		t.Errorf("error should mention name, got: %v", msg)
	}
	if !strings.Contains(msg, "kind") {
		t.Errorf("error should mention kind, got: %v", msg)
	}
}

// ---- countWords tests -------------------------------------------------------

func TestCountWords(t *testing.T) {
	cases := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"   ", 0},
		{"one", 1},
		{"one two three", 3},
		{"  leading and trailing  ", 3},
		{"multiple   spaces", 2},
		{"newline\nword", 2},
	}
	for _, tc := range cases {
		got := countWords(tc.input)
		if got != tc.want {
			t.Errorf("countWords(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// ---- Marshal round-trip tests -----------------------------------------------

func TestMarshal_RoundTrip(t *testing.T) {
	c := validCard()
	c.Links = []string{"feat-abc123"}

	data, err := Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("parse after marshal: %v", err)
	}

	if parsed.Name != c.Name {
		t.Errorf("name: got %q, want %q", parsed.Name, c.Name)
	}
	if parsed.Kind != c.Kind {
		t.Errorf("kind: got %q, want %q", parsed.Kind, c.Kind)
	}
	if parsed.Body != c.Body {
		t.Errorf("body: got %q, want %q", parsed.Body, c.Body)
	}
	if !strings.Contains(string(data), "body:") {
		t.Errorf("marshal should emit explicit body field, got:\n%s", string(data))
	}
}

// ---- Store tests ------------------------------------------------------------

func TestStore_CreateAndGet(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	card := validCard()
	if err := store.Create(card); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, LedgerFilename)); err != nil {
		t.Fatalf("expected canonical ledger file: %v", err)
	}

	got, err := store.Get(card.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != card.Name {
		t.Errorf("name: got %q, want %q", got.Name, card.Name)
	}
}

func TestStore_Get_LegacyMarkdownCard(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	raw := []byte(validFrontmatter())
	if err := os.WriteFile(filepath.Join(store.Dir(), "auth-subsystem.md"), raw, 0o644); err != nil {
		t.Fatalf("write legacy card: %v", err)
	}

	got, err := store.Get("auth-subsystem")
	if err != nil {
		t.Fatalf("Get legacy card: %v", err)
	}
	if got.Body != "Body text here." {
		t.Errorf("body = %q, want %q", got.Body, "Body text here.")
	}
}

func TestStore_DuplicateGlobSet(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	first := validCard()
	first.Name = "first-card"
	if err := store.Create(first); err != nil {
		t.Fatalf("Create first: %v", err)
	}

	// Same glob set, different name — must be rejected.
	second := validCard()
	second.Name = "second-card"
	err := store.Create(second)
	if err == nil {
		t.Fatal("expected ErrDuplicateGlobSet for same path glob set")
	}
	if !strings.Contains(err.Error(), "glob") && !strings.Contains(err.Error(), "paths") {
		t.Errorf("error should mention glob/paths, got: %v", err)
	}

	// Order-insensitive: reversed glob set must also be rejected.
	third := validCard()
	third.Name = "third-card"
	third.Paths = []string{"cmd/**", "internal/auth/**"} // reversed relative to validCard
	// Note: validCard has only "internal/auth/**", so this is different — use matching set.
	third.Paths = []string{"internal/auth/**"} // identical set
	err = store.Create(third)
	if err == nil {
		t.Fatal("expected ErrDuplicateGlobSet for identical single-element set")
	}
}

func TestStore_DuplicateGlobSet_EmptyExempt(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	// Two cards with no paths — must both be accepted.
	a := validCard()
	a.Name = "no-paths-a"
	a.Paths = nil
	if err := store.Create(a); err != nil {
		t.Fatalf("Create a: %v", err)
	}

	b := validCard()
	b.Name = "no-paths-b"
	b.Paths = nil
	if err := store.Create(b); err != nil {
		t.Errorf("empty glob set should not trigger dedup: %v", err)
	}
}

func TestStore_DuplicateSlug(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	card := validCard()
	if err := store.Create(card); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	err := store.Create(card)
	if err == nil {
		t.Fatal("expected ErrDuplicateSlug on second Create")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected duplicate slug error, got: %v", err)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	_, err := store.Get("nonexistent")
	if err == nil {
		t.Fatal("expected ErrNotFound")
	}
}

func TestStore_List_HidesRetiredByDefault(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	active := validCard()
	active.Name = "active-card"
	store.Create(active)

	retired := validCard()
	retired.Name = "retired-card"
	retired.Paths = []string{"internal/other/**"} // distinct glob set
	store.Create(retired)
	store.Deprecate("retired-card", "active-card")

	cards, err := store.List(false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, c := range cards {
		if c.Name == "retired-card" {
			t.Error("retired card should be hidden from default list")
		}
	}
	if len(cards) != 1 {
		t.Errorf("expected 1 active card, got %d", len(cards))
	}
}

func TestStore_List_IncludeRetiredWithFlag(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	active := validCard()
	active.Name = "active-card"
	store.Create(active)

	retired := validCard()
	retired.Name = "retired-card"
	retired.Paths = []string{"internal/other/**"} // distinct glob set
	store.Create(retired)
	store.Deprecate("retired-card", "active-card")

	cards, err := store.List(true)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cards) != 2 {
		t.Errorf("expected 2 cards with --all, got %d", len(cards))
	}
}

func TestStore_Deprecate_WithSuccessor(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	card := validCard()
	store.Create(card)

	if err := store.Deprecate(card.Name, "new-card"); err != nil {
		t.Fatalf("Deprecate: %v", err)
	}

	got, _ := store.Get(card.Name)
	if got.SupersededBy != "new-card" {
		t.Errorf("superseded_by = %q, want %q", got.SupersededBy, "new-card")
	}
	if !got.IsRetired() {
		t.Error("expected IsRetired() = true after Deprecate")
	}
}

func TestStore_Deprecate_Outright(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	card := validCard()
	store.Create(card)

	if err := store.Deprecate(card.Name, ""); err != nil {
		t.Fatalf("Deprecate (outright): %v", err)
	}

	got, _ := store.Get(card.Name)
	if got.SupersededBy != "" {
		t.Errorf("expected empty superseded_by for outright retirement, got %q", got.SupersededBy)
	}
	if !got.IsRetired() {
		t.Error("expected IsRetired() = true after outright Deprecate")
	}
}

func TestStore_Update(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	card := validCard()
	store.Create(card)

	card.Body = "Updated body content."
	if err := store.Update(card); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := store.Get(card.Name)
	if got.Body != "Updated body content." {
		t.Errorf("body = %q, want %q", got.Body, "Updated body content.")
	}
}

func TestStore_Update_LegacyCardMigratesToLedger(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	legacyPath := filepath.Join(store.Dir(), "auth-subsystem.md")
	if err := os.WriteFile(legacyPath, []byte(validFrontmatter()), 0o644); err != nil {
		t.Fatalf("write legacy card: %v", err)
	}

	card, err := store.Get("auth-subsystem")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	card.Body = "Updated body content."
	if err := store.Update(card); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, LedgerFilename)); err != nil {
		t.Fatalf("expected canonical ledger after update: %v", err)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy markdown card should be removed, stat err=%v", err)
	}
}

func TestStore_CreateConcurrentLedgerWritesPreserveCards(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const count = 12
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			card := validCard()
			card.Name = fmt.Sprintf("card-%02d", i)
			card.Paths = []string{fmt.Sprintf("internal/card-%02d/**", i)}
			errs <- store.Create(card)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	cards, err := ReadLedger(filepath.Join(dir, LedgerFilename))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(cards) != count {
		t.Fatalf("ledger card count = %d, want %d", len(cards), count)
	}
}

func TestReadLedger_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	card := validCard()
	card.Links = []string{"feat-abc12345"}
	card.CreatedAt = time.Now().UTC().Round(0)
	card.UpdatedAt = card.CreatedAt
	if err := WriteLedger(filepath.Join(dir, LedgerFilename), []*Card{card}); err != nil {
		t.Fatalf("WriteLedger: %v", err)
	}

	cards, err := ReadLedger(filepath.Join(dir, LedgerFilename))
	if err != nil {
		t.Fatalf("ReadLedger: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("len(cards) = %d, want 1", len(cards))
	}
	if got := cards[0]; got.Name != card.Name || got.Body != card.Body {
		t.Fatalf("round-trip card mismatch: got %+v want %+v", got, card)
	}
}

func TestStore_Update_NotFound(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	card := validCard()
	err := store.Update(card)
	if err == nil {
		t.Fatal("expected ErrNotFound")
	}
}

func TestStore_Update_DuplicateGlobSet(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	// Create the first card with a specific path set.
	card1 := validCard()
	card1.Name = "card-one"
	card1.Paths = []string{"internal/auth/**", "pkg/auth/**"}
	store.Create(card1)

	// Create a second card with different paths.
	card2 := validCard()
	card2.Name = "card-two"
	card2.Paths = []string{"internal/db/**"}
	store.Create(card2)

	// Try to update card2 to have the same path set as card1 — should fail.
	card2.Paths = []string{"pkg/auth/**", "internal/auth/**"} // same set, different order
	err := store.Update(card2)
	if err == nil {
		t.Fatal("expected ErrDuplicateGlobSet when updating to another card's path set")
	}
	if !strings.Contains(err.Error(), "same path glob set already exists") {
		t.Errorf("expected duplicate glob set error, got: %v", err)
	}
}

func TestStore_ValidateAll(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStore(dir)

	// Write a valid card.
	card := validCard()
	store.Create(card)

	// Write an invalid card directly (bypass Create's validation).
	bad := []byte("---\nname: bad-card\nkind: not-a-kind\ncreated_by: agent\n---\nBody.\n")
	os.WriteFile(filepath.Join(store.Dir(), "bad-card.md"), bad, 0o644)

	errs, _, err := store.ValidateAll()
	if err != nil {
		t.Fatalf("ValidateAll: %v", err)
	}
	if len(errs) != 1 {
		t.Errorf("expected 1 validation error, got %d: %v", len(errs), errs)
	}
	if _, ok := errs["bad-card"]; !ok {
		t.Error("expected error for bad-card")
	}
}

func TestStore_ParseFile_NotFound(t *testing.T) {
	_, err := ParseFile("/nonexistent/path/card.md")
	if err == nil {
		t.Fatal("expected ErrNotFound")
	}
}

// ---- Path validation tests (bug-c06a0457) -----------------------------------

func TestValidate_AbsolutePath_Error(t *testing.T) {
	c := validCard()
	c.Paths = []string{"/workspaces/wipnote/cmd/main.go"}
	err := Validate(c)
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error should mention absolute, got: %v", err)
	}
}

func TestValidate_UnresolvedPrefix_Error(t *testing.T) {
	c := validCard()
	c.Paths = []string{"unresolved:/home/vscode/something.md"}
	err := Validate(c)
	if err == nil {
		t.Fatal("expected error for unresolved: path")
	}
	if !strings.Contains(err.Error(), "unresolved:") {
		t.Errorf("error should mention unresolved:, got: %v", err)
	}
}

func TestValidate_DotDotEscape_Error(t *testing.T) {
	cases := []string{
		"../outside-repo/file.go",
		"..",
		// Interior dot-dot segments that net-escape the repo must also be
		// caught (bug-fddf5820, finding 9): these have no "../" prefix but
		// resolve to a path above the repo root.
		"a/../../etc/passwd",
		"internal/../../outside.go",
		"foo/bar/../../../escape.go",
	}
	for _, p := range cases {
		c := validCard()
		c.Paths = []string{p}
		err := Validate(c)
		if err == nil {
			t.Errorf("expected error for ../ escape path %q", p)
		}
		if !strings.Contains(err.Error(), "escapes") {
			t.Errorf("error should mention escapes, got: %v", err)
		}
	}
}

func TestValidate_RepoRelativePath_OK(t *testing.T) {
	c := validCard()
	c.Paths = []string{"cmd/wipnote/arch_cmds.go", "core/arch/card.go"}
	if err := Validate(c); err != nil {
		t.Errorf("unexpected error for repo-relative paths: %v", err)
	}
}

func TestValidatePaths_SuspiciousPaths_WarnOnly(t *testing.T) {
	// These paths are repo-relative (not absolute) but suspicious.
	// They should produce a warning but NOT fail Validate().
	suspiciousCases := []struct {
		path     string
		contains string
	}{
		// tmp/ as a relative path prefix (not absolute) — suspicious but valid.
		{"tmp/claude-transcript.jsonl", "tmp"},
		// agent-memory directory reference.
		{"agent-memory/context.md", "agent memory"},
		// dead worktree relative path.
		{".claude/worktrees/some-branch/file.go", "worktree"},
	}
	for _, tc := range suspiciousCases {
		warns := ValidatePaths([]string{tc.path})
		if len(warns) == 0 {
			t.Errorf("expected warning for suspicious path %q", tc.path)
			continue
		}
		if !strings.Contains(warns[0], tc.contains) {
			t.Errorf("warning for %q should contain %q, got: %s", tc.path, tc.contains, warns[0])
		}
		// Validate must NOT return an error for warn-only paths.
		c := validCard()
		c.Paths = []string{tc.path}
		if err := Validate(c); err != nil {
			t.Errorf("Validate should not error for warn-only relative path %q, got: %v", tc.path, err)
		}
	}
}

func TestValidatePaths_CleanPath_NoWarn(t *testing.T) {
	warns := ValidatePaths([]string{"cmd/wipnote/arch_cmds.go"})
	if len(warns) != 0 {
		t.Errorf("expected no warnings for clean path, got: %v", warns)
	}
}

// ValidateAll now returns (errs, warnings, err) — ensure both are plumbed.
func TestStore_ValidateAll_WithWarnings(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// Write a card with a suspicious (warn-only) path directly to bypass validation.
	raw := []byte("---\nname: warn-card\nkind: invariant\ncreated_by: agent\npaths:\n  - agent-memory/ctx.md\n---\nBody here.\n")
	os.WriteFile(filepath.Join(store.Dir(), "warn-card.md"), raw, 0o644)

	// Write a card with an error-class absolute path.
	bad := []byte("---\nname: bad-card\nkind: invariant\ncreated_by: agent\npaths:\n  - /abs/path/to/file.go\n---\nBody.\n")
	os.WriteFile(filepath.Join(store.Dir(), "bad-card.md"), bad, 0o644)

	errs, warnings, valErr := store.ValidateAll()
	if valErr != nil {
		t.Fatalf("ValidateAll: %v", valErr)
	}
	if _, ok := errs["bad-card"]; !ok {
		t.Error("expected error for bad-card with absolute path")
	}
	if _, ok := warnings["warn-card"]; !ok {
		t.Error("expected warning for warn-card with agent-memory path")
	}
	// warn-card should NOT appear in errs.
	if _, ok := errs["warn-card"]; ok {
		t.Error("warn-card should not appear in errs (warn-only path)")
	}
}

// ---- Store.Create timestamp-preservation tests --------------------------------

// TestStore_Create_PreservesNonZeroTimestamps verifies that when a Card is
// passed to store.Create with explicit non-zero CreatedAt / UpdatedAt (e.g.
// from legacy .md frontmatter migration), those timestamps are preserved in
// the ledger unchanged. This is the core requirement of roborev finding 524.
func TestStore_Create_PreservesNonZeroTimestamps(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	wantCreated := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	wantUpdated := time.Date(2025, 9, 15, 8, 30, 0, 0, time.UTC)

	card := validCard()
	card.CreatedAt = wantCreated
	card.UpdatedAt = wantUpdated

	if err := store.Create(card); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(card.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt: got %v, want %v (Create overwrote frontmatter timestamp)", got.CreatedAt, wantCreated)
	}
	if !got.UpdatedAt.Equal(wantUpdated) {
		t.Errorf("UpdatedAt: got %v, want %v (Create overwrote frontmatter timestamp)", got.UpdatedAt, wantUpdated)
	}
}

// TestStore_Create_StampsNowWhenTimestampsUnset verifies that when a Card has
// zero CreatedAt / UpdatedAt (normal new-card creation), store.Create assigns
// the current time — not the zero value. This ensures the timestamp-preservation
// fix in finding 524 does not regress normal creation.
func TestStore_Create_StampsNowWhenTimestampsUnset(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	before := time.Now().UTC()
	card := validCard()
	// Explicitly zero — simulates normal `wipnote arch add` path.
	card.CreatedAt = time.Time{}
	card.UpdatedAt = time.Time{}

	if err := store.Create(card); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := store.Get(card.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero after Create with unset timestamp; expected now()")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero after Create with unset timestamp; expected now()")
	}
	if got.CreatedAt.Before(before) {
		t.Errorf("CreatedAt %v is before test-start %v", got.CreatedAt, before)
	}
	if got.UpdatedAt.Before(before) {
		t.Errorf("UpdatedAt %v is before test-start %v", got.UpdatedAt, before)
	}
}

