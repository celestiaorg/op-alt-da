package worker

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/celestiaorg/celestia-node/blob"
	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	"github.com/celestiaorg/celestia-node/state"
	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/ethereum/go-ethereum/log"
	"golang.org/x/sync/errgroup"

	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/commitment"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/metrics"
)

// retryOnDBLock retries a database operation if it fails with "database is locked"
func retryOnDBLock(fn func() error) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			time.Sleep(100 * time.Millisecond * time.Duration(attempt))
		}
		err = fn()
		if err == nil {
			return nil
		}
		// Only retry on SQLite lock errors
		errStr := err.Error()
		if !strings.Contains(errStr, "database is locked") && !strings.Contains(errStr, "SQLITE_BUSY") {
			return err
		}
	}
	return err
}

type SubmissionWorker struct {
	store      *db.BlobStore
	celestia   blobAPI.Module
	namespace  libshare.Namespace
	signerAddr []byte // 20-byte signer address for BlobV1 (CIP-21)
	log        log.Logger
	batchCfg   *batch.Config
	workerCfg  *Config
	metrics    *metrics.CelestiaMetrics

	// Mutex to prevent overlapping submission attempts
	// This ensures blobs aren't picked up by multiple ticks simultaneously
	submitting   bool
	submittingMu sync.Mutex
}

func NewSubmissionWorker(
	store *db.BlobStore,
	celestia blobAPI.Module,
	namespace libshare.Namespace,
	signerAddr []byte,
	batchCfg *batch.Config,
	workerCfg *Config,
	metrics *metrics.CelestiaMetrics,
	log log.Logger,
) *SubmissionWorker {
	return &SubmissionWorker{
		store:      store,
		celestia:   celestia,
		namespace:  namespace,
		signerAddr: signerAddr,
		log:        log,
		batchCfg:   batchCfg,
		workerCfg:  workerCfg,
		metrics:    metrics,
	}
}

func (w *SubmissionWorker) Run(ctx context.Context) error {
	if w.workerCfg.SubmitPeriod <= 0 {
		return fmt.Errorf("invalid submit period: %v (must be positive)", w.workerCfg.SubmitPeriod)
	}

	ticker := time.NewTicker(w.workerCfg.SubmitPeriod)
	defer ticker.Stop()

	w.log.Info("Submission worker started",
		"max_blobs_per_batch", w.batchCfg.MaxBlobs,
		"max_batch_size_kb", w.batchCfg.MaxBatchSizeBytes/1024,
		"max_parallel", w.workerCfg.MaxParallelSubmissions,
		"period", w.workerCfg.SubmitPeriod)

	for {
		select {
		case <-ctx.Done():
			w.log.Info("Submission worker stopping")
			return ctx.Err()
		case <-ticker.C:
			if err := w.submitPendingBlobs(ctx); err != nil {
				w.log.Error("Submit pending blobs failed", "error", err)
			}
		}
	}
}

// pendingBatch holds prepared batch data BEFORE DB insertion
// DB record is only created AFTER successful Celestia submission
type pendingBatch struct {
	blobIDs         []int64
	batchCommitment []byte
	packedData      []byte
	celestiaBlob    *blob.Blob
	blobs           []*db.Blob
}

// submitBatch submits a single batch to Celestia and persists to DB on success.
// Returns true if successful, false if failed (blobs will retry next tick).
func (w *SubmissionWorker) submitBatch(ctx context.Context, batch *pendingBatch) bool {
	// Step 1: Submit to Celestia with retries
	height, err := w.submitWithRetry(ctx, batch.celestiaBlob)
	if err != nil {
		w.log.Error("Batch submission failed",
			"blob_count", len(batch.blobIDs),
			"size_bytes", len(batch.packedData),
			"error", err)
		return false
	}

	// Step 2: Persist to DB (only after successful Celestia submission)
	dbBatchID, err := w.persistBatchToDB(ctx, batch, height)
	if err != nil {
		w.log.Error("Failed to persist batch to DB",
			"height", height,
			"blob_count", len(batch.blobIDs),
			"error", err)
		return false // Will be picked up by backfill
	}

	// Step 3: Log success and record metrics
	w.log.Info("✅ Batch submitted",
		"batch_id", dbBatchID,
		"height", height,
		"blob_count", len(batch.blobIDs),
		"size_bytes", len(batch.packedData))

	w.recordMetrics(batch)
	return true
}

