package worker

import (
	"context"
	"fmt"
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

type SubmissionWorker struct {
	store     *db.BlobStore
	celestia  blobAPI.Module
	namespace libshare.Namespace
	log       log.Logger
	batchCfg  *batch.Config
	workerCfg *Config
	metrics   *metrics.CelestiaMetrics
}

func NewSubmissionWorker(
	store *db.BlobStore,
	celestia blobAPI.Module,
	namespace libshare.Namespace,
	batchCfg *batch.Config,
	workerCfg *Config,
	metrics *metrics.CelestiaMetrics,
	log log.Logger,
) *SubmissionWorker {
	return &SubmissionWorker{
		store:     store,
		celestia:  celestia,
		namespace: namespace,
		log:       log,
		batchCfg:  batchCfg,
		workerCfg: workerCfg,
		metrics:   metrics,
	}
}

func (w *SubmissionWorker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.workerCfg.SubmitPeriod)
	defer ticker.Stop()

	w.log.Info("Submission worker started",
		"target_blobs", w.batchCfg.TargetBlobs,
		"max_blobs", w.batchCfg.MaxBlobs,
		"period", w.workerCfg.SubmitPeriod,
		"submit_timeout", w.workerCfg.SubmitTimeout)

	for {
		select {
		case <-ctx.Done():
			w.log.Info("Submission worker stopping")
			return ctx.Err()
		case <-ticker.C:
			if err := w.processBatch(ctx); err != nil {
				w.log.Error("Process batch failed", "error", err)
			}
		}
	}
}

func (w *SubmissionWorker) processBatch(ctx context.Context) error {
	// Get pending blobs (FIFO order) - fetch up to TargetBlobs
	blobs, err := w.store.GetPendingBlobs(ctx, w.batchCfg.TargetBlobs)
	if err != nil {
		return fmt.Errorf("get pending blobs: %w", err)
	}

	if len(blobs) == 0 {
		return nil // No work to do
	}

	// Check if we should batch now
	totalSize := 0
	for _, b := range blobs {
		totalSize += len(b.Data)
	}

	// Only batch if we have enough blobs or enough data
	if !w.batchCfg.ShouldBatch(len(blobs), totalSize) {
		// Not enough blobs yet, wait for more
		return nil
	}

	w.log.Info("Processing batch", "blob_count", len(blobs), "total_size", totalSize)

	// Pack blobs into one batch
	packedData, err := batch.PackBlobs(blobs, w.batchCfg)
	if err != nil {
		return fmt.Errorf("pack blobs: %w", err)
	}

	// Compute batch commitment
	batchCommitment, err := commitment.ComputeCommitment(packedData, w.namespace)
	if err != nil {
		return fmt.Errorf("compute batch commitment: %w", err)
	}

	// Extract blob IDs for database update
	blobIDs := make([]int64, len(blobs))
	for i, b := range blobs {
		blobIDs[i] = b.ID
	}

	// Create batch in database
	batchID, err := w.store.CreateBatch(ctx, blobIDs, batchCommitment, packedData)
	if err != nil {
		return fmt.Errorf("create batch: %w", err)
	}

	w.log.Info("Created batch", "batch_id", batchID, "blob_count", len(blobs))

	// Submit to Celestia
	if err := w.submitBatch(ctx, batchCommitment, packedData); err != nil {
		w.log.Error("Submit batch failed", "batch_id", batchID, "error", err)
		return err
	}

	w.log.Info("Batch submitted to Celestia", "batch_id", batchID)
	return nil
}

func (w *SubmissionWorker) submitBatch(ctx context.Context, batchCommitment, packedData []byte) error {
	// Create Celestia blob
	celestiaBlob, err := blob.NewBlobV0(w.namespace, packedData)
	if err != nil {
		return fmt.Errorf("create celestia blob: %w", err)
	}

	// Implement retry logic with exponential backoff
	var height uint64
	var lastErr error
	backoff := 1 * time.Second

	for attempt := 0; attempt <= w.workerCfg.MaxRetries; attempt++ {
		if attempt > 0 {
			// Wait with exponential backoff before retry
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				// Double backoff for next attempt, cap at 60s
				backoff *= 2
				if backoff > 60*time.Second {
					backoff = 60 * time.Second
				}
			}

			w.log.Info("Retrying batch submission",
				"attempt", attempt+1,
				"max_retries", w.workerCfg.MaxRetries,
				"backoff", backoff)
		}

		// Submit to Celestia with configurable timeout
		submitCtx, cancel := context.WithTimeout(ctx, w.workerCfg.SubmitTimeout)

		// Record submission metrics (time + size)
		startTime := time.Now()
		height, lastErr = w.celestia.Submit(submitCtx, []*blob.Blob{celestiaBlob}, state.NewTxConfig())
		duration := time.Since(startTime)
		cancel() // Clean up context

		if lastErr == nil {
			// Success! Record metrics and return
			if w.metrics != nil {
				w.metrics.RecordSubmission(duration, len(packedData))
			}

			w.log.Info("Batch submitted to Celestia",
				"height", height,
				"commitment", fmt.Sprintf("%x", batchCommitment[:8]),
				"size_bytes", len(packedData),
				"duration_ms", duration.Milliseconds(),
				"attempts", attempt+1)

			return nil
		}

		// Failed - log and potentially retry
		w.log.Warn("Batch submission attempt failed",
			"attempt", attempt+1,
			"error", lastErr)
	}

	// All retries exhausted
	if w.metrics != nil {
		w.metrics.RecordSubmissionError()
	}

	return fmt.Errorf("celestia submit failed after %d attempts: %w", w.workerCfg.MaxRetries+1, lastErr)
}
