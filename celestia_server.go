package celestia

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/celestiaorg/op-alt-da/fallback"
	"github.com/celestiaorg/op-alt-da/metrics"
	altda "github.com/ethereum-optimism/optimism/op-alt-da"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// CelestiaServer implements the HTTP server for the stateless DA server.
// This is a simplified version without S3 caching/fallback, database, or workers.
type CelestiaServer struct {
	log      log.Logger
	endpoint string
	host     string

	// Celestia storage layer
	store *CelestiaStore

	// Timeouts and settings
	submitTimeout time.Duration
	getTimeout    time.Duration

	// HTTP server
	httpServer *http.Server
	listener   net.Listener

	// Metrics (from Agent C)
	metricsEnabled bool
	metricsPort    int
	metrics        *metrics.CelestiaMetrics

	// Fallback provider (Agent F)
	// When enabled, fallback performs both write-through (PUT) and read-fallback (GET)
	fallback fallback.Provider

	// Track pending async operations for graceful shutdown
	wg sync.WaitGroup
}

// NewCelestiaServer creates a new stateless Celestia server.
func NewCelestiaServer(
	host string,
	port int,
	store *CelestiaStore,
	submitTimeout time.Duration,
	getTimeout time.Duration,
	metricsEnabled bool,
	metricsPort int,
	fallbackProvider fallback.Provider,
	log log.Logger,
) *CelestiaServer {
	endpoint := net.JoinHostPort(host, strconv.Itoa(port))

	// Default to NoopProvider if nil
	if fallbackProvider == nil {
		fallbackProvider = &fallback.NoopProvider{}
	}

	server := &CelestiaServer{
		log:            log,
		endpoint:       endpoint,
		host:           host,
		store:          store,
		submitTimeout:  submitTimeout,
		getTimeout:     getTimeout,
		metricsEnabled: metricsEnabled,
		metricsPort:    metricsPort,
		fallback:       fallbackProvider,
		httpServer: &http.Server{
			Addr: endpoint,
		},
	}

	// Initialize metrics if enabled
	if metricsEnabled {
		server.metrics = metrics.NewCelestiaMetrics(prometheus.DefaultRegisterer)
	}

	return server
}

func (d *CelestiaServer) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/get/", d.HandleGet)
	mux.HandleFunc("/put/", d.HandlePut)
	mux.HandleFunc("/put", d.HandlePut)
	mux.HandleFunc("/health", d.HandleHealth)

	d.httpServer.Handler = mux

	listener, err := net.Listen("tcp", d.endpoint)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	d.listener = listener

	d.endpoint = listener.Addr().String()
	errCh := make(chan error, 1)
	go func() {
		if err := d.httpServer.Serve(d.listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Start metrics server if enabled
	if d.metricsEnabled {
		go func() {
			metricsAddr := fmt.Sprintf(":%d", d.metricsPort)
			d.log.Info("Starting metrics server", "addr", metricsAddr)
			if err := http.ListenAndServe(metricsAddr, promhttp.Handler()); err != nil {
				d.log.Error("Metrics server failed", "err", err)
			}
		}()
	}

	// Verify that the server comes up
	tick := time.NewTimer(10 * time.Millisecond)
	defer tick.Stop()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server failed: %w", err)
	case <-tick.C:
		return nil
	}
}

// HandleHealth returns 200 OK for health checks.
func (d *CelestiaServer) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK"))
}

