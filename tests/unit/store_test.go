package unit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/celestiaorg/op-alt-da/db"
)

func setupTestDB(t *testing.T) (*db.BlobStore, func()) {
	// Create temporary database
	tmpFile, err := os.CreateTemp("", "test-blobs-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()

	store, err := db.NewBlobStore(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("Failed to create store: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.Remove(tmpFile.Name())
	}

	return store, cleanup
}

func TestBlobStore_InsertAndGet(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Insert blob
	blob := &db.Blob{
		Commitment: []byte("test-commitment-123"),
		Namespace:  []byte("test-namespace"),
		Data:       []byte("test data content"),
		Size:       17,
		Status:     "pending_submission",
	}

	blobID, err := store.InsertBlob(ctx, blob)
	if err != nil {
		t.Fatalf("InsertBlob failed: %v", err)
	}

	if blobID == 0 {
		t.Fatal("Expected non-zero blob ID")
	}

	// Get blob by commitment
	retrieved, err := store.GetBlobByCommitment(ctx, blob.Commitment)
	if err != nil {
		t.Fatalf("GetBlobByCommitment failed: %v", err)
	}

	if retrieved.ID != blobID {
		t.Errorf("Expected ID %d, got %d", blobID, retrieved.ID)
	}

	if string(retrieved.Data) != string(blob.Data) {
		t.Errorf("Expected data %q, got %q", blob.Data, retrieved.Data)
	}

	if retrieved.Status != "pending_submission" {
		t.Errorf("Expected status pending_submission, got %s", retrieved.Status)
	}
}

func TestBlobStore_GetPendingBlobs(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Insert multiple blobs
	for i := 0; i < 5; i++ {
		blob := &db.Blob{
			Commitment: []byte{byte(i)},
			Namespace:  []byte("test"),
			Data:       []byte{byte(i)},
			Size:       1,
			Status:     "pending_submission",
		}
		_, err := store.InsertBlob(ctx, blob)
		if err != nil {
			t.Fatalf("InsertBlob failed: %v", err)
		}
	}

	// Get pending blobs
	pending, err := store.GetPendingBlobs(ctx, 3)
	if err != nil {
		t.Fatalf("GetPendingBlobs failed: %v", err)
	}

	if len(pending) != 3 {
		t.Errorf("Expected 3 pending blobs, got %d", len(pending))
	}

	// Verify FIFO order (IDs should be ascending)
	for i := 1; i < len(pending); i++ {
		if pending[i].ID <= pending[i-1].ID {
			t.Error("Pending blobs not in FIFO order")
		}
	}
}

func TestBlobStore_CreateBatch(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Insert blobs
	var blobIDs []int64
	for i := 0; i < 3; i++ {
		blob := &db.Blob{
			Commitment: []byte{byte(i)},
			Namespace:  []byte("test"),
			Data:       []byte{byte(i)},
			Size:       1,
			Status:     "pending_submission",
		}
		id, err := store.InsertBlob(ctx, blob)
		if err != nil {
			t.Fatalf("InsertBlob failed: %v", err)
		}
		blobIDs = append(blobIDs, id)
	}

	// Create batch
	batchCommitment := []byte("batch-commitment")
	batchData := []byte("packed-batch-data")

	batchID, err := store.CreateBatch(ctx, blobIDs, batchCommitment, batchData)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	if batchID == 0 {
		t.Fatal("Expected non-zero batch ID")
	}

	// Verify blobs are marked as batched
	for _, blobID := range blobIDs {
		blob, err := store.GetBlobByCommitment(ctx, []byte{byte(blobID - 1)})
		if err != nil {
			t.Fatalf("GetBlobByCommitment failed: %v", err)
		}

		if blob.Status != "batched" {
			t.Errorf("Expected status batched, got %s", blob.Status)
		}

		if blob.BatchID == nil || *blob.BatchID != batchID {
			t.Errorf("Expected batch_id %d, got %v", batchID, blob.BatchID)
		}
	}
}

func TestBlobStore_MarkBatchConfirmed(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Insert blobs
	var blobIDs []int64
	for i := 0; i < 2; i++ {
		blob := &db.Blob{
			Commitment: []byte{byte(i)},
			Namespace:  []byte("test"),
			Data:       []byte{byte(i)},
			Size:       1,
			Status:     "pending_submission",
		}
		id, err := store.InsertBlob(ctx, blob)
		if err != nil {
			t.Fatalf("InsertBlob failed: %v", err)
		}
		blobIDs = append(blobIDs, id)
	}

	// Create batch
	batchCommitment := []byte("batch-commitment-abc")
	batchData := []byte("packed-data")

	batchID, err := store.CreateBatch(ctx, blobIDs, batchCommitment, batchData)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	// Mark batch as submitted first
	err = store.MarkBatchSubmitted(ctx, batchID)
	if err != nil {
		t.Fatalf("MarkBatchSubmitted failed: %v", err)
	}

	// Mark batch as confirmed
	height := uint64(12345)
	err = store.MarkBatchConfirmed(ctx, batchCommitment, height)
	if err != nil {
		t.Fatalf("MarkBatchConfirmed failed: %v", err)
	}

	// Verify all blobs are confirmed with height
	for _, blobID := range blobIDs {
		blob, err := store.GetBlobByCommitment(ctx, []byte{byte(blobID - 1)})
		if err != nil {
			t.Fatalf("GetBlobByCommitment failed: %v", err)
		}

		if blob.Status != "confirmed" {
			t.Errorf("Expected status confirmed, got %s", blob.Status)
		}

		if blob.CelestiaHeight == nil || *blob.CelestiaHeight != height {
			t.Errorf("Expected height %d, got %v", height, blob.CelestiaHeight)
		}
	}
}

func TestBlobStore_GetUnconfirmedBatches(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Create old batch (submitted 5 minutes ago)
	blobIDs := []int64{}
	blob := &db.Blob{
		Commitment: []byte("test1"),
		Namespace:  []byte("ns"),
		Data:       []byte("data1"),
		Size:       5,
		Status:     "pending_submission",
	}
	id, _ := store.InsertBlob(ctx, blob)
	blobIDs = append(blobIDs, id)

	batchCommitment := []byte("old-batch")
	batchData := []byte("data")
	batchID, err := store.CreateBatch(ctx, blobIDs, batchCommitment, batchData)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	// Manually set submitted_at to 5 minutes ago
	store.GetDB().Exec("UPDATE batches SET submitted_at = datetime('now', '-5 minutes') WHERE batch_id = ?", batchID)

	// Get unconfirmed batches older than 2 minutes
	batches, err := store.GetUnconfirmedBatches(ctx, 2*time.Minute)
	if err != nil {
		t.Fatalf("GetUnconfirmedBatches failed: %v", err)
	}

	if len(batches) != 1 {
		t.Errorf("Expected 1 unconfirmed batch, got %d", len(batches))
	}

	if len(batches) > 0 && batches[0].BatchID != batchID {
		t.Errorf("Expected batch ID %d, got %d", batchID, batches[0].BatchID)
	}
}

func TestBlobStore_MarkRead(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Insert blob
	blob := &db.Blob{
		Commitment: []byte("commit"),
		Namespace:  []byte("ns"),
		Data:       []byte("data"),
		Size:       4,
		Status:     "confirmed",
	}

	blobID, err := store.InsertBlob(ctx, blob)
	if err != nil {
		t.Fatalf("InsertBlob failed: %v", err)
	}

	// Mark as read
	err = store.MarkRead(ctx, blobID)
	if err != nil {
		t.Fatalf("MarkRead failed: %v", err)
	}

	// Verify read status
	_, err = store.GetBlobByCommitment(ctx, blob.Commitment)
	if err != nil {
		t.Fatalf("GetBlobByCommitment failed: %v", err)
	}

	// Check read count (we don't expose read_status in current Blob struct, but it's in DB)
	// Just verify no error for now
}

func TestBlobStore_GetStats(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Insert blobs and put them through different states
	var blobIDs []int64

	// Create 3 blobs
	for i := 0; i < 3; i++ {
		blob := &db.Blob{
			Commitment: []byte{byte(i)},
			Namespace:  []byte("ns"),
			Data:       []byte{byte(i)},
			Size:       1,
			Status:     "pending_submission",
		}
		id, err := store.InsertBlob(ctx, blob)
		if err != nil {
			t.Fatalf("InsertBlob failed: %v", err)
		}
		blobIDs = append(blobIDs, id)
	}

	// Create a batch with the first blob (puts it in "batched" status)
	batchID, err := store.CreateBatch(ctx, []int64{blobIDs[0]}, []byte("batch1"), []byte("data1"))
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	// Mark batch as submitted
	err = store.MarkBatchSubmitted(ctx, batchID)
	if err != nil {
		t.Fatalf("MarkBatchSubmitted failed: %v", err)
	}

	// Mark one batch as confirmed (puts blobs in "confirmed" status)
	err = store.MarkBatchConfirmed(ctx, []byte("batch1"), 12345)
	if err != nil {
		t.Fatalf("MarkBatchConfirmed failed: %v", err)
	}

	// Create another batch with second blob
	batchID2, err := store.CreateBatch(ctx, []int64{blobIDs[1]}, []byte("batch2"), []byte("data2"))
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	// Mark as submitted (leaves it in "batched" state without confirming)
	err = store.MarkBatchSubmitted(ctx, batchID2)
	if err != nil {
		t.Fatalf("MarkBatchSubmitted failed: %v", err)
	}

	// Third blob remains as pending_submission

	// Get stats
	stats, err := store.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	blobCounts := stats["blob_counts"].(map[string]int)

	if blobCounts["pending_submission"] != 1 {
		t.Errorf("Expected 1 pending blob, got %d", blobCounts["pending_submission"])
	}

	if blobCounts["batched"] != 1 {
		t.Errorf("Expected 1 batched blob, got %d", blobCounts["batched"])
	}

	if blobCounts["confirmed"] != 1 {
		t.Errorf("Expected 1 confirmed blob, got %d", blobCounts["confirmed"])
	}
}

func TestBlobStore_ConcurrentInserts(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	// Insert 100 blobs concurrently
	errChan := make(chan error, 100)
	for i := 0; i < 100; i++ {
		go func(idx int) {
			blob := &db.Blob{
				Commitment: []byte{byte(idx >> 8), byte(idx)},
				Namespace:  []byte("ns"),
				Data:       []byte{byte(idx)},
				Size:       1,
				Status:     "pending_submission",
			}
			_, err := store.InsertBlob(ctx, blob)
			errChan <- err
		}(i)
	}

	// Check all inserts succeeded
	for i := 0; i < 100; i++ {
		if err := <-errChan; err != nil {
			t.Errorf("Concurrent insert failed: %v", err)
		}
	}

	// Verify count
	pending, err := store.GetPendingBlobs(ctx, 200)
	if err != nil {
		t.Fatalf("GetPendingBlobs failed: %v", err)
	}

	if len(pending) != 100 {
		t.Errorf("Expected 100 blobs, got %d", len(pending))
	}
}
