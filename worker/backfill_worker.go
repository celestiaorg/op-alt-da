package worker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/celestiaorg/celestia-node/blob"
	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/ethereum/go-ethereum/log"

	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/commitment"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/metrics"
)

// BackfillWorker continuously scans Celestia blocks to discover and index batches
// for read-only servers that don't submit blobs themselves
type BackfillWorker struct {
	store     *db.BlobStore
	celestia  blobAPI.Module
	namespace libshare.Namespace
	batchCfg  *batch.Config
	workerCfg *Config
	metrics   *metrics.CelestiaMetrics
	log       log.Logger

	// Track the current sync height
	currentHeight uint64
}

func NewBackfillWorker(
	store *db.BlobStore,
	celestia blobAPI.Module,
	namespace libshare.Namespace,
	batchCfg *batch.Config,
	workerCfg *Config,
	metrics *metrics.CelestiaMetrics,
	log log.Logger,
) *BackfillWorker {
	return &BackfillWorker{
		store:         store,
		celestia:      celestia,
		namespace:     namespace,
		batchCfg:      batchCfg,
		workerCfg:     workerCfg,
		metrics:       metrics,
		log:           log,
		currentHeight: workerCfg.StartHeight,
	}
}

func (w *BackfillWorker) Run(ctx context.Context) error {
	w.log.Info("Backfill worker starting",
		"start_height", w.workerCfg.StartHeight,
		"backfill_period", w.workerCfg.BackfillPeriod,
		"namespace", w.namespace.String())

	// Load persisted sync state from database
	lastSyncedHeight, err := w.store.GetSyncState(ctx, "backfill_worker")
	if err != nil {
		return fmt.Errorf("failed to load sync state: %w", err)
	}

	// Determine starting height
	if lastSyncedHeight > 0 {
		// Resume from where we left off
		w.currentHeight = lastSyncedHeight + 1
		w.log.Info("Resuming backfill from persisted state", "last_synced_height", lastSyncedHeight, "resuming_from", w.currentHeight)
	} else if w.workerCfg.StartHeight > 0 {
		// Use configured start height
		w.currentHeight = w.workerCfg.StartHeight
		w.log.Info("Starting backfill from configured height", "start_height", w.currentHeight)
	} else {
		// Start from height 1
		w.currentHeight = 1
		w.log.Info("Starting backfill from genesis", "start_height", 1)
	}

	ticker := time.NewTicker(w.workerCfg.BackfillPeriod)
	defer ticker.Stop()

	// Run initial backfill immediately
	if err := w.scanAndIndexBlocks(ctx); err != nil {
		w.log.Error("Initial backfill failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			w.log.Info("Backfill worker stopping")
			return ctx.Err()

		case <-ticker.C:
			if err := w.scanAndIndexBlocks(ctx); err != nil {
				w.log.Error("Backfill iteration failed", "error", err)
			}
		}
	}
}

