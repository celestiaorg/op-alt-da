package celestia

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/celestiaorg/op-alt-da/metrics"
	"github.com/ethereum/go-ethereum/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockCelestiaStore is a mock implementation of the storage layer for testing.
// It allows testing server handlers without actual Celestia network calls.
type mockCelestiaStore struct {
	putFunc func(data []byte) ([]byte, []byte, error)
	getFunc func(key []byte) ([]byte, error)
}

// createTestServer creates a CelestiaServer for testing with mocked storage.
func createTestServer(t *testing.T, store *CelestiaStore) *CelestiaServer {
	logger := log.New()

	return NewCelestiaServer(
		"127.0.0.1",
		0,               // port 0 = let OS assign
		store,
		30*time.Second,  // submitTimeout
		30*time.Second,  // getTimeout
		30*time.Second,  // httpReadTimeout (C2)
		120*time.Second, // httpWriteTimeout (C2)
		60*time.Second,  // httpIdleTimeout (C2)
		2*1024*1024,     // maxBlobSize (2MB) (C1)
		false,           // metrics disabled for unit tests
		0,
		nil, // fallback provider (nil = NoopProvider)
		logger,
	)
}

// TestHandleHealth verifies the health endpoint returns 200 OK.
func TestHandleHealth(t *testing.T) {
	server := &CelestiaServer{
		log: log.New(),
	}

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	server.HandleHealth(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "OK", w.Body.String())
}

// TestHandlePut_EmptyBlob verifies that empty blob data returns 400 Bad Request.
func TestHandlePut_EmptyBlob(t *testing.T) {
	server := &CelestiaServer{
		log:           log.New(),
		submitTimeout: 30 * time.Second,
		maxBlobSize:   2 * 1024 * 1024, // 2MB
	}

	// Test empty body
	req := httptest.NewRequest(http.MethodPut, "/put", bytes.NewReader([]byte{}))
	w := httptest.NewRecorder()

	server.HandlePut(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "empty blob not allowed")
}

