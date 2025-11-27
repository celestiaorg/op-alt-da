package worker

import "time"

// Config holds worker configuration
type Config struct {
	// Submission worker settings
	SubmitPeriod    time.Duration // How often to check for pending blobs
	SubmitTimeout   time.Duration // Timeout for Celestia submit operations
	MaxRetries      int           // Maximum retries for failed submissions
	MaxBlobWaitTime time.Duration // Max time a blob waits before forced submission (time-based batching)

	// Event listener settings
	ReconcilePeriod time.Duration // How often to reconcile unconfirmed batches
	ReconcileAge    time.Duration // Age threshold for reconciliation
	GetTimeout      time.Duration // Timeout for Celestia get operations

	// Read-only mode settings
	ReadOnly        bool     // If true, reject PUT requests and disable submission worker
	TrustedSigners  []string // Celestia addresses of trusted write servers (for HA failover via CIP-21)

	// Backfill worker settings
	BackfillEnabled bool          // Enable backfill worker for read-only servers
	StartHeight     uint64        // Celestia block height to start syncing from
	BackfillPeriod  time.Duration // How often to scan for new Celestia blocks
	BlocksPerScan   int           // How many blocks to scan per iteration (default: 10)

	// Multi-blob submission settings
	MaxTxSizeBytes    int // Maximum Celestia transaction size in bytes (default: 1.8MB)
	MaxBlockSizeBytes int // Maximum Celestia block size in bytes (default: 32MB)
}

// DefaultConfig returns default worker configuration
func DefaultConfig() *Config {
	return &Config{
		SubmitPeriod:    2 * time.Second,
		SubmitTimeout:   60 * time.Second,
		MaxRetries:      10,
		MaxBlobWaitTime: 30 * time.Second, // Force submit after 30s (time-based batching)
		ReconcilePeriod: 30 * time.Second,
		ReconcileAge:    2 * time.Minute,
		GetTimeout:      30 * time.Second,
		ReadOnly:        false,
		TrustedSigners:  []string{}, // Empty = accept all blobs (insecure for read-only mode)
		BackfillEnabled: false,
		StartHeight:     0,
		BackfillPeriod:    15 * time.Second, // Check for new blocks every 15s
		BlocksPerScan:     10,               // Scan 10 blocks per iteration
		MaxTxSizeBytes:    1843200,          // 1.8MB (safe limit below Celestia's 2MB max)
		MaxBlockSizeBytes: 33554432,         // 32MB Celestia block size
	}
}
