package celestia

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	libshare "github.com/celestiaorg/go-square/v3/share"

	"github.com/celestiaorg/op-alt-da/commitment"
	"github.com/celestiaorg/op-alt-da/db"
)

func setupConcurrencyTest(t *testing.T) (*db.BlobStore, libshare.Namespace, func()) {
	tmpFile, err := os.CreateTemp("", "test-concurrency-*.db")
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

	cleanup := func() {
		store.Close()
		os.Remove(tmpFile.Name())
	}

	return store, namespace, cleanup
}

// TestConcurrentInserts tests many concurrent blob insertions
func TestConcurrentInserts(t *testing.T) {
	store, namespace, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()
	numGoroutines := 100
	blobsPerGoroutine := 10

	var wg sync.WaitGroup
	errChan := make(chan error, numGoroutines*blobsPerGoroutine)

	startTime := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()

			for j := 0; j < blobsPerGoroutine; j++ {
				data := []byte(fmt.Sprintf("routine-%d-blob-%d", routineID, j))

				// Compute commitment
				comm, err := commitment.ComputeCommitment(data, namespace, make([]byte, 20))
				if err != nil {
					errChan <- fmt.Errorf("compute commitment: %w", err)
					continue
				}

				blob := &db.Blob{
					Commitment: comm,
					Namespace:  namespace.Bytes(),
					Data:       data,
					Size:       len(data),
					Status:     "pending_submission",
				}

				_, err = store.InsertBlob(ctx, blob)
				if err != nil {
					errChan <- fmt.Errorf("insert blob: %w", err)
				}
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	duration := time.Since(startTime)
	t.Logf("Inserted %d blobs in %v (%d ops/sec)",
		numGoroutines*blobsPerGoroutine,
		duration,
		int(float64(numGoroutines*blobsPerGoroutine)/duration.Seconds()))

	// Check for errors
	errorCount := 0
	for err := range errChan {
		errorCount++
		t.Errorf("Concurrent insert error: %v", err)
	}

	if errorCount > 0 {
		t.Fatalf("Got %d errors during concurrent inserts", errorCount)
	}

	// Verify count
	pending, err := store.GetPendingBlobs(ctx, numGoroutines*blobsPerGoroutine+10)
	if err != nil {
		t.Fatalf("GetPendingBlobs failed: %v", err)
	}

	if len(pending) != numGoroutines*blobsPerGoroutine {
		t.Errorf("Expected %d blobs, got %d", numGoroutines*blobsPerGoroutine, len(pending))
	}
}

// TestConcurrentReads tests many concurrent blob reads
func TestConcurrentReads(t *testing.T) {
	store, namespace, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()

	// Insert 100 blobs first (will be in pending_submission status)
	commitments := make([][]byte, 100)
	for i := 0; i < 100; i++ {
		data := []byte(fmt.Sprintf("blob-%d-data", i))
		comm, _ := commitment.ComputeCommitment(data, namespace, make([]byte, 20))

		blob := &db.Blob{
			Commitment: comm,
			Namespace:  namespace.Bytes(),
			Data:       data,
			Size:       len(data),
		}

		_, err := store.InsertBlob(ctx, blob)
		if err != nil {
			t.Fatalf("InsertBlob failed: %v", err)
		}
		commitments[i] = comm
	}

	// Now read them concurrently
	numGoroutines := 50
	readsPerGoroutine := 20

	var wg sync.WaitGroup
	errChan := make(chan error, numGoroutines*readsPerGoroutine)

	startTime := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()

			for j := 0; j < readsPerGoroutine; j++ {
				// Read random blob
				idx := (routineID*readsPerGoroutine + j) % len(commitments)
				comm := commitments[idx]

				_, err := store.GetBlobByCommitment(ctx, comm)
				if err != nil {
					errChan <- fmt.Errorf("get blob: %w", err)
					continue
				}

				// Successfully read blob - no error
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	duration := time.Since(startTime)
	t.Logf("Read %d blobs in %v (%d ops/sec)",
		numGoroutines*readsPerGoroutine,
		duration,
		int(float64(numGoroutines*readsPerGoroutine)/duration.Seconds()))

	// Check for errors
	errorCount := 0
	for err := range errChan {
		errorCount++
		t.Errorf("Concurrent read error: %v", err)
	}

	if errorCount > 0 {
		t.Fatalf("Got %d errors during concurrent reads", errorCount)
	}
}

// TestConcurrentMixedOps tests concurrent reads and writes
func TestConcurrentMixedOps(t *testing.T) {
	store, namespace, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()

	// Pre-populate with some blobs
	existingCommitments := make([][]byte, 50)
	for i := 0; i < 50; i++ {
		data := []byte(fmt.Sprintf("existing-blob-%d", i))
		comm, _ := commitment.ComputeCommitment(data, namespace, make([]byte, 20))

		blob := &db.Blob{
			Commitment: comm,
			Namespace:  namespace.Bytes(),
			Data:       data,
			Size:       len(data),
			Status:     "confirmed",
		}

		_, err := store.InsertBlob(ctx, blob)
		if err != nil {
			t.Fatalf("InsertBlob failed: %v", err)
		}
		existingCommitments[i] = comm
	}

	var wg sync.WaitGroup
	errChan := make(chan error, 200)

	startTime := time.Now()

	// Writer goroutines
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()

			for j := 0; j < 10; j++ {
				data := []byte(fmt.Sprintf("new-blob-%d-%d", routineID, j))
				comm, err := commitment.ComputeCommitment(data, namespace, make([]byte, 20))
				if err != nil {
					errChan <- err
					continue
				}

				blob := &db.Blob{
					Commitment: comm,
					Namespace:  namespace.Bytes(),
					Data:       data,
					Size:       len(data),
					Status:     "pending_submission",
				}

				_, err = store.InsertBlob(ctx, blob)
				if err != nil {
					errChan <- err
				}
			}
		}(i)
	}

	// Reader goroutines
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()

			for j := 0; j < 10; j++ {
				idx := (routineID*10 + j) % len(existingCommitments)
				comm := existingCommitments[idx]

				_, err := store.GetBlobByCommitment(ctx, comm)
				if err != nil {
					errChan <- err
				}
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	duration := time.Since(startTime)
	t.Logf("Mixed ops completed in %v", duration)

	// Check for errors
	errorCount := 0
	for err := range errChan {
		errorCount++
		t.Errorf("Concurrent operation error: %v", err)
	}

	if errorCount > 0 {
		t.Fatalf("Got %d errors during mixed operations", errorCount)
	}
}

// TestConcurrentBatchCreation tests concurrent batch creation
func TestConcurrentBatchCreation(t *testing.T) {
	store, namespace, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()

	// Insert many blobs
	numBlobs := 100
	blobIDs := make([]int64, numBlobs)
	for i := 0; i < numBlobs; i++ {
		data := []byte(fmt.Sprintf("blob-%d", i))
		comm, _ := commitment.ComputeCommitment(data, namespace, make([]byte, 20))

		blob := &db.Blob{
			Commitment: comm,
			Namespace:  namespace.Bytes(),
			Data:       data,
			Size:       len(data),
			Status:     "pending_submission",
		}

		id, err := store.InsertBlob(ctx, blob)
		if err != nil {
			t.Fatalf("InsertBlob failed: %v", err)
		}
		blobIDs[i] = id
	}

	// Create batches concurrently (10 blobs per batch)
	numBatches := 10
	var wg sync.WaitGroup
	errChan := make(chan error, numBatches)

	for i := 0; i < numBatches; i++ {
		wg.Add(1)
		go func(batchNum int) {
			defer wg.Done()

			// Get 10 blob IDs for this batch
			start := batchNum * 10
			end := start + 10
			batchBlobIDs := blobIDs[start:end]

			batchCommitment := []byte(fmt.Sprintf("batch-%d-commitment", batchNum))
			batchData := []byte(fmt.Sprintf("batch-%d-data", batchNum))

			_, err := store.CreateBatch(ctx, batchBlobIDs, batchCommitment, batchData)
			if err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	errorCount := 0
	for err := range errChan {
		errorCount++
		t.Errorf("Concurrent batch creation error: %v", err)
	}

	if errorCount > 0 {
		t.Fatalf("Got %d errors during concurrent batch creation", errorCount)
	}

	// Verify all blobs are batched
	pending, err := store.GetPendingBlobs(ctx, 200)
	if err != nil {
		t.Fatalf("GetPendingBlobs failed: %v", err)
	}

	if len(pending) != 0 {
		t.Errorf("Expected 0 pending blobs, got %d (all should be batched)", len(pending))
	}
}

// TestConcurrentCommitmentComputation tests concurrent commitment computation
func TestConcurrentCommitmentComputation(t *testing.T) {
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

	numGoroutines := 50
	computationsPerGoroutine := 20

	var wg sync.WaitGroup
	errChan := make(chan error, numGoroutines*computationsPerGoroutine)

	startTime := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()

			for j := 0; j < computationsPerGoroutine; j++ {
				data := []byte(fmt.Sprintf("data-%d-%d", routineID, j))

				comm, err := commitment.ComputeCommitment(data, namespace, make([]byte, 20))
				if err != nil {
					errChan <- err
					continue
				}

				if len(comm) == 0 {
					errChan <- fmt.Errorf("empty commitment")
				}
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	duration := time.Since(startTime)
	t.Logf("Computed %d commitments in %v (%d ops/sec)",
		numGoroutines*computationsPerGoroutine,
		duration,
		int(float64(numGoroutines*computationsPerGoroutine)/duration.Seconds()))

	// Check for errors
	errorCount := 0
	for err := range errChan {
		errorCount++
		t.Errorf("Concurrent commitment error: %v", err)
	}

	if errorCount > 0 {
		t.Fatalf("Got %d errors during concurrent commitment computation", errorCount)
	}
}

// TestRaceConditions uses race detector to find data races
func TestRaceConditions(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping race condition test in short mode")
	}

	store, namespace, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()

	// Shared data
	sharedCommitments := make([][]byte, 0)
	var mu sync.Mutex

	var wg sync.WaitGroup

	// Writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for j := 0; j < 10; j++ {
				data := []byte(fmt.Sprintf("data-%d-%d", id, j))
				comm, _ := commitment.ComputeCommitment(data, namespace, make([]byte, 20))

				blob := &db.Blob{
					Commitment: comm,
					Namespace:  namespace.Bytes(),
					Data:       data,
					Size:       len(data),
					Status:     "pending_submission",
				}

				store.InsertBlob(ctx, blob)

				mu.Lock()
				sharedCommitments = append(sharedCommitments, comm)
				mu.Unlock()
			}
		}(i)
	}

	// Readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < 20; j++ {
				mu.Lock()
				if len(sharedCommitments) > 0 {
					comm := sharedCommitments[len(sharedCommitments)-1]
					mu.Unlock()

					store.GetBlobByCommitment(ctx, comm)
				} else {
					mu.Unlock()
				}

				time.Sleep(1 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
}

// TestStressLoad performs a stress test with high load
func TestStressLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	store, namespace, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()

	numGoroutines := 200
	opsPerGoroutine := 100

	var wg sync.WaitGroup
	var successCount, errorCount int64
	var mu sync.Mutex

	startTime := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()

			for j := 0; j < opsPerGoroutine; j++ {
				data := []byte(fmt.Sprintf("stress-%d-%d", routineID, j))
				comm, err := commitment.ComputeCommitment(data, namespace, make([]byte, 20))
				if err != nil {
					mu.Lock()
					errorCount++
					mu.Unlock()
					continue
				}

				blob := &db.Blob{
					Commitment: comm,
					Namespace:  namespace.Bytes(),
					Data:       data,
					Size:       len(data),
					Status:     "pending_submission",
				}

				_, err = store.InsertBlob(ctx, blob)
				if err != nil {
					mu.Lock()
					errorCount++
					mu.Unlock()
				} else {
					mu.Lock()
					successCount++
					mu.Unlock()
				}
			}
		}(i)
	}

	wg.Wait()

	duration := time.Since(startTime)

	t.Logf("Stress test: %d goroutines, %d ops each", numGoroutines, opsPerGoroutine)
	t.Logf("Completed in %v", duration)
	t.Logf("Success: %d, Errors: %d", successCount, errorCount)
	t.Logf("Throughput: %d ops/sec", int(float64(successCount)/duration.Seconds()))

	if errorCount > 0 {
		t.Errorf("Got %d errors during stress test", errorCount)
	}

	expectedOps := int64(numGoroutines * opsPerGoroutine)
	if successCount != expectedOps {
		t.Errorf("Expected %d successful ops, got %d", expectedOps, successCount)
	}
}

