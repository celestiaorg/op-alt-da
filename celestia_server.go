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

	// HTTP server
	httpServer *http.Server
	listener   net.Listener

	// Metrics
	metricsEnabled  bool
	metricsPort     int
	metricsRegistry *prometheus.Registry
	celestiaMetrics *metrics.CelestiaMetrics
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
		host:            host, // Store host for metrics server
		store:           store,
		namespace:       celestiaStore.Namespace,
		celestiaStore:   celestiaStore,
		batchCfg:        batchCfg,
		workerCfg:       workerCfg,
		metricsEnabled:  metricsEnabled,
		metricsPort:     metricsPort,
		metricsRegistry: metricsRegistry,
		celestiaMetrics: celestiaMetrics,
		httpServer: &http.Server{
			Addr:         endpoint,
			ReadTimeout:  30 * time.Second,  // Prevent slow client attacks
			WriteTimeout: 30 * time.Second,  // Prevent slow writes
			IdleTimeout:  120 * time.Second, // Close idle connections
		},
	}

	// Create workers with metrics
	server.submissionWorker = worker.NewSubmissionWorker(
		store,
		celestiaStore.Client,
		celestiaStore.Namespace,
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

	// Start HTTP server
	g.Go(func() error {
		s.log.Info("HTTP server listening", "endpoint", s.endpoint)
		if err := s.httpServer.Serve(s.listener); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("http server error: %w", err)
		}
		return nil
	})

	// Start submission worker
	g.Go(func() error {
		s.log.Info("Starting submission worker")
		if err := s.submissionWorker.Run(ctx); err != nil && err != context.Canceled {
			return fmt.Errorf("submission worker error: %w", err)
		}
		return nil
	})

	// Start reconciliation worker
	g.Go(func() error {
		s.log.Info("Starting reconciliation worker")
		if err := s.eventListener.Run(ctx); err != nil && err != context.Canceled {
			return fmt.Errorf("reconciliation worker error: %w", err)
		}
		return nil
	})

	// Start metrics server if enabled
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
			return nil
		})

		// Shutdown metrics server on context cancel
		g.Go(func() error {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return metricsServer.Shutdown(shutdownCtx)
		})
	}

	// Shutdown HTTP server on context cancel
	g.Go(func() error {
		<-ctx.Done()
		s.log.Info("Shutting down HTTP server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	})

	// Wait for all goroutines
	return g.Wait()
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

	// Pre-compute commitment (deterministic)
	blobCommitment, err := commitment.ComputeCommitment(blobData, s.namespace)
	if err != nil {
		s.log.Error("Failed to compute commitment", "error", err)
		http.Error(w, "failed to compute commitment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Debug: log full commitment details
	s.log.Debug("Computed commitment",
		"length", len(blobCommitment),
		"full_hex", hex.EncodeToString(blobCommitment),
		"truncated", hex.EncodeToString(blobCommitment[:min(8, len(blobCommitment))]))

	// Check if blob already exists (idempotent PUT behavior)
	existingBlob, err := s.store.GetBlobByCommitment(r.Context(), blobCommitment)
	if err == nil {
		// Blob already exists - return existing commitment (idempotent, normal behavior)
		s.log.Debug("Blob retrieved from cache (already submitted)",
			"blob_id", existingBlob.ID,
			"size", len(blobData),
			"commitment", hex.EncodeToString(blobCommitment),
			"status", existingBlob.Status,
			"latency_ms", time.Since(startTime).Milliseconds())

		// Return commitment in GenericCommitment format (binary bytes)
		// Format: [commitment_type_byte][version_byte][blob_commitment]
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

	s.log.Info("Blob stored",
		"blob_id", blobID,
		"size", len(blobData),
		"commitment", hex.EncodeToString(blobCommitment),
		"latency_ms", time.Since(startTime).Milliseconds())

	// Return commitment in GenericCommitment format (binary bytes)
	// This matches OP-Batcher expectations: [commitment_type_byte][version_byte][blob_commitment]
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

func (s *CelestiaServer) HandleGet(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	defer func() {
		// Record HTTP request duration
		if s.celestiaMetrics != nil {
			s.celestiaMetrics.RecordHTTPRequest("get", time.Since(startTime))
		}
	}()

	// Parse commitment from URL path
	commitmentHex := strings.TrimPrefix(r.URL.Path, "/get/")
	commitmentHex = strings.TrimPrefix(commitmentHex, "0x")

	encodedCommitment, err := hex.DecodeString(commitmentHex)
	if err != nil {
		s.log.Error("Invalid commitment format", "error", err, "hex", commitmentHex)
		http.Error(w, "invalid commitment format", http.StatusBadRequest)
		return
	}

	// Validate commitment is not empty
	if len(encodedCommitment) == 0 {
		s.log.Error("Empty commitment")
		http.Error(w, "invalid commitment format", http.StatusBadRequest)
		return
	}

	// Decode GenericCommitment format
	// Expected format: [commitment_type_byte][version_byte][blob_commitment...]
	var requestedCommitment []byte

	// Try to decode as GenericCommitment first
	_, decodeErr := altda.DecodeCommitmentData(encodedCommitment)
	if decodeErr == nil && len(encodedCommitment) >= 34 {
		// Successfully decoded GenericCommitment
		// Format: [type_byte][version_byte][32_byte_blob_commitment]
		// Extract just the blob commitment (skip first 2 bytes)
		if encodedCommitment[1] == VersionByte {
			requestedCommitment = encodedCommitment[2:]
		} else {
			// Fallback: might have different format
			requestedCommitment = encodedCommitment[1:]
		}
	} else {
		// Not GenericCommitment format - treat as raw commitment
		// Check if it starts with version byte
		if len(encodedCommitment) > 1 && encodedCommitment[0] == VersionByte {
			requestedCommitment = encodedCommitment[1:]
		} else {
			requestedCommitment = encodedCommitment
		}
	}

	// Debug: log what we're looking for
	s.log.Info("GET request details",
		"encoded_length", len(encodedCommitment),
		"encoded_hex", hex.EncodeToString(encodedCommitment),
		"commitment_length", len(requestedCommitment),
		"commitment_hex", hex.EncodeToString(requestedCommitment))

	// Query database for blob metadata
	blob, err := s.store.GetBlobByCommitment(r.Context(), requestedCommitment)
	if err == db.ErrBlobNotFound {
		// Not found as individual blob - try as batch commitment
		batch, batchErr := s.store.GetBatchByCommitment(r.Context(), requestedCommitment)
		if batchErr == db.ErrBatchNotFound {
			// Not found in our DB
			s.log.Warn("Blob not found in DB",
				"commitment", hex.EncodeToString(requestedCommitment))
			http.Error(w, "blob not found", http.StatusNotFound)
			return
		}
		if batchErr != nil {
			s.log.Error("Failed to query batch", "error", batchErr)
			http.Error(w, "failed to query batch: "+batchErr.Error(), http.StatusInternalServerError)
			return
		}

		// Check if batch is confirmed on DA layer
		if batch.Status != "confirmed" || batch.CelestiaHeight == nil {
			s.log.Warn("Batch not yet confirmed on DA layer",
				"batch_id", batch.BatchID,
				"status", batch.Status,
				"has_height", batch.CelestiaHeight != nil)
			http.Error(w, "blob not yet available on DA layer", http.StatusNotFound)
			return
		}

		// Batch is confirmed - return the packed batch data (what's actually on Celestia)
		s.log.Info("Batch retrieved",
			"batch_id", batch.BatchID,
			"size", batch.BatchSize,
			"blob_count", batch.BlobCount,
			"status", batch.Status,
			"celestia_height", *batch.CelestiaHeight,
			"commitment", hex.EncodeToString(requestedCommitment),
			"latency_ms", time.Since(startTime).Milliseconds())

		w.WriteHeader(http.StatusOK)
		w.Write(batch.BatchData)
		return
	}
	if err != nil {
		s.log.Error("Failed to query blob", "error", err)
		http.Error(w, "failed to query blob: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Only return blob if it's confirmed on DA layer
	if blob.Status != "confirmed" || blob.CelestiaHeight == nil || blob.BatchID == nil {
		s.log.Warn("Blob not yet confirmed on DA layer",
			"blob_id", blob.ID,
			"status", blob.Status,
			"has_height", blob.CelestiaHeight != nil,
			"has_batch", blob.BatchID != nil)
		http.Error(w, "blob not yet available on DA layer", http.StatusNotFound)
		return
	}

	// Blob is confirmed - get batch from DB
	s.log.Debug("Retrieving blob from confirmed batch",
		"blob_id", blob.ID,
		"batch_id", *blob.BatchID,
		"height", *blob.CelestiaHeight)

	// Get batch metadata and data from DB
	batchRecord, err := s.store.GetBatchByID(r.Context(), *blob.BatchID)
	if err != nil {
		s.log.Error("Failed to get batch metadata", "batch_id", *blob.BatchID, "error", err)
		http.Error(w, "failed to get batch metadata: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Use cached batch data from database
	// This is faster than fetching from Celestia every time, and we know it's confirmed
	packedData := batchRecord.BatchData

	// Optionally verify against Celestia if celestiaStore is available
	// (In production, the confirmation worker already verified the data matches)
	if s.celestiaStore != nil && s.celestiaStore.Client != nil {
		// This is optional - we could fetch from Celestia to double-check
		// but for now we trust our cached data since it was confirmed by the worker
		s.log.Debug("Using cached batch data (already confirmed on Celestia)",
			"batch_id", *blob.BatchID,
			"height", *blob.CelestiaHeight)
	}

	// Unpack batch to extract individual blob
	unpackedBlobs, err := batch.UnpackBlobs(packedData, s.batchCfg)
	if err != nil {
		s.log.Error("Failed to unpack batch from Celestia", "error", err)
		http.Error(w, "failed to unpack batch: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if blob.BatchIndex == nil {
		s.log.Error("Blob has no batch index", "blob_id", blob.ID)
		http.Error(w, "blob missing batch index", http.StatusInternalServerError)
		return
	}

	if *blob.BatchIndex >= len(unpackedBlobs) || *blob.BatchIndex < 0 {
		s.log.Error("Blob batch index out of range",
			"blob_id", blob.ID,
			"batch_index", *blob.BatchIndex,
			"batch_size", len(unpackedBlobs))
		http.Error(w, "blob batch index out of range", http.StatusInternalServerError)
		return
	}

	// Directly access blob by its index - much more efficient!
	blobData := unpackedBlobs[*blob.BatchIndex]
	s.log.Debug("Retrieved blob from batch using index",
		"blob_id", blob.ID,
		"batch_index", *blob.BatchIndex,
		"size", len(blobData))

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

	s.log.Info("Blob retrieved",
		"blob_id", blob.ID,
		"size", len(blobData),
		"status", blob.Status,
		"commitment", hex.EncodeToString(requestedCommitment),
		"latency_ms", time.Since(startTime).Milliseconds())

	// Return blob data
	w.WriteHeader(http.StatusOK)
	w.Write(blobData)
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
