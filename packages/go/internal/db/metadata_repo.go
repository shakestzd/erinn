package db

import (
	"database/sql"
	"fmt"
)

// GetMetadata retrieves a metadata value by key.
// Returns ("", nil) if the key does not exist.
func GetMetadata(db *sql.DB, key string) (string, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM metadata WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get metadata %q: %w", key, err)
	}
	return value, nil
}

// SetMetadata upserts a metadata key-value pair.
func SetMetadata(db *sql.DB, key, value string) error {
	_, err := db.Exec(`
		INSERT INTO metadata (key, value, updated_at)
		VALUES (?, ?, datetime('now'))
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("set metadata %q: %w", key, err)
	}
	return nil
}