// TestConcurrentStatsAccess tests concurrent access to stats
func TestConcurrentStatsAccess(t *testing.T) {
	store, namespace, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()

	// Insert some blobs
	for i := 0; i < 50; i++ {
		data := []byte(fmt.Sprintf("blob-%d", i))
		comm, _ := commitment.ComputeCommitment(data, namespace, make([]byte, 20))

		blob := &db.Blob{
			Commitment: comm,
			Namespace:  namespace.Bytes(),
			Data:       data,
			Size:       len(data),
			Status:     "pending_submission",
		}

		store.InsertBlob(ctx, blob)
	}

	// Concurrently access stats
	var wg sync.WaitGroup
	errChan := make(chan error, 100)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			stats, err := store.GetStats(ctx)
			if err != nil {
				errChan <- err
				return
			}

			if stats["blob_counts"] == nil {
				errChan <- fmt.Errorf("missing blob_counts in stats")
			}
		}()
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	for err := range errChan {
		t.Errorf("Stats access error: %v", err)
	}
}

// TestConcurrentDuplicateInserts tests handling of duplicate commitment inserts
func TestConcurrentDuplicateInserts(t *testing.T) {
	store, namespace, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()

	// Same data (will produce same commitment)
	data := []byte("duplicate-data")
	comm, _ := commitment.ComputeCommitment(data, namespace, make([]byte, 20))

	var wg sync.WaitGroup
	successChan := make(chan bool, 10)
	errorChan := make(chan error, 10)

	// Try to insert same blob 10 times concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			blob := &db.Blob{
				Commitment: comm,
				Namespace:  namespace.Bytes(),
				Data:       data,
				Size:       len(data),
				Status:     "pending_submission",
			}

			_, err := store.InsertBlob(ctx, blob)
			if err != nil {
				errorChan <- err
			} else {
				successChan <- true
			}
		}()
	}

	wg.Wait()
	close(successChan)
	close(errorChan)

	successCount := len(successChan)
	errorCount := len(errorChan)

	t.Logf("Duplicate inserts: %d success, %d errors", successCount, errorCount)

	// Exactly one should succeed (UNIQUE constraint on commitment)
	if successCount != 1 {
		t.Errorf("Expected 1 successful insert, got %d", successCount)
	}

	if errorCount != 9 {
		t.Logf("Expected 9 errors (duplicate key), got %d", errorCount)
	}
}

