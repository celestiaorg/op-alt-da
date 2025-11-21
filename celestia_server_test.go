package celestia

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	libshare "github.com/celestiaorg/go-square/v3/share"
	altda "github.com/ethereum-optimism/optimism/op-alt-da"
	"github.com/ethereum/go-ethereum/log"

	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/commitment"
	"github.com/celestiaorg/op-alt-da/db"
)

func setupServerTest(t *testing.T) (*CelestiaServer, *db.BlobStore, func()) {
	tmpFile, err := os.CreateTemp("", "test-server-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()

	store, err := db.NewBlobStore(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("Failed to create store: %v", err)
	}

	nsBytes := []byte{
		0x00, // version byte
		// 18 leading zeros (required for version 0)
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		// 10 bytes of actual ID
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
	}
	namespace, err := libshare.NewNamespaceFromBytes(nsBytes)
	if err != nil {
		t.Fatalf("Failed to create namespace: %v", err)
	}

	logger := log.NewLogger(log.DiscardHandler())

	server := &CelestiaServer{
		log:       logger,
		store:     store,
		namespace: namespace,
		batchCfg:  batch.DefaultConfig(),
	}

	cleanup := func() {
		store.Close()
		os.Remove(tmpFile.Name())
	}

	return server, store, cleanup
}

// TestHandlePut tests the PUT endpoint
func TestHandlePut(t *testing.T) {
	server, _, cleanup := setupServerTest(t)
	defer cleanup()

	testData := []byte("test blob data for PUT request")

	req := httptest.NewRequest("PUT", "/put/", bytes.NewReader(testData))
	w := httptest.NewRecorder()

	server.HandlePut(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)

	// Response should be binary GenericCommitment format
	// Expected: [commitment_type_byte][version_byte][blob_commitment]
	if len(body) < 34 { // 1 + 1 + 32 minimum
		t.Errorf("Response too short: got %d bytes, expected at least 34", len(body))
	}

	// Check version byte is at position 1 (after commitment type byte)
	if body[1] != 0x0c {
		t.Errorf("Expected version byte 0x0c at position 1, got 0x%02x", body[1])
	}

	t.Logf("PUT response (hex): %x", body)
	t.Logf("PUT response length: %d bytes", len(body))
}

// TestHandleGet tests the GET endpoint
func TestHandleGet(t *testing.T) {
	server, store, cleanup := setupServerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Insert blob first
	testData := []byte("test blob data for GET request")
	comm, err := commitment.ComputeCommitment(testData, server.namespace)
	if err != nil {
		t.Fatalf("ComputeCommitment failed: %v", err)
	}

	blob := &db.Blob{
		Commitment: comm,
		Namespace:  server.namespace.Bytes(),
		Data:       testData,
		Size:       len(testData),
		Status:     "confirmed",
	}

	_, err = store.InsertBlob(ctx, blob)
	if err != nil {
		t.Fatalf("InsertBlob failed: %v", err)
	}

	// GET request with version byte
	commitmentHex := hex.EncodeToString(comm)
	url := fmt.Sprintf("/get/0x0c%s", commitmentHex)

	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()

	server.HandleGet(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)

	if !bytes.Equal(body, testData) {
		t.Errorf("Data mismatch: expected %s, got %s", testData, body)
	}
}

// TestHandleGet_WithoutVersionByte tests GET with commitment without version byte
func TestHandleGet_WithoutVersionByte(t *testing.T) {
	server, store, cleanup := setupServerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Insert blob
	testData := []byte("test data without version byte")
	comm, _ := commitment.ComputeCommitment(testData, server.namespace)

	blob := &db.Blob{
		Commitment: comm,
		Namespace:  server.namespace.Bytes(),
		Data:       testData,
		Size:       len(testData),
		Status:     "confirmed",
	}

	store.InsertBlob(ctx, blob)

	// GET without version byte prefix
	commitmentHex := hex.EncodeToString(comm)
	url := fmt.Sprintf("/get/%s", commitmentHex)

	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()

	server.HandleGet(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)

	if !bytes.Equal(body, testData) {
		t.Errorf("Data mismatch without version byte")
	}
}

// TestHandleGet_NotFound tests GET for non-existent blob
func TestHandleGet_NotFound(t *testing.T) {
	server, _, cleanup := setupServerTest(t)
	defer cleanup()

	// Request non-existent commitment
	fakeCommitment := "0x0c" + strings.Repeat("aa", 32)
	url := fmt.Sprintf("/get/%s", fakeCommitment)

	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()

	server.HandleGet(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", resp.StatusCode)
	}
}

// TestHandleGet_InvalidCommitment tests GET with invalid commitment format
func TestHandleGet_InvalidCommitment(t *testing.T) {
	server, _, cleanup := setupServerTest(t)
	defer cleanup()

	testCases := []struct {
		name string
		url  string
	}{
		{"not hex", "/get/notahexstring"},
		{"odd length", "/get/0x0cabc"},
		{"empty", "/get/"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.url, nil)
			w := httptest.NewRecorder()

			server.HandleGet(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("Expected status 400, got %d for %s", resp.StatusCode, tc.name)
			}
		})
	}
}

