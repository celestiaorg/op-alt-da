package worker

import (
	"context"
	"encoding/hex"
	"fmt"
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

type SubmissionWorker struct {
	store      *db.BlobStore
	celestia   blobAPI.Module
	namespace  libshare.Namespace
	signerAddr []byte // 20-byte signer address for BlobV1 (CIP-21)
	log        log.Logger
	batchCfg   *batch.Config
	workerCfg  *Config
	metrics    *metrics.CelestiaMetrics
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
	if w.workerCfg.SubmitTimeout <= 0 {
		return fmt.Errorf("invalid submit timeout: %v (must be positive)", w.workerCfg.SubmitTimeout)
	}

	ticker := time.NewTicker(w.workerCfg.SubmitPeriod)
	defer ticker.Stop()

	w.log.Info("Submission worker started",
		"max_blobs_per_batch", w.batchCfg.MaxBlobs,
		"max_batch_size_kb", w.batchCfg.MaxBatchSizeBytes/1024,
		"period", w.workerCfg.SubmitPeriod,
		"submit_timeout", w.workerCfg.SubmitTimeout)

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
// and submits ALL batches to Celestia in a single Submit() call.
func (w *SubmissionWorker) submitPendingBlobs(ctx context.Context) error {
	// Fetch pending blobs - get enough for multiple batches
	maxFetch := w.batchCfg.MaxBlobs * 10 // Fetch enough for ~10 batches
	if maxFetch < 500 {
		maxFetch = 500
	}

	pendingBlobs, err := w.store.GetPendingBlobs(ctx, maxFetch)
	if err != nil {
		return fmt.Errorf("get pending blobs: %w", err)
	}

	if len(pendingBlobs) == 0 {
		w.log.Debug("No pending blobs")
		return nil
	}

	w.log.Info("Found pending blobs", "count", len(pendingBlobs))

	// Split into batches and prepare Celestia blobs
	batches, err := w.prepareBatches(ctx, pendingBlobs)
	if err != nil {
		return fmt.Errorf("prepare batches: %w", err)
	}

	if len(batches) == 0 {
		return nil
	}

	// Determine max concurrent submissions (semaphore)
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

	// Use errgroup with context for graceful shutdown
	// Each batch gets its own Submit() call with ONE blob (to stay under 8MB tx limit)
	// The semaphore limits how many concurrent submissions
	g, gCtx := errgroup.WithContext(ctx)
	semaphore := make(chan struct{}, maxConcurrent)

	// Track results with mutex for thread-safe access
	type submitResult struct {
		batchIdx int
		height   uint64
		batch    *batchInfo
	}
	var (
		resultsMu sync.Mutex
		results   []submitResult
	)
	successCount := 0

	for batchIdx, b := range batches {
		// Capture loop variables
		idx := batchIdx
		batch := b

		g.Go(func() error {
			// Acquire semaphore slot
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-gCtx.Done():
				w.log.Warn("Submission cancelled before start", "batch_id", batch.dbBatchID)
				return gCtx.Err()
			}

			// Submit ONE blob per Submit() call (to stay under 8MB tx limit)
			height, err := w.submitToCelestia(gCtx, []*blob.Blob{batch.celestiaBlob})
			if err != nil {
				w.log.Error("Batch submission failed",
					"batch_id", batch.dbBatchID,
					"size_bytes", len(batch.packedData),
					"error", err)
				return nil // Don't fail the whole group
			}

			// Store result thread-safely
			resultsMu.Lock()
			results = append(results, submitResult{
				batchIdx: idx,
				height:   height,
				batch:    batch,
			})
			resultsMu.Unlock()

			return nil
		})
	}

	// Wait for all goroutines to complete or context cancellation
	if err := g.Wait(); err != nil {
		w.log.Warn("Parallel submission interrupted", "error", err)
	}

	// Process results
	for _, result := range results {
		successCount++

		if err := w.store.UpdateBatchHeight(ctx, result.batch.dbBatchID, result.height); err != nil {
			w.log.Error("Failed to update batch height",
				"batch_id", result.batch.dbBatchID,
				"height", result.height,
				"error", err)
		}

		w.log.Info("✅ Batch submitted",
			"batch_id", result.batch.dbBatchID,
			"height", result.height,
			"blob_count", len(result.batch.blobIDs),
			"size_bytes", len(result.batch.packedData),
			"celenium_block", fmt.Sprintf("https://mocha.celenium.io/block/%d", result.height))
	}

	// Record metrics
	if w.metrics != nil {
		now := time.Now()
		for _, b := range batches {
			for _, blob := range b.blobs {
				w.metrics.RecordTimeToBatch(now.Sub(blob.CreatedAt))
			}
		}
	}

	w.log.Info("✅ Submission complete",
		"batch_count", len(batches),
		"successful", successCount,
		"max_concurrent", maxConcurrent)

	// Return context error if we were interrupted
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// prepareBatches splits pending blobs into batches and creates DB records
func (w *SubmissionWorker) prepareBatches(ctx context.Context, pendingBlobs []*db.Blob) ([]*batchInfo, error) {
	var batches []*batchInfo
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

		// Create batch in DB
		dbBatchID, err := w.store.CreateBatch(ctx, blobIDs, batchCommitment, packedData)
		if err != nil {
			return nil, fmt.Errorf("create batch in db: %w", err)
		}

		// Create Celestia blob
		celestiaBlob, err := blob.NewBlobV1(w.namespace, packedData, w.signerAddr)
		if err != nil {
			return nil, fmt.Errorf("create celestia blob: %w", err)
		}

		batches = append(batches, &batchInfo{
			blobIDs:         blobIDs,
			batchCommitment: batchCommitment,
			packedData:      packedData,
			celestiaBlob:    celestiaBlob,
			dbBatchID:       dbBatchID,
			blobs:           selected,
		})

		w.log.Debug("Prepared batch",
			"batch_id", dbBatchID,
			"blob_count", len(selected),
			"size_bytes", len(packedData),
			"commitment", hex.EncodeToString(batchCommitment[:8]))

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
