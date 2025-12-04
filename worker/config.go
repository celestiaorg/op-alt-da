package worker

import "time"

// Config holds worker configuration
type Config struct {
	// Submission worker settings
	SubmitPeriod           time.Duration // How often to check for pending blobs
	SubmitTimeout          time.Duration // Timeout for Celestia submit operations
	MaxRetries             int           // Maximum retries for failed submissions
	RetryBackoff           time.Duration // Wait time between retries (linear backoff)
	MaxBlobWaitTime        time.Duration // Max time a blob waits before forced submission (time-based batching)
	MaxParallelSubmissions int           // Number of parallel Submit() calls (should match TxWorkerAccounts)

	// Event listener settings
	ReconcilePeriod time.Duration // How often to reconcile unconfirmed batches
	ReconcileAge    time.Duration // Age threshold for reconciliation
	GetTimeout      time.Duration // Timeout for Celestia get operations

	// Read-only mode settings
	ReadOnly       bool     // If true, reject PUT requests and disable submission worker
	TrustedSigners []string // Celestia addresses of trusted write servers (for HA failover via CIP-21)

	// Backfill worker settings
	BackfillEnabled   bool          // Enable backfill worker for read-only servers
	StartHeight       uint64        // Celestia block height to start syncing from
	BackfillPeriod    time.Duration // How often to scan for new Celestia blocks
	BlocksPerScan     int           // How many blocks to scan per iteration (also used as concurrency)
	ConfirmationDepth uint64        // Re-scan this many recent heights to catch late-propagating blobs

	// Multi-blob submission settings
	MaxTxSizeBytes    int // Maximum Celestia transaction size in bytes (default: 1.8MB)
	MaxBlockSizeBytes int // Maximum Celestia block size in bytes (default: 32MB)
}

// DefaultConfig returns default worker configuration
func DefaultConfig() *Config {
	return &Config{
		SubmitPeriod:           2 * time.Second,
		SubmitTimeout:          60 * time.Second,
		MaxRetries:             3,
		RetryBackoff:           1 * time.Second,  // Linear backoff between retries
		MaxBlobWaitTime:        30 * time.Second, // Force submit after 30s (time-based batching)
		MaxParallelSubmissions: 1,                // Should match TxWorkerAccounts on Celestia node
		ReconcilePeriod:   5 * time.Second,  // Check frequently for any missed confirmations
		ReconcileAge:      10 * time.Second, // Reconcile batches after just 10s (edge case fallback)
		GetTimeout:        30 * time.Second,
		ReadOnly:          false,
		TrustedSigners:    []string{}, // Empty = accept all blobs (insecure for read-only mode)
		BackfillEnabled:   false,
		StartHeight:       0,
		BackfillPeriod:    500 * time.Millisecond, // Check every 500ms for aggressive catch-up
		BlocksPerScan:     50,                     // Scan 50 blocks in parallel per iteration
		ConfirmationDepth: 10,                     // Re-scan last 10 heights to catch late-propagating blobs
		MaxTxSizeBytes:    1843200,          // 1.8MB (safe limit below Celestia's 2MB max)
		MaxBlockSizeBytes: 33554432,         // 32MB Celestia block size
	}
}
