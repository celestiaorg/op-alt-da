package celestia

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	libshare "github.com/celestiaorg/go-square/v3/share"
	altda "github.com/ethereum-optimism/optimism/op-alt-da"
	"github.com/ethereum/go-ethereum/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"

	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/commitment"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/metrics"
	"github.com/celestiaorg/op-alt-da/worker"
)

type CelestiaServer struct {
	log      log.Logger
	endpoint string
	host     string // Store host for metrics server

	// Storage
	store     *db.BlobStore
	namespace libshare.Namespace

	// Celestia client (from existing store)
	celestiaStore *CelestiaStore

	// Configuration
	batchCfg  *batch.Config
	workerCfg *worker.Config

	// Workers
	submissionWorker *worker.SubmissionWorker
	eventListener    *worker.EventListener
	backfillWorker   *worker.BackfillWorker

	// HTTP server
	httpServer *http.Server
	listener   net.Listener

	// Metrics
	metricsEnabled  bool
	metricsPort     int
	metricsRegistry *prometheus.Registry
	celestiaMetrics *metrics.CelestiaMetrics

	firstRequestTimes sync.Map

	// GET stats aggregator for batched logging
	getStats *getStatsAggregator
}

// getStatsAggregator collects GET request stats for periodic logging
type getStatsAggregator struct {
	mu              sync.Mutex
	requestCount    int64
	totalBytes      int64
	totalLatencyMs  int64
	heightCounts    map[uint64]int64 // count per height
	lastLogTime     time.Time
	logInterval     time.Duration
}

func newGetStatsAggregator(logInterval time.Duration) *getStatsAggregator {
	return &getStatsAggregator{
		heightCounts: make(map[uint64]int64),
		lastLogTime:  time.Now(),
		logInterval:  logInterval,
	}
}

func (g *getStatsAggregator) record(height uint64, sizeBytes int, latencyMs int64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.requestCount++
	g.totalBytes += int64(sizeBytes)
	g.totalLatencyMs += latencyMs
	g.heightCounts[height]++
}

// flush returns stats and resets if interval elapsed, returns nil otherwise
func (g *getStatsAggregator) flush() *getStatsSummary {
	g.mu.Lock()
	defer g.mu.Unlock()

	if time.Since(g.lastLogTime) < g.logInterval || g.requestCount == 0 {
		return nil
	}

	summary := &getStatsSummary{
		requestCount:   g.requestCount,
		totalBytes:     g.totalBytes,
		avgLatencyMs:   g.totalLatencyMs / g.requestCount,
		uniqueHeights:  len(g.heightCounts),
		heightCounts:   g.heightCounts,
	}

	// Reset
	g.requestCount = 0
	g.totalBytes = 0
	g.totalLatencyMs = 0
	g.heightCounts = make(map[uint64]int64)
	g.lastLogTime = time.Now()

	return summary
}

type getStatsSummary struct {
	requestCount   int64
	totalBytes     int64
	avgLatencyMs   int64
	uniqueHeights  int
	heightCounts   map[uint64]int64
}

