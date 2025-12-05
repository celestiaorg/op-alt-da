package db

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	BatchIndex     *int // Position within batch (0-based)
	CelestiaHeight *uint64
	RetryCount     int
	CreatedAt      time.Time
	SubmittedAt    *time.Time
	ConfirmedAt    *time.Time
}

type Batch struct {
	BatchID         int64
	BatchCommitment []byte
	BatchData       []byte
	BatchSize       int
	BlobCount       int
	Status          string
	CelestiaHeight  *uint64
	CelestiaTxHash  *string
	CreatedAt       time.Time
	SubmittedAt     time.Time
	ConfirmedAt     *time.Time
}

// NewBlobStore creates a new blob store with SQLite backend
func NewBlobStore(dbPath string) (*BlobStore, error) {
	var connStr string

	// Handle in-memory database specially
	if dbPath == ":memory:" {
		// For in-memory databases with shared cache, use file:memdb1?mode=memory&cache=shared
		// This ensures all connections share the same in-memory database
		connStr = "file:memdb1?mode=memory&cache=shared&_busy_timeout=5000&_synchronous=NORMAL"
	} else {
		// Create parent directory if it doesn't exist
		dir := filepath.Dir(dbPath)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("create database directory: %w", err)
			}
		}
		connStr = dbPath + "?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL&cache=shared"
	}

	db, err := sql.Open("sqlite3", connStr)
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

	// Apply migrations for existing databases
	if err := MigrateDatabase(db); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
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