// scanAndIndexBlocks scans a range of Celestia blocks and indexes discovered batches
func (w *BackfillWorker) scanAndIndexBlocks(ctx context.Context) error {
	// Scan blocks in batches to avoid overwhelming the system
	// For now, scan one block at a time
	const maxBlocksPerScan = 10

	startHeight := w.currentHeight
	endHeight := startHeight + maxBlocksPerScan

	w.log.Debug("Scanning Celestia blocks",
		"start_height", startHeight,
		"end_height", endHeight)

	blocksScanned := 0
	batchesFound := 0

	for height := startHeight; height < endHeight; height++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Get all blobs at this height for our namespace
		blobs, err := w.celestia.GetAll(ctx, height, []libshare.Namespace{w.namespace})
		if err != nil {
			// If we get an error (e.g., height doesn't exist yet), stop scanning
			w.log.Debug("Reached current tip or error scanning block",
				"height", height,
				"error", err)
			break
		}

		blocksScanned++

		if len(blobs) == 0 {
			w.log.Debug("No blobs found at height", "height", height)
			w.currentHeight = height + 1
			continue
		}

		w.log.Info("Found blobs at height",
			"height", height,
			"blob_count", len(blobs))

		// Index each blob (which is a batch in our system)
		for _, celestiaBlob := range blobs {
			if err := w.indexBatch(ctx, celestiaBlob, height); err != nil {
				w.log.Error("Failed to index batch",
					"height", height,
					"error", err)
				continue
			}
			batchesFound++
		}

		w.currentHeight = height + 1
	}

	// Persist sync progress after successful scan
	if blocksScanned > 0 {
		// Save the last successfully scanned height (currentHeight - 1)
		lastScannedHeight := w.currentHeight - 1
		if err := w.store.UpdateSyncState(ctx, "backfill_worker", lastScannedHeight); err != nil {
			w.log.Error("Failed to persist sync state", "error", err, "height", lastScannedHeight)
			// Don't fail the whole scan, just log the error
		} else {
			w.log.Debug("Persisted sync state", "last_synced_height", lastScannedHeight)
		}
	}

	if blocksScanned > 0 || batchesFound > 0 {
		w.log.Info("Backfill scan complete",
			"blocks_scanned", blocksScanned,
			"batches_indexed", batchesFound,
			"current_height", w.currentHeight)
	}

	return nil
}

