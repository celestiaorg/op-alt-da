package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/celestiaorg/celestia-node/blob"
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
	l.log.Info("Event listener starting",
		"reconcile_period", l.workerCfg.ReconcilePeriod,
		"reconcile_age", l.workerCfg.ReconcileAge,
		"get_timeout", l.workerCfg.GetTimeout)

	// Start event subscription with timeout to prevent hanging
	subCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	eventChan, err := l.celestia.Subscribe(subCtx, l.namespace)
	if err != nil {
		return fmt.Errorf("subscribe to namespace: %w", err)
	}

	l.log.Info("Subscribed to namespace events", "namespace", l.namespace.String())

	// Start reconciliation ticker
	reconcileTicker := time.NewTicker(l.workerCfg.ReconcilePeriod)
	defer reconcileTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.log.Info("Event listener stopping")
			return ctx.Err()

		case event := <-eventChan:
			if event == nil {
				l.log.Warn("Received nil event, subscription may have closed")
				continue
			}

			if err := l.handleEvent(ctx, event); err != nil {
				l.log.Error("Handle event failed", "error", err)
			}

		case <-reconcileTicker.C:
			if err := l.reconcileUnconfirmed(ctx); err != nil {
				l.log.Error("Reconcile unconfirmed failed", "error", err)
			}
		}
	}
}

func (l *EventListener) handleEvent(ctx context.Context, event *blob.SubscriptionResponse) error {
	if len(event.Blobs) == 0 {
		// No blobs at this height in our namespace
		return nil
	}

	l.log.Info("Received blob event",
		"height", event.Height,
		"blob_count", len(event.Blobs))

	// Process each blob in the event
	for _, b := range event.Blobs {
		commitment := b.Commitment

		// Try to match event to a batch
		err := l.store.MarkBatchConfirmed(ctx, commitment, event.Height)
		if err == db.ErrBatchNotFound {
			// This might be a blob we don't know about, or already confirmed
			l.log.Debug("Event for unknown batch", "commitment", fmt.Sprintf("%x", commitment[:8]))
			continue
		}
		if err != nil {
			l.log.Error("Failed to mark batch confirmed", "error", err)
			continue
		}

		l.log.Info("Batch confirmed", "height", event.Height, "commitment", fmt.Sprintf("%x", commitment[:8]))
	}

	return nil
}

func (l *EventListener) reconcileUnconfirmed(ctx context.Context) error {
	// Get batches older than configured age that are not confirmed
	batches, err := l.store.GetUnconfirmedBatches(ctx, l.workerCfg.ReconcileAge)
	if err != nil {
		return fmt.Errorf("get unconfirmed batches: %w", err)
	}

	if len(batches) == 0 {
		l.log.Debug("No unconfirmed batches to reconcile")
		return nil
	}

	l.log.Info("Reconciling unconfirmed batches", "count", len(batches))

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

	return nil
}