// TestHandlePut_WrongRoute verifies that wrong PUT routes return 400.
func TestHandlePut_WrongRoute(t *testing.T) {
	server := &CelestiaServer{
		log:         log.New(),
		maxBlobSize: 2 * 1024 * 1024, // 2MB
	}

	req := httptest.NewRequest(http.MethodPut, "/put/extra/path", bytes.NewReader([]byte("data")))
	w := httptest.NewRecorder()

	server.HandlePut(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleGet_InvalidCommitmentFormat verifies invalid hex returns 400.
func TestHandleGet_InvalidCommitmentFormat(t *testing.T) {
	server := &CelestiaServer{
		log:        log.New(),
		getTimeout: 30 * time.Second,
	}

	// Test invalid hex commitment
	req := httptest.NewRequest(http.MethodGet, "/get/0xZZZZ", nil)
	w := httptest.NewRecorder()

	server.HandleGet(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleGet_WrongRoute verifies wrong route returns 400.
func TestHandleGet_WrongRoute(t *testing.T) {
	server := &CelestiaServer{
		log: log.New(),
	}

	req := httptest.NewRequest(http.MethodGet, "/wrong/path", nil)
	w := httptest.NewRecorder()

	server.HandleGet(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestHandleGet_TooShortCommitment verifies short commitments return 400.
func TestHandleGet_TooShortCommitment(t *testing.T) {
	server := &CelestiaServer{
		log:        log.New(),
		getTimeout: 30 * time.Second,
		store:      &CelestiaStore{Log: log.New()},
	}

	// Test commitment that's too short (less than 40 bytes = minimum BlobID)
	req := httptest.NewRequest(http.MethodGet, "/get/0x0102030405", nil)
	w := httptest.NewRecorder()

	server.HandleGet(w, req)

	// Should return 500 because commitment can't be parsed (actual error, not "not found")
	assert.True(t, w.Code == http.StatusBadRequest || w.Code == http.StatusInternalServerError,
		"Expected 400 or 500 for invalid short commitment, got %d", w.Code)
}

// TestMetricsRecording verifies metrics are recorded correctly.
func TestMetricsRecording(t *testing.T) {
	registry := prometheus.NewRegistry()
	m := metrics.NewCelestiaMetrics(registry)

	// Record various operations
	m.RecordHTTPRequest("get", 100*time.Millisecond)
	m.RecordHTTPRequest("put", 5*time.Second)
	m.RecordSubmission(10*time.Second, 4096)
	m.RecordRetrieval(500*time.Millisecond, 2048)
	m.SetInclusionHeight(12345)
	m.RecordSubmissionError()
	m.RecordRetrievalError()

	// Verify metrics were recorded
	metricFamilies, err := registry.Gather()
	require.NoError(t, err)

	expectedMetrics := []string{
		"op_altda_request_duration_seconds",
		"op_altda_blob_size_bytes",
		"op_altda_inclusion_height",
		"celestia_submission_duration_seconds",
		"celestia_submissions_total",
		"celestia_submission_errors_total",
		"celestia_retrieval_duration_seconds",
		"celestia_retrievals_total",
		"celestia_retrieval_errors_total",
	}

	foundMetrics := make(map[string]bool)
	for _, mf := range metricFamilies {
		foundMetrics[mf.GetName()] = true
	}

	for _, expected := range expectedMetrics {
		assert.True(t, foundMetrics[expected], "Expected metric %s to be registered", expected)
	}
}

// TestServerEndpoints verifies that all expected endpoints are registered.
func TestServerEndpoints(t *testing.T) {
	server := &CelestiaServer{
		log:           log.New(),
		endpoint:      "127.0.0.1:0",
		submitTimeout: 30 * time.Second,
		getTimeout:    30 * time.Second,
		maxBlobSize:   2 * 1024 * 1024, // 2MB
		httpServer:    &http.Server{},
	}

	// Test that endpoints are handled correctly
	endpoints := []struct {
		method string
		path   string
		want   int // expected status code for basic request
	}{
		{http.MethodGet, "/health", http.StatusOK},
		{http.MethodPut, "/put", http.StatusBadRequest}, // empty body = 400
		{http.MethodGet, "/get/invalid", http.StatusBadRequest},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+"_"+strings.TrimPrefix(ep.path, "/"), func(t *testing.T) {
			var req *http.Request
			if ep.method == http.MethodPut {
				req = httptest.NewRequest(ep.method, ep.path, bytes.NewReader([]byte{}))
			} else {
				req = httptest.NewRequest(ep.method, ep.path, nil)
			}
			w := httptest.NewRecorder()

			switch {
			case strings.HasPrefix(ep.path, "/health"):
				server.HandleHealth(w, req)
			case strings.HasPrefix(ep.path, "/put"):
				server.HandlePut(w, req)
			case strings.HasPrefix(ep.path, "/get"):
				server.HandleGet(w, req)
			}

			assert.Equal(t, ep.want, w.Code, "endpoint %s %s", ep.method, ep.path)
		})
	}
}

// TestBlobIDVersioning ensures proper handling of different commitment formats.
func TestBlobIDVersioning(t *testing.T) {
	// Test CelestiaBlobID encoding/decoding at different formats
	testCases := []struct {
		name      string
		blobID    CelestiaBlobID
		expectLen int
	}{
		{
			name: "compact format",
			blobID: CelestiaBlobID{
				Height:     12345,
				Commitment: make([]byte, CommitmentSize),
			},
			expectLen: CompactBlobIDSize,
		},
		{
			name: "full format",
			blobID: CelestiaBlobID{
				Height:      12345,
				Commitment:  make([]byte, CommitmentSize),
				ShareOffset: 10,
				ShareSize:   100,
			},
			expectLen: FullBlobIDSize,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set isCompact based on expected length
			if tc.expectLen == CompactBlobIDSize {
				tc.blobID.SetCompact(true)
			}

			data, err := tc.blobID.MarshalBinary()
			require.NoError(t, err)
			assert.Len(t, data, tc.expectLen)

			var decoded CelestiaBlobID
			err = decoded.UnmarshalBinary(data)
			require.NoError(t, err)
			assert.Equal(t, tc.blobID.Height, decoded.Height)
		})
	}
}

