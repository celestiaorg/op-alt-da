package worker

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/log"

	"github.com/celestiaorg/op-alt-da/commitment"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/metrics"
)

// retryOnDBLock retries a database operation if it fails with "database is locked"
func retryOnDBLock(ctx context.Context, maxRetries int, backoff time.Duration, op func() error) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		lastErr = op()
		if lastErr == nil {
			return nil
		}
		if !strings.Contains(lastErr.Error(), "database is locked") {
			return lastErr
		}
		// Database locked - wait and retry
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff * time.Duration(attempt+1)):
		}
	}
	return lastErr
}

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

	l.log.Info("Verification worker starting",
		"verify_period", l.workerCfg.ReconcilePeriod,
		"verify_age", l.workerCfg.ReconcileAge)

	ticker := time.NewTicker(l.workerCfg.ReconcilePeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			l.log.Info("Verification worker stopping")
			return ctx.Err()

		case <-ticker.C:
			if err := l.verifyConfirmedBatches(ctx); err != nil {
				l.log.Error("Verify batches failed", "error", err)
			}
		}
	}
}

// verifyConfirmedBatches uses blob.Get to verify confirmed batches are on Celestia.
// If verification succeeds, marks as 'verified'. If fails, reverts to pending for resubmission.
func (l *EventListener) verifyConfirmedBatches(ctx context.Context) error {
	// Get confirmed batches that need verification (older than configured age)
	batches, err := l.store.GetUnverifiedBatches(ctx, l.workerCfg.ReconcileAge)
	if err != nil {
		return fmt.Errorf("get unverified batches: %w", err)
	}

	if len(batches) == 0 {
		return nil
	}

	l.log.Debug("Verifying batches", "count", len(batches))

	verified := 0
	reverted := 0
	for _, batch := range batches {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		verifyResult := l.verifyBatch(ctx, batch)
		if verifyResult.err != nil {
			recordedHeight := uint64(0)
			if batch.CelestiaHeight != nil {
				recordedHeight = *batch.CelestiaHeight
			}

			if verifyResult.actualHeight > 0 {
				// CRITICAL: blob.Submit returned wrong height - this is a bug to report!
				l.log.Error("BLOB HEIGHT MISMATCH: blob.Submit returned incorrect height",
					"batch_id", batch.BatchID,
					"recorded_height", recordedHeight,
					"actual_height", verifyResult.actualHeight,
					"height_diff", int64(verifyResult.actualHeight)-int64(recordedHeight),
					"note", "This indicates blob.Submit() returned a wrong height - please report to protocol devs")
			} else {
				l.log.Warn("Verification failed: blob not found at recorded height or nearby",
					"batch_id", batch.BatchID,
					"recorded_height", recordedHeight,
					"search_range", fmt.Sprintf("%d to %d", verifyResult.searchStart, verifyResult.searchEnd),
					"error", verifyResult.err)
			}

			// Revert to pending for resubmission
			if revertErr := l.store.RevertBatchToPending(ctx, batch.BatchID); revertErr != nil {
				l.log.Error("Failed to revert batch", "batch_id", batch.BatchID, "error", revertErr)
			} else {
				reverted++
			}
		} else {
			verified++
		}
	}

	if verified > 0 || reverted > 0 {
		l.log.Info("Verification complete", "verified", verified, "reverted", reverted)
	}

	return nil
}

// verifyResult contains detailed information about a verification attempt.
type verifyResult struct {
	err          error  // nil if verification succeeded
	actualHeight uint64 // non-zero if blob was found at a different height than recorded
	searchStart  uint64 // start of height range searched
	searchEnd    uint64 // end of height range searched
}