// submitWithRetry attempts to submit a blob to Celestia with linear backoff.
func (w *SubmissionWorker) submitWithRetry(ctx context.Context, celestiaBlob *blob.Blob) (uint64, error) {
	var lastErr error

	for attempt := 1; attempt <= w.workerCfg.MaxRetries; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(w.workerCfg.RetryBackoff):
			}
			w.log.Info("Retrying submission", "attempt", attempt)
		}

		height, err := w.submitToCelestia(ctx, []*blob.Blob{celestiaBlob})
		if err == nil {
			return height, nil
		}

		lastErr = err
		w.log.Warn("Submission attempt failed", "attempt", attempt, "error", err)
	}

	return 0, fmt.Errorf("failed after %d attempts: %w", w.workerCfg.MaxRetries, lastErr)
}

// persistBatchToDB creates the batch record and marks it confirmed.
func (w *SubmissionWorker) persistBatchToDB(ctx context.Context, batch *pendingBatch, height uint64) (int64, error) {
	var dbBatchID int64

	// Create batch record
	err := retryOnDBLock(func() error {
		var createErr error
		dbBatchID, createErr = w.store.CreateBatch(ctx, batch.blobIDs, batch.batchCommitment, batch.packedData)
		return createErr
	})
	if err != nil {
		return 0, fmt.Errorf("create batch: %w", err)
	}

	// Update height
	if err := retryOnDBLock(func() error {
		return w.store.UpdateBatchHeight(ctx, dbBatchID, height)
	}); err != nil {
		w.log.Warn("Failed to update batch height", "batch_id", dbBatchID, "error", err)
	}

	// Mark confirmed
	if err := retryOnDBLock(func() error {
		return w.store.MarkBatchConfirmedByID(ctx, dbBatchID)
	}); err != nil {
		w.log.Warn("Failed to mark batch confirmed", "batch_id", dbBatchID, "error", err)
	}

	return dbBatchID, nil
}

// recordMetrics records timing metrics for successfully submitted blobs.
func (w *SubmissionWorker) recordMetrics(batch *pendingBatch) {
	if w.metrics == nil {
		return
	}
	now := time.Now()
	for _, b := range batch.blobs {
		w.metrics.RecordTimeToBatch(now.Sub(b.CreatedAt))
	}
}