func NewCelestiaServer(
	host string,
	port int,
	store *db.BlobStore,
	celestiaStore *CelestiaStore,
	batchCfg *batch.Config,
	workerCfg *worker.Config,
	metricsEnabled bool,
	metricsPort int,
	log log.Logger,
) *CelestiaServer {
	endpoint := net.JoinHostPort(host, strconv.Itoa(port))

	// Create metrics registry and Celestia metrics
	var metricsRegistry *prometheus.Registry
	var celestiaMetrics *metrics.CelestiaMetrics
	if metricsEnabled {
		metricsRegistry = prometheus.NewRegistry()
		celestiaMetrics = metrics.NewCelestiaMetrics(metricsRegistry)
		log.Info("Celestia DA metrics enabled")
	}

	server := &CelestiaServer{
		log:             log,
		endpoint:        endpoint,
		host:            host,
		store:           store,
		namespace:       celestiaStore.Namespace,
		celestiaStore:   celestiaStore,
		batchCfg:        batchCfg,
		workerCfg:       workerCfg,
		metricsEnabled:  metricsEnabled,
		metricsPort:     metricsPort,
		metricsRegistry: metricsRegistry,
		celestiaMetrics: celestiaMetrics,
		getStats:        newGetStatsAggregator(10 * time.Second), // Log GET stats every 10s
		httpServer: &http.Server{
			Addr:         endpoint,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  120 * time.Second, // Close idle connections
		},
	}

	// Create submission worker for batching and submitting blobs to Celestia
	server.submissionWorker = worker.NewSubmissionWorker(
		store,
		celestiaStore.Client,
		celestiaStore.Header, // For checking if blob landed after timeout
		celestiaStore.Namespace,
		celestiaStore.SignerAddr, // Real signer address from keyring/RPC
		batchCfg,
		workerCfg,
		celestiaMetrics,
		log.New("component", "submission_worker"),
	)

	server.eventListener = worker.NewEventListener(
		store,
		celestiaStore.Client,
		celestiaStore.Namespace,
		workerCfg,
		celestiaMetrics,
		log.New("component", "event_listener"),
	)

	// Create backfill worker if enabled (for historical data migration)
	if workerCfg.BackfillEnabled && workerCfg.BackfillTargetHeight > 0 {
		server.backfillWorker = worker.NewBackfillWorker(
			store,
			celestiaStore.Client,
			celestiaStore.Namespace,
			batchCfg,
			workerCfg,
			celestiaMetrics,
			log.New("component", "backfill_worker"),
		)
	}

	return server
}