// TestHandlePut_EmptyData tests PUT with empty data
func TestHandlePut_EmptyData(t *testing.T) {
	server, _, cleanup := setupServerTest(t)
	defer cleanup()

	req := httptest.NewRequest("PUT", "/put/", bytes.NewReader([]byte{}))
	w := httptest.NewRecorder()

	server.HandlePut(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("Expected status 400 for empty data, got %d", resp.StatusCode)
	}
}

// TestHandleHealth tests the health endpoint
func TestHandleHealth(t *testing.T) {
	server, _, cleanup := setupServerTest(t)
	defer cleanup()

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	server.HandleHealth(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "OK" {
		t.Errorf("Expected 'OK', got %s", body)
	}
}

// TestHandleStats tests the stats endpoint
func TestHandleStats(t *testing.T) {
	server, store, cleanup := setupServerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Insert some blobs with different statuses
	statuses := []string{"pending_submission", "batched", "confirmed"}
	for i, status := range statuses {
		data := []byte(fmt.Sprintf("blob-%d", i))
		comm, _ := commitment.ComputeCommitment(data, server.namespace)

		blob := &db.Blob{
			Commitment: comm,
			Namespace:  server.namespace.Bytes(),
			Data:       data,
			Size:       len(data),
			Status:     status,
		}

		store.InsertBlob(ctx, blob)
	}

	req := httptest.NewRequest("GET", "/stats", nil)
	w := httptest.NewRecorder()

	server.HandleStats(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	// Should contain JSON with counts
	if !strings.Contains(bodyStr, "blob_counts") {
		t.Errorf("Stats should contain 'blob_counts', got: %s", bodyStr)
	}

	if !strings.Contains(bodyStr, "batch_counts") {
		t.Errorf("Stats should contain 'batch_counts', got: %s", bodyStr)
	}

	t.Logf("Stats response: %s", bodyStr)
}

// TestPutGetRoundTrip tests a full PUT then GET workflow
func TestPutGetRoundTrip(t *testing.T) {
	server, store, cleanup := setupServerTest(t)
	defer cleanup()

	testData := []byte("round trip test data")

	// PUT request
	putReq := httptest.NewRequest("PUT", "/put/", bytes.NewReader(testData))
	putW := httptest.NewRecorder()

	server.HandlePut(putW, putReq)

	putResp := putW.Result()
	defer putResp.Body.Close()

	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT failed with status %d", putResp.StatusCode)
	}

	// Get commitment from response (binary bytes)
	putBody, _ := io.ReadAll(putResp.Body)

	t.Logf("PUT returned commitment (hex): %x", putBody)
	t.Logf("PUT returned commitment length: %d", len(putBody))

	// Decode what was returned to see the blob commitment
	comm, err := altda.DecodeCommitmentData(putBody)
	if err == nil {
		txData := comm.TxData()
		t.Logf("TxData from commitment: %x (len=%d)", txData, len(txData))
		if len(txData) > 1 {
			t.Logf("Blob commitment (after stripping version): %x (len=%d)", txData[1:], len(txData)-1)
		}
	} else {
		t.Logf("Failed to decode as GenericCommitment: %v", err)
	}

	// Check what's actually in the DB
	blobs, _ := store.GetPendingBlobs(context.Background(), 10)
	if len(blobs) > 0 {
		t.Logf("DB has blob with commitment: %x (len=%d)", blobs[0].Commitment, len(blobs[0].Commitment))
	}

	// Hex-encode commitment for URL path
	commitmentHex := hex.EncodeToString(putBody)

	t.Logf("GET URL: /get/%s", commitmentHex)

	// GET request using the hex-encoded commitment
	getURL := fmt.Sprintf("/get/%s", commitmentHex)
	getReq := httptest.NewRequest("GET", getURL, nil)
	getW := httptest.NewRecorder()

	server.HandleGet(getW, getReq)

	getResp := getW.Result()
	defer getResp.Body.Close()

	if getResp.StatusCode != http.StatusOK {
		getBody, _ := io.ReadAll(getResp.Body)
		t.Fatalf("GET failed with status %d: %s", getResp.StatusCode, getBody)
	}

	// Verify data matches
	getBody, _ := io.ReadAll(getResp.Body)

	if !bytes.Equal(getBody, testData) {
		t.Errorf("Round trip data mismatch: sent %s, got %s", testData, getBody)
	}

	t.Logf("Round trip successful: %d bytes", len(testData))
}

// TestMultiplePutsSameData tests idempotent PUT behavior (same data = same commitment)
func TestMultiplePutsSameData(t *testing.T) {
	server, _, cleanup := setupServerTest(t)
	defer cleanup()

	testData := []byte("duplicate data")

	// First PUT
	req1 := httptest.NewRequest("PUT", "/put/", bytes.NewReader(testData))
	w1 := httptest.NewRecorder()
	server.HandlePut(w1, req1)

	resp1 := w1.Result()
	defer resp1.Body.Close()

	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("First PUT failed with status %d", resp1.StatusCode)
	}

	body1, _ := io.ReadAll(resp1.Body)

	// Second PUT (same data) - should succeed (idempotent)
	req2 := httptest.NewRequest("PUT", "/put/", bytes.NewReader(testData))
	w2 := httptest.NewRecorder()
	server.HandlePut(w2, req2)

	resp2 := w2.Result()
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("Second PUT (duplicate) should succeed with 200 OK (idempotent), got %d: %s",
			resp2.StatusCode, body)
	}

	body2, _ := io.ReadAll(resp2.Body)

	// Commitments should be identical (binary comparison)
	if !bytes.Equal(body1, body2) {
		t.Errorf("Same data produced different commitments: %x vs %x", body1, body2)
	}

	t.Logf("Idempotent PUT verified: both returned %x", body1)
}

