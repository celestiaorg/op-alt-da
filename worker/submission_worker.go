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

	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/commitment"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/metrics"
)

// retryDBOp retries a database operation if it fails with "database is locked"
func retryDBOp(maxRetries int, backoff time.Duration, op func() error) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		lastErr = op()
		if lastErr == nil {
			return nil
		}
		if !strings.Contains(lastErr.Error(), "database is locked") {
			return lastErr
		}
		// Database locked - wait and retry
		time.Sleep(backoff * time.Duration(attempt+1))
	}
	return lastErr
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
	semaphore  chan struct{} // Persistent semaphore for concurrent submissions
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
	// Initialize semaphore for concurrent submissions
	maxConcurrent := workerCfg.MaxParallelSubmissions
	if maxConcurrent <= 0 {
		maxConcurrent = 100 // Reasonable default
	}

	return &SubmissionWorker{
		store:      store,
		celestia:   celestia,
		namespace:  namespace,
		signerAddr: signerAddr,
		log:        log,
		batchCfg:   batchCfg,
		workerCfg:  workerCfg,
		metrics:    metrics,
		semaphore:  make(chan struct{}, maxConcurrent),
	}
}

func (w *SubmissionWorker) Run(ctx context.Context) error {
	if w.workerCfg.SubmitTimeout <= 0 {
		return fmt.Errorf("invalid submit timeout: %v (must be positive)", w.workerCfg.SubmitTimeout)
	}

	w.log.Info("Submission worker started (continuous mode)",
		"max_blobs_per_batch", w.batchCfg.MaxBlobs,
		"max_batch_size_bytes", w.batchCfg.MaxBatchSizeBytes,
		"max_batch_size_kb", w.batchCfg.MaxBatchSizeBytes/1024,
		"max_parallel", w.workerCfg.MaxParallelSubmissions,
		"submit_timeout", w.workerCfg.SubmitTimeout)

	// Fire-fast loop - don't hold back, let Celestia queue handle the rest
	// 50ms checks to keep up with high-rate PUTs (100ms+)
	// Semaphore limits concurrent submissions, excess goroutines wait
	// Celestia's 10-12s inclusion time naturally queues submissions

	for {
		if ctx.Err() != nil {
			w.log.Info("Submission worker stopping")
			return ctx.Err()
		}

		// Fire submissions for ALL pending blobs immediately
		submitted, err := w.submitPendingBlobs(ctx)
		if err != nil && ctx.Err() == nil {
			w.log.Error("Submit pending blobs failed", "error", err)
		}

		// If we submitted something, immediately check for more (no delay)
		// Only pause briefly if nothing was pending
		if submitted == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Millisecond): // Short pause only when idle
			}
		}
		// When actively submitting, NO DELAY - fire as fast as possible
	}
}

// batchInfo holds metadata about a prepared batch
type batchInfo struct {
	blobIDs         []int64
	batchCommitment []byte
	packedData      []byte
	celestiaBlob    *blob.Blob
	dbBatchID       int64
	blobs           []*db.Blob // Original blobs for metrics
}

