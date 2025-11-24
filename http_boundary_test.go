package celestia

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/worker"
	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHTTP_PutBoundaries tests PUT endpoint with boundary cases
func TestHTTP_PutBoundaries(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	namespace := createTestNamespace(t)
	batchCfg := batch.DefaultConfig()
	workerCfg := worker.DefaultConfig()

	server := &CelestiaServer{
		store:       store,
		namespace:   namespace,
		batchCfg:    batchCfg,
		workerCfg:   workerCfg,
		log:         log.New(),
	}

	tests := []struct {
		name           string
		body           io.Reader
		expectedStatus int
		errorContains  string
	}{
		{
			name:           "empty body",
			body:           bytes.NewReader([]byte{}),
			expectedStatus: http.StatusBadRequest,
			errorContains:  "empty blob data",
		},
		{
			name:           "nil body",
			body:           nil,
			expectedStatus: http.StatusBadRequest,
			errorContains:  "",
		},
		{
			name:           "single byte",
			body:           bytes.NewReader([]byte{0x01}),
			expectedStatus: http.StatusOK,
			errorContains:  "",
		},
		{
			name:           "maximum allowed size",
			body:           bytes.NewReader(make([]byte, batchCfg.MaxBatchSizeBytes)),
			expectedStatus: http.StatusOK,
			errorContains:  "",
		},
		{
			name: "exceeds maximum size",
			body: bytes.NewReader(make([]byte, batchCfg.MaxBatchSizeBytes+batchCfg.MaxBatchSizeBytes/10+1)),
			expectedStatus: http.StatusRequestEntityTooLarge,
			errorContains:  "too large",
		},
		{
			name:           "all zeros (valid data)",
			body:           bytes.NewReader(make([]byte, 100)),
			expectedStatus: http.StatusOK,
			errorContains:  "",
		},
		{
			name:           "all 0xFF (valid data)",
			body:           bytes.NewReader(bytes.Repeat([]byte{0xFF}, 100)),
			expectedStatus: http.StatusOK,
			errorContains:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/put", tt.body)
			w := httptest.NewRecorder()

			server.HandlePut(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code, "status code mismatch")

			if tt.errorContains != "" {
				assert.Contains(t, w.Body.String(), tt.errorContains, "error message should contain expected text")
			}

			if tt.expectedStatus == http.StatusOK {
				// Verify response is a valid commitment
				commitment := w.Body.Bytes()
				assert.NotEmpty(t, commitment, "commitment should not be empty")
				// GenericCommitment format: [type_byte][version_byte][32_byte_commitment]
				assert.GreaterOrEqual(t, len(commitment), 34, "commitment should be at least 34 bytes")
			}
		})
	}
}

// TestHTTP_GetBoundaries tests GET endpoint with boundary cases
func TestHTTP_GetBoundaries(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	namespace := createTestNamespace(t)
	batchCfg := batch.DefaultConfig()
	workerCfg := worker.DefaultConfig()

	server := &CelestiaServer{
		store:       store,
		namespace:   namespace,
		batchCfg:    batchCfg,
		workerCfg:   workerCfg,
		log:         log.New(),
	}

	tests := []struct {
		name           string
		commitment     string
		expectedStatus int
		errorContains  string
	}{
		{
			name:           "empty commitment",
			commitment:     "",
			expectedStatus: http.StatusBadRequest,
			errorContains:  "invalid commitment",
		},
		{
			name:           "single byte commitment",
			commitment:     "0x01",
			expectedStatus: http.StatusNotFound,
			errorContains:  "not found",
		},
		{
			name:           "31 bytes (just under 32)",
			commitment:     "0x" + strings.Repeat("ab", 31),
			expectedStatus: http.StatusNotFound,
			errorContains:  "not found",
		},
		{
			name:           "32 bytes (normal)",
			commitment:     "0x" + strings.Repeat("cd", 32),
			expectedStatus: http.StatusNotFound,
			errorContains:  "not found",
		},
		{
			name:           "33 bytes (just over 32)",
			commitment:     "0x" + strings.Repeat("ef", 33),
			expectedStatus: http.StatusNotFound,
			errorContains:  "not found",
		},
		{
			name:           "invalid hex characters",
			commitment:     "0xZZZZ",
			expectedStatus: http.StatusBadRequest,
			errorContains:  "invalid commitment",
		},
		{
			name:           "odd length hex",
			commitment:     "0x123",
			expectedStatus: http.StatusBadRequest,
			errorContains:  "invalid commitment",
		},
		{
			name:           "no 0x prefix",
			commitment:     strings.Repeat("ab", 32),
			expectedStatus: http.StatusNotFound,
			errorContains:  "not found",
		},
		{
			name:           "special characters",
			commitment:     "0x../../../etc/passwd",
			expectedStatus: http.StatusBadRequest,
			errorContains:  "invalid commitment",
		},
		{
			name:           "sql injection attempt",
			commitment:     "0x%27%20OR%20%271%27%3D%271", // URL-encoded: 0x' OR '1'='1
			expectedStatus: http.StatusBadRequest,
			errorContains:  "invalid commitment",
		},
		{
			name:           "unicode characters",
			commitment:     "0x你好世界",
			expectedStatus: http.StatusBadRequest,
			errorContains:  "invalid commitment",
		},
		{
			name:           "extremely long commitment",
			commitment:     "0x" + strings.Repeat("ab", 10000),
			expectedStatus: http.StatusNotFound,
			errorContains:  "not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/get/"+tt.commitment, nil)
			w := httptest.NewRecorder()

			server.HandleGet(w, req)

			assert.Equal(t, tt.expectedStatus, w.Code, "status code mismatch")

			if tt.errorContains != "" {
				assert.Contains(t, w.Body.String(), tt.errorContains, "error message should contain expected text")
			}
		})
	}
}

// TestHTTP_ReadOnlyMode tests PUT rejection in read-only mode
func TestHTTP_ReadOnlyMode(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	namespace := createTestNamespace(t)
	batchCfg := batch.DefaultConfig()
	workerCfg := worker.DefaultConfig()
	workerCfg.ReadOnly = true

	server := &CelestiaServer{
		store:       store,
		namespace:   namespace,
		batchCfg:    batchCfg,
		workerCfg:   workerCfg,
		log:         log.New(),
	}

	req := httptest.NewRequest(http.MethodPost, "/put", bytes.NewReader([]byte("test data")))
	w := httptest.NewRecorder()

	server.HandlePut(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code, "should reject PUT in read-only mode")
	assert.Contains(t, w.Body.String(), "read-only mode", "error should mention read-only mode")
}

// Helper functions

func setupTestDB(t *testing.T) (*db.BlobStore, func()) {
	store, err := db.NewBlobStore(":memory:")
	require.NoError(t, err)
	return store, func() { store.Close() }
}

func createTestNamespace(t *testing.T) libshare.Namespace {
	// Create a valid namespace (29 bytes, version 0)
	// Format: [version_byte (1)][18_leading_zeros][10_byte_id]
	nsBytes := []byte{
		0x00, // version byte
		// 18 leading zeros (required for version 0)
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		// 10 bytes of actual ID
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
	}
	ns, err := libshare.NewNamespaceFromBytes(nsBytes)
	require.NoError(t, err)
	return ns
}