// InsertBlob inserts a new blob into the database.
// Handles race conditions gracefully - if a blob with the same commitment
// already exists (due to concurrent requests), returns the existing blob's ID
// instead of an error. This ensures idempotent behavior at the database level.
func (s *BlobStore) InsertBlob(ctx context.Context, blob *Blob) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO blobs (
			commitment, namespace, blob_data, blob_size, status
		) VALUES (?, ?, ?, ?, 'pending_submission')
	`, blob.Commitment, blob.Namespace, blob.Data, len(blob.Data))

	if err != nil {
		// Check for UNIQUE constraint violation (SQLite error code 19 / SQLITE_CONSTRAINT)
		// This happens in race conditions where two concurrent requests insert the same blob
		if isUniqueConstraintError(err) {
			// Blob already exists - fetch and return existing ID (idempotent success)
			var existingID int64
			lookupErr := s.db.QueryRowContext(ctx, `
				SELECT id FROM blobs WHERE commitment = ?
			`, blob.Commitment).Scan(&existingID)
			if lookupErr != nil {
				return 0, fmt.Errorf("insert blob: %w (also failed to lookup existing: %v)", err, lookupErr)
			}
			return existingID, nil
		}
		return 0, fmt.Errorf("insert blob: %w", err)
	}

	return result.LastInsertId()
}

// isUniqueConstraintError checks if the error is a SQLite UNIQUE constraint violation
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	// SQLite returns "UNIQUE constraint failed" in the error message
	errStr := err.Error()
	return strings.Contains(errStr, "UNIQUE constraint failed")
}

// GetBlobByCommitment retrieves a blob by its commitment
func (s *BlobStore) GetBlobByCommitment(ctx context.Context, commitment []byte) (*Blob, error) {
	var blob Blob
	var batchID sql.NullInt64
	var batchIndex sql.NullInt64
	var celestiaHeight sql.NullInt64

	err := s.db.QueryRowContext(ctx, `
		SELECT id, commitment, namespace, blob_data, blob_size, status,
		       batch_id, batch_index, celestia_height, created_at
		FROM blobs
		WHERE commitment = ?
	`, commitment).Scan(
		&blob.ID, &blob.Commitment, &blob.Namespace, &blob.Data, &blob.Size,
		&blob.Status, &batchID, &batchIndex, &celestiaHeight, &blob.CreatedAt,
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
	if batchIndex.Valid {
		idx := int(batchIndex.Int64)
		blob.BatchIndex = &idx
	}
	if celestiaHeight.Valid {
		height := uint64(celestiaHeight.Int64)
		blob.CelestiaHeight = &height
	}

	return &blob, nil
}

// GetPendingBlobs retrieves N pending blobs for batching
// Prioritizes retries (higher retry_count first), then FIFO for same retry count
func (s *BlobStore) GetPendingBlobs(ctx context.Context, limit int) ([]*Blob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, commitment, namespace, blob_data, blob_size, retry_count, created_at
		FROM blobs
		WHERE status = 'pending_submission'
		  AND (next_retry_at IS NULL OR next_retry_at <= CURRENT_TIMESTAMP)
		ORDER BY retry_count DESC, id ASC
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
			&blob.Data, &blob.Size, &blob.RetryCount, &blob.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan blob: %w", err)
		}
		blobs = append(blobs, &blob)
	}

	return blobs, rows.Err()
}

// CreateBatchAndConfirm creates a batch AND marks blobs as confirmed in one atomic operation.
// Used after successful Celestia submission to avoid DB writes before submission.
// If batch already exists (duplicate submission), just confirms the blobs.
func (s *BlobStore) CreateBatchAndConfirm(ctx context.Context, blobIDs []int64, batchCommitment, batchData []byte, height uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Try to insert batch - might fail if already exists (duplicate submission)
	var batchID int64
	result, err := tx.ExecContext(ctx, `
		INSERT INTO batches (
			batch_commitment, batch_data, batch_size, blob_count, 
			celestia_height, status, confirmed_at
		) VALUES (?, ?, ?, ?, ?, 'confirmed', CURRENT_TIMESTAMP)
	`, batchCommitment, batchData, len(batchData), len(blobIDs), height)

	if err != nil {
		// Check if it's a duplicate - batch already exists
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			// Get existing batch ID
			err = tx.QueryRowContext(ctx, `
				SELECT batch_id FROM batches WHERE batch_commitment = ?
			`, batchCommitment).Scan(&batchID)
			if err != nil {
				return fmt.Errorf("get existing batch: %w", err)
			}
			// Batch already exists - just confirm the blobs below
		} else {
			return fmt.Errorf("insert batch: %w", err)
		}
	} else {
		batchID, err = result.LastInsertId()
		if err != nil {
			return fmt.Errorf("get batch id: %w", err)
		}
	}

	// Update all blobs to confirmed in one query using IN clause
	if len(blobIDs) > 0 {
		placeholders := make([]string, len(blobIDs))
		args := make([]interface{}, len(blobIDs)+2)
		args[0] = height
		args[1] = batchID
		for i, id := range blobIDs {
			placeholders[i] = "?"
			args[i+2] = id
		}
		query := fmt.Sprintf(`
			UPDATE blobs
			SET status = 'confirmed',
			    celestia_height = ?,
			    batch_id = ?,
			    confirmed_at = CURRENT_TIMESTAMP
			WHERE id IN (%s)
		`, strings.Join(placeholders, ","))
		_, err = tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("update blobs: %w", err)
		}
	}

	return tx.Commit()
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

	// Update blobs to reference batch and set their index
	for i, blobID := range blobIDs {
		_, err := tx.ExecContext(ctx, `
			UPDATE blobs
			SET status = 'batched',
			    batch_id = ?,
			    batch_index = ?
			WHERE id = ?
		`, batchID, i, blobID)
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

// UpdateBatchHeight updates the Celestia height for a submitted batch AND marks it confirmed.
// This is called immediately after successful Celestia submission, providing fast confirmation.
func (s *BlobStore) UpdateBatchHeight(ctx context.Context, batchID int64, height uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Update batch with height AND mark confirmed
	_, err = tx.ExecContext(ctx, `
		UPDATE batches
		SET celestia_height = ?,
		    status = 'confirmed',
		    confirmed_at = CURRENT_TIMESTAMP
		WHERE batch_id = ?
	`, height, batchID)
	if err != nil {
		return fmt.Errorf("update batch: %w", err)
	}

	// Also mark all blobs in this batch as confirmed
	_, err = tx.ExecContext(ctx, `
		UPDATE blobs
		SET status = 'confirmed',
		    celestia_height = ?,
		    confirmed_at = CURRENT_TIMESTAMP
		WHERE batch_id = ?
		  AND status = 'batched'
	`, height, batchID)
	if err != nil {
		return fmt.Errorf("update blobs: %w", err)
	}

	return tx.Commit()
}

// UpdateBatchHeightOnly updates ONLY the Celestia height (no confirmation).
// Used when verification fails but we want reconciliation to be able to find the batch.
func (s *BlobStore) UpdateBatchHeightOnly(ctx context.Context, batchID int64, height uint64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batches
		SET celestia_height = ?
		WHERE batch_id = ?
	`, height, batchID)
	return err
}

