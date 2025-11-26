package worker

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/celestiaorg/celestia-node/blob"
	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
	sdk "github.com/cosmos/cosmos-sdk/types"
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
	// Validate configuration to prevent runtime panics
	if w.workerCfg.BackfillPeriod <= 0 {
		return fmt.Errorf("invalid backfill period: %v (must be positive)", w.workerCfg.BackfillPeriod)
	}

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
	// Determine blocks to scan per iteration
	maxBlocksPerScan := w.workerCfg.BlocksPerScan
	if maxBlocksPerScan <= 0 {
		maxBlocksPerScan = 10 // Default fallback
	}

	startHeight := w.currentHeight
	endHeight := startHeight + uint64(maxBlocksPerScan)

	w.log.Debug("Scanning Celestia blocks",
		"start_height", startHeight,
		"end_height", endHeight)

	blocksScanned := 0
	batchesFound := 0

	for height := startHeight; height < endHeight; height++ {
		if err := ctx.Err(); err != nil {
			return err
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
		w.currentHeight = height + 1

		if len(blobs) == 0 {
			w.log.Debug("No blobs found at height", "height", height)
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

// indexBatch verifies, unpacks, and indexes a discovered batch from Celestia.
// It verifies the signer, checks for duplicate batches, unpacks individual blobs,
// and persists everything to the database.
func (w *BackfillWorker) indexBatch(ctx context.Context, celestiaBlob *blob.Blob, height uint64) error {
	batchData := celestiaBlob.Data()
	batchCommitment := celestiaBlob.Commitment

	if err := w.verifySigner(celestiaBlob, height); err != nil {
		return err
	}

	w.log.Debug("Indexing batch",
		"height", height,
		"batch_size", len(batchData),
		"commitment", fmt.Sprintf("%x", batchCommitment))

	existingBatch, err := w.store.GetBatchByCommitment(ctx, batchCommitment)
	if err == nil && existingBatch != nil {
		w.log.Debug("Batch already indexed, skipping",
			"batch_id", existingBatch.BatchID,
			"commitment", fmt.Sprintf("%x", batchCommitment))
		return nil
	}

	unpacked, err := w.unpackBatch(batchData, height, batchCommitment)
	if err != nil {
		return err
	}

	w.log.Info("Unpacked batch",
		"blob_count", len(unpacked),
		"height", height)

	blobSigner := celestiaBlob.Signer()
	batchID, err := w.persistBatch(ctx, batchCommitment, batchData, unpacked, blobSigner, height)
	if err != nil {
		return err
	}

	w.log.Info("Successfully indexed batch",
		"batch_id", batchID,
		"blob_count", len(unpacked),
		"height", height)

	return nil
}

func (w *BackfillWorker) verifySigner(celestiaBlob *blob.Blob, height uint64) error {
	if len(w.workerCfg.TrustedSigners) == 0 {
		w.log.Warn("No trusted signers configured - accepting all blobs (INSECURE)",
			"height", height,
			"commitment", fmt.Sprintf("%x", celestiaBlob.Commitment))
		return nil
	}

	blobSigner := celestiaBlob.Signer()
	if len(blobSigner) == 0 {
		w.log.Warn("Rejecting unsigned blob (potential junk data)",
			"height", height,
			"commitment", fmt.Sprintf("%x", celestiaBlob.Commitment))
		return fmt.Errorf("blob has no signer, rejecting for security")
	}

	// Convert blob signer (raw bytes) to Bech32 address for comparison
	blobSignerAddr := sdk.AccAddress(blobSigner)
	blobSignerBech32 := blobSignerAddr.String()

	for _, trustedSigner := range w.workerCfg.TrustedSigners {
		if blobSignerBech32 == trustedSigner {
			w.log.Debug("Blob signer verified",
				"height", height,
				"signer", blobSignerBech32,
				"matched_trusted_signer", trustedSigner)
			return nil
		}
	}

	w.log.Warn("Rejecting blob from untrusted signer (potential attack)",
		"height", height,
		"commitment", fmt.Sprintf("%x", celestiaBlob.Commitment),
		"blob_signer", blobSignerBech32,
		"trusted_signers", fmt.Sprintf("%v", w.workerCfg.TrustedSigners))
	return fmt.Errorf("blob signer does not match any trusted signer, rejecting")
}

func (w *BackfillWorker) unpackBatch(batchData []byte, height uint64, commitment []byte) ([][]byte, error) {
	unpacked, err := batch.UnpackBlobs(batchData, w.batchCfg)
	if err != nil {
		w.log.Warn("Failed to unpack batch - rejecting (malformed or junk data)",
			"height", height,
			"commitment", fmt.Sprintf("%x", commitment),
			"error", err)
		return nil, fmt.Errorf("unpack batch failed, rejecting: %w", err)
	}

	if len(unpacked) == 0 {
		w.log.Warn("Unpacked batch contains zero blobs - rejecting",
			"height", height,
			"commitment", fmt.Sprintf("%x", commitment))
		return nil, fmt.Errorf("batch contains no blobs, rejecting")
	}
	return unpacked, nil
}

func (w *BackfillWorker) persistBatch(ctx context.Context, batchCommitment, batchData []byte, unpacked [][]byte, signerAddr []byte, height uint64) (int64, error) {
	tx, err := w.store.BeginTx(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	now := time.Now()
	batchRecord := &db.Batch{
		BatchCommitment: batchCommitment,
		BatchData:       batchData,
		BatchSize:       len(batchData),
		BlobCount:       len(unpacked),
		Status:          "confirmed",
		CelestiaHeight:  &height,
		CreatedAt:       now,
		SubmittedAt:     now,
		ConfirmedAt:     &now,
	}

	batchID, err := w.store.InsertBatchTx(ctx, tx, batchRecord)
	if err != nil {
		return 0, fmt.Errorf("insert batch: %w", err)
	}

	successfulInserts := 0
	for i, blobData := range unpacked {
		if err := w.persistBlob(ctx, tx, blobData, signerAddr, batchID, i, height, now); err != nil {
			return 0, err
		}
		successfulInserts++
	}

	if successfulInserts != len(unpacked) {
		return 0, fmt.Errorf("only indexed %d/%d blobs, failing transaction for consistency", successfulInserts, len(unpacked))
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	return batchID, nil
}

func (w *BackfillWorker) persistBlob(ctx context.Context, tx *sql.Tx, blobData []byte, signerAddr []byte, batchID int64, batchIndex int, height uint64, now time.Time) error {
	// Compute commitment using the same signer that was used when the blob was submitted to Celestia
	blobCommitment, err := commitment.ComputeCommitment(blobData, w.namespace, signerAddr)
	if err != nil {
		w.log.Error("Failed to compute commitment for blob",
			"batch_index", batchIndex,
			"error", err)
		return fmt.Errorf("compute commitment for blob %d: %w", batchIndex, err)
	}

	existing, err := w.store.GetBlobByCommitment(ctx, blobCommitment)
	if err == nil && existing != nil {
		w.log.Debug("Blob already exists, skipping",
			"commitment", fmt.Sprintf("%x", blobCommitment))
		return nil
	}

	idx := batchIndex
	blobRecord := &db.Blob{
		Commitment:     blobCommitment,
		Namespace:      w.namespace.Bytes(),
		Data:           blobData,
		Size:           len(blobData),
		Status:         "confirmed",
		CelestiaHeight: &height,
		BatchID:        &batchID,
		BatchIndex:     &idx,
		CreatedAt:      now,
		SubmittedAt:    &now,
		ConfirmedAt:    &now,
	}

	if err := w.store.InsertBlobTx(ctx, tx, blobRecord); err != nil {
		w.log.Error("Failed to insert blob - failing entire batch to maintain consistency",
			"batch_index", batchIndex,
			"commitment", fmt.Sprintf("%x", blobCommitment),
			"error", err)
		return fmt.Errorf("insert blob %d: %w", batchIndex, err)
	}

	w.log.Debug("Indexed blob",
		"batch_index", batchIndex,
		"commitment", fmt.Sprintf("%x", blobCommitment))
	return nil
}
