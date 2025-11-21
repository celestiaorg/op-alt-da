package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

var (
	ErrBlobNotFound  = errors.New("blob not found")
	ErrBatchNotFound = errors.New("batch not found")
)

type BlobStore struct {
	db *sql.DB
}

type Blob struct {
	ID             int64
	Commitment     []byte
	Namespace      []byte
	Data           []byte
	Size           int
	Status         string
	BatchID        *int64
	CelestiaHeight *uint64
	RetryCount     int
	CreatedAt      time.Time
	SubmittedAt    *time.Time
	ConfirmedAt    *time.Time
}

type Batch struct {
	BatchID          int64
	BatchCommitment  []byte
	BatchData        []byte
	BlobCount        int
	Status           string
	CelestiaHeight   *uint64
	CelestiaTxHash   *string
	CreatedAt        time.Time
	SubmittedAt      time.Time
	ConfirmedAt      *time.Time
}

// NewBlobStore creates a new blob store with SQLite backend
func NewBlobStore(dbPath string) (*BlobStore, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&cache=shared")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Set connection pool settings
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Initialize schema
	if _, err := db.Exec(schemaSQL); err != nil {
		return nil, fmt.Errorf("initialize schema: %w", err)
	}

	// Enable foreign key constraints
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// Configure SQLite for better concurrency
	if _, err := db.Exec("PRAGMA cache_size = -64000"); err != nil { // 64MB cache
		return nil, fmt.Errorf("set cache size: %w", err)
	}

	return &BlobStore{db: db}, nil
}

// Close closes the database connection
func (s *BlobStore) Close() error {
	return s.db.Close()
}

// GetDB returns the underlying sql.DB for backup operations
func (s *BlobStore) GetDB() *sql.DB {
	return s.db
}

// InsertBlob inserts a new blob into the database
func (s *BlobStore) InsertBlob(ctx context.Context, blob *Blob) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO blobs (
			commitment, namespace, blob_data, blob_size, status
		) VALUES (?, ?, ?, ?, 'pending_submission')
	`, blob.Commitment, blob.Namespace, blob.Data, len(blob.Data))

	if err != nil {
		return 0, fmt.Errorf("insert blob: %w", err)
	}

	return result.LastInsertId()
}

// GetBlobByCommitment retrieves a blob by its commitment
func (s *BlobStore) GetBlobByCommitment(ctx context.Context, commitment []byte) (*Blob, error) {
	var blob Blob
	var batchID sql.NullInt64
	var celestiaHeight sql.NullInt64

	err := s.db.QueryRowContext(ctx, `
		SELECT id, commitment, namespace, blob_data, blob_size, status,
		       batch_id, celestia_height, created_at
		FROM blobs
		WHERE commitment = ?
	`, commitment).Scan(
		&blob.ID, &blob.Commitment, &blob.Namespace, &blob.Data, &blob.Size,
		&blob.Status, &batchID, &celestiaHeight, &blob.CreatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, ErrBlobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query blob: %w", err)
	}

	if batchID.Valid {
		blob.BatchID = &batchID.Int64
	}
	if celestiaHeight.Valid {
		height := uint64(celestiaHeight.Int64)
		blob.CelestiaHeight = &height
	}

	return &blob, nil
}

// GetPendingBlobs retrieves N pending blobs for batching (FIFO order)
func (s *BlobStore) GetPendingBlobs(ctx context.Context, limit int) ([]*Blob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, commitment, namespace, blob_data, blob_size, retry_count
		FROM blobs
		WHERE status = 'pending_submission'
		  AND (next_retry_at IS NULL OR next_retry_at <= CURRENT_TIMESTAMP)
		ORDER BY id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query pending blobs: %w", err)
	}
	defer rows.Close()

	var blobs []*Blob
	for rows.Next() {
		var blob Blob
		if err := rows.Scan(&blob.ID, &blob.Commitment, &blob.Namespace,
			&blob.Data, &blob.Size, &blob.RetryCount); err != nil {
			return nil, fmt.Errorf("scan blob: %w", err)
		}
		blobs = append(blobs, &blob)
	}

	return blobs, rows.Err()
}

// CreateBatch creates a new batch and marks blobs as batched
func (s *BlobStore) CreateBatch(ctx context.Context, blobIDs []int64, batchCommitment, batchData []byte) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Insert batch
	result, err := tx.ExecContext(ctx, `
		INSERT INTO batches (
			batch_commitment, batch_data, batch_size, blob_count, status
		) VALUES (?, ?, ?, ?, 'submitted')
	`, batchCommitment, batchData, len(batchData), len(blobIDs))
	if err != nil {
		return 0, fmt.Errorf("insert batch: %w", err)
	}

	batchID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("get batch id: %w", err)
	}

	// Update blobs to reference batch
	for _, blobID := range blobIDs {
		_, err := tx.ExecContext(ctx, `
			UPDATE blobs
			SET status = 'batched',
			    batch_id = ?
			WHERE id = ?
		`, batchID, blobID)
		if err != nil {
			return 0, fmt.Errorf("update blob %d: %w", blobID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	return batchID, nil
}

// MarkBatchSubmitted marks a batch as submitted (already submitted on creation)
func (s *BlobStore) MarkBatchSubmitted(ctx context.Context, batchID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batches
		SET status = 'submitted',
		    submitted_at = CURRENT_TIMESTAMP
		WHERE batch_id = ?
	`, batchID)

	return err
}

