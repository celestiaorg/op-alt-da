package worker

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/celestiaorg/celestia-node/blob"
	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	headerAPI "github.com/celestiaorg/celestia-node/nodebuilder/header"
	libshare "github.com/celestiaorg/go-square/v3/share"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/log"
	"golang.org/x/sync/errgroup"

	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/commitment"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/metrics"
)

// contains is a helper to check if a string contains a substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// BackfillWorker uses a dual-mode approach for reliable blob discovery:
// 1. Subscribe: Real-time notifications for new blocks at chain tip (via WebSocket)
// 2. GetAll: Historical backfill for catching up and filling gaps
//
// This ensures no blocks are missed while maintaining low latency for new data.
type BackfillWorker struct {
	store     *db.BlobStore
	celestia  blobAPI.Module
	header    headerAPI.Module
	namespace libshare.Namespace
	batchCfg  *batch.Config
	workerCfg *Config
	metrics   *metrics.CelestiaMetrics
	log       log.Logger

	// Sync state tracking
	backfillHeight uint64     // Current height being backfilled (GetAll)
	subscriberTip  uint64     // Latest height seen by subscriber
	mu             sync.Mutex // Protects height tracking

	// Subscriber state
	subscriptionActive atomic.Bool

	// All trusted signer addresses (decoded from bech32)
	trustedSignerAddrs [][]byte
}

func NewBackfillWorker(
	store *db.BlobStore,
	celestia blobAPI.Module,
	header headerAPI.Module,
	namespace libshare.Namespace,
	batchCfg *batch.Config,
	workerCfg *Config,
	metrics *metrics.CelestiaMetrics,
	log log.Logger,
) *BackfillWorker {
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
		header:             header,
		namespace:          namespace,
		batchCfg:           batchCfg,
		workerCfg:          workerCfg,
		metrics:            metrics,
		log:                log,
		backfillHeight:     workerCfg.StartHeight,
		trustedSignerAddrs: trustedSignerAddrs,
	}
}

func (w *BackfillWorker) Run(ctx context.Context) error {
	if w.workerCfg.BackfillPeriod <= 0 {
		return fmt.Errorf("invalid backfill period: %v (must be positive)", w.workerCfg.BackfillPeriod)
	}

	// Load persisted sync state
	lastSyncedHeight, err := w.store.GetSyncState(ctx, "backfill_worker")
	if err != nil {
		return fmt.Errorf("failed to load sync state: %w", err)
	}

	// Determine starting height
	if lastSyncedHeight > 0 {
		w.backfillHeight = lastSyncedHeight + 1
		w.log.Info("Resuming backfill from persisted state",
			"last_synced", lastSyncedHeight,
			"resuming_from", w.backfillHeight)
	} else if w.workerCfg.StartHeight > 0 {
		w.backfillHeight = w.workerCfg.StartHeight
		w.log.Info("Starting backfill from configured height",
			"start_height", w.backfillHeight)
	} else {
		w.backfillHeight = 1
		w.log.Info("Starting backfill from genesis")
	}

	// Run subscriber and backfiller in parallel
	g, gCtx := errgroup.WithContext(ctx)

	// Start subscriber for real-time tip tracking
	g.Go(func() error {
		return w.runSubscriber(gCtx)
	})

	// Start backfiller for historical data
	g.Go(func() error {
		return w.runBackfiller(gCtx)
	})

	return g.Wait()
}

// runSubscriber maintains a subscription to new blocks and processes them in real-time
func (w *BackfillWorker) runSubscriber(ctx context.Context) error {
	w.log.Info("📡 Starting blob subscriber",
		"namespace", w.namespace.String())

	// Track consecutive failures to detect unsupported subscriptions
	consecutiveFailures := 0
	const maxFailuresBeforeGiveUp = 3

	for {
		select {
		case <-ctx.Done():
			w.log.Info("Subscriber stopping")
			return ctx.Err()
		default:
		}

		// Start subscription
		subCh, err := w.celestia.Subscribe(ctx, w.namespace)
		if err != nil {
			consecutiveFailures++

			// Check if this is a "not supported" error from hosted providers
			errStr := err.Error()
			isUnsupported := contains(errStr, "not supported") ||
				contains(errStr, "no out channel") ||
				contains(errStr, "-32601")

			if isUnsupported && consecutiveFailures >= maxFailuresBeforeGiveUp {
				w.log.Info("📡 Subscription not supported by this node provider - using backfill-only mode",
					"hint", "This is normal for hosted providers like QuikNode. Run your own Celestia node to enable subscriptions.")
				// Exit subscriber goroutine - backfiller will handle everything
				return nil
			}

			w.log.Warn("Failed to subscribe, will retry",
				"error", err,
				"attempt", consecutiveFailures,
				"retry_in", "5s")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}

		// Reset failure counter on successful connection
		consecutiveFailures = 0
		w.subscriptionActive.Store(true)
		w.log.Info("✅ Subscription established")

		// Process incoming blocks
		for {
			select {
			case <-ctx.Done():
				w.subscriptionActive.Store(false)
				return ctx.Err()

			case resp, ok := <-subCh:
				if !ok {
					// Channel closed - subscription lost
					w.subscriptionActive.Store(false)
					w.log.Warn("Subscription channel closed, reconnecting...")
					break
				}

				w.processSubscriptionResponse(ctx, resp)
			}

			// If we got here due to break (channel closed), restart outer loop
			if !w.subscriptionActive.Load() {
				break
			}
		}
	}
}