func (s *CelestiaServer) Start(ctx context.Context) error {
	// Setup HTTP routes
	mux := http.NewServeMux()
	mux.HandleFunc("/get/", s.HandleGet)
	mux.HandleFunc("/put/", s.HandlePut)
	mux.HandleFunc("/put", s.HandlePut)
	mux.HandleFunc("/health", s.HandleHealth)
	mux.HandleFunc("/stats", s.HandleStats)

	s.httpServer.Handler = mux

	// Create listener
	listener, err := net.Listen("tcp", s.endpoint)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	s.listener = listener

	s.log.Info("Server starting", "endpoint", s.endpoint)

	// Use errgroup for proper goroutine management
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		s.log.Info("HTTP server listening", "endpoint", s.endpoint)
		if err := s.httpServer.Serve(s.listener); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("http server error: %w", err)
		}
		s.log.Info("HTTP server stopped")
		return nil
	})

	g.Go(func() error {
		s.log.Info("Starting submission worker")
		if err := s.submissionWorker.Run(ctx); err != nil && err != context.Canceled {
			return fmt.Errorf("submission worker error: %w", err)
		}
		s.log.Info("Submission worker stopped")
		return nil
	})

	g.Go(func() error {
		s.log.Info("Starting reconciliation worker")
		if err := s.eventListener.Run(ctx); err != nil && err != context.Canceled {
			return fmt.Errorf("reconciliation worker error: %w", err)
		}
		s.log.Info("Reconciliation worker stopped")
		return nil
	})

	if s.backfillWorker != nil {
		g.Go(func() error {
			s.log.Info("Starting backfill worker")
			if err := s.backfillWorker.Run(ctx); err != nil && err != context.Canceled {
				return fmt.Errorf("backfill worker error: %w", err)
			}
			s.log.Info("Backfill worker stopped")
			return nil
		})
	}

	if s.metricsEnabled {
		// Create metrics HTTP handler with our custom registry
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", promhttp.HandlerFor(s.metricsRegistry, promhttp.HandlerOpts{}))

		// Use same host as main server to avoid exposing metrics publicly
		metricsServer := &http.Server{
			Addr:    net.JoinHostPort(s.host, strconv.Itoa(s.metricsPort)),
			Handler: metricsMux,
		}

		g.Go(func() error {
			metricsURL := fmt.Sprintf("http://%s/metrics", net.JoinHostPort(s.host, strconv.Itoa(s.metricsPort)))
			s.log.Info("========================================")
			s.log.Info("Metrics server starting", "endpoint", metricsURL)
			s.log.Info("Access metrics at:", "url", metricsURL)
			s.log.Info("========================================")
			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("metrics server error: %w", err)
			}
			s.log.Info("Metrics server stopped")
			return nil
		})

		// Shutdown metrics server on context cancel
		g.Go(func() error {
			<-ctx.Done()
			s.log.Info("Shutting down metrics server")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return metricsServer.Shutdown(shutdownCtx)
		})
	}

	g.Go(func() error {
		<-ctx.Done()
		s.log.Info("Shutting down HTTP server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	})

	// Wait for all goroutines
	s.log.Info("Waiting for all services to stop...")
	err = g.Wait()
	s.log.Info("All services stopped")
	return err
}

func (s *CelestiaServer) Stop() error {
	s.log.Info("Server stopping")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}

func (s *CelestiaServer) HandlePut(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() {
		// Record HTTP request duration
		if s.celestiaMetrics != nil {
			s.celestiaMetrics.RecordHTTPRequest("put", time.Since(startTime))
		}
	}()

	// Limit request body size to prevent DoS attacks
	// Use max batch size + 10% buffer for overhead
	maxSize := int64(s.batchCfg.MaxBatchSizeBytes + (s.batchCfg.MaxBatchSizeBytes / 10))
	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	// Read blob data
	blobData, err := io.ReadAll(r.Body)
	if err != nil {
		if err.Error() == "http: request body too large" {
			s.log.Warn("Request body too large", "max_size", maxSize)
			http.Error(w, fmt.Sprintf("request body too large (max: %d bytes)", maxSize), http.StatusRequestEntityTooLarge)
			return
		}
		s.log.Error("Failed to read request body", "error", err)
		http.Error(w, "failed to read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(blobData) == 0 {
		http.Error(w, "empty blob data", http.StatusBadRequest)
		return
	}

	// Record blob size metric
	if s.celestiaMetrics != nil {
		s.celestiaMetrics.RecordBlobSize(len(blobData))
	}

	// Compute commitment using the same signer as submission
	blobCommitment, err := commitment.ComputeCommitment(blobData, s.namespace, s.celestiaStore.SignerAddr)
	if err != nil {
		s.log.Error("Failed to compute commitment", "error", err)
		http.Error(w, "failed to compute commitment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.log.Debug("Computed commitment",
		"length", len(blobCommitment),
		"full_hex", hex.EncodeToString(blobCommitment),
		"truncated", hex.EncodeToString(blobCommitment[:min(8, len(blobCommitment))]))

	existingBlob, err := s.store.GetBlobByCommitment(r.Context(), blobCommitment)
	if err == nil {
		s.log.Debug("Blob already exists (idempotent)",
			"blob_id", existingBlob.ID,
			"size", len(blobData),
			"commitment", hex.EncodeToString(blobCommitment),
			"status", existingBlob.Status,
			"latency_ms", time.Since(startTime).Milliseconds())

		// Return commitment in GenericCommitment format (binary bytes)
		genericComm := altda.NewGenericCommitment(append([]byte{VersionByte}, blobCommitment...))
		encodedComm := genericComm.Encode()

		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(encodedComm); err != nil {
			s.log.Error("Failed to write commitment response", "error", err)
		}
		return
	}

	// Check for unexpected errors (anything other than "not found")
	if err != db.ErrBlobNotFound {
		s.log.Error("Database query failed while checking for existing blob", "error", err)
		http.Error(w, "database error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Blob doesn't exist - proceed with insertion

	// Insert into database
	namespaceBytes := s.namespace.Bytes()
	blob := &db.Blob{
		Commitment: blobCommitment,
		Namespace:  namespaceBytes,
		Data:       blobData,
		Size:       len(blobData),
		Status:     "pending_submission",
	}

	blobID, err := s.store.InsertBlob(r.Context(), blob)
	if err != nil {
		s.log.Error("Failed to insert blob", "error", err)
		http.Error(w, "failed to store blob: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Only log every 100 blobs to reduce noise
	if blobID%100 == 0 {
		s.log.Info("Blobs stored", "latest_id", blobID, "size", len(blobData))
	}

	// Return commitment in GenericCommitment format (binary bytes)
	// [commitment_type_byte][version_byte][blob_commitment]
	genericComm := altda.NewGenericCommitment(append([]byte{VersionByte}, blobCommitment...))
	encodedComm := genericComm.Encode()

	s.log.Debug("Returning commitment",
		"encoded_length", len(encodedComm),
		"encoded_hex", hex.EncodeToString(encodedComm))

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(encodedComm); err != nil {
		s.log.Error("Failed to write commitment response", "error", err)
	}
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (s *CelestiaServer) HandleGet(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	rw := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

	defer func() {
		duration := time.Since(startTime)

		// Record metrics if enabled
		if s.celestiaMetrics != nil {
			// Legacy metric (for compatibility)
			s.celestiaMetrics.RecordHTTPRequest("get", duration)

			// Option B: Record GET request with status code
			s.celestiaMetrics.RecordGetRequest(rw.statusCode, duration)
		}
	}()

	// Parse commitment from URL path
	commitmentHex := strings.TrimPrefix(r.URL.Path, "/get/")
	commitmentHex = strings.TrimPrefix(commitmentHex, "0x")

	encodedCommitment, err := hex.DecodeString(commitmentHex)
	if err != nil {
		s.log.Error("Invalid commitment format", "error", err, "hex", commitmentHex)
		http.Error(rw, "invalid commitment format", http.StatusBadRequest)
		return
	}

	// Validate commitment is not empty
	if len(encodedCommitment) == 0 {
		s.log.Error("Empty commitment")
		http.Error(rw, "invalid commitment format", http.StatusBadRequest)
		return
	}

	// Decode GenericCommitment format
	// Expected format: [commitment_type_byte][version_byte][blob_commitment...]
	var requestedCommitment []byte

	// Try to decode as GenericCommitment first
	_, decodeErr := altda.DecodeCommitmentData(encodedCommitment)
	if decodeErr == nil && len(encodedCommitment) >= 34 {
		if encodedCommitment[1] == VersionByte {
			requestedCommitment = encodedCommitment[2:]
		} else {
			requestedCommitment = encodedCommitment[1:]
		}
	} else {
		if len(encodedCommitment) > 1 && encodedCommitment[0] == VersionByte {
			requestedCommitment = encodedCommitment[1:]
		} else {
			requestedCommitment = encodedCommitment
		}
	}

	// Log at debug level - success/failure logs below are more informative
	commitmentKey := hex.EncodeToString(requestedCommitment)
	logCommitment := commitmentKey
	if len(logCommitment) > 16 {
		logCommitment = logCommitment[:16] + "..."
	}
	s.log.Debug("GET request received", "commitment", logCommitment)
	s.firstRequestTimes.LoadOrStore(commitmentKey, startTime)

	// Defer cleanup to prevent memory leak (remove after 1 hour or on success)
	defer func() {
		if rw.statusCode == http.StatusOK {
			if firstTime, ok := s.firstRequestTimes.Load(commitmentKey); ok && s.celestiaMetrics != nil {
				firstRequestTime := firstTime.(time.Time)
				timeToAvailability := time.Since(firstRequestTime)
				s.celestiaMetrics.RecordTimeToAvailability(timeToAvailability)
				s.log.Debug("Time to availability recorded",
					"commitment", logCommitment,
					"seconds", timeToAvailability.Seconds())
			}
			s.firstRequestTimes.Delete(commitmentKey)
		}
	}()

	blob, err := s.store.GetBlobByCommitment(r.Context(), requestedCommitment)
	if err == db.ErrBlobNotFound {
		batch, batchErr := s.store.GetBatchByCommitment(r.Context(), requestedCommitment)
		if batchErr == db.ErrBatchNotFound {
			s.log.Debug("GET miss - commitment not found", "commitment", logCommitment)
			http.Error(rw, "blob not found", http.StatusNotFound)
			return
		}
		if batchErr != nil {
			s.log.Error("Failed to query batch", "error", batchErr)
			http.Error(rw, "failed to query batch: "+batchErr.Error(), http.StatusInternalServerError)
			return
		}

		// Allow both 'confirmed' and 'verified' statuses
		if (batch.Status != "confirmed" && batch.Status != "verified") || batch.CelestiaHeight == nil {
			s.log.Warn("Batch not yet available on DA layer",
				"batch_id", batch.BatchID,
				"status", batch.Status,
				"has_height", batch.CelestiaHeight != nil)
			http.Error(rw, "blob not yet available on DA layer", http.StatusNotFound)
			return
		}

		// Batch is available - return the packed batch data (what's on Celestia)
		latencyMs := time.Since(startTime).Milliseconds()
		s.getStats.record(*batch.CelestiaHeight, batch.BatchSize, latencyMs)
		s.logGetStatsIfReady()

		rw.WriteHeader(http.StatusOK)
		rw.Write(batch.BatchData)
		return
	}
	if err != nil {
		s.log.Error("Failed to query blob", "error", err)
		http.Error(rw, "failed to query blob: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Only return blob if it's confirmed or verified on DA layer
	if (blob.Status != "confirmed" && blob.Status != "verified") || blob.CelestiaHeight == nil {
		// Only warn if blob is stuck in unexpected state
		if blob.Status != "pending_submission" && blob.Status != "batched" {
			s.log.Warn("Blob not yet available",
				"blob_id", blob.ID,
				"status", blob.Status,
				"has_height", blob.CelestiaHeight != nil)
		}
		http.Error(rw, "blob not yet available on DA layer", http.StatusNotFound)
		return
	}

	blobData := blob.Data

	// Record inclusion height if available
	if s.celestiaMetrics != nil && blob.CelestiaHeight != nil {
		s.celestiaMetrics.SetInclusionHeight(*blob.CelestiaHeight)
	}

	// Update read tracking (async, don't block response)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.store.MarkRead(ctx, blob.ID); err != nil {
			s.log.Error("Failed to mark read", "blob_id", blob.ID, "error", err)
		}
	}()

	latencyMs := time.Since(startTime).Milliseconds()
	s.getStats.record(*blob.CelestiaHeight, len(blobData), latencyMs)
	s.logGetStatsIfReady()

	// Return blob data
	rw.WriteHeader(http.StatusOK)
	rw.Write(blobData)
}

func (s *CelestiaServer) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (s *CelestiaServer) HandleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetStats(r.Context())
	if err != nil {
		http.Error(w, "failed to get stats: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Properly marshal to JSON
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(stats); err != nil {
		s.log.Error("Failed to encode stats", "error", err)
		// Status already sent, can't send error response
	}
}

// logGetStatsIfReady logs aggregated GET stats if the log interval has elapsed
func (s *CelestiaServer) logGetStatsIfReady() {
	summary := s.getStats.flush()
	if summary == nil {
		return
	}

	// Format heights for logging
	heightsStr := formatHeightCounts(summary.heightCounts)

	s.log.Info("GET requests served",
		"requests", summary.requestCount,
		"total_bytes", summary.totalBytes,
		"avg_latency_ms", summary.avgLatencyMs,
		"heights", heightsStr)
}

// formatHeightCounts formats height counts for logging
func formatHeightCounts(heightCounts map[uint64]int64) string {
	if len(heightCounts) == 0 {
		return "none"
	}

	// If only one height, just return it
	if len(heightCounts) == 1 {
		for h, c := range heightCounts {
			return fmt.Sprintf("%d (%d reqs)", h, c)
		}
	}

	// Multiple heights - find min/max and total
	var minH, maxH uint64
	first := true
	for h := range heightCounts {
		if first {
			minH, maxH = h, h
			first = false
		} else {
			if h < minH {
				minH = h
			}
			if h > maxH {
				maxH = h
			}
		}
	}

	return fmt.Sprintf("%d-%d (%d unique)", minH, maxH, len(heightCounts))
}
