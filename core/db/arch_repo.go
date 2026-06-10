package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ArchCardRow is the SQLite representation of an architectural memory card.
// It mirrors the arch_cards table schema and is populated by reindex.
type ArchCardRow struct {
	Slug         string
	Kind         string
	PathsJSON    string
	VerifiedAt   string
	LinksJSON    string
	CreatedBy    string
	SupersededBy string
	Retired      bool
	Body         string
	CreatedAt    *time.Time
	UpdatedAt    *time.Time
}

// UpsertArchCard inserts or replaces an arch card row in the read index.
func UpsertArchCard(db *sql.DB, row *ArchCardRow) error {
	retiredInt := 0
	if row.Retired {
		retiredInt = 1
	}
	_, err := db.Exec(`
		INSERT INTO arch_cards
			(slug, kind, paths_json, verified_at, links_json, created_by,
			 superseded_by, retired, body, created_at, updated_at, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(slug) DO UPDATE SET
			kind          = excluded.kind,
			paths_json    = excluded.paths_json,
			verified_at   = excluded.verified_at,
			links_json    = excluded.links_json,
			created_by    = excluded.created_by,
			superseded_by = excluded.superseded_by,
			retired       = excluded.retired,
			body          = excluded.body,
			created_at    = excluded.created_at,
			updated_at    = excluded.updated_at,
			indexed_at    = CURRENT_TIMESTAMP`,
		row.Slug, row.Kind, row.PathsJSON, row.VerifiedAt, row.LinksJSON,
		row.CreatedBy, row.SupersededBy, retiredInt, row.Body,
		row.CreatedAt, row.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert arch card %s: %w", row.Slug, err)
	}
	return nil
}

// DeleteArchCard removes a card row from the read index by slug.
func DeleteArchCard(db *sql.DB, slug string) error {
	_, err := db.Exec(`DELETE FROM arch_cards WHERE slug = ?`, slug)
	return err
}

// ArchCardRowFromCard converts a card to a row, JSON-encoding slices.
// It is defined here (near the DB layer) to avoid importing core/arch from core/db.
// Callers pass the fields directly.
func ArchCardRowFromFields(
	slug, kind string,
	paths []string,
	verifiedAt string,
	links []string,
	createdBy, supersededBy string,
	retired bool,
	body string,
	createdAt, updatedAt *time.Time,
) (*ArchCardRow, error) {
	// Normalize nil slices to empty slices so JSON encodes as [] not null.
	if paths == nil {
		paths = []string{}
	}
	if links == nil {
		links = []string{}
	}
	pathsJSON, err := json.Marshal(paths)
	if err != nil {
		return nil, fmt.Errorf("marshal paths: %w", err)
	}
	linksJSON, err := json.Marshal(links)
	if err != nil {
		return nil, fmt.Errorf("marshal links: %w", err)
	}
	return &ArchCardRow{
		Slug:         slug,
		Kind:         kind,
		PathsJSON:    string(pathsJSON),
		VerifiedAt:   verifiedAt,
		LinksJSON:    string(linksJSON),
		CreatedBy:    createdBy,
		SupersededBy: supersededBy,
		Retired:      retired,
		Body:         body,
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}, nil
}
