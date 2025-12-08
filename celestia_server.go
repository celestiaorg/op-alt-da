package celestia

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strconv"
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
	fallback     fallback.Provider
	fallbackMode string // "write_through", "read_fallback", "both"
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
	fallbackMode string,
	log log.Logger,
) *CelestiaServer {
	endpoint := net.JoinHostPort(host, strconv.Itoa(port))

	// Default to NoOpProvider if nil
	if fallbackProvider == nil {
		fallbackProvider = &fallback.NoOpProvider{}
	}

	// Default fallback mode
	if fallbackMode == "" {
		fallbackMode = "both"
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
		fallbackMode:   fallbackMode,
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

	route := path.Dir(r.URL.Path)
	if route != "/get" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	key := path.Base(r.URL.Path)
	comm, err := hexutil.Decode(key)
	if err != nil {
		d.log.Warn("Invalid commitment format", "key", key, "err", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Create context with configurable timeout
	ctx, cancel := context.WithTimeout(r.Context(), d.getTimeout)
	defer cancel()

	// Read blob directly from Celestia (stateless - no caching)
	celestiaData, err := d.store.Get(ctx, comm)
	if err != nil {
		// Record metrics for failed retrieval
		if d.metrics != nil {
			d.metrics.RecordRetrievalError()
			d.metrics.RecordHTTPRequest("get", time.Since(start))
		}

		if isNotFoundError(err) {
			d.log.Debug("Blob not found", "commitment", hex.EncodeToString(comm))
			w.WriteHeader(http.StatusNotFound)
		} else {
			d.log.Error("Failed to retrieve blob", "commitment", hex.EncodeToString(comm), "err", err)
			w.WriteHeader(http.StatusNotFound) // Return 404 on any error per requirements
		}
		return
	}

	if celestiaData == nil {
		// Try fallback provider if enabled
		if d.fallback.Available() && (d.fallbackMode == "read_fallback" || d.fallbackMode == "both") {
			fbCtx, fbCancel := context.WithTimeout(r.Context(), d.fallback.Timeout())
			defer fbCancel()
			fbData, fbErr := d.fallback.Get(fbCtx, comm)
			if fbErr == nil && fbData != nil {
				d.log.Info("Served from fallback",
					"provider", d.fallback.Name(),
					"commitment", hex.EncodeToString(comm),
					"size", len(fbData))
				if d.metrics != nil {
					d.metrics.RecordRetrieval(time.Since(start), len(fbData))
					d.metrics.RecordHTTPRequest("get", time.Since(start))
				}
				if _, err := w.Write(fbData); err != nil {
					d.log.Error("Failed to write fallback response", "err", err)
				}
				return
			}
			if fbErr != nil && fbErr != fallback.ErrNotFound {
				d.log.Warn("Fallback read failed", "provider", d.fallback.Name(), "err", fbErr)
			}
		}

		if d.metrics != nil {
			d.metrics.RecordRetrievalError()
			d.metrics.RecordHTTPRequest("get", time.Since(start))
		}
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Record metrics for successful retrieval
	duration := time.Since(start)
	if d.metrics != nil {
		d.metrics.RecordRetrieval(duration, len(celestiaData))
		d.metrics.RecordHTTPRequest("get", duration)
	}

	d.log.Info("Blob retrieved successfully",
		"commitment", hex.EncodeToString(comm),
		"size", len(celestiaData),
		"duration", duration)

	if _, err := w.Write(celestiaData); err != nil {
		d.log.Error("Failed to write response", "err", err)
	}
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
	if d.fallback.Available() && (d.fallbackMode == "write_through" || d.fallbackMode == "both") {
		go func(comm []byte, data []byte) {
			fbCtx, fbCancel := context.WithTimeout(context.Background(), d.fallback.Timeout())
			defer fbCancel()
			if err := d.fallback.Put(fbCtx, comm, data); err != nil {
				d.log.Warn("Fallback write failed", "provider", d.fallback.Name(), "err", err)
			} else {
				d.log.Debug("Fallback write succeeded", "provider", d.fallback.Name())
			}
		}(commitment, input)
	}

	if _, err := w.Write(commitment); err != nil {
		d.log.Error("Failed to write response", "err", err)
	}
}

func (d *CelestiaServer) Endpoint() string {
	return d.listener.Addr().String()
}

func (d *CelestiaServer) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.httpServer.Shutdown(ctx)
	return nil
}

// isNotFoundError checks if the error indicates the blob was not found.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if err == altda.ErrNotFound {
		return true
	}
	// Check for common "not found" error patterns
	errStr := err.Error()
	return contains(errStr, "not found") ||
		contains(errStr, "blob not found") ||
		contains(errStr, "no blob for commitment")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
