package db

import (
	"database/sql"
	"fmt"
)

// MigrateDatabase applies any necessary schema migrations to an existing database
func MigrateDatabase(db *sql.DB) error {
	// Check if batch_index column exists
	rows, err := db.Query("PRAGMA table_info(blobs)")
	if err != nil {
		return fmt.Errorf("failed to get table info: %w", err)
	}
	defer rows.Close()

	hasBatchIndex := false
	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notnull int
		var dfltValue sql.NullString
		var pk int

		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return fmt.Errorf("failed to scan column info: %w", err)
		}

		if name == "batch_index" {
			hasBatchIndex = true
			break
		}
	}

	// Add batch_index column if it doesn't exist
	if !hasBatchIndex {
		_, err := db.Exec("ALTER TABLE blobs ADD COLUMN batch_index INTEGER")
		if err != nil {
			return fmt.Errorf("failed to add batch_index column: %w", err)
		}
	}

	return nil
}
