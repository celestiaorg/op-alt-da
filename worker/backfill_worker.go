package worker

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/celestiaorg/celestia-node/blob"
	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/log"
	"golang.org/x/sync/errgroup"

	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/commitment"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/metrics"
)

// BackfillWorker handles historical data migration from Celestia.
// It scans blocks from start_height to target_height using GetAll,
// ensuring all historical data is safely captured in the local DB.
//
// This worker is designed for one-time migrations or catch-up scenarios,
// NOT for continuous operation. Once it reaches target_height, it stops.
type BackfillWorker struct {
	store     *db.BlobStore
	celestia  blobAPI.Module
	namespace libshare.Namespace
	batchCfg  *batch.Config
	workerCfg *Config
	metrics   *metrics.CelestiaMetrics
	log       log.Logger

	// Current height being backfilled
	currentHeight uint64
	mu            sync.Mutex

	// All trusted signer addresses (decoded from bech32)
	trustedSignerAddrs [][]byte
}

func NewBackfillWorker(
	store *db.BlobStore,
	celestia blobAPI.Module,
	namespace libshare.Namespace,
	batchCfg *batch.Config,
	workerCfg *Config,
	metrics *metrics.CelestiaMetrics,
	logger log.Logger,
) *BackfillWorker {
	log := logger
	// Decode all trusted signers to raw addresses
	var trustedSignerAddrs [][]byte
	for _, signer := range workerCfg.TrustedSigners {
		addr, err := sdk.AccAddressFromBech32(signer)
		if err != nil {
			log.Error("Failed to decode trusted signer",
				"signer", signer,
				"error", err)
			continue
		}
		trustedSignerAddrs = append(trustedSignerAddrs, addr.Bytes())
	}

	if len(trustedSignerAddrs) > 0 {
		log.Info("Loaded trusted signers for commitment computation",
			"count", len(trustedSignerAddrs),
			"signers", workerCfg.TrustedSigners)
	} else {
		log.Warn("No trusted signers configured - will use on-chain signer only")
	}

	return &BackfillWorker{
		store:              store,
		celestia:           celestia,
		namespace:          namespace,
		batchCfg:           batchCfg,
		workerCfg:          workerCfg,
		metrics:            metrics,
		log:                log,
		currentHeight:      workerCfg.StartHeight,
		trustedSignerAddrs: trustedSignerAddrs,
	}
}

func (w *BackfillWorker) Run(ctx context.Context) error {
	// Validate configuration
	if w.workerCfg.BackfillTargetHeight == 0 {
		w.log.Info("Backfill target height not set, backfill worker disabled")
		return nil
	}

	if w.workerCfg.StartHeight >= w.workerCfg.BackfillTargetHeight {
		w.log.Info("Already caught up to target height",
			"start_height", w.workerCfg.StartHeight,
			"target_height", w.workerCfg.BackfillTargetHeight)
		return nil
	}

	// Load persisted sync state
	lastSyncedHeight, err := w.store.GetSyncState(ctx, "backfill_worker")
	if err != nil {
		return fmt.Errorf("failed to load sync state: %w", err)
	}

	// Determine starting height
	if lastSyncedHeight > 0 && lastSyncedHeight >= w.workerCfg.StartHeight {
		w.currentHeight = lastSyncedHeight + 1
		w.log.Info("Resuming backfill from persisted state",
			"last_synced", lastSyncedHeight,
			"resuming_from", w.currentHeight,
			"target", w.workerCfg.BackfillTargetHeight)
	} else {
		w.currentHeight = w.workerCfg.StartHeight
		w.log.Info("Starting backfill from configured height",
			"start_height", w.currentHeight,
			"target", w.workerCfg.BackfillTargetHeight)
	}

	// Check if already complete
	if w.currentHeight > w.workerCfg.BackfillTargetHeight {
		w.log.Info("✅ Backfill already complete",
			"current", w.currentHeight,
			"target", w.workerCfg.BackfillTargetHeight)
		return nil
	}

	w.log.Info("🔄 Starting historical backfill",
		"from", w.currentHeight,
		"to", w.workerCfg.BackfillTargetHeight,
		"blocks", w.workerCfg.BackfillTargetHeight-w.currentHeight+1)

	// Run backfill loop
	ticker := time.NewTicker(w.workerCfg.BackfillPeriod)
	defer ticker.Stop()

	// Initial backfill iteration
	if done := w.doBackfill(ctx); done {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			w.log.Info("Backfill worker stopping (context cancelled)")
			return ctx.Err()

		case <-ticker.C:
			if done := w.doBackfill(ctx); done {
				return nil
			}
		}
	}
}

