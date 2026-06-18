package db

import (
	"database/sql"
	"fmt"
	"time"
)

// RecapRow is the SQLite representation of a committed recap artifact. It mirrors
// the recaps table schema and is populated by reindex. The canonical store is
// the HTML file under .wipnote/recaps/; this row is a derived read index and is
// never authoritative.
type RecapRow struct {
	ID         string
	Kind       string
	Input      string
	GitRange   string
	Grounded   bool
	Title      string
	Outcome    string
	WorkItemID string
	CreatedAt  *time.Time
	UpdatedAt  *time.Time
}

// UpsertRecap inserts or replaces a recap row in the read index.
func UpsertRecap(db *sql.DB, row *RecapRow) error {
	groundedInt := 0
	if row.Grounded {
		groundedInt = 1
	}
	_, err := db.Exec(`
		INSERT INTO recaps
			(id, kind, input, git_range, grounded, title, outcome,
			 work_item_id, created_at, updated_at, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			kind         = excluded.kind,
			input        = excluded.input,
			git_range    = excluded.git_range,
			grounded     = excluded.grounded,
			title        = excluded.title,
			outcome      = excluded.outcome,
			work_item_id = excluded.work_item_id,
			created_at   = excluded.created_at,
			updated_at   = excluded.updated_at,
			indexed_at   = CURRENT_TIMESTAMP`,
		row.ID, row.Kind, row.Input, row.GitRange, groundedInt, row.Title,
		row.Outcome, row.WorkItemID, row.CreatedAt, row.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert recap %s: %w", row.ID, err)
	}
	return nil
}

// DeleteRecap removes a recap row from the read index by id.
func DeleteRecap(db *sql.DB, id string) error {
	_, err := db.Exec(`DELETE FROM recaps WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete recap %s: %w", id, err)
	}
	return nil
}

// GetRecap returns a single recap row by id, or (nil, nil) when absent.
func GetRecap(db *sql.DB, id string) (*RecapRow, error) {
	row := db.QueryRow(`
		SELECT id, kind, input, git_range, grounded, title, outcome,
		       work_item_id, created_at, updated_at
		FROM recaps WHERE id = ?`, id)
	rec, err := scanRecap(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get recap %s: %w", id, err)
	}
	return rec, nil
}

// ListRecaps returns all recap rows ordered by created_at descending (most
// recent first), then by id for deterministic tie-breaking.
func ListRecaps(db *sql.DB) ([]*RecapRow, error) {
	rows, err := db.Query(`
		SELECT id, kind, input, git_range, grounded, title, outcome,
		       work_item_id, created_at, updated_at
		FROM recaps
		ORDER BY created_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list recaps: %w", err)
	}
	defer rows.Close()

	var out []*RecapRow
	for rows.Next() {
		rec, scanErr := scanRecap(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan recap: %w", scanErr)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recaps: %w", err)
	}
	return out, nil
}

// rowScanner abstracts *sql.Row and *sql.Rows so scanRecap serves both.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanRecap reads one recaps row into a RecapRow.
func scanRecap(s rowScanner) (*RecapRow, error) {
	var (
		rec        RecapRow
		grounded   int
		created    sql.NullTime
		updated    sql.NullTime
	)
	if err := s.Scan(
		&rec.ID, &rec.Kind, &rec.Input, &rec.GitRange, &grounded,
		&rec.Title, &rec.Outcome, &rec.WorkItemID, &created, &updated,
	); err != nil {
		return nil, err
	}
	rec.Grounded = grounded != 0
	if created.Valid {
		t := created.Time
		rec.CreatedAt = &t
	}
	if updated.Valid {
		t := updated.Time
		rec.UpdatedAt = &t
	}
	return &rec, nil
}
