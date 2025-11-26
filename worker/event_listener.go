package worker

import (
	"context"
	"fmt"
	"time"

	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/ethereum/go-ethereum/log"

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
	if l.workerCfg.GetTimeout <= 0 {
		return fmt.Errorf("invalid get timeout: %v (must be positive)", l.workerCfg.GetTimeout)
	}

	l.log.Info("Reconciliation worker starting (using Get operations only)",
		"reconcile_period", l.workerCfg.ReconcilePeriod,
		"reconcile_age", l.workerCfg.ReconcileAge,
		"get_timeout", l.workerCfg.GetTimeout)

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

	l.log.Info("Reconciling batch via Get",
		"batch_id", batch.BatchID,
		"height", *batch.CelestiaHeight)

	// Try to get blob from Celestia using Get with configurable timeout
	getCtx, cancel := context.WithTimeout(ctx, l.workerCfg.GetTimeout)
	defer cancel()

	// Record retrieval metrics (time + size)
	startTime := time.Now()
	celestiaBlob, err := l.celestia.Get(getCtx, *batch.CelestiaHeight, l.namespace, batch.BatchCommitment)
	duration := time.Since(startTime)

	if err != nil {
		// Record error metric
		if l.metrics != nil {
			l.metrics.RecordRetrievalError()
		}
		l.log.Warn("Get blob failed during reconciliation",
			"batch_id", batch.BatchID,
			"height", *batch.CelestiaHeight,
			"error", err)
		return fmt.Errorf("get blob: %w", err)
	}

	blobSize := len(celestiaBlob.Data())

	// Record successful retrieval metrics
	if l.metrics != nil {
		l.metrics.RecordRetrieval(duration, blobSize)
	}

	// Blob exists! Mark as confirmed
	err = l.store.MarkBatchConfirmed(ctx, batch.BatchCommitment, *batch.CelestiaHeight)
	if err != nil {
		return fmt.Errorf("mark batch confirmed: %w", err)
	}

	l.log.Info("Batch confirmed via reconciliation",
		"batch_id", batch.BatchID,
		"height", *batch.CelestiaHeight,
		"size", blobSize,
		"duration_ms", duration.Milliseconds())

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
