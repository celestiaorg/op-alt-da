package db

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestFIFOOrdering verifies that GetPendingBlobs returns blobs in strict FIFO order (ORDER BY id ASC)
func TestFIFOOrdering(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Insert 50 blobs with predictable IDs
	expectedIDs := make([]int64, 50)
	for i := 0; i < 50; i++ {
		testBlob := &Blob{
			Commitment: []byte(fmt.Sprintf("commitment-%03d", i)),
			Namespace:  []byte("test-namespace"),
			Data:       []byte(fmt.Sprintf("blob-data-%03d", i)),
			Size:       20,
			Status:     "pending_submission",
		}

		blobID, err := store.InsertBlob(ctx, testBlob)
		if err != nil {
			t.Fatalf("Failed to insert blob %d: %v", i, err)
		}

		expectedIDs[i] = blobID

		// Small delay to ensure different timestamps
		time.Sleep(1 * time.Millisecond)
	}

	// Retrieve all pending blobs
	pending, err := store.GetPendingBlobs(ctx, 100)
	if err != nil {
		t.Fatalf("GetPendingBlobs failed: %v", err)
	}

	if len(pending) != 50 {
		t.Fatalf("Expected 50 pending blobs, got %d", len(pending))
	}

	// Verify strict FIFO ordering (IDs should be ascending)
	for i := 0; i < len(pending); i++ {
		if pending[i].ID != expectedIDs[i] {
			t.Errorf("FIFO violation at position %d: expected ID %d, got ID %d",
				i, expectedIDs[i], pending[i].ID)
		}

		// Verify IDs are strictly ascending
		if i > 0 && pending[i].ID <= pending[i-1].ID {
			t.Errorf("IDs not strictly ascending: blob[%d].ID=%d, blob[%d].ID=%d",
				i-1, pending[i-1].ID, i, pending[i].ID)
		}
	}

	t.Logf("✓ FIFO ordering verified: 50 blobs in correct insertion order")
}

// TestFIFOOrderingWithLimit verifies FIFO order is maintained with LIMIT
func TestFIFOOrderingWithLimit(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Insert 30 blobs
	var firstBlobID int64
	for i := 0; i < 30; i++ {
		testBlob := &Blob{
			Commitment: []byte(fmt.Sprintf("commit-%d", i)),
			Namespace:  []byte("ns"),
			Data:       []byte(fmt.Sprintf("data-%d", i)),
			Size:       10,
			Status:     "pending_submission",
		}

		blobID, err := store.InsertBlob(ctx, testBlob)
		if err != nil {
			t.Fatalf("Insert failed: %v", err)
		}

		if i == 0 {
			firstBlobID = blobID
		}
	}

	// Get first 10 blobs
	firstBatch, err := store.GetPendingBlobs(ctx, 10)
	if err != nil {
		t.Fatalf("GetPendingBlobs failed: %v", err)
	}

	if len(firstBatch) != 10 {
		t.Fatalf("Expected 10 blobs, got %d", len(firstBatch))
	}

	// First blob should have the smallest ID
	if firstBatch[0].ID != firstBlobID {
		t.Errorf("Expected first blob ID %d, got %d", firstBlobID, firstBatch[0].ID)
	}

	// Verify ascending order within batch
	for i := 1; i < len(firstBatch); i++ {
		if firstBatch[i].ID <= firstBatch[i-1].ID {
			t.Errorf("Batch not in FIFO order: blob[%d].ID=%d, blob[%d].ID=%d",
				i-1, firstBatch[i-1].ID, i, firstBatch[i].ID)
		}
	}

	// Get next 10 blobs
	secondBatch, err := store.GetPendingBlobs(ctx, 10)
	if err != nil {
		t.Fatalf("GetPendingBlobs failed: %v", err)
	}

	// Second batch should have same blobs (they're still pending)
	if len(secondBatch) != 10 {
		t.Fatalf("Expected 10 blobs in second batch, got %d", len(secondBatch))
	}

	// Should get the same blobs since they're still pending
	for i := 0; i < 10; i++ {
		if secondBatch[i].ID != firstBatch[i].ID {
			t.Errorf("Second batch blob %d mismatch: expected ID %d, got %d",
				i, firstBatch[i].ID, secondBatch[i].ID)
		}
	}

	t.Logf("✓ FIFO ordering with LIMIT verified")
}

// TestFIFOAfterBatching verifies FIFO is maintained after some blobs are batched
func TestFIFOAfterBatching(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Insert 20 blobs
	allIDs := make([]int64, 20)
	for i := 0; i < 20; i++ {
		testBlob := &Blob{
			Commitment: []byte(fmt.Sprintf("c-%d", i)),
			Namespace:  []byte("ns"),
			Data:       []byte(fmt.Sprintf("d-%d", i)),
			Size:       5,
			Status:     "pending_submission",
		}

		id, err := store.InsertBlob(ctx, testBlob)
		if err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
		allIDs[i] = id
	}

	// Batch the first 10 blobs
	_, err := store.CreateBatch(ctx, allIDs[0:10], []byte("batch-1"), []byte("packed-data"))
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	// Get pending blobs (should only return the last 10)
	pending, err := store.GetPendingBlobs(ctx, 20)
	if err != nil {
		t.Fatalf("GetPendingBlobs failed: %v", err)
	}

	if len(pending) != 10 {
		t.Fatalf("Expected 10 pending blobs, got %d", len(pending))
	}

	// Verify these are blobs 10-19 in FIFO order
	for i := 0; i < len(pending); i++ {
		expectedID := allIDs[10+i]
		if pending[i].ID != expectedID {
			t.Errorf("FIFO violation after batching: position %d expected ID %d, got %d",
				i, expectedID, pending[i].ID)
		}
	}

	t.Logf("✓ FIFO ordering maintained after batching")
}

