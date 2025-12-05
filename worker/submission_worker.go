package worker

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/celestiaorg/celestia-node/blob"
	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	headerAPI "github.com/celestiaorg/celestia-node/nodebuilder/header"
	"github.com/celestiaorg/celestia-node/state"
	libshare "github.com/celestiaorg/go-square/v3/share"
	sdk "github.com/cosmos/cosmos-sdk/types"
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
	header     headerAPI.Module
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
	header headerAPI.Module,
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
		header:     header,
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
		"max_batch_size_kb", w.batchCfg.MaxBatchSizeBytes/1024,
		"max_parallel", w.workerCfg.MaxParallelSubmissions,
		"submit_timeout", w.workerCfg.SubmitTimeout,
		"tx_priority", w.workerCfg.TxPriority)

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

// batchInfo holds metadata about a prepared batch ready for Celestia submission
type batchInfo struct {
	blobIDs         []int64    // IDs of blobs in this batch (for DB update after submit)
	batchCommitment []byte     // Commitment hash for this batch
	packedData      []byte     // Packed blob data to submit
	celestiaBlob    *blob.Blob // Celestia blob object
	blobs           []*db.Blob // Original blob records (for metrics)
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

	w.log.Debug("Submitting", "blobs", totalBlobsToSubmit)

	// Submit all groups concurrently, wait for completion
	var wg sync.WaitGroup
	for _, group := range blobGroups {
		wg.Add(1)
		go w.submitBatchGroup(ctx, group, &wg)
	}
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

// submitBatchGroup submits a group of batches to Celestia and confirms them in DB.
// Called as a goroutine - acquires semaphore slot to limit concurrency.
func (w *SubmissionWorker) submitBatchGroup(ctx context.Context, group []*batchInfo, wg *sync.WaitGroup) {
	defer wg.Done()

	// Acquire semaphore slot (limits concurrent submissions)
	select {
	case w.semaphore <- struct{}{}:
		defer func() { <-w.semaphore }()
	case <-ctx.Done():
		return
	}

	// Collect Celestia blobs and calculate total size + blob count
	celestiaBlobs := make([]*blob.Blob, len(group))
	totalSizeBytes := 0
	totalBlobCount := 0
	for i, b := range group {
		celestiaBlobs[i] = b.celestiaBlob
		totalSizeBytes += len(b.packedData)
		totalBlobCount += len(b.blobIDs)
	}

	// Submit to Celestia
	height, err := w.submitToCelestia(ctx, celestiaBlobs)
	if err != nil {
		w.log.Error("Submission failed",
			"blobs", totalBlobCount,
			"size_mb", fmt.Sprintf("%.2f", float64(totalSizeBytes)/(1024*1024)),
			"error", err)
		return // Blobs stay pending, will retry next tick
	}

	// Confirm in DB
	confirmed := w.confirmBatchGroup(ctx, group, height)

	// Log block-level submission stats
	w.log.Info("✅ Celestia block submission",
		"height", height,
		"blobs", confirmed,
		"size_mb", fmt.Sprintf("%.2f", float64(totalSizeBytes)/(1024*1024)),
		"batches", len(group))
}

// confirmBatchGroup creates batch records and marks blobs as confirmed.
// Returns count of confirmed blobs.
func (w *SubmissionWorker) confirmBatchGroup(ctx context.Context, group []*batchInfo, height uint64) int {
	confirmed := 0
	for _, b := range group {
		err := retryDBOp(5, 100*time.Millisecond, func() error {
			return w.store.CreateBatchAndConfirm(ctx, b.blobIDs, b.batchCommitment, b.packedData, height)
		})
		if err != nil {
			w.log.Error("Failed to confirm batch",
				"height", height,
				"blobs", len(b.blobIDs),
				"error", err)
			continue
		}
		confirmed += len(b.blobIDs)
	}
	return confirmed
}

// prepareBatches splits pending blobs into batches for Celestia submission.
// No DB writes happen here - batch records are created only after successful submission.
func (w *SubmissionWorker) prepareBatches(ctx context.Context, pendingBlobs []*db.Blob) ([]*batchInfo, error) {
	if len(pendingBlobs) == 0 {
		return nil, nil
	}

	var batches []*batchInfo
	remaining := pendingBlobs

	for len(remaining) > 0 {
		// Check for cancellation between batches
		if ctx.Err() != nil {
			return batches, ctx.Err()
		}

		// Select blobs that fit in one batch
		selected := w.selectBlobsForBatch(remaining)
		if len(selected) == 0 {
			// First blob too large - skip it
			w.log.Error("Blob too large, skipping",
				"blob_id", remaining[0].ID,
				"size", len(remaining[0].Data),
				"max", w.batchCfg.MaxBatchSizeBytes)
			remaining = remaining[1:]
			continue
		}

		// Pack blobs into batch format
		packedData, err := batch.PackBlobs(selected, w.batchCfg)
		if err != nil {
			return batches, fmt.Errorf("pack blobs: %w", err)
		}

		// Compute batch commitment for Celestia
		batchCommitment, err := commitment.ComputeCommitment(packedData, w.namespace, w.signerAddr)
		if err != nil {
			return batches, fmt.Errorf("compute commitment: %w", err)
		}

		// Create Celestia blob
		celestiaBlob, err := blob.NewBlobV1(w.namespace, packedData, w.signerAddr)
		if err != nil {
			return batches, fmt.Errorf("create celestia blob: %w", err)
		}

		// Extract blob IDs for later DB update
		blobIDs := make([]int64, len(selected))
		for i, b := range selected {
			blobIDs[i] = b.ID
		}

		batches = append(batches, &batchInfo{
			blobIDs:         blobIDs,
			batchCommitment: batchCommitment,
			packedData:      packedData,
			celestiaBlob:    celestiaBlob,
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

// submitToCelestia submits all blobs in a single call with retry logic.
// On timeout, checks if blob actually landed before retrying to prevent duplicates.
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
		// Use configured priority (1=low, 2=medium, 3=high) to affect gas price
		txConfig := state.NewTxConfig(state.WithTxPriority(w.workerCfg.TxPriority))
		height, lastErr = w.celestia.Submit(submitCtx, blobs, txConfig)
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

		// Check if this was a timeout - blob might have actually landed
		isTimeout := strings.Contains(lastErr.Error(), "context deadline exceeded") ||
			strings.Contains(lastErr.Error(), "timeout")

		if isTimeout && len(blobs) > 0 {
			w.log.Info("Submission timed out, checking if blob landed",
				"attempt", attempt+1,
				"blob_count", len(blobs))

			// Check if first blob landed (all blobs in same TX land together)
			// Use first blob's data to check
			if foundHeight, found := w.checkBlobOnCelestia(ctx, blobs[0].Data()); found {
				w.log.Info("Blob found on Celestia after timeout - no retry needed",
					"height", foundHeight,
					"blob_count", len(blobs))

				if w.metrics != nil {
					totalSize := 0
					for _, b := range blobs {
						totalSize += len(b.Data())
					}
					w.metrics.RecordSubmission(duration, totalSize)
				}

				return foundHeight, nil
			}

			w.log.Info("Blob not found after timeout, will retry",
				"attempt", attempt+1)
		} else {
			w.log.Warn("Submission attempt failed",
				"attempt", attempt+1,
				"error", lastErr)
		}
	}

	if w.metrics != nil {
		w.metrics.RecordSubmissionError()
	}

	return 0, fmt.Errorf("celestia submit failed after %d attempts: %w", w.workerCfg.MaxRetries+1, lastErr)
}

// checkBlobOnCelestia checks if blob data exists on Celestia by trying Get with each trusted signer.
// Used after timeout to verify if submission actually landed before retrying.
// Returns (height, true) if found, (0, false) if not found.
func (w *SubmissionWorker) checkBlobOnCelestia(ctx context.Context, blobData []byte) (uint64, bool) {
	// Get latest height to search from
	var latestHeight uint64
	if w.header != nil {
		checkCtx, cancel := context.WithTimeout(ctx, w.workerCfg.GetTimeout)
		localHead, err := w.header.LocalHead(checkCtx)
		cancel()
		if err != nil {
			w.log.Debug("Failed to get local head for check", "error", err)
			return 0, false
		}
		latestHeight = localHead.Height()
	}

	if latestHeight == 0 {
		w.log.Debug("No header module or zero height, skipping check")
		return 0, false
	}

	// Search recent blocks (last 10 blocks should be enough for just-submitted blob)
	searchWindow := uint64(10)
	startHeight := latestHeight
	if startHeight > searchWindow {
		startHeight = latestHeight - searchWindow
	} else {
		startHeight = 1
	}

	w.log.Debug("Checking if blob landed on Celestia",
		"start_height", startHeight,
		"end_height", latestHeight,
		"trusted_signers", len(w.workerCfg.TrustedSigners))

	// Try each trusted signer's commitment
	for _, signerBech32 := range w.workerCfg.TrustedSigners {
		signerAddr, err := sdk.AccAddressFromBech32(signerBech32)
		if err != nil {
			continue
		}

		commitmentBytes, err := commitment.ComputeCommitment(blobData, w.namespace, signerAddr.Bytes())
		if err != nil {
			continue
		}

		// Try Get at each height in the search window
		for height := latestHeight; height >= startHeight; height-- {
			checkCtx, cancel := context.WithTimeout(ctx, w.workerCfg.GetTimeout)
			celestiaBlob, err := w.celestia.Get(checkCtx, height, w.namespace, commitmentBytes)
			cancel()

			if err != nil {
				continue // Not found at this height
			}

			// Verify data matches
			if bytes.Equal(celestiaBlob.Data(), blobData) {
				w.log.Info("Found blob on Celestia after timeout",
					"height", height,
					"signer", signerBech32)
				return height, true
			}
		}
	}

	return 0, false
}