// TestLargeBlobPut tests PUT with large blob
func TestLargeBlobPut(t *testing.T) {
	server, _, cleanup := setupServerTest(t)
	defer cleanup()

	// Create 100KB blob
	largeData := make([]byte, 100*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	req := httptest.NewRequest("PUT", "/put/", bytes.NewReader(largeData))
	w := httptest.NewRecorder()

	server.HandlePut(w, req)

	resp := w.Result()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Large blob PUT failed with status %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	t.Logf("Large blob (100KB) commitment: %s", body)
}

// TestConcurrentPutGet tests concurrent PUT and GET operations
func TestConcurrentPutGet(t *testing.T) {
	server, _, cleanup := setupServerTest(t)
	defer cleanup()

	numRequests := 20

	// Channel to collect commitments from PUTs (binary bytes)
	commitments := make(chan []byte, numRequests)
	errors := make(chan error, numRequests*2)

	// Concurrent PUTs
	for i := 0; i < numRequests; i++ {
		go func(id int) {
			data := []byte(fmt.Sprintf("concurrent-data-%d", id))
			req := httptest.NewRequest("PUT", "/put/", bytes.NewReader(data))
			w := httptest.NewRecorder()

			server.HandlePut(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				errors <- fmt.Errorf("PUT %d failed: %d", id, resp.StatusCode)
				return
			}

			body, _ := io.ReadAll(resp.Body)
			commitments <- body
		}(i)
	}

	// Wait for all PUTs to complete
	var collectedCommitments [][]byte
	for i := 0; i < numRequests; i++ {
		select {
		case comm := <-commitments:
			collectedCommitments = append(collectedCommitments, comm)
		case err := <-errors:
			t.Errorf("PUT error: %v", err)
		}
	}

	// Concurrent GETs
	for _, comm := range collectedCommitments {
		go func(commitment []byte) {
			// Hex-encode for URL
			commitmentHex := hex.EncodeToString(commitment)
			url := fmt.Sprintf("/get/%s", commitmentHex)
			req := httptest.NewRequest("GET", url, nil)
			w := httptest.NewRecorder()

			server.HandleGet(w, req)

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				errors <- fmt.Errorf("GET failed: %d", resp.StatusCode)
			}
		}(comm)
	}

	// Check for GET errors
	for i := 0; i < len(collectedCommitments); i++ {
		select {
		case err := <-errors:
			t.Errorf("GET error: %v", err)
		default:
			// No error
		}
	}

	t.Logf("Concurrent test: %d PUTs and GETs completed", numRequests)
}

// TestHandlerEdgeCases tests various edge cases
func TestHandlerEdgeCases(t *testing.T) {
	server, _, cleanup := setupServerTest(t)
	defer cleanup()

	testCases := []struct {
		name           string
		method         string
		url            string
		body           []byte
		expectedStatus int
	}{
		{"PUT single byte", "PUT", "/put/", []byte{0x42}, http.StatusOK},
		{"GET with 0x prefix", "GET", "/get/0x" + strings.Repeat("aa", 32), nil, http.StatusNotFound},
		{"GET without 0x prefix", "GET", "/get/" + strings.Repeat("bb", 32), nil, http.StatusNotFound},
		{"Health check", "GET", "/health", nil, http.StatusOK},
		{"Stats", "GET", "/stats", nil, http.StatusOK},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != nil {
				body = bytes.NewReader(tc.body)
			}

			req := httptest.NewRequest(tc.method, tc.url, body)
			w := httptest.NewRecorder()

			switch {
			case strings.HasPrefix(tc.url, "/put"):
				server.HandlePut(w, req)
			case strings.HasPrefix(tc.url, "/get"):
				server.HandleGet(w, req)
			case strings.HasPrefix(tc.url, "/health"):
				server.HandleHealth(w, req)
			case strings.HasPrefix(tc.url, "/stats"):
				server.HandleStats(w, req)
			}

			resp := w.Result()
			defer resp.Body.Close()

			if resp.StatusCode != tc.expectedStatus {
				t.Errorf("Expected status %d, got %d", tc.expectedStatus, resp.StatusCode)
			}
		})
	}
}
