package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	libshare "github.com/celestiaorg/go-square/v3/share"
	_ "github.com/mattn/go-sqlite3"
)

// TestMigrateDatabase_AddBatchIndex tests that batch_index column is added to existing databases
func TestMigrateDatabase_AddBatchIndex(t *testing.T) {
	// Create temp database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_migration.db")

	// Create "old" database without batch_index column
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}

	// Create old schema (without batch_index)
	oldSchema := `
		CREATE TABLE IF NOT EXISTS blobs (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			commitment          BLOB NOT NULL UNIQUE,
			namespace           BLOB NOT NULL,
			blob_data           BLOB NOT NULL,
			blob_size           INTEGER NOT NULL,
			status              TEXT NOT NULL,
			created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			submitted_at        TIMESTAMP,
			confirmed_at        TIMESTAMP,
			batch_id            INTEGER,
			celestia_height     INTEGER,
			retry_count         INTEGER NOT NULL DEFAULT 0,
			last_error          TEXT,
			next_retry_at       TIMESTAMP,
			read_status         TEXT NOT NULL DEFAULT 'unread',
			last_read_at        TIMESTAMP,
			read_count          INTEGER NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS batches (
			batch_id            INTEGER PRIMARY KEY AUTOINCREMENT,
			batch_commitment    BLOB NOT NULL UNIQUE,
			batch_data          BLOB NOT NULL,
			batch_size          INTEGER NOT NULL,
			blob_count          INTEGER NOT NULL,
			celestia_height     INTEGER,
			celestia_tx_hash    TEXT,
			status              TEXT NOT NULL,
			created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			submitted_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			confirmed_at        TIMESTAMP,
			retry_count         INTEGER NOT NULL DEFAULT 0,
			last_error          TEXT
		);
	`

	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatalf("Failed to create old schema: %v", err)
	}

	// Insert a test blob using old schema
	nsBytes := []byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09,
	}
	namespace, err := libshare.NewNamespaceFromBytes(nsBytes)
	if err != nil {
		t.Fatalf("Failed to create namespace: %v", err)
	}

	testCommitment := []byte("test-commitment")
	testData := []byte("test-data")

	_, err = db.Exec(`
		INSERT INTO blobs (commitment, namespace, blob_data, blob_size, status)
		VALUES (?, ?, ?, ?, ?)
	`, testCommitment, namespace.Bytes(), testData, len(testData), "pending_submission")
	if err != nil {
		t.Fatalf("Failed to insert test blob: %v", err)
	}

	// Verify batch_index column doesn't exist
	rows, err := db.Query("PRAGMA table_info(blobs)")
	if err != nil {
		t.Fatalf("Failed to get table info: %v", err)
	}

	hasBatchIndexBefore := false
	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notnull int
		var dfltValue sql.NullString
		var pk int

		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("Failed to scan column info: %v", err)
		}

		if name == "batch_index" {
			hasBatchIndexBefore = true
		}
	}
	rows.Close()

	if hasBatchIndexBefore {
		t.Fatalf("batch_index column should not exist before migration")
	}

	// Run migration
	if err := MigrateDatabase(db); err != nil {
		t.Fatalf("MigrateDatabase failed: %v", err)
	}

	// Verify batch_index column was added
	rows, err = db.Query("PRAGMA table_info(blobs)")
	if err != nil {
		t.Fatalf("Failed to get table info after migration: %v", err)
	}

	hasBatchIndexAfter := false
	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notnull int
		var dfltValue sql.NullString
		var pk int

		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("Failed to scan column info: %v", err)
		}

		if name == "batch_index" {
			hasBatchIndexAfter = true
		}
	}
	rows.Close()

	if !hasBatchIndexAfter {
		t.Fatalf("batch_index column should exist after migration")
	}

	// Verify existing data is still accessible
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query blobs after migration: %v", err)
	}

	if count != 1 {
		t.Fatalf("Expected 1 blob after migration, got %d", count)
	}

	// Verify we can now use the BlobStore with migrated database
	db.Close()

	store, err := NewBlobStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to open BlobStore with migrated database: %v", err)
	}
	defer store.Close()

	// Verify we can query the existing blob
	ctx := context.Background()
	blob, err := store.GetBlobByCommitment(ctx, testCommitment)
	if err != nil {
		t.Fatalf("Failed to get blob from migrated database: %v", err)
	}

	if string(blob.Data) != string(testData) {
		t.Fatalf("Expected blob data %q, got %q", testData, blob.Data)
	}
}

// TestMigrateDatabase_IdempotentBatchIndex tests that migration is idempotent
func TestMigrateDatabase_IdempotentBatchIndex(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_idempotent.db")

	// Create fresh database with NewBlobStore (already has batch_index)
	store, err := NewBlobStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	// Run migration again - should be idempotent
	err = MigrateDatabase(store.db)
	if err != nil {
		t.Fatalf("Second migration should succeed: %v", err)
	}

	store.Close()
}