// UpdateBatchCommitment updates the batch commitment with the actual on-chain commitment.
// This is needed because TxWorkerAccounts may cause the Celestia node to use a different
// signer, which changes the commitment.
func (s *BlobStore) UpdateBatchCommitment(ctx context.Context, batchID int64, newCommitment []byte) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE batches
		SET batch_commitment = ?
		WHERE batch_id = ?
	`, newCommitment, batchID)

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

// MarkBatchConfirmedByID marks a batch and all its blobs as confirmed using batch ID
func (s *BlobStore) MarkBatchConfirmedByID(ctx context.Context, batchID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Get batch info first to get the height
	var height sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT celestia_height FROM batches WHERE batch_id = ?
	`, batchID).Scan(&height)
	if err == sql.ErrNoRows {
		return ErrBatchNotFound
	}
	if err != nil {
		return fmt.Errorf("get batch height: %w", err)
	}

	// Update batch
	result, err := tx.ExecContext(ctx, `
		UPDATE batches
		SET status = 'confirmed',
		    confirmed_at = CURRENT_TIMESTAMP
		WHERE batch_id = ?
		  AND status = 'submitted'
	`, batchID)
	if err != nil {
		return fmt.Errorf("update batch: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return ErrBatchNotFound
	}

	// Update all blobs in batch
	if height.Valid {
		_, err = tx.ExecContext(ctx, `
			UPDATE blobs
			SET status = 'confirmed',
			    confirmed_at = CURRENT_TIMESTAMP,
			    celestia_height = ?
			WHERE batch_id = ?
			  AND status = 'batched'
		`, height.Int64, batchID)
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE blobs
			SET status = 'confirmed',
			    confirmed_at = CURRENT_TIMESTAMP
			WHERE batch_id = ?
			  AND status = 'batched'
		`, batchID)
	}
	if err != nil {
		return fmt.Errorf("update blobs: %w", err)
	}

	return tx.Commit()
}

// RevertBatchToPending reverts a failed batch - deletes batch record and marks blobs as pending_submission
func (s *BlobStore) RevertBatchToPending(ctx context.Context, batchID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Mark blobs back to pending_submission and increment retry_count
	_, err = tx.ExecContext(ctx, `
		UPDATE blobs
		SET status = 'pending_submission',
		    batch_id = NULL,
		    batch_index = NULL,
		    retry_count = retry_count + 1
		WHERE batch_id = ?
		  AND status = 'batched'
	`, batchID)
	if err != nil {
		return fmt.Errorf("revert blobs: %w", err)
	}

	// Delete the batch record
	_, err = tx.ExecContext(ctx, `
		DELETE FROM batches WHERE batch_id = ?
	`, batchID)
	if err != nil {
		return fmt.Errorf("delete batch: %w", err)
	}

	return tx.Commit()
}

// GetUnconfirmedBatches retrieves batches that are submitted but not confirmed
func (s *BlobStore) GetUnconfirmedBatches(ctx context.Context, olderThan time.Duration) ([]*Batch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT batch_id, batch_commitment, batch_data, celestia_height, submitted_at
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
		if err := rows.Scan(&batch.BatchID, &batch.BatchCommitment, &batch.BatchData,
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

// GetUnverifiedBatches retrieves batches that are confirmed but not yet verified with blob.Get.
// Used by reconciliation worker to verify data is actually on Celestia.
func (s *BlobStore) GetUnverifiedBatches(ctx context.Context, olderThan time.Duration) ([]*Batch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT batch_id, batch_commitment, batch_data, celestia_height, confirmed_at
		FROM batches
		WHERE status = 'confirmed'
		  AND celestia_height IS NOT NULL
		  AND confirmed_at < datetime('now', ?)
		ORDER BY batch_id ASC
	`, fmt.Sprintf("-%d seconds", int(olderThan.Seconds())))
	if err != nil {
		return nil, fmt.Errorf("query unverified batches: %w", err)
	}
	defer rows.Close()

	var batches []*Batch
	for rows.Next() {
		var batch Batch
		var celestiaHeight sql.NullInt64
		var confirmedAt sql.NullTime
		if err := rows.Scan(&batch.BatchID, &batch.BatchCommitment, &batch.BatchData,
			&celestiaHeight, &confirmedAt); err != nil {
			return nil, fmt.Errorf("scan batch: %w", err)
		}
		if celestiaHeight.Valid {
			height := uint64(celestiaHeight.Int64)
			batch.CelestiaHeight = &height
		}
		if confirmedAt.Valid {
			batch.ConfirmedAt = &confirmedAt.Time
		}
		batches = append(batches, &batch)
	}

	return batches, rows.Err()
}

