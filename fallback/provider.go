package fallback

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when blob is not found in fallback provider.
var ErrNotFound = errors.New("fallback: blob not found")

// Provider defines the interface for fallback storage providers.
// Implementations can store/retrieve blob data as a backup to Celestia.
type Provider interface {
	// Name returns the provider identifier (e.g., "s3", "ethereum")
	Name() string

	// Put stores blob data with commitment as key.
	Put(ctx context.Context, commitment []byte, data []byte) error

	// Get retrieves blob data by commitment.
	Get(ctx context.Context, commitment []byte) ([]byte, error)

	// Available returns true if provider is configured and ready.
	Available() bool

	// Timeout returns the configured timeout for operations.
	Timeout() time.Duration
}

// NoOpProvider is used when fallback is disabled.
type NoOpProvider struct{}

func (n *NoOpProvider) Name() string                                      { return "noop" }
func (n *NoOpProvider) Put(ctx context.Context, c, d []byte) error        { return nil }
func (n *NoOpProvider) Get(ctx context.Context, c []byte) ([]byte, error) { return nil, ErrNotFound }
func (n *NoOpProvider) Available() bool                                   { return false }
func (n *NoOpProvider) Timeout() time.Duration                            { return 0 }

