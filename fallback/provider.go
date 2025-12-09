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

// NoopProvider is used when fallback is disabled.
// Named "Noop" (not "NoOp") to avoid confusion with "OP" from OP Stack.
type NoopProvider struct{}

func (n *NoopProvider) Name() string                                      { return "noop" }
func (n *NoopProvider) Put(ctx context.Context, c, d []byte) error        { return nil }
func (n *NoopProvider) Get(ctx context.Context, c []byte) ([]byte, error) { return nil, ErrNotFound }
func (n *NoopProvider) Available() bool                                   { return false }
func (n *NoopProvider) Timeout() time.Duration                            { return 0 }

