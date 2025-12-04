package worker

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/celestiaorg/celestia-node/blob"
	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/log"

	"github.com/celestiaorg/op-alt-da/commitment"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/metrics"
)

type EventListener struct {
	store     *db.BlobStore
	celestia  blobAPI.Module
	namespace libshare.Namespace
	log       log.Logger
	workerCfg *Config
	metrics   *metrics.CelestiaMetrics
}

func NewEventListener(
	store *db.BlobStore,
	celestia blobAPI.Module,
	namespace libshare.Namespace,
	workerCfg *Config,
	metrics *metrics.CelestiaMetrics,
	log log.Logger,
) *EventListener {
	return &EventListener{
		store:     store,
		celestia:  celestia,
		namespace: namespace,
		log:       log,
		workerCfg: workerCfg,
		metrics:   metrics,
	}
}

func (l *EventListener) Run(ctx context.Context) error {
	// Validate configuration to prevent runtime panics
	if l.workerCfg.ReconcilePeriod <= 0 {
		return fmt.Errorf("invalid reconcile period: %v (must be positive)", l.workerCfg.ReconcilePeriod)
	}
	if l.workerCfg.ReconcileAge <= 0 {
		return fmt.Errorf("invalid reconcile age: %v (must be positive)", l.workerCfg.ReconcileAge)
	}

	l.log.Info("Reconciliation worker starting (using Get operations only)",
		"reconcile_period", l.workerCfg.ReconcilePeriod,
		"reconcile_age", l.workerCfg.ReconcileAge)

	// Start reconciliation ticker
	reconcileTicker := time.NewTicker(l.workerCfg.ReconcilePeriod)
	defer reconcileTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.log.Info("Reconciliation worker stopping")
			return ctx.Err()

		case <-reconcileTicker.C:
			if err := l.reconcileUnconfirmed(ctx); err != nil {
				l.log.Error("Reconcile unconfirmed failed", "error", err)
			}
		}
	}
}

func (l *EventListener) reconcileUnconfirmed(ctx context.Context) error {
	// Get total count of all unconfirmed batches (regardless of age)
	totalUnconfirmed, err := l.store.CountUnconfirmedBatches(ctx)
	if err != nil {
		return fmt.Errorf("count unconfirmed batches: %w", err)
	}

	// Get batches older than configured age that need reconciliation
	batches, err := l.store.GetUnconfirmedBatches(ctx, l.workerCfg.ReconcileAge)
	if err != nil {
		return fmt.Errorf("get unconfirmed batches: %w", err)
	}

	if len(batches) == 0 {
		if totalUnconfirmed > 0 {
			l.log.Debug("No batches ready for reconciliation",
				"total_unconfirmed", totalUnconfirmed,
				"min_age", l.workerCfg.ReconcileAge)
		}
		return nil
	}

	l.log.Info("Reconciling unconfirmed batches",
		"ready_for_reconciliation", len(batches),
		"total_unconfirmed", totalUnconfirmed,
		"min_age", l.workerCfg.ReconcileAge)

	for _, batch := range batches {
		if err := l.reconcileBatch(ctx, batch); err != nil {
			l.log.Error("Reconcile batch failed",
				"batch_id", batch.BatchID,
				"error", err)
		}
	}

	return nil
}

