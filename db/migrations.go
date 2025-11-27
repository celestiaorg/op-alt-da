package db

import (
	"database/sql"
	"fmt"
)

// MigrateDatabase applies any necessary schema migrations to an existing database
func MigrateDatabase(db *sql.DB) error {
	// Migration 1: Add batch_index column if it doesn't exist
	if err := migrateBatchIndex(db); err != nil {
		return err
	}

	// Migration 2: Create sync_state table for backfill progress
	if err := migrateSyncState(db); err != nil {
		return err
	}

	return nil
}

func migrateBatchIndex(db *sql.DB) error {
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

	if !hasBatchIndex {
		_, err := db.Exec("ALTER TABLE blobs ADD COLUMN batch_index INTEGER")
		if err != nil {
			return fmt.Errorf("failed to add batch_index column: %w", err)
		}
	}

	return nil
}

func migrateSyncState(db *sql.DB) error {
	// Check if sync_state table exists
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sync_state'").Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to check sync_state table: %w", err)
	}

	if count == 0 {
		// Create sync_state table
		_, err := db.Exec(`
			CREATE TABLE sync_state (
				worker_name TEXT PRIMARY KEY,
				last_height INTEGER NOT NULL,
				updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			)
		`)
		if err != nil {
			return fmt.Errorf("failed to create sync_state table: %w", err)
		}
	}

	return nil
}