// processSubscriptionResponse handles a block received via subscription
func (w *BackfillWorker) processSubscriptionResponse(ctx context.Context, resp *blob.SubscriptionResponse) {
	height := resp.Height
	blobs := resp.Blobs

	// Update subscriber tip
	w.mu.Lock()
	if height > w.subscriberTip {
		w.subscriberTip = height
	}
	w.mu.Unlock()

	// Process blobs if any
	if len(blobs) == 0 {
		w.log.Debug("📡 Block (no blobs in namespace)",
			"height", height)
		return
	}

	batchesIndexed := 0
	for i, celestiaBlob := range blobs {
		if err := w.indexBatch(ctx, celestiaBlob, height); err != nil {
			w.log.Error("❌ Failed to index from subscription",
				"height", height,
				"blob", i,
				"error", err)
			continue
		}
		batchesIndexed++
	}

	w.log.Info("📡 Subscription block",
		"height", height,
		"blobs", len(blobs),
		"indexed", batchesIndexed)
}

// runBackfiller catches up historical blocks using GetAll
func (w *BackfillWorker) runBackfiller(ctx context.Context) error {
	w.log.Info("🔄 Starting historical backfiller",
		"start_height", w.backfillHeight)

	ticker := time.NewTicker(w.workerCfg.BackfillPeriod)
	defer ticker.Stop()

	// Initial backfill
	w.doBackfill(ctx)

	for {
		select {
		case <-ctx.Done():
			w.log.Info("Backfiller stopping")
			return ctx.Err()

		case <-ticker.C:
			w.doBackfill(ctx)
		}
	}
}

// doBackfill performs one iteration of historical backfilling
func (w *BackfillWorker) doBackfill(ctx context.Context) {
	maxBlocksPerScan := w.workerCfg.BlocksPerScan
	if maxBlocksPerScan <= 0 {
		maxBlocksPerScan = 10
	}

	// Get network tip to know our target
	tipHeight, err := w.getNetworkTipHeight(ctx)
	if err != nil {
		w.log.Debug("Could not get tip height", "error", err)
		return
	}

	w.mu.Lock()
	startHeight := w.backfillHeight
	w.mu.Unlock()

	// If subscription is active and we've caught up, skip backfilling
	if w.subscriptionActive.Load() {
		w.mu.Lock()
		subscriberTip := w.subscriberTip
		w.mu.Unlock()

		// If subscriber has seen data and backfill caught up, we're done
		if subscriberTip > 0 && startHeight >= subscriberTip {
			return
		}
	}

	// Calculate range to scan
	endHeight := startHeight + uint64(maxBlocksPerScan)
	if endHeight > tipHeight+1 {
		endHeight = tipHeight + 1
	}

	if startHeight >= endHeight {
		return
	}

	// Scan the range
	batchesFound, blocksScanned := w.parallelScan(ctx, startHeight, endHeight)

	// Update backfill progress
	w.mu.Lock()
	newHeight := w.backfillHeight
	w.mu.Unlock()

	// Persist sync progress
	if blocksScanned > 0 {
		if err := w.store.UpdateSyncState(ctx, "backfill_worker", newHeight-1); err != nil {
			w.log.Error("Failed to persist sync state", "error", err)
		}
	}

	// Log progress
	if blocksScanned > 0 {
		w.log.Info("🔄 Backfill",
			"range", fmt.Sprintf("%d→%d", startHeight, newHeight-1),
			"scanned", blocksScanned,
			"indexed", batchesFound,
			"tip", tipHeight,
			"subscription", w.subscriptionActive.Load())
	}
}

// getNetworkTipHeight queries Celestia for the current network head height
func (w *BackfillWorker) getNetworkTipHeight(ctx context.Context) (uint64, error) {
	if w.header == nil {
		return 0, fmt.Errorf("header module not available")
	}

	head, err := w.header.NetworkHead(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get network head: %w", err)
	}

	return head.Height(), nil
}

// scanResult holds the result of scanning a single height
type scanResult struct {
	height uint64
	blobs  []*blob.Blob
	err    error
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
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		w.log.Debug("Parallel fetch interrupted", "error", err)
	}

	return resultMap
}

// parallelScan scans multiple heights concurrently for faster discovery
func (w *BackfillWorker) parallelScan(ctx context.Context, startHeight, endHeight uint64) (batchesFound, blocksScanned int) {
	heights := make([]uint64, 0, endHeight-startHeight)
	for h := startHeight; h < endHeight; h++ {
		heights = append(heights, h)
	}

	resultMap := w.fetchHeightsParallel(ctx, heights)

	// Process results IN ORDER (important for height tracking)
	for _, h := range heights {
		r, ok := resultMap[h]
		if !ok {
			break
		}

		if r.err != nil {
			// Reached chain tip or error - stop here
			break
		}

		blocksScanned++

		// Update backfill height
		w.mu.Lock()
		w.backfillHeight = h + 1
		w.mu.Unlock()

		if len(r.blobs) == 0 {
			continue
		}

		for i, celestiaBlob := range r.blobs {
			if err := w.indexBatch(ctx, celestiaBlob, h); err != nil {
				w.log.Error("❌ Failed to index", "height", h, "blob", i, "error", err)
				continue
			}
			batchesFound++
		}
	}

	return
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

	w.log.Error("🚨 REJECTING BLOB - signer not in trusted_signers list!",
		"height", height,
		"commitment", fmt.Sprintf("%x", celestiaBlob.Commitment[:8]),
		"blob_signer", blobSignerBech32,
		"num_trusted_signers", len(w.workerCfg.TrustedSigners),
		"hint", "Add this signer to your trusted_signers config")
	return fmt.Errorf("blob signer %s not in trusted_signers", blobSignerBech32)
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
			w.log.Error("Failed to compute commitment for blob",
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
				"commitment", fmt.Sprintf("%x", blobCommitment),
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