// TestDataIntegrity verifies data is not corrupted under concurrent load
func TestDataIntegrity(t *testing.T) {
	store, namespace, cleanup := setupConcurrencyTest(t)
	defer cleanup()

	ctx := context.Background()

	// Insert known data concurrently
	numBlobs := 100
	expectedData := make(map[string][]byte)
	var dataMu sync.Mutex

	var wg sync.WaitGroup

	for i := 0; i < numBlobs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			data := []byte(fmt.Sprintf("integrity-test-data-%d", idx))
			comm, _ := commitment.ComputeCommitment(data, namespace, make([]byte, 20))

			blob := &db.Blob{
				Commitment: comm,
				Namespace:  namespace.Bytes(),
				Data:       data,
				Size:       len(data),
				Status:     "confirmed",
			}

			_, err := store.InsertBlob(ctx, blob)
			if err == nil {
				dataMu.Lock()
				expectedData[string(comm)] = data
				dataMu.Unlock()
			}
		}(i)
	}

	wg.Wait()

	// Verify all data is intact
	dataMu.Lock()
	commitments := make([][]byte, 0, len(expectedData))
	for commStr := range expectedData {
		commitments = append(commitments, []byte(commStr))
	}
	dataMu.Unlock()

	// Read back concurrently and verify
	errChan := make(chan error, len(commitments))

	for _, comm := range commitments {
		wg.Add(1)
		go func(commitment []byte) {
			defer wg.Done()

			blob, err := store.GetBlobByCommitment(ctx, commitment)
			if err != nil {
				errChan <- err
				return
			}

			dataMu.Lock()
			expected := expectedData[string(commitment)]
			dataMu.Unlock()

			if !bytes.Equal(blob.Data, expected) {
				errChan <- fmt.Errorf("data mismatch for commitment %x", commitment[:8])
			}
		}(comm)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	for err := range errChan {
		t.Errorf("Data integrity error: %v", err)
	}
}