// doBackfill performs one iteration of historical backfilling.
// Returns true when backfill is complete (reached target height).
func (w *BackfillWorker) doBackfill(ctx context.Context) bool {
	maxBlocksPerScan := w.workerCfg.BlocksPerScan
	if maxBlocksPerScan <= 0 {
		maxBlocksPerScan = 10
	}

	w.mu.Lock()
	startHeight := w.currentHeight
	w.mu.Unlock()

	targetHeight := w.workerCfg.BackfillTargetHeight

	// Check if we've completed
	if startHeight > targetHeight {
		w.log.Info("✅ Backfill complete!",
			"final_height", startHeight-1,
			"target_height", targetHeight)
		return true
	}

	// Calculate range to scan (don't exceed target)
	endHeight := startHeight + uint64(maxBlocksPerScan)
	if endHeight > targetHeight+1 {
		endHeight = targetHeight + 1
	}

	// Scan the range using GetAll
	batchesFound, blocksScanned := w.scanRange(ctx, startHeight, endHeight)

	// Get new height after scanning
	w.mu.Lock()
	newHeight := w.currentHeight
	w.mu.Unlock()

	// Persist sync progress
	if blocksScanned > 0 {
		if err := w.store.UpdateSyncState(ctx, "backfill_worker", newHeight-1); err != nil {
			w.log.Error("Failed to persist sync state", "error", err)
		}
	}

	// Calculate progress
	totalBlocks := targetHeight - w.workerCfg.StartHeight + 1
	completedBlocks := newHeight - w.workerCfg.StartHeight
	progressPct := float64(completedBlocks) / float64(totalBlocks) * 100

	// Log progress
	if blocksScanned > 0 {
		w.log.Info("🔄 Backfill progress",
			"range", fmt.Sprintf("%d→%d", startHeight, newHeight-1),
			"scanned", blocksScanned,
			"indexed", batchesFound,
			"progress", fmt.Sprintf("%.1f%%", progressPct),
			"remaining", targetHeight-newHeight+1)
	}

	return newHeight > targetHeight
}

// scanResult holds the result of scanning a single height
type scanResult struct {
	height uint64
	blobs  []*blob.Blob
	err    error
}

// scanRange scans multiple heights using parallel GetAll calls
func (w *BackfillWorker) scanRange(ctx context.Context, startHeight, endHeight uint64) (batchesFound, blocksScanned int) {
	heights := make([]uint64, 0, endHeight-startHeight)
	for h := startHeight; h < endHeight; h++ {
		heights = append(heights, h)
	}

	// Fetch all heights in parallel
	resultMap := w.fetchHeightsParallel(ctx, heights)

	// Process results IN ORDER (important for height tracking)
	for _, h := range heights {
		r, ok := resultMap[h]
		if !ok {
			// Height not fetched, stop here to maintain order
			break
		}

		if r.err != nil {
			// Log error but continue with available results
			w.log.Debug("GetAll error (will retry)",
				"height", h,
				"error", r.err)
			break
		}

		blocksScanned++

		// Update current height
		w.mu.Lock()
		w.currentHeight = h + 1
		w.mu.Unlock()

		if len(r.blobs) == 0 {
			continue
		}

		// Index blobs found at this height
		for i, celestiaBlob := range r.blobs {
			if err := w.indexBatch(ctx, celestiaBlob, h); err != nil {
				w.log.Debug("Failed to index (may be expected)",
					"height", h,
					"blob", i,
					"error", err)
				continue
			}
			batchesFound++
		}
	}

	return
}