// indexBatch unpacks a discovered batch and indexes individual blobs
func (w *BackfillWorker) indexBatch(ctx context.Context, celestiaBlob *blob.Blob, height uint64) error {
	batchData := celestiaBlob.Data()

	// Compute batch commitment
	batchCommitment := celestiaBlob.Commitment

	// CIP-21 Signer Verification for Security
	// Verify the blob came from one of our trusted write servers
	if len(w.workerCfg.TrustedSigners) > 0 {
		blobSigner := celestiaBlob.Signer()

		if len(blobSigner) == 0 {
			// Blob has no signer - reject it
			w.log.Warn("Rejecting unsigned blob (potential junk data)",
				"height", height,
				"commitment", fmt.Sprintf("%x", batchCommitment))
			return fmt.Errorf("blob has no signer, rejecting for security")
		}

		// Check if blob signer matches any of the trusted signers
		// Note: Celestia blob.Signer() returns 20-byte raw address (not bech32)
		blobSignerHex := fmt.Sprintf("%x", blobSigner)

		signerTrusted := false
		for _, trustedSigner := range w.workerCfg.TrustedSigners {
			// Trusted signer should be hex (40 chars) - Celestia addresses are bech32 but
			// blob.Signer() returns raw 20-byte address, so we expect hex in config
			// Compare hex representations (case-insensitive)
			if strings.EqualFold(blobSignerHex, trustedSigner) {
				signerTrusted = true
				w.log.Debug("Blob signer verified",
					"height", height,
					"signer", blobSignerHex,
					"matched_trusted_signer", trustedSigner)
				break
			}
		}

		if !signerTrusted {
			// Signer doesn't match any trusted signer - this is junk data from another party
			w.log.Warn("Rejecting blob from untrusted signer (potential attack)",
				"height", height,
				"commitment", fmt.Sprintf("%x", batchCommitment),
				"blob_signer", blobSignerHex,
				"trusted_signers", fmt.Sprintf("%v", w.workerCfg.TrustedSigners))
			return fmt.Errorf("blob signer does not match any trusted signer, rejecting")
		}
	} else {
		// No trusted signers configured - log warning
		w.log.Warn("No trusted signers configured - accepting all blobs (INSECURE)",
			"height", height,
			"commitment", fmt.Sprintf("%x", batchCommitment))
	}

	w.log.Debug("Indexing batch",
		"height", height,
		"batch_size", len(batchData),
		"commitment", fmt.Sprintf("%x", batchCommitment))

	// Check if we already have this batch
	existingBatch, err := w.store.GetBatchByCommitment(ctx, batchCommitment)
	if err == nil && existingBatch != nil {
		w.log.Debug("Batch already indexed, skipping",
			"batch_id", existingBatch.BatchID,
			"commitment", fmt.Sprintf("%x", batchCommitment))
		return nil
	}

	// Unpack the batch to get individual blobs
	// This also serves as secondary validation - malformed batches will fail to unpack
	unpacked, err := batch.UnpackBlobs(batchData, w.batchCfg)
	if err != nil {
		w.log.Warn("Failed to unpack batch - rejecting (malformed or junk data)",
			"height", height,
			"commitment", fmt.Sprintf("%x", batchCommitment),
			"error", err)
		return fmt.Errorf("unpack batch failed, rejecting: %w", err)
	}

	// Validate unpacked data is not empty
	if len(unpacked) == 0 {
		w.log.Warn("Unpacked batch contains zero blobs - rejecting",
			"height", height,
			"commitment", fmt.Sprintf("%x", batchCommitment))
		return fmt.Errorf("batch contains no blobs, rejecting")
	}

	w.log.Info("Unpacked batch",
		"blob_count", len(unpacked),
		"height", height)

	// Start a transaction for atomic insertion
	tx, err := w.store.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Create batch record
	now := time.Now()
	batchRecord := &db.Batch{
		BatchCommitment: batchCommitment,
		BatchData:       batchData,
		BatchSize:       len(batchData),
		BlobCount:       len(unpacked),
		Status:          "confirmed", // Already on Celestia, so confirmed
		CelestiaHeight:  &height,
		CreatedAt:       now,
		SubmittedAt:     now,
		ConfirmedAt:     &now,
	}

	batchID, err := w.store.InsertBatchTx(ctx, tx, batchRecord)
	if err != nil {
		return fmt.Errorf("insert batch: %w", err)
	}

	// Index each individual blob
	successfulInserts := 0
	for i, blobData := range unpacked {
		// Compute commitment for this individual blob
		blobCommitment, err := commitment.ComputeCommitment(blobData, w.namespace)
		if err != nil {
			w.log.Error("Failed to compute commitment for blob",
				"batch_index", i,
				"error", err)
			// Fail the entire transaction if we can't compute commitment
			return fmt.Errorf("compute commitment for blob %d: %w", i, err)
		}

		// Check if blob already exists (to avoid duplicate work)
		existing, err := w.store.GetBlobByCommitment(ctx, blobCommitment)
		if err == nil && existing != nil {
			w.log.Debug("Blob already exists, skipping",
				"commitment", fmt.Sprintf("%x", blobCommitment))
			successfulInserts++
			continue
		}

		// Create blob record
		batchIndex := i
		blobRecord := &db.Blob{
			Commitment:     blobCommitment,
			Namespace:      w.namespace.Bytes(),
			Data:           blobData,
			Size:           len(blobData),
			Status:         "confirmed",
			CelestiaHeight: &height,
			BatchID:        &batchID,
			BatchIndex:     &batchIndex,
			CreatedAt:      now,
			SubmittedAt:    &now,
			ConfirmedAt:    &now,
		}

		if err := w.store.InsertBlobTx(ctx, tx, blobRecord); err != nil {
			w.log.Error("Failed to insert blob - failing entire batch to maintain consistency",
				"batch_index", i,
				"commitment", fmt.Sprintf("%x", blobCommitment),
				"error", err)
			// Fail entire transaction to maintain consistency
			return fmt.Errorf("insert blob %d: %w", i, err)
		}

		successfulInserts++
		w.log.Debug("Indexed blob",
			"batch_index", i,
			"commitment", fmt.Sprintf("%x", blobCommitment))
	}

	// Verify all blobs were successfully indexed
	if successfulInserts != len(unpacked) {
		return fmt.Errorf("only indexed %d/%d blobs, failing transaction for consistency", successfulInserts, len(unpacked))
	}

	// Commit transaction
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	w.log.Info("Successfully indexed batch",
		"batch_id", batchID,
		"blob_count", len(unpacked),
		"height", height)

	return nil
}
