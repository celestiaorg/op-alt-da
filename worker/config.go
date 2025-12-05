package worker

import "time"

// Config holds worker configuration
type Config struct {
	// Submission worker settings (continuous mode - no tick period)
	SubmitTimeout          time.Duration // Timeout for Celestia submit operations
	MaxRetries             int           // Maximum retries for failed submissions
	RetryBackoff           time.Duration // Wait time between retries (linear backoff)
	MaxParallelSubmissions int           // Number of parallel Submit() calls (should match TxWorkerAccounts)

	// Event listener settings
	ReconcilePeriod time.Duration // How often to reconcile unconfirmed batches
	ReconcileAge    time.Duration // Age threshold for reconciliation
	GetTimeout      time.Duration // Timeout for Celestia get operations

	// Trusted signers for blob verification
	TrustedSigners []string // Celestia addresses of trusted write servers (for CIP-21 verification)

	// Backfill worker settings (for historical data migration)
	BackfillEnabled      bool          // Enable backfill worker
	StartHeight          uint64        // Celestia block height to start syncing from
	BackfillTargetHeight uint64        // Target height to backfill to (0 = disabled)
	BackfillPeriod       time.Duration // How often to run backfill iterations
	BlocksPerScan        int           // How many blocks to scan per iteration (also used as concurrency)
}

// DefaultConfig returns default worker configuration
func DefaultConfig() *Config {
	return &Config{
		// Submission settings (continuous mode - submits immediately)
		SubmitTimeout:          60 * time.Second,
		MaxRetries:             3,
		RetryBackoff:           1 * time.Second,
		MaxParallelSubmissions: 1,

		// Event listener settings
		ReconcilePeriod: 5 * time.Second,
		ReconcileAge:    10 * time.Second,
		GetTimeout:      30 * time.Second,

		// Trusted signers (empty = accept all - only safe in single-server mode)
		TrustedSigners: []string{},

		// Backfill settings (disabled by default)
		BackfillEnabled:      false,
		StartHeight:          0,
		BackfillTargetHeight: 0, // 0 = disabled
		BackfillPeriod:       1 * time.Second,
		BlocksPerScan:        50, // Scan 50 blocks in parallel per iteration
	}
}