// fetchHeightsParallel fetches blobs from multiple heights concurrently
func (w *BackfillWorker) fetchHeightsParallel(ctx context.Context, heights []uint64) map[uint64]scanResult {
	maxConcurrent := w.workerCfg.BlocksPerScan
	if maxConcurrent <= 0 {
		maxConcurrent = 10
	}

	var (
		mu        sync.Mutex
		resultMap = make(map[uint64]scanResult)
	)

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrent)

	for _, h := range heights {
		height := h
		g.Go(func() error {
			if gCtx.Err() != nil {
				return gCtx.Err()
			}
			blobs, err := w.celestia.GetAll(gCtx, height, []libshare.Namespace{w.namespace})
			mu.Lock()
			resultMap[height] = scanResult{height: height, blobs: blobs, err: err}
			mu.Unlock()
			return nil // Don't fail the group, we collect errors in resultMap
		})
	}

	if err := g.Wait(); err != nil {
		w.log.Debug("Parallel fetch interrupted", "error", err)
	}

	return resultMap
}

// indexBatch verifies, unpacks, and indexes a discovered batch from Celestia.
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

	w.log.Info("✅ Indexed batch from Celestia",
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
		w.log.Debug("Rejecting unsigned blob",
			"height", height,
			"commitment", fmt.Sprintf("%x", celestiaBlob.Commitment))
		return fmt.Errorf("blob has no signer, rejecting")
	}

	blobSignerAddr := sdk.AccAddress(blobSigner)
	blobSignerBech32 := blobSignerAddr.String()

	for _, trustedSigner := range w.workerCfg.TrustedSigners {
		if blobSignerBech32 == trustedSigner {
			return nil
		}
	}

	w.log.Debug("Rejecting blob - signer not trusted",
		"height", height,
		"signer", blobSignerBech32)
	return fmt.Errorf("blob signer %s not in trusted_signers", blobSignerBech32)
}

func (w *BackfillWorker) unpackBatch(batchData []byte, height uint64, commitment []byte) ([][]byte, error) {
	unpacked, err := batch.UnpackBlobs(batchData, w.batchCfg)
	if err != nil {
		w.log.Debug("Failed to unpack batch - rejecting",
			"height", height,
			"commitment", fmt.Sprintf("%x", commitment),
			"error", err)
		return nil, fmt.Errorf("unpack batch failed: %w", err)
	}

	if len(unpacked) == 0 {
		return nil, fmt.Errorf("batch contains no blobs")
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
		return 0, fmt.Errorf("only indexed %d/%d blobs", successfulInserts, len(unpacked))
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	return batchID, nil
}

func (w *BackfillWorker) persistBlob(ctx context.Context, tx *sql.Tx, blobData []byte, signerAddr []byte, batchID int64, batchIndex int, height uint64, now time.Time) error {
	signersToUse := w.trustedSignerAddrs
	if len(signersToUse) == 0 {
		signersToUse = [][]byte{signerAddr}
	}

	// Check if blob already exists
	for _, signer := range signersToUse {
		comm, err := commitment.ComputeCommitment(blobData, w.namespace, signer)
		if err != nil {
			continue
		}
		existing, err := w.store.GetBlobByCommitment(ctx, comm)
		if err == nil && existing != nil {
			w.log.Debug("Blob already exists, skipping",
				"commitment", fmt.Sprintf("%x", comm))
			return nil
		}
	}

	// Insert blob record for EACH trusted signer's commitment
	for i, signer := range signersToUse {
		blobCommitment, err := commitment.ComputeCommitment(blobData, w.namespace, signer)
		if err != nil {
			w.log.Error("Failed to compute commitment",
				"batch_index", batchIndex,
				"signer_index", i,
				"error", err)
			continue
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
			w.log.Debug("Insert blob (may be duplicate)",
				"batch_index", batchIndex,
				"signer_index", i,
				"error", err)
			continue
		}

		w.log.Debug("Indexed blob",
			"batch_index", batchIndex,
			"signer_index", i,
			"commitment", fmt.Sprintf("%x", blobCommitment))
	}

	return nil
}