// submitPendingBlobs fetches all pending blobs, packs them into batches,
// and fires submissions to Celestia. Returns count of blobs processed.
func (w *SubmissionWorker) submitPendingBlobs(ctx context.Context) (int, error) {
	// Fetch as many pending blobs as possible
	maxFetch := 1000 // Process up to 1000 blobs per tick

	pendingBlobs, err := w.store.GetPendingBlobs(ctx, maxFetch)
	if err != nil {
		return 0, fmt.Errorf("get pending blobs: %w", err)
	}

	if len(pendingBlobs) == 0 {
		return 0, nil
	}

	// Backlog logging at debug level only
	w.log.Debug("Processing", "pending", len(pendingBlobs))

	// Split into batches and prepare Celestia blobs
	prepareStart := time.Now()
	batches, err := w.prepareBatches(ctx, pendingBlobs)
	prepareTime := time.Since(prepareStart)
	if prepareTime > 500*time.Millisecond {
		w.log.Debug("Batch preparation", "duration_ms", prepareTime.Milliseconds(), "batches", len(batches))
	}
	if err != nil {
		return 0, fmt.Errorf("prepare batches: %w", err)
	}

	if len(batches) == 0 {
		return 0, nil
	}

	// Group batches for multi-blob submission
	blobGroups := w.groupBatchesForSubmit(batches)

	// Count total blobs being submitted
	totalBlobsToSubmit := 0
	totalSizeToSubmit := 0
	for _, group := range blobGroups {
		for _, b := range group {
			totalBlobsToSubmit += len(b.blobIDs)
			totalSizeToSubmit += len(b.packedData)
		}
	}

	// Submitting logged at debug level
	w.log.Debug("Submitting", "blobs", totalBlobsToSubmit)

	// Use WaitGroup to wait for all submissions before returning
	// This prevents re-fetching the same blobs while submissions are in flight
	var wg sync.WaitGroup

	for _, group := range blobGroups {
		// Capture loop variable
		batchGroup := group
		wg.Add(1)

		go func() {
			defer wg.Done()
			// Acquire semaphore slot from persistent semaphore
			select {
			case w.semaphore <- struct{}{}:
				defer func() { <-w.semaphore }()
			case <-ctx.Done():
				// Cancelled before submission - blobs stay in pending_submission, will retry next tick
				w.log.Debug("Submission cancelled", "blob_count", len(batchGroup))
				return
			}

			// Collect all Celestia blobs for this group
			celestiaBlobs := make([]*blob.Blob, len(batchGroup))
			totalSize := 0
			for i, b := range batchGroup {
				celestiaBlobs[i] = b.celestiaBlob
				totalSize += len(b.packedData)
			}

			// Submit ALL blobs in this group in ONE call
			height, err := w.submitToCelestia(ctx, celestiaBlobs)
			if err != nil {
				w.log.Error("Submission failed - blobs will retry",
					"blob_count", len(batchGroup),
					"total_size_bytes", totalSize,
					"error", err)
				// No revert needed - blobs stay in pending_submission, will retry next tick
				return
			}

			// Submit succeeded - NOW create batch records and mark confirmed
			// This moves all DB writes to AFTER successful Celestia submission
			totalBlobs := 0
			for _, b := range batchGroup {
				// Create batch and mark blobs confirmed in one atomic operation
				err := retryDBOp(5, 100*time.Millisecond, func() error {
					return w.store.CreateBatchAndConfirm(context.Background(), b.blobIDs, b.batchCommitment, b.packedData, height)
				})
				if err != nil {
					w.log.Error("Failed to create batch after submit",
						"height", height,
						"blob_count", len(b.blobIDs),
						"error", err)
					continue
				}
				totalBlobs += len(b.blobIDs)
			}

			// Individual confirmations at debug level
			w.log.Debug("Confirmed",
				"height", height,
				"blobs", totalBlobs)
		}()
	}

	// Wait for all submissions to complete before returning
	// This prevents re-fetching the same blobs while submissions are in flight
	wg.Wait()

	// Record metrics
	if w.metrics != nil {
		now := time.Now()
		for _, b := range batches {
			for _, blob := range b.blobs {
				w.metrics.RecordTimeToBatch(now.Sub(blob.CreatedAt))
			}
		}
	}

	return len(pendingBlobs), nil
}

// prepareBatches splits pending blobs into batches and creates DB records
func (w *SubmissionWorker) prepareBatches(ctx context.Context, pendingBlobs []*db.Blob) ([]*batchInfo, error) {
	var batches []*batchInfo
	remaining := pendingBlobs

	// Debug: log total size and first blob size
	if len(pendingBlobs) > 0 {
		totalSize := 0
		for _, b := range pendingBlobs {
			totalSize += len(b.Data)
		}
		w.log.Debug("Preparing batches",
			"blobs", len(pendingBlobs),
			"total_size_mb", totalSize/(1024*1024),
			"first_blob_size", len(pendingBlobs[0].Data),
			"max_batch_size", w.batchCfg.MaxBatchSizeBytes)
	}

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
			return nil, fmt.Errorf("pack blobs: %w", err)
		}

		// Compute batch commitment
		batchCommitment, err := commitment.ComputeCommitment(packedData, w.namespace, w.signerAddr)
		if err != nil {
			return nil, fmt.Errorf("compute commitment: %w", err)
		}

		// Extract blob IDs
		blobIDs := make([]int64, len(selected))
		for i, b := range selected {
			blobIDs[i] = b.ID
		}

		// Create Celestia blob (NO DB write yet - defer until after successful submit)
		celestiaBlob, err := blob.NewBlobV1(w.namespace, packedData, w.signerAddr)
		if err != nil {
			return nil, fmt.Errorf("create celestia blob: %w", err)
		}

		batches = append(batches, &batchInfo{
			blobIDs:         blobIDs,
			batchCommitment: batchCommitment,
			packedData:      packedData,
			celestiaBlob:    celestiaBlob,
			dbBatchID:       0, // Will be set after successful submit
			blobs:           selected,
		})

		remaining = remaining[len(selected):]
	}

	return batches, nil
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
				w.log.Warn("First blob too large for batch",
					"blob_size", len(blob.Data),
					"max_batch_size", w.batchCfg.MaxBatchSizeBytes)
				return nil // First blob too large
			}
			w.log.Debug("Batch size limit reached",
				"selected", len(selected),
				"current_size", currentSize,
				"max", w.batchCfg.MaxBatchSizeBytes)
			break
		}

		if len(selected) >= w.batchCfg.MaxBlobs {
			w.log.Debug("Batch blob count limit reached",
				"selected", len(selected),
				"max_blobs", w.batchCfg.MaxBlobs)
			break
		}

		selected = append(selected, blob)
		currentSize = newSize
	}

	return selected
}