// MarkBatchVerified marks a batch and its blobs as verified after successful blob.Get.
func (s *BlobStore) MarkBatchVerified(ctx context.Context, batchID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Update batch status
	_, err = tx.ExecContext(ctx, `
		UPDATE batches
		SET status = 'verified'
		WHERE batch_id = ?
		  AND status = 'confirmed'
	`, batchID)
	if err != nil {
		return fmt.Errorf("update batch: %w", err)
	}

	// Update all blobs in batch
	_, err = tx.ExecContext(ctx, `
		UPDATE blobs
		SET status = 'verified'
		WHERE batch_id = ?
		  AND status = 'confirmed'
	`, batchID)
	if err != nil {
		return fmt.Errorf("update blobs: %w", err)
	}

	return tx.Commit()
}

// GetStuckBatches returns batches with no celestia_height that are older than threshold.
// These batches failed submission and need to be reverted to pending.
func (s *BlobStore) GetStuckBatches(ctx context.Context, olderThan time.Duration) ([]*Batch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT batch_id, batch_commitment, submitted_at
		FROM batches
		WHERE status = 'submitted'
		  AND celestia_height IS NULL
		  AND submitted_at < datetime('now', ?)
		ORDER BY batch_id ASC
	`, fmt.Sprintf("-%d seconds", int(olderThan.Seconds())))
	if err != nil {
		return nil, fmt.Errorf("query stuck batches: %w", err)
	}
	defer rows.Close()

	var batches []*Batch
	for rows.Next() {
		var batch Batch
		if err := rows.Scan(&batch.BatchID, &batch.BatchCommitment, &batch.SubmittedAt); err != nil {
			return nil, fmt.Errorf("scan batch: %w", err)
		}
		batches = append(batches, &batch)
	}

	return batches, rows.Err()
}

// CountUnconfirmedBatches returns the total count of all unconfirmed batches regardless of age
func (s *BlobStore) CountUnconfirmedBatches(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM batches
		WHERE status = 'submitted'
	`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count unconfirmed batches: %w", err)
	}
	return count, nil
}

