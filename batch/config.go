package batch

import "fmt"

// Config holds batch processing configuration
type Config struct {
	// MinBlobs is the minimum number of blobs before creating a batch
	MinBlobs int

	// MaxBlobs is the maximum number of blobs per batch
	MaxBlobs int

	// TargetBlobs is the target number of blobs to fetch for batching
	TargetBlobs int

	// MaxBatchSizeBytes is the maximum size of a batch in bytes
	MaxBatchSizeBytes int

	// MinBatchSizeBytes is the minimum size before forcing batch submission
	MinBatchSizeBytes int
}

// DefaultConfig returns default batch configuration
func DefaultConfig() *Config {
	return &Config{
		MinBlobs:          10,
		MaxBlobs:          50,
		TargetBlobs:       20,
		MaxBatchSizeBytes: 1 * 1024 * 1024, // 1MB
		MinBatchSizeBytes: 500 * 1024,      // 500KB
	}
}

// ShouldBatch determines if we should create a batch based on config
func (c *Config) ShouldBatch(blobCount int, totalSize int) bool {
	// Never batch with zero blobs (defensive check for invalid input)
	if blobCount <= 0 {
		return false
	}

	// Batch if we've reached maximum blobs
	if blobCount >= c.MaxBlobs {
		return true
	}

	// Batch if we've reached minimum blobs
	if blobCount >= c.MinBlobs {
		return true
	}

	// Batch if total size is getting large
	if totalSize >= c.MinBatchSizeBytes {
		return true
	}

	// Don't batch if too few blobs and size is small
	return false
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	if c.MinBlobs <= 0 {
		return fmt.Errorf("MinBlobs must be positive, got %d", c.MinBlobs)
	}
	if c.MaxBlobs <= 0 {
		return fmt.Errorf("MaxBlobs must be positive, got %d", c.MaxBlobs)
	}
	if c.MinBlobs > c.MaxBlobs {
		return fmt.Errorf("MinBlobs (%d) must be <= MaxBlobs (%d)", c.MinBlobs, c.MaxBlobs)
	}
	if c.TargetBlobs <= 0 {
		return fmt.Errorf("TargetBlobs must be positive, got %d", c.TargetBlobs)
	}
	if c.TargetBlobs > c.MaxBlobs {
		return fmt.Errorf("TargetBlobs (%d) should be <= MaxBlobs (%d)", c.TargetBlobs, c.MaxBlobs)
	}
	if c.MaxBatchSizeBytes <= 0 {
		return fmt.Errorf("MaxBatchSizeBytes must be positive, got %d", c.MaxBatchSizeBytes)
	}
	if c.MinBatchSizeBytes < 0 {
		return fmt.Errorf("MinBatchSizeBytes must be non-negative, got %d", c.MinBatchSizeBytes)
	}
	if c.MinBatchSizeBytes > c.MaxBatchSizeBytes {
		return fmt.Errorf("MinBatchSizeBytes (%d) must be <= MaxBatchSizeBytes (%d)", c.MinBatchSizeBytes, c.MaxBatchSizeBytes)
	}
	return nil
}