// HandleGet retrieves a blob directly from Celestia.
// Request: GET /get/<hex-encoded-commitment>
// Response: Raw blob data on success, 404 on not found, 500 on error.
func (d *CelestiaServer) HandleGet(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	d.log.Debug("GET", "url", r.URL)

	// Validate route
	if path.Dir(r.URL.Path) != "/get" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Parse commitment
	comm, err := d.parseCommitment(r.URL.Path)
	if err != nil {
		d.log.Warn("Invalid commitment format", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Try to retrieve blob (Celestia first, then fallback)
	ctx := r.Context()
	data, fromFallback, err := d.getBlob(ctx, comm)
	if err != nil {
		d.handleGetError(w, comm, err, start)
		return
	}

	// Success - record metrics
	d.recordGetSuccess(start, len(data))

	d.log.Info("Blob retrieved successfully",
		"commitment", hex.EncodeToString(comm),
		"size", len(data),
		"fromFallback", fromFallback,
		"duration", time.Since(start))

	// Read-through: populate fallback with Celestia data for future requests
	if !fromFallback && d.fallback.Available() {
		d.runAsync(func() { d.putFallback(comm, data) })
	}

	if _, err := w.Write(data); err != nil {
		d.log.Error("Failed to write response", "err", err)
	}
}

// parseCommitment extracts and validates commitment from URL path.
func (d *CelestiaServer) parseCommitment(urlPath string) ([]byte, error) {
	key := path.Base(urlPath)
	comm, err := hexutil.Decode(key)
	if err != nil {
		// Sanitize for logging
		safeKey := strings.ReplaceAll(strings.ReplaceAll(key, "\n", ""), "\r", "")
		return nil, fmt.Errorf("invalid hex %q: %w", safeKey, err)
	}
	return comm, nil
}

// getBlob retrieves blob data. Tries Celestia first, then fallback if not found.
// Returns (data, fromFallback, error). fromFallback=false means data came from Celestia.
func (d *CelestiaServer) getBlob(ctx context.Context, comm []byte) ([]byte, bool, error) {
	// Try Celestia (default/primary source)
	data, err := d.getCelestia(ctx, comm)
	if err == nil {
		return data, false, nil
	}

	// If actual error (not NotFound), don't try fallback
	if !isNotFoundError(err) {
		return nil, false, err
	}

	// Celestia returned NotFound - try fallback if available
	if !d.fallback.Available() {
		return nil, false, altda.ErrNotFound
	}

	d.log.Debug("Blob not found in Celestia, trying fallback", "commitment", hex.EncodeToString(comm))

	data, err = d.getFallback(ctx, comm)
	if err != nil {
		if !errors.Is(err, fallback.ErrNotFound) {
			d.log.Warn("Fallback read failed", "provider", d.fallback.Name(), "err", err)
		}
		return nil, false, altda.ErrNotFound
	}

	return data, true, nil
}

// getCelestia retrieves blob from Celestia with configured timeout.
func (d *CelestiaServer) getCelestia(ctx context.Context, comm []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, d.getTimeout)
	defer cancel()
	return d.store.Get(ctx, comm)
}

// handleGetError handles errors from getBlob and writes appropriate HTTP response.
func (d *CelestiaServer) handleGetError(w http.ResponseWriter, comm []byte, err error, start time.Time) {
	if d.metrics != nil {
		d.metrics.RecordRetrievalError()
		d.metrics.RecordHTTPRequest("get", time.Since(start))
	}

	if isNotFoundError(err) {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	d.log.Error("Failed to retrieve blob", "commitment", hex.EncodeToString(comm), "err", err)
	w.WriteHeader(http.StatusInternalServerError)
}

// recordGetSuccess records metrics for successful blob retrieval.
func (d *CelestiaServer) recordGetSuccess(start time.Time, size int) {
	if d.metrics == nil {
		return
	}
	d.metrics.RecordRetrieval(time.Since(start), size)
	d.metrics.RecordHTTPRequest("get", time.Since(start))
}

// HandlePut submits a blob to Celestia and returns the commitment.
// Request: PUT /put with blob data in body
// Response: GenericCommitment on success, 500 on error (let op-batcher retry).
func (d *CelestiaServer) HandlePut(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	d.log.Debug("PUT", "url", r.URL)

	route := path.Base(r.URL.Path)
	if route != "put" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	input, err := io.ReadAll(r.Body)
	if err != nil {
		d.log.Error("Failed to read request body", "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Validate non-empty blob
	if len(input) == 0 {
		d.log.Warn("Empty blob rejected")
		http.Error(w, "empty blob not allowed", http.StatusBadRequest)
		return
	}

	// Create context with configurable timeout
	ctx, cancel := context.WithTimeout(r.Context(), d.submitTimeout)
	defer cancel()

	// Submit blob directly to Celestia (stateless - synchronous, blocks until confirmed)
	commitment, _, err := d.store.Put(ctx, input)
	if err != nil {
		// Record metrics for failed submission
		if d.metrics != nil {
			d.metrics.RecordSubmissionError()
			d.metrics.RecordHTTPRequest("put", time.Since(start))
		}

		d.log.Error("Failed to submit blob to Celestia",
			"size", len(input),
			"err", err)
		// Return 500 with error message - let op-batcher retry
		http.Error(w, fmt.Sprintf("submission failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Record metrics for successful submission
	duration := time.Since(start)
	if d.metrics != nil {
		d.metrics.RecordSubmission(duration, len(input))
		d.metrics.RecordHTTPRequest("put", duration)

		// Parse blob ID to get height for metrics
		var blobID CelestiaBlobID
		// Skip first 2 bytes which are frame version and altda version
		if err := blobID.UnmarshalBinary(commitment[2:]); err == nil {
			d.metrics.SetInclusionHeight(blobID.Height)
		}
	}

	d.log.Info("Blob submitted successfully",
		"commitment", hex.EncodeToString(commitment),
		"size", len(input),
		"duration", duration)

	// Write to fallback provider asynchronously (non-blocking)
	if d.fallback.Available() {
		d.runAsync(func() { d.putFallback(commitment, input) })
	}

	if _, err := w.Write(commitment); err != nil {
		d.log.Error("Failed to write response", "err", err)
	}
}

func (d *CelestiaServer) Endpoint() string {
	return d.listener.Addr().String()
}

// runAsync executes fn in a tracked goroutine. Tracked by WaitGroup for graceful shutdown.
func (d *CelestiaServer) runAsync(fn func()) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		fn()
	}()
}

// getFallback retrieves data from fallback provider (synchronous).
func (d *CelestiaServer) getFallback(ctx context.Context, commitment []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, d.fallback.Timeout())
	defer cancel()
	return d.fallback.Get(ctx, commitment)
}

// putFallback writes to fallback provider (synchronous).
// Uses context.Background() since typically called async, independent of HTTP request.
func (d *CelestiaServer) putFallback(commitment []byte, data []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), d.fallback.Timeout())
	defer cancel()

	if err := d.fallback.Put(ctx, commitment, data); err != nil {
		d.log.Warn("Fallback write failed", "provider", d.fallback.Name(), "err", err)
	} else {
		d.log.Debug("Fallback write succeeded", "provider", d.fallback.Name())
	}
}

func (d *CelestiaServer) Stop() error {
	// Shutdown HTTP server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.httpServer.Shutdown(ctx)

	// Wait for pending async operations (fallback writes) to complete
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		d.log.Info("All pending fallback operations completed")
	case <-time.After(10 * time.Second):
		d.log.Warn("Timeout waiting for pending fallback operations")
	}

	return nil
}

// isNotFoundError checks if the error indicates the blob was not found.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, altda.ErrNotFound)
}