// groupBatchesForSubmit groups batches into Submit() calls (1 group = 1 TX)
// Each TX is limited by max_size_kb from batch config
// One account can submit MULTIPLE TXs per Celestia block
// All groups submitted in parallel ASAP to land in same block
func (w *SubmissionWorker) groupBatchesForSubmit(batches []*batchInfo) [][]*batchInfo {
	if len(batches) == 0 {
		return nil
	}

	// Per-TX limit from batch config (max_size_kb)
	maxTxSize := w.batchCfg.MaxBatchSizeBytes
	if maxTxSize <= 0 {
		maxTxSize = 2 * 1024 * 1024 // 2MB default
	}

	w.log.Debug("Grouping batches for submit",
		"batches", len(batches),
		"max_tx_size", maxTxSize,
		"first_batch_size", len(batches[0].packedData))

	var groups [][]*batchInfo
	var currentGroup []*batchInfo
	currentSize := 0

	for _, batch := range batches {
		blobSize := len(batch.packedData)

		// If single batch exceeds TX limit, it gets its own group
		if blobSize > maxTxSize {
			if len(currentGroup) > 0 {
				groups = append(groups, currentGroup)
				currentGroup = nil
				currentSize = 0
			}
			groups = append(groups, []*batchInfo{batch})
			continue
		}

		// If adding this batch would exceed TX limit, start new group (new TX)
		if currentSize+blobSize > maxTxSize && len(currentGroup) > 0 {
			groups = append(groups, currentGroup)
			currentGroup = nil
			currentSize = 0
		}

		// Add batch to current group
		currentGroup = append(currentGroup, batch)
		currentSize += blobSize
	}

	// Don't forget the last group
	if len(currentGroup) > 0 {
		groups = append(groups, currentGroup)
	}

	return groups
}

// submitToCelestia submits all blobs in a single call with retry logic
func (w *SubmissionWorker) submitToCelestia(ctx context.Context, blobs []*blob.Blob) (uint64, error) {
	if len(w.signerAddr) != 20 {
		return 0, fmt.Errorf("invalid signer address: expected 20 bytes, got %d", len(w.signerAddr))
	}

	var height uint64
	var lastErr error
	backoff := 1 * time.Second

	for attempt := 0; attempt <= w.workerCfg.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(backoff):
				backoff *= 2
				if backoff > 60*time.Second {
					backoff = 60 * time.Second
				}
			}

			w.log.Info("Retrying submission",
				"attempt", attempt+1,
				"max_retries", w.workerCfg.MaxRetries,
				"blob_count", len(blobs))
		}

		submitCtx, cancel := context.WithTimeout(ctx, w.workerCfg.SubmitTimeout)
		startTime := time.Now()

		// Submit ALL blobs in one call - Celestia puts them in same block
		height, lastErr = w.celestia.Submit(submitCtx, blobs, state.NewTxConfig())
		duration := time.Since(startTime)
		cancel()

		if lastErr == nil {
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

		w.log.Warn("Submission attempt failed",
			"attempt", attempt+1,
			"error", lastErr)
	}

	if w.metrics != nil {
		w.metrics.RecordSubmissionError()
	}

	return 0, fmt.Errorf("celestia submit failed after %d attempts: %w", w.workerCfg.MaxRetries+1, lastErr)
}