// verifyBatch uses blob.Get to verify a confirmed batch is actually on Celestia.
// Returns verifyResult with detailed information about the verification.
// If the blob is found at a different height than recorded, actualHeight will be set.
func (l *EventListener) verifyBatch(ctx context.Context, batch *db.Batch) verifyResult {
	if batch.CelestiaHeight == nil {
		return verifyResult{err: fmt.Errorf("batch has no height recorded")}
	}

	recordedHeight := *batch.CelestiaHeight
	startTime := time.Now()

	l.log.Debug("Starting verification",
		"batch_id", batch.BatchID,
		"height", recordedHeight,
		"data_size", len(batch.BatchData),
		"trusted_signers", len(l.workerCfg.TrustedSigners))

	// Track errors for final error reporting
	signersTried := 0
	var lastGetErr error

	// First, try at the recorded height with each trusted signer
	for _, signerBech32 := range l.workerCfg.TrustedSigners {
		signerAddr, err := sdk.AccAddressFromBech32(signerBech32)
		if err != nil {
			continue
		}

		commitmentBytes, err := commitment.ComputeCommitment(batch.BatchData, l.namespace, signerAddr.Bytes())
		if err != nil {
			continue
		}

		signersTried++

		// Try blob.Get with this commitment at recorded height
		celestiaBlob, err := l.celestia.Get(ctx, recordedHeight, l.namespace, commitmentBytes)
		if err != nil {
			lastGetErr = err
			continue // Try next signer silently
		}

		// Found it - verify data matches
		if !bytes.Equal(celestiaBlob.Data(), batch.BatchData) {
			l.log.Warn("Data mismatch during verification",
				"batch_id", batch.BatchID,
				"height", recordedHeight,
				"signer", signerBech32)
			continue
		}

		// Verification successful - mark as verified
		if err := l.store.MarkBatchVerified(ctx, batch.BatchID); err != nil {
			return verifyResult{err: fmt.Errorf("mark verified: %w", err)}
		}

		// Record metrics
		if l.metrics != nil {
			l.metrics.RecordRetrieval(time.Since(startTime), len(batch.BatchData))
		}

		l.log.Info("Batch verified",
			"batch_id", batch.BatchID,
			"height", recordedHeight,
			"signer", signerBech32,
			"latency_ms", time.Since(startTime).Milliseconds())
		return verifyResult{} // Success
	}

	// Not found at recorded height - search nearby to detect potential blob.Submit bug
	// Search ±10 blocks around the recorded height
	searchRadius := uint64(10)
	searchStart := recordedHeight
	if searchStart > searchRadius {
		searchStart = recordedHeight - searchRadius
	} else {
		searchStart = 1
	}
	searchEnd := recordedHeight + searchRadius

	l.log.Debug("Blob not at recorded height, searching nearby",
		"batch_id", batch.BatchID,
		"recorded_height", recordedHeight,
		"search_range", fmt.Sprintf("%d to %d", searchStart, searchEnd))

	// Search nearby heights to find where blob actually is
	for _, signerBech32 := range l.workerCfg.TrustedSigners {
		signerAddr, err := sdk.AccAddressFromBech32(signerBech32)
		if err != nil {
			continue
		}

		commitmentBytes, err := commitment.ComputeCommitment(batch.BatchData, l.namespace, signerAddr.Bytes())
		if err != nil {
			continue
		}

		for height := searchStart; height <= searchEnd; height++ {
			if height == recordedHeight {
				continue // Already checked
			}

			celestiaBlob, err := l.celestia.Get(ctx, height, l.namespace, commitmentBytes)
			if err != nil {
				continue
			}

			// Found blob at different height - verify data matches
			if bytes.Equal(celestiaBlob.Data(), batch.BatchData) {
				// FOUND AT DIFFERENT HEIGHT - this indicates blob.Submit returned wrong height
				if l.metrics != nil {
					l.metrics.RecordRetrievalError()
				}
				return verifyResult{
					err:          fmt.Errorf("blob.Submit returned height %d but blob found at height %d with signer %s", recordedHeight, height, signerBech32),
					actualHeight: height,
					searchStart:  searchStart,
					searchEnd:    searchEnd,
				}
			}
		}
	}

	// Not found anywhere in search range - NOW log the error
	if l.metrics != nil {
		l.metrics.RecordRetrievalError()
	}

	l.log.Error("Blob not found with any trusted signer",
		"batch_id", batch.BatchID,
		"recorded_height", recordedHeight,
		"signers_tried", signersTried,
		"search_range", fmt.Sprintf("%d to %d", searchStart, searchEnd),
		"last_error", lastGetErr)

	return verifyResult{
		err:         fmt.Errorf("blob not found at recorded height %d or in range %d-%d (tried %d signers)", recordedHeight, searchStart, searchEnd, signersTried),
		searchStart: searchStart,
		searchEnd:   searchEnd,
	}
}
