package worker

import "time"

// Config holds worker configuration
type Config struct {
	// Submission worker settings
	SubmitPeriod  time.Duration // How often to check for pending blobs
	SubmitTimeout time.Duration // Timeout for Celestia submit operations
	MaxRetries    int           // Maximum retries for failed submissions

	// Event listener settings
	ReconcilePeriod time.Duration // How often to reconcile unconfirmed batches
	ReconcileAge    time.Duration // Age threshold for reconciliation
	GetTimeout      time.Duration // Timeout for Celestia get operations
}

// DefaultConfig returns default worker configuration
func DefaultConfig() *Config {
	return &Config{
		SubmitPeriod:    2 * time.Second,
		SubmitTimeout:   60 * time.Second,
		MaxRetries:      10,
		ReconcilePeriod: 30 * time.Second,
		ReconcileAge:    2 * time.Minute,
		GetTimeout:      30 * time.Second,
	}
}