func (l *EventListener) reconcileBatch(ctx context.Context, batch *db.Batch) error {
	// If we don't have height, we can't query Celestia
	if batch.CelestiaHeight == nil {
		l.log.Debug("Batch has no height, cannot reconcile", "batch_id", batch.BatchID)
		return nil
	}

	l.log.Info("Reconciling batch",
		"batch_id", batch.BatchID,
		"height", *batch.CelestiaHeight,
		"trusted_signers", len(l.workerCfg.TrustedSigners))

	// Try to find the blob by computing commitment with each trusted signer
	// This is more efficient than GetAll + data matching
	var matchedBlob *blob.Blob
	var matchedSigner string
	startTime := time.Now()

	for _, signerBech32 := range l.workerCfg.TrustedSigners {
		// Parse signer address
		signerAddr, err := sdk.AccAddressFromBech32(signerBech32)
		if err != nil {
			l.log.Warn("Invalid trusted signer address", "signer", signerBech32, "error", err)
			continue
		}

		// Compute commitment with this signer
		commitmentBytes, err := commitment.ComputeCommitment(batch.BatchData, l.namespace, signerAddr.Bytes())
		if err != nil {
			l.log.Warn("Failed to compute commitment", "signer", signerBech32, "error", err)
			continue
		}

		// Try to Get with this commitment - let Celestia client handle timeouts
		celestiaBlob, err := l.celestia.Get(ctx, *batch.CelestiaHeight, l.namespace, commitmentBytes)

		if err != nil {
			// Not found with this signer, try next
			l.log.Debug("Blob not found with signer commitment",
				"batch_id", batch.BatchID,
				"signer", signerBech32,
				"commitment", hex.EncodeToString(commitmentBytes))
			continue
		}

		// Found it!
		matchedBlob = celestiaBlob
		matchedSigner = signerBech32
		l.log.Debug("Found blob with trusted signer",
			"batch_id", batch.BatchID,
			"signer", signerBech32,
			"commitment", hex.EncodeToString(commitmentBytes))
		break
	}

	duration := time.Since(startTime)

	if matchedBlob == nil {
		if l.metrics != nil {
			l.metrics.RecordRetrievalError()
		}
		l.log.Warn("Batch not found with any trusted signer",
			"batch_id", batch.BatchID,
			"height", *batch.CelestiaHeight,
			"signers_tried", len(l.workerCfg.TrustedSigners))
		return fmt.Errorf("batch not found at height %d with any trusted signer", *batch.CelestiaHeight)
	}

	blobSize := len(matchedBlob.Data())
	onChainCommitment := matchedBlob.Commitment

	// Record successful retrieval metrics
	if l.metrics != nil {
		l.metrics.RecordRetrieval(duration, blobSize)
	}

	// Update batch with actual on-chain commitment (may differ from ours)
	if !bytes.Equal(onChainCommitment, batch.BatchCommitment) {
		l.log.Info("Updating batch with on-chain commitment",
			"batch_id", batch.BatchID,
			"matched_signer", matchedSigner,
			"our_commitment", hex.EncodeToString(batch.BatchCommitment),
			"onchain_commitment", hex.EncodeToString(onChainCommitment))
		if err := l.store.UpdateBatchCommitment(ctx, batch.BatchID, onChainCommitment); err != nil {
			l.log.Error("Failed to update batch commitment", "batch_id", batch.BatchID, "error", err)
		}
	}

	// Mark as confirmed using the batch ID
	err := l.store.MarkBatchConfirmedByID(ctx, batch.BatchID)
	if err != nil {
		return fmt.Errorf("mark batch confirmed: %w", err)
	}

	l.log.Info("✅ Batch confirmed via reconciliation",
		"batch_id", batch.BatchID,
		"height", *batch.CelestiaHeight,
		"size", blobSize,
		"matched_signer", matchedSigner,
		"duration_ms", duration.Milliseconds(),
		"onchain_commitment", hex.EncodeToString(onChainCommitment))

	// Record time-to-confirmation metrics for each blob in the batch
	if l.metrics != nil {
		blobs, err := l.store.GetBlobsByBatchID(ctx, batch.BatchID)
		if err != nil {
			l.log.Warn("Failed to get blobs for metrics", "batch_id", batch.BatchID, "error", err)
		} else {
			now := time.Now()
			for _, b := range blobs {
				timeToConfirmation := now.Sub(b.CreatedAt)
				l.metrics.RecordTimeToConfirmation(timeToConfirmation)
			}
		}
	}

	return nil
}