// GetBatchByID retrieves a batch by its ID with full batch data
func (s *BlobStore) GetBatchByID(ctx context.Context, batchID int64) (*Batch, error) {
	var batch Batch
	var celestiaHeight sql.NullInt64
	var celestiaTxHash sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT batch_id, batch_commitment, batch_data, batch_size, blob_count,
		       celestia_height, celestia_tx_hash, status, created_at, submitted_at, confirmed_at
		FROM batches
		WHERE batch_id = ?
	`, batchID).Scan(
		&batch.BatchID,
		&batch.BatchCommitment,
		&batch.BatchData,
		&batch.BatchSize,
		&batch.BlobCount,
		&celestiaHeight,
		&celestiaTxHash,
		&batch.Status,
		&batch.CreatedAt,
		&batch.SubmittedAt,
		&batch.ConfirmedAt,
	)

	if err == sql.ErrNoRows {
		return nil, ErrBlobNotFound // Reuse same error for "not found"
	}
	if err != nil {
		return nil, fmt.Errorf("query batch: %w", err)
	}

	if celestiaHeight.Valid {
		height := uint64(celestiaHeight.Int64)
		batch.CelestiaHeight = &height
	}
	if celestiaTxHash.Valid {
		txHash := celestiaTxHash.String
		batch.CelestiaTxHash = &txHash
	}

	return &batch, nil
}

// GetBlobsByBatchID retrieves all blobs in a batch (for metrics/reporting)
func (s *BlobStore) GetBlobsByBatchID(ctx context.Context, batchID int64) ([]*Blob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, commitment, namespace, blob_data, blob_size, status,
		       batch_id, batch_index, celestia_height, created_at, confirmed_at
		FROM blobs
		WHERE batch_id = ?
		ORDER BY batch_index ASC
	`, batchID)
	if err != nil {
		return nil, fmt.Errorf("query blobs by batch: %w", err)
	}
	defer rows.Close()

	var blobs []*Blob
	for rows.Next() {
		var blob Blob
		var batchID sql.NullInt64
		var batchIndex sql.NullInt64
		var celestiaHeight sql.NullInt64
		var confirmedAt sql.NullTime

		if err := rows.Scan(&blob.ID, &blob.Commitment, &blob.Namespace,
			&blob.Data, &blob.Size, &blob.Status, &batchID, &batchIndex,
			&celestiaHeight, &blob.CreatedAt, &confirmedAt); err != nil {
			return nil, fmt.Errorf("scan blob: %w", err)
		}

		if batchID.Valid {
			blob.BatchID = &batchID.Int64
		}
		if batchIndex.Valid {
			idx := int(batchIndex.Int64)
			blob.BatchIndex = &idx
		}
		if celestiaHeight.Valid {
			height := uint64(celestiaHeight.Int64)
			blob.CelestiaHeight = &height
		}
		if confirmedAt.Valid {
			blob.ConfirmedAt = &confirmedAt.Time
		}

		blobs = append(blobs, &blob)
	}

	return blobs, rows.Err()
}

