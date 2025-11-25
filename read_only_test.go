package celestia

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/ethereum/go-ethereum/log"

	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/worker"
)

func TestReadOnlyMode_BlocksPUT(t *testing.T) {
	// Setup server in read-only mode
	tmpFile, err := os.CreateTemp("", "test-readonly-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	store, err := db.NewBlobStore(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	nsBytes := []byte{
		0x00, // version byte
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
	}
	namespace, err := libshare.NewNamespaceFromBytes(nsBytes)
	if err != nil {
		t.Fatalf("Failed to create namespace: %v", err)
	}

	logger := log.NewLogger(log.DiscardHandler())

	// Create worker config with ReadOnly enabled
	workerCfg := worker.DefaultConfig()
	workerCfg.ReadOnly = true

	// Create mock celestia store with test signer
	testSignerAddr := make([]byte, 20)
	for i := range testSignerAddr { testSignerAddr[i] = byte(i + 1) }
	celestiaStore := &CelestiaStore{Log: logger, Namespace: namespace, SignerAddr: testSignerAddr}

	server := &CelestiaServer{
		log:           logger,
		store:         store,
		namespace:     namespace,
		celestiaStore: celestiaStore,
		batchCfg:      batch.DefaultConfig(),
		workerCfg:     workerCfg,
	}

	// Try to PUT data
	testData := []byte("test blob data for PUT request")
	req := httptest.NewRequest("PUT", "/put/", bytes.NewReader(testData))
	w := httptest.NewRecorder()

	server.HandlePut(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	// Should return 403 Forbidden
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Expected status 403 Forbidden for PUT in read-only mode, got %d", resp.StatusCode)
	}
}

func TestReadOnlyMode_AllowsGET(t *testing.T) {
	// Setup server in read-only mode
	tmpFile, err := os.CreateTemp("", "test-readonly-get-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	store, err := db.NewBlobStore(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	nsBytes := []byte{
		0x00, // version byte
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
	}
	namespace, err := libshare.NewNamespaceFromBytes(nsBytes)
	if err != nil {
		t.Fatalf("Failed to create namespace: %v", err)
	}

	logger := log.NewLogger(log.DiscardHandler())

	// Create worker config with ReadOnly enabled
	workerCfg := worker.DefaultConfig()
	workerCfg.ReadOnly = true

	// Create mock celestia store with test signer
	testSignerAddr := make([]byte, 20)
	for i := range testSignerAddr { testSignerAddr[i] = byte(i + 1) }
	celestiaStore := &CelestiaStore{Log: logger, Namespace: namespace, SignerAddr: testSignerAddr}

	server := &CelestiaServer{
		log:           logger,
		store:         store,
		namespace:     namespace,
		celestiaStore: celestiaStore,
		batchCfg:      batch.DefaultConfig(),
		workerCfg:     workerCfg,
	}

	// Try to GET data (should work even in read-only mode, though will return 404 for non-existent data)
	req := httptest.NewRequest("GET", "/get/0102030405060708090a0b0c0d0e0f10", nil)
	w := httptest.NewRecorder()

	server.HandleGet(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	// Should return 404 (not found) not 403 (forbidden)
	// This shows that GET is allowed in read-only mode
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("GET request should be allowed in read-only mode, got 403 Forbidden")
	}
}

func TestNormalMode_AllowsPUT(t *testing.T) {
	// Setup server in normal mode (not read-only)
	tmpFile, err := os.CreateTemp("", "test-normal-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	store, err := db.NewBlobStore(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	nsBytes := []byte{
		0x00, // version byte
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
	}
	namespace, err := libshare.NewNamespaceFromBytes(nsBytes)
	if err != nil {
		t.Fatalf("Failed to create namespace: %v", err)
	}

	logger := log.NewLogger(log.DiscardHandler())

	// Create worker config with ReadOnly = false (default)
	workerCfg := worker.DefaultConfig()
	workerCfg.ReadOnly = false

	// Create mock celestia store with test signer
	testSignerAddr := make([]byte, 20)
	for i := range testSignerAddr { testSignerAddr[i] = byte(i + 1) }
	celestiaStore := &CelestiaStore{Log: logger, Namespace: namespace, SignerAddr: testSignerAddr}

	server := &CelestiaServer{
		log:           logger,
		store:         store,
		namespace:     namespace,
		celestiaStore: celestiaStore,
		batchCfg:      batch.DefaultConfig(),
		workerCfg:     workerCfg,
	}

	// Try to PUT data
	testData := []byte("test blob data for PUT request")
	req := httptest.NewRequest("PUT", "/put/", bytes.NewReader(testData))
	w := httptest.NewRecorder()

	server.HandlePut(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	// Should return 200 OK (or at least not 403)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("PUT request should be allowed in normal mode, got 403 Forbidden")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200 OK for PUT in normal mode, got %d", resp.StatusCode)
	}
}
