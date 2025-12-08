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

// securityHeadersMiddleware adds security headers to all responses (M2)
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// recoveryMiddleware recovers from panics and returns 500 instead of crashing (M5)
func recoveryMiddleware(next http.Handler, logger log.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				logger.Error("Handler panic recovered",
					"err", err,
					"path", r.URL.Path,
					"method", r.Method)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// CelestiaServer implements the HTTP server for the stateless DA server.
// Supports optional fallback provider for redundant storage.
type CelestiaServer struct {
	log      log.Logger
	endpoint string
	host     string

	// Celestia storage layer
	store *CelestiaStore

	// Timeouts and settings
	submitTimeout time.Duration
	getTimeout    time.Duration
	maxBlobSize   int64 // Maximum blob size in bytes (C1)

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

	// H2: Metrics server for proper shutdown
	metricsServer *http.Server
}

// NewCelestiaServer creates a new stateless Celestia server.
func NewCelestiaServer(
	host string,
	port int,
	store *CelestiaStore,
	submitTimeout time.Duration,
	getTimeout time.Duration,
	httpReadTimeout time.Duration, // C2: HTTP server read timeout
	httpWriteTimeout time.Duration, // C2: HTTP server write timeout
	httpIdleTimeout time.Duration, // C2: HTTP server idle timeout
	maxBlobSize int64, // C1: Maximum blob size in bytes
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
		maxBlobSize:    maxBlobSize, // C1
		metricsEnabled: metricsEnabled,
		metricsPort:    metricsPort,
		fallback:       fallbackProvider,
		httpServer: &http.Server{
			Addr:           endpoint,
			ReadTimeout:    httpReadTimeout,  // C2: From config
			WriteTimeout:   httpWriteTimeout, // C2: From config
			IdleTimeout:    httpIdleTimeout,  // C2: From config
			MaxHeaderBytes: 1 << 16,          // 64KB hardcoded
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

	// Wrap mux with middleware (order matters: recovery outermost)
	var handler http.Handler = mux
	handler = securityHeadersMiddleware(handler)
	handler = recoveryMiddleware(handler, d.log)

	d.httpServer.Handler = handler

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

	// Start metrics server if enabled (H2: track for proper shutdown)
	if d.metricsEnabled {
		d.metricsServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", d.metricsPort),
			Handler: promhttp.Handler(),
		}
		go func() {
			d.log.Info("Starting metrics server", "addr", d.metricsServer.Addr)
			if err := d.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
	// Validate HTTP method (M1)
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

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

	// Retrieve blob (Celestia first, fallback if not found)
	ctx := r.Context()
	data, err := d.getBlob(ctx, comm)
	if err != nil {
		d.handleGetError(w, comm, err, start)
		return
	}

	// Success
	d.recordGetSuccess(start, len(data))
	d.log.Info("Blob retrieved", "commitment", hex.EncodeToString(comm), "size", len(data), "duration", time.Since(start))

	w.Header().Set("Content-Type", "application/octet-stream")
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

// getBlob retrieves blob from Celestia. If not found and fallback is available,
// tries fallback. If found in Celestia and fallback is available, populates fallback (read-through).
func (d *CelestiaServer) getBlob(ctx context.Context, comm []byte) ([]byte, error) {
	getCtx, cancel := context.WithTimeout(ctx, d.getTimeout)
	defer cancel()

	data, err := d.store.Get(getCtx, comm)
	if err == nil {
		// Success from Celestia - read-through to fallback for future requests
		if d.fallback.Available() {
			d.wg.Add(1)
			go d.putFallback(context.Background(), comm, data)
		}
		return data, nil
	}

	// If actual error (not NotFound), return it
	if !isNotFoundError(err) {
		return nil, err
	}

	// NotFound - try fallback if available
	if !d.fallback.Available() {
		return nil, altda.ErrNotFound
	}

	d.log.Debug("Blob not found in Celestia, trying fallback", "commitment", hex.EncodeToString(comm))
	data, err = d.getFallback(ctx, comm)
	if err != nil {
		if !errors.Is(err, fallback.ErrNotFound) {
			d.log.Warn("Fallback read failed", "provider", d.fallback.Name(), "err", err)
		}
		return nil, altda.ErrNotFound
	}

	return data, nil
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
	// Validate HTTP method - accept both PUT and POST (M1)
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	start := time.Now()
	d.log.Debug("PUT", "url", r.URL)

	route := path.Base(r.URL.Path)
	if route != "put" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// C1: Limit request body size using config value
	r.Body = http.MaxBytesReader(w, r.Body, d.maxBlobSize)

	input, err := io.ReadAll(r.Body)
	if err != nil {
		// C1: Check for body too large error
		if err.Error() == "http: request body too large" {
			d.log.Warn("Request body too large", "limit", d.maxBlobSize)
			http.Error(w, "blob exceeds maximum size", http.StatusRequestEntityTooLarge)
			return
		}
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
		d.wg.Add(1)
		go d.putFallback(context.Background(), commitment, input)
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := w.Write(commitment); err != nil {
		d.log.Error("Failed to write response", "err", err)
	}
}

func (d *CelestiaServer) Endpoint() string {
	return d.listener.Addr().String()
}

// getFallback retrieves data from fallback provider (synchronous).
func (d *CelestiaServer) getFallback(ctx context.Context, commitment []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, d.fallback.Timeout())
	defer cancel()
	return d.fallback.Get(ctx, commitment)
}

// putFallback writes to fallback provider. Must be called with `go` after `d.wg.Add(1)`.
// Caller provides parent context (typically context.Background() for async operations).
func (d *CelestiaServer) putFallback(ctx context.Context, commitment []byte, data []byte) {
	defer d.wg.Done()

	ctx, cancel := context.WithTimeout(ctx, d.fallback.Timeout())
	defer cancel()

	if err := d.fallback.Put(ctx, commitment, data); err != nil {
		d.log.Warn("Fallback write failed", "provider", d.fallback.Name(), "err", err)
		// Record metric (M4)
		if d.metrics != nil {
			d.metrics.RecordFallbackWriteError()
		}
	} else {
		d.log.Debug("Fallback write succeeded", "provider", d.fallback.Name())
		// Record metric (M4)
		if d.metrics != nil {
			d.metrics.RecordFallbackWrite()
		}
	}
}

func (d *CelestiaServer) Stop() error {
	var errs []error

	// Shutdown HTTP server first
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := d.httpServer.Shutdown(ctx); err != nil {
		d.log.Error("HTTP server shutdown error", "err", err)
		errs = append(errs, fmt.Errorf("http server shutdown: %w", err))
	}

	// H2: Shutdown metrics server if running
	if d.metricsServer != nil {
		if err := d.metricsServer.Shutdown(ctx); err != nil {
			d.log.Error("Metrics server shutdown error", "err", err)
			errs = append(errs, fmt.Errorf("metrics server shutdown: %w", err))
		}
	}

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
		errs = append(errs, fmt.Errorf("timeout waiting for pending fallback operations"))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
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