// GetBatchByCommitment retrieves a batch by its commitment
func (s *BlobStore) GetBatchByCommitment(ctx context.Context, commitment []byte) (*Batch, error) {
	var batch Batch
	var celestiaHeight sql.NullInt64
	var celestiaTxHash sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT batch_id, batch_commitment, batch_data, batch_size, blob_count,
		       celestia_height, celestia_tx_hash, status, created_at, submitted_at, confirmed_at
		FROM batches
		WHERE batch_commitment = ?
	`, commitment).Scan(
		&batch.BatchID,
		&batch.BatchCommitment,
		&batch.BatchData,
		&batch.BatchSize,
		&batch.BlobCount,
		&celestiaHeight,
		&celestiaTxHash,
		&batch.Status,
		&batch.CreatedAt,
		&batch.SubmittedAt,
		&batch.ConfirmedAt,
	)

	if err == sql.ErrNoRows {
		return nil, ErrBatchNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query batch by commitment: %w", err)
	}

	if celestiaHeight.Valid {
		height := uint64(celestiaHeight.Int64)
		batch.CelestiaHeight = &height
	}
	if celestiaTxHash.Valid {
		txHash := celestiaTxHash.String
		batch.CelestiaTxHash = &txHash
	}

	return &batch, nil
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

// BeginTx starts a new database transaction
func (s *BlobStore) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return s.db.BeginTx(ctx, nil)
}

// InsertBatchTx inserts a batch record within an existing transaction and returns the batch ID
func (s *BlobStore) InsertBatchTx(ctx context.Context, tx *sql.Tx, batch *Batch) (int64, error) {
	var height sql.NullInt64
	if batch.CelestiaHeight != nil {
		height = sql.NullInt64{Int64: int64(*batch.CelestiaHeight), Valid: true}
	}

	var confirmedAt sql.NullTime
	if batch.ConfirmedAt != nil {
		confirmedAt = sql.NullTime{Time: *batch.ConfirmedAt, Valid: true}
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO batches (
			batch_commitment, batch_data, batch_size, blob_count,
			celestia_height, status, created_at, submitted_at, confirmed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, batch.BatchCommitment, batch.BatchData, batch.BatchSize, batch.BlobCount,
		height, batch.Status, batch.CreatedAt, batch.SubmittedAt, confirmedAt)

	if err != nil {
		return 0, err
	}

	return result.LastInsertId()
}

// InsertBlobTx inserts a blob record within an existing transaction
func (s *BlobStore) InsertBlobTx(ctx context.Context, tx *sql.Tx, blob *Blob) error {
	var batchID sql.NullInt64
	if blob.BatchID != nil {
		batchID = sql.NullInt64{Int64: *blob.BatchID, Valid: true}
	}

	var batchIndex sql.NullInt64
	if blob.BatchIndex != nil {
		batchIndex = sql.NullInt64{Int64: int64(*blob.BatchIndex), Valid: true}
	}

	var height sql.NullInt64
	if blob.CelestiaHeight != nil {
		height = sql.NullInt64{Int64: int64(*blob.CelestiaHeight), Valid: true}
	}

	var submittedAt sql.NullTime
	if blob.SubmittedAt != nil {
		submittedAt = sql.NullTime{Time: *blob.SubmittedAt, Valid: true}
	}

	var confirmedAt sql.NullTime
	if blob.ConfirmedAt != nil {
		confirmedAt = sql.NullTime{Time: *blob.ConfirmedAt, Valid: true}
	}

	_, err := tx.ExecContext(ctx, `
		INSERT INTO blobs (
			commitment, namespace, blob_data, blob_size, status,
			batch_id, batch_index, celestia_height,
			created_at, submitted_at, confirmed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, blob.Commitment, blob.Namespace, blob.Data, blob.Size, blob.Status,
		batchID, batchIndex, height,
		blob.CreatedAt, submittedAt, confirmedAt)

	return err
}

// GetSyncState retrieves the last synced height for a worker
func (s *BlobStore) GetSyncState(ctx context.Context, workerName string) (uint64, error) {
	var lastHeight uint64
	err := s.db.QueryRowContext(ctx, `
		SELECT last_height FROM sync_state WHERE worker_name = ?
	`, workerName).Scan(&lastHeight)

	if err == sql.ErrNoRows {
		// No sync state yet, return 0
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("failed to get sync state: %w", err)
	}

	return lastHeight, nil
}

// UpdateSyncState updates the last synced height for a worker
func (s *BlobStore) UpdateSyncState(ctx context.Context, workerName string, lastHeight uint64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_state (worker_name, last_height, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(worker_name) DO UPDATE SET
			last_height = excluded.last_height,
			updated_at = CURRENT_TIMESTAMP
	`, workerName, lastHeight)

	if err != nil {
		return fmt.Errorf("failed to update sync state: %w", err)
	}

	return nil
}