// submitBatchesConcurrently submits batches in parallel with a concurrency limit.
// Returns the number of successfully submitted batches.
func (w *SubmissionWorker) submitBatchesConcurrently(ctx context.Context, batches []*pendingBatch, maxConcurrent int) int {
	var (
		mu           sync.Mutex
		successCount int
	)

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrent)

	for _, batch := range batches {
		b := batch
		g.Go(func() error {
			if gCtx.Err() != nil {
				return gCtx.Err()
			}
			if w.submitBatch(gCtx, b) {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		w.log.Debug("Batch submission interrupted", "error", err)
	}
	return successCount
}

// submitPendingBlobs fetches ALL pending blobs, splits into batches, and submits in parallel.
// CRITICAL: DB batch records are only created AFTER successful Celestia submission.
// This ensures failed submissions don't leave blobs stuck in "batched" status.
func (w *SubmissionWorker) submitPendingBlobs(ctx context.Context) error {
	// Prevent overlapping submission attempts
	// If previous tick is still submitting, skip this tick
	w.submittingMu.Lock()
	if w.submitting {
		w.submittingMu.Unlock()
		w.log.Debug("Skipping tick - previous submission still in progress")
		return nil
	}
	w.submitting = true
	w.submittingMu.Unlock()

	defer func() {
		w.submittingMu.Lock()
		w.submitting = false
		w.submittingMu.Unlock()
	}()

	// Fetch ALL pending blobs (large limit to fill a Celestia block)
	maxFetch := w.batchCfg.MaxBlobs * w.workerCfg.MaxParallelSubmissions * 2
	if maxFetch < 100 {
		maxFetch = 100
	}

	pendingBlobs, err := w.store.GetPendingBlobs(ctx, maxFetch)
	if err != nil {
		return fmt.Errorf("get pending blobs: %w", err)
	}

	if len(pendingBlobs) == 0 {
		return nil
	}

	w.log.Info("Found pending blobs", "count", len(pendingBlobs))

	// Prepare batches WITHOUT creating DB records yet
	batches := w.prepareBatches(pendingBlobs)
	if len(batches) == 0 {
		return nil
	}

	// Determine concurrency
	maxConcurrent := w.workerCfg.MaxParallelSubmissions
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	if maxConcurrent > len(batches) {
		maxConcurrent = len(batches)
	}

	w.log.Info("Submitting batches to Celestia",
		"batch_count", len(batches),
		"max_concurrent", maxConcurrent,
		"total_blobs", len(pendingBlobs))

	successCount := w.submitBatchesConcurrently(ctx, batches, maxConcurrent)

	w.log.Info("✅ Submission complete",
		"batch_count", len(batches),
		"successful", successCount,
		"failed", len(batches)-successCount)

	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// prepareBatches splits pending blobs into batches WITHOUT creating DB records.
// DB records are only created after successful Celestia submission.
func (w *SubmissionWorker) prepareBatches(pendingBlobs []*db.Blob) []*pendingBatch {
	var batches []*pendingBatch
	remaining := pendingBlobs

	for len(remaining) > 0 {
		// Select blobs that fit in one batch
		selected := w.selectBlobsForBatch(remaining)
		if len(selected) == 0 {
			// First blob too large - skip it
			if len(remaining) > 0 {
				w.log.Error("Blob too large, skipping",
					"blob_id", remaining[0].ID,
					"size", len(remaining[0].Data),
					"max", w.batchCfg.MaxBatchSizeBytes)
				remaining = remaining[1:]
			}
			continue
		}

		// Pack blobs
		packedData, err := batch.PackBlobs(selected, w.batchCfg)
		if err != nil {
			w.log.Error("Failed to pack blobs", "error", err)
			remaining = remaining[len(selected):]
			continue
		}

		// Compute batch commitment
		batchCommitment, err := commitment.ComputeCommitment(packedData, w.namespace, w.signerAddr)
		if err != nil {
			w.log.Error("Failed to compute commitment", "error", err)
			remaining = remaining[len(selected):]
			continue
		}

		// Extract blob IDs
		blobIDs := make([]int64, len(selected))
		for i, b := range selected {
			blobIDs[i] = b.ID
		}

		// Create Celestia blob
		celestiaBlob, err := blob.NewBlobV1(w.namespace, packedData, w.signerAddr)
		if err != nil {
			w.log.Error("Failed to create celestia blob", "error", err)
			remaining = remaining[len(selected):]
			continue
		}

		batches = append(batches, &pendingBatch{
			blobIDs:         blobIDs,
			batchCommitment: batchCommitment,
			packedData:      packedData,
			celestiaBlob:    celestiaBlob,
			blobs:           selected,
		})

		w.log.Debug("Prepared batch",
			"blob_count", len(selected),
			"size_bytes", len(packedData))

		remaining = remaining[len(selected):]
	}

	return batches
}

// selectBlobsForBatch selects blobs that fit within MaxBatchSizeBytes
func (w *SubmissionWorker) selectBlobsForBatch(blobs []*db.Blob) []*db.Blob {
	if len(blobs) == 0 {
		return nil
	}

	const countOverhead = 4
	const sizeOverhead = 4

	selected := make([]*db.Blob, 0, len(blobs))
	currentSize := countOverhead

	for i, blob := range blobs {
		blobPackedSize := sizeOverhead + len(blob.Data)
		newSize := currentSize + blobPackedSize

		if newSize > w.batchCfg.MaxBatchSizeBytes {
			if i == 0 {
				return nil // First blob too large
			}
			break
		}

		if len(selected) >= w.batchCfg.MaxBlobs {
			break
		}

		selected = append(selected, blob)
		currentSize = newSize
	}

	return selected
}

// submitToCelestia submits blobs directly to Celestia without timeout
// The Celestia client handles its own internal timeouts and retries
func (w *SubmissionWorker) submitToCelestia(ctx context.Context, blobs []*blob.Blob) (uint64, error) {
	if len(w.signerAddr) != 20 {
		return 0, fmt.Errorf("invalid signer address: expected 20 bytes, got %d", len(w.signerAddr))
	}

	startTime := time.Now()

	// Submit directly - let Celestia client handle timeouts internally
	height, err := w.celestia.Submit(ctx, blobs, state.NewTxConfig())
	duration := time.Since(startTime)

	if err != nil {
		w.log.Error("Celestia submission failed",
			"blob_count", len(blobs),
			"duration_ms", duration.Milliseconds(),
			"error", err)
		if w.metrics != nil {
			w.metrics.RecordSubmissionError()
		}
		return 0, fmt.Errorf("celestia submit: %w", err)
	}

	if w.metrics != nil {
		totalSize := 0
		for _, b := range blobs {
			totalSize += len(b.Data())
		}
		w.metrics.RecordSubmission(duration, totalSize)
	}

	w.log.Debug("Celestia submission successful",
		"height", height,
		"blob_count", len(blobs),
		"duration_ms", duration.Milliseconds())

	return height, nil
}