// MarkBatchConfirmed marks a batch and all its blobs as confirmed
func (s *BlobStore) MarkBatchConfirmed(ctx context.Context, batchCommitment []byte, height uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Update batch
	result, err := tx.ExecContext(ctx, `
		UPDATE batches
		SET status = 'confirmed',
		    confirmed_at = CURRENT_TIMESTAMP,
		    celestia_height = ?
		WHERE batch_commitment = ?
		  AND status = 'submitted'
	`, height, batchCommitment)
	if err != nil {
		return fmt.Errorf("update batch: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return ErrBatchNotFound
	}

	// Get batch ID
	var batchID int64
	err = tx.QueryRowContext(ctx, `
		SELECT batch_id FROM batches WHERE batch_commitment = ?
	`, batchCommitment).Scan(&batchID)
	if err != nil {
		return fmt.Errorf("get batch id: %w", err)
	}

	// Update all blobs in batch
	_, err = tx.ExecContext(ctx, `
		UPDATE blobs
		SET status = 'confirmed',
		    confirmed_at = CURRENT_TIMESTAMP,
		    celestia_height = ?
		WHERE batch_id = ?
		  AND status = 'batched'
	`, height, batchID)
	if err != nil {
		return fmt.Errorf("update blobs: %w", err)
	}

	return tx.Commit()
}

// GetUnconfirmedBatches retrieves batches that are submitted but not confirmed
func (s *BlobStore) GetUnconfirmedBatches(ctx context.Context, olderThan time.Duration) ([]*Batch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT batch_id, batch_commitment, celestia_height, submitted_at
		FROM batches
		WHERE status = 'submitted'
		  AND submitted_at < datetime('now', ?)
		ORDER BY batch_id ASC
	`, fmt.Sprintf("-%d seconds", int(olderThan.Seconds())))
	if err != nil {
		return nil, fmt.Errorf("query unconfirmed batches: %w", err)
	}
	defer rows.Close()

	var batches []*Batch
	for rows.Next() {
		var batch Batch
		var celestiaHeight sql.NullInt64
		if err := rows.Scan(&batch.BatchID, &batch.BatchCommitment,
			&celestiaHeight, &batch.SubmittedAt); err != nil {
			return nil, fmt.Errorf("scan batch: %w", err)
		}
		if celestiaHeight.Valid {
			height := uint64(celestiaHeight.Int64)
			batch.CelestiaHeight = &height
		}
		batches = append(batches, &batch)
	}

	return batches, rows.Err()
}

// MarkRead updates read tracking for a blob
func (s *BlobStore) MarkRead(ctx context.Context, blobID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE blobs
		SET read_status = 'read',
		    read_count = read_count + 1,
		    last_read_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, blobID)

	return err
}

// GetStats returns database statistics
func (s *BlobStore) GetStats(ctx context.Context) (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	// Blob counts by status
	rows, err := s.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM blobs GROUP BY status
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	blobCounts := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		blobCounts[status] = count
	}
	stats["blob_counts"] = blobCounts

	// Batch counts by status
	rows, err = s.db.QueryContext(ctx, `
		SELECT status, COUNT(*) FROM batches GROUP BY status
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	batchCounts := make(map[string]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		batchCounts[status] = count
	}
	stats["batch_counts"] = batchCounts

	return stats, nil
}
