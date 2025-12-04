package worker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/celestiaorg/celestia-node/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/ethereum/go-ethereum/log"

	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/sdkconfig"
)

func init() {
	// Initialize Celestia SDK prefix for Bech32 address parsing in tests
	sdkconfig.InitCelestiaPrefix()
}

// Mock Celestia API - implements blob.Module interface
type mockCelestiaAPI struct {
	submitFunc func(ctx context.Context, blobs []*blob.Blob) (uint64, error)
	getFunc    func(ctx context.Context, height uint64, ns libshare.Namespace, commitment blob.Commitment) (*blob.Blob, error)
	getAllFunc func(ctx context.Context, height uint64, namespaces []libshare.Namespace) ([]*blob.Blob, error)
}

// Submit implements blob.Module interface
func (m *mockCelestiaAPI) Submit(ctx context.Context, blobs []*blob.Blob, opts *blob.SubmitOptions) (uint64, error) {
	if m.submitFunc != nil {
		return m.submitFunc(ctx, blobs)
	}
	return 12345, nil // Default height
}

// Subscribe implements blob.Module interface (not used - reconciliation only)
func (m *mockCelestiaAPI) Subscribe(ctx context.Context, ns libshare.Namespace) (<-chan *blob.SubscriptionResponse, error) {
	// Not used in reconciliation-only mode
	return nil, nil
}

// Get implements blob.Module interface
func (m *mockCelestiaAPI) Get(ctx context.Context, height uint64, ns libshare.Namespace, commitment blob.Commitment) (*blob.Blob, error) {
	if m.getFunc != nil {
		return m.getFunc(ctx, height, ns, commitment)
	}
	return nil, nil
}

// GetAll implements blob.Module interface
func (m *mockCelestiaAPI) GetAll(ctx context.Context, height uint64, namespaces []libshare.Namespace) ([]*blob.Blob, error) {
	if m.getAllFunc != nil {
		return m.getAllFunc(ctx, height, namespaces)
	}
	return nil, nil
}

// GetProof implements blob.Module interface (not used in tests)
func (m *mockCelestiaAPI) GetProof(ctx context.Context, height uint64, ns libshare.Namespace, commitment blob.Commitment) (*blob.Proof, error) {
	return nil, nil
}

// Included implements blob.Module interface (not used in tests)
func (m *mockCelestiaAPI) Included(ctx context.Context, height uint64, ns libshare.Namespace, proof *blob.Proof, commitment blob.Commitment) (bool, error) {
	return false, nil
}

// GetCommitmentProof implements blob.Module interface (not used in tests)
func (m *mockCelestiaAPI) GetCommitmentProof(ctx context.Context, height uint64, namespace libshare.Namespace, shareCommitment []byte) (*blob.CommitmentProof, error) {
	return nil, nil
}

func setupWorkerTest(t *testing.T) (*db.BlobStore, libshare.Namespace, func()) {
	// Create temporary database
	tmpFile, err := os.CreateTemp("", "test-worker-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()

	store, err := db.NewBlobStore(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("Failed to create store: %v", err)
	}

	// Create test namespace (29 bytes: 1 version + 28 ID with 18 leading zeros)
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

// TestSubmissionWorker_SubmitPendingBlobs tests the submission worker submits all pending blobs
func TestSubmissionWorker_SubmitPendingBlobs(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Insert 15 pending blobs
	for i := 0; i < 15; i++ {
		testBlob := &db.Blob{
			Commitment: []byte{byte(i), 0x00, 0x00, 0x00},
			Namespace:  namespace.Bytes(),
			Data:       []byte{byte(i)},
			Size:       1,
			Status:     "pending_submission",
		}
		_, err := store.InsertBlob(ctx, testBlob)
		if err != nil {
			t.Fatalf("InsertBlob failed: %v", err)
		}
	}

	// Create mock API that tracks submissions
	submitted := false
	mock := &mockCelestiaAPI{
		submitFunc: func(ctx context.Context, blobs []*blob.Blob) (uint64, error) {
			submitted = true
			return 12345, nil
		},
	}

	// Create worker with short period for testing
	logger := log.NewLogger(log.DiscardHandler())
	batchCfg := batch.DefaultConfig()
	workerCfg := DefaultConfig()
	workerCfg.SubmitPeriod = 100 * time.Millisecond
	signerAddr := make([]byte, 20) // Test signer
	worker := NewSubmissionWorker(store, mock, namespace, signerAddr, batchCfg, workerCfg, nil, logger)

	// Submit pending blobs
	err := worker.submitPendingBlobs(ctx)
	if err != nil {
		t.Fatalf("submitPendingBlobs failed: %v", err)
	}

	// Verify batch was submitted
	if !submitted {
		t.Error("Batch was not submitted to Celestia")
	}

	// Verify blobs are marked as batched
	pending, err := store.GetPendingBlobs(ctx, 20)
	if err != nil {
		t.Fatalf("GetPendingBlobs failed: %v", err)
	}

	// Should have no more pending blobs (all batched)
	if len(pending) != 0 {
		t.Errorf("Expected 0 pending blobs, got %d", len(pending))
	}
}

// TestSubmissionWorker_NoBlobs tests that worker handles empty queue gracefully
func TestSubmissionWorker_NoBlobs(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// No blobs inserted

	// Create mock API that tracks submissions
	submitted := false
	mock := &mockCelestiaAPI{
		submitFunc: func(ctx context.Context, blobs []*blob.Blob) (uint64, error) {
			submitted = true
			return 12345, nil
		},
	}

	logger := log.NewLogger(log.DiscardHandler())
	batchCfg := batch.DefaultConfig()
	workerCfg := DefaultConfig()
	signerAddr := make([]byte, 20) // Test signer
	worker := NewSubmissionWorker(store, mock, namespace, signerAddr, batchCfg, workerCfg, nil, logger)

	// Submit pending blobs (there are none)
	err := worker.submitPendingBlobs(ctx)
	if err != nil {
		t.Fatalf("submitPendingBlobs failed: %v", err)
	}

	// Should NOT have submitted (no blobs)
	if submitted {
		t.Error("Batch was submitted despite no blobs")
	}
}

// TestSubmissionWorker_FewBlobs tests that we submit even with few blobs
func TestSubmissionWorker_FewBlobs(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Insert only 3 blobs
	for i := 0; i < 3; i++ {
		testBlob := &db.Blob{
			Commitment: []byte{byte(i), 0x00, 0x00, 0x00},
			Namespace:  namespace.Bytes(),
			Data:       []byte{byte(i)},
			Size:       1,
			Status:     "pending_submission",
		}
		_, err := store.InsertBlob(ctx, testBlob)
		if err != nil {
			t.Fatalf("InsertBlob failed: %v", err)
		}
	}

	// Create mock API that tracks submissions
	submitted := false
	var submittedBlobCount int
	mock := &mockCelestiaAPI{
		submitFunc: func(ctx context.Context, blobs []*blob.Blob) (uint64, error) {
			submitted = true
			submittedBlobCount = len(blobs)
			return 12345, nil
		},
	}

	logger := log.NewLogger(log.DiscardHandler())
	batchCfg := batch.DefaultConfig()
	workerCfg := DefaultConfig()
	signerAddr := make([]byte, 20) // Test signer
	worker := NewSubmissionWorker(store, mock, namespace, signerAddr, batchCfg, workerCfg, nil, logger)

	// Submit pending blobs
	err := worker.submitPendingBlobs(ctx)
	if err != nil {
		t.Fatalf("submitPendingBlobs failed: %v", err)
	}

	// Should have submitted (we always submit pending blobs)
	if !submitted {
		t.Error("Batch was not submitted")
	}

	// Should have 1 batch with all 3 blobs packed
	if submittedBlobCount != 1 {
		t.Errorf("Expected 1 batch (Celestia blob), got %d", submittedBlobCount)
	}

	// Blobs should no longer be pending
	pending, err := store.GetPendingBlobs(ctx, 20)
	if err != nil {
		t.Fatalf("GetPendingBlobs failed: %v", err)
	}

	if len(pending) != 0 {
		t.Errorf("Expected 0 pending blobs, got %d", len(pending))
	}
}

// TestSubmissionWorker_LargeSize tests batching handles large blobs correctly
func TestSubmissionWorker_LargeSize(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Insert 3 large blobs (over 500KB total)
	largeData := make([]byte, 200*1024) // 200KB each
	for i := 0; i < 3; i++ {
		testBlob := &db.Blob{
			Commitment: []byte{byte(i), 0x00, 0x00, 0x00},
			Namespace:  namespace.Bytes(),
			Data:       largeData,
			Size:       len(largeData),
			Status:     "pending_submission",
		}
		_, err := store.InsertBlob(ctx, testBlob)
		if err != nil {
			t.Fatalf("InsertBlob failed: %v", err)
		}
	}

	submitted := false
	mock := &mockCelestiaAPI{
		submitFunc: func(ctx context.Context, blobs []*blob.Blob) (uint64, error) {
			submitted = true
			return 12345, nil
		},
	}

	logger := log.NewLogger(log.DiscardHandler())
	batchCfg := batch.DefaultConfig()
	workerCfg := DefaultConfig()
	signerAddr := make([]byte, 20) // Test signer
	worker := NewSubmissionWorker(store, mock, namespace, signerAddr, batchCfg, workerCfg, nil, logger)

	// Submit pending blobs
	err := worker.submitPendingBlobs(ctx)
	if err != nil {
		t.Fatalf("submitPendingBlobs failed: %v", err)
	}

	// Should have submitted
	if !submitted {
		t.Error("Batch was not submitted")
	}
}

// TestSubmissionWorker_ManyLargeBlobs tests that batching correctly handles many large blobs
// by creating multiple batches that each fit within the MaxBatchSizeBytes limit
func TestSubmissionWorker_ManyLargeBlobs(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Insert 20 blobs of 800KB each (16MB total - way over 1MB limit)
	// Each blob has unique data to avoid duplicate batch commitments
	for i := 0; i < 20; i++ {
		largeData := make([]byte, 800*1024) // 800KB each
		// Make data unique by setting first bytes to index
		largeData[0] = byte(i)
		largeData[1] = byte(i >> 8)
		testBlob := &db.Blob{
			Commitment: []byte{byte(i), 0x00, 0x00, 0x00},
			Namespace:  namespace.Bytes(),
			Data:       largeData,
			Size:       len(largeData),
			Status:     "pending_submission",
		}
		_, err := store.InsertBlob(ctx, testBlob)
		if err != nil {
			t.Fatalf("InsertBlob failed: %v", err)
		}
	}

	var submitCallCount int
	var totalBlobsSubmitted int
	var mu sync.Mutex
	mock := &mockCelestiaAPI{
		submitFunc: func(ctx context.Context, blobs []*blob.Blob) (uint64, error) {
			mu.Lock()
			submitCallCount++
			totalBlobsSubmitted += len(blobs)
			mu.Unlock()
			// Verify each packed batch doesn't exceed 1MB
			for _, b := range blobs {
				if len(b.Data()) > 1*1024*1024 {
					return 0, fmt.Errorf("batch size %d exceeds 1MB limit", len(b.Data()))
				}
			}
			return 12345, nil
		},
	}

	logger := log.NewLogger(log.DiscardHandler())
	batchCfg := batch.DefaultConfig()
	workerCfg := DefaultConfig()
	signerAddr := make([]byte, 20) // Test signer
	worker := NewSubmissionWorker(store, mock, namespace, signerAddr, batchCfg, workerCfg, nil, logger)

	// Submit pending blobs
	err := worker.submitPendingBlobs(ctx)
	if err != nil {
		t.Fatalf("submitPendingBlobs failed: %v", err)
	}

	// Should have submitted (at least one call)
	if submitCallCount == 0 {
		t.Error("No batches were submitted")
	}

	// Should have created 20 batches total (each 800KB blob = 1 batch since 2*800KB > 1MB)
	// With one-blob-per-Submit, we get 20 Submit() calls
	if totalBlobsSubmitted != 20 {
		t.Errorf("Expected 20 batches total (one per 800KB blob), got %d", totalBlobsSubmitted)
	}

	// Verify all blobs are now batched (none pending)
	pending, err := store.GetPendingBlobs(ctx, 30)
	if err != nil {
		t.Fatalf("GetPendingBlobs failed: %v", err)
	}

	if len(pending) != 0 {
		t.Errorf("Expected 0 pending blobs remaining, got %d", len(pending))
	}
}

// TestSubmissionWorker_ContextCancellation tests worker stops on context cancel
func TestSubmissionWorker_ContextCancellation(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	mock := &mockCelestiaAPI{}
	logger := log.NewLogger(log.DiscardHandler())
	batchCfg := batch.DefaultConfig()
	workerCfg := DefaultConfig()
	workerCfg.SubmitPeriod = 10 * time.Millisecond
	signerAddr := make([]byte, 20) // Test signer
	worker := NewSubmissionWorker(store, mock, namespace, signerAddr, batchCfg, workerCfg, nil, logger)

	// Create context with cancel
	ctx, cancel := context.WithCancel(context.Background())

	// Start worker in goroutine
	done := make(chan error, 1)
	go func() {
		done <- worker.Run(ctx)
	}()

	// Cancel after 50ms
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Wait for worker to stop
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Expected context.Canceled, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("Worker did not stop after context cancellation")
	}
}

// TestEventListener_Reconciliation tests reconciliation of unconfirmed batches
func TestEventListener_Reconciliation(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create batch
	testBlob := &db.Blob{
		Commitment: []byte("test1"),
		Namespace:  namespace.Bytes(),
		Data:       []byte("data1"),
		Size:       5,
		Status:     "pending_submission",
	}
	blobID, _ := store.InsertBlob(ctx, testBlob)

	batchCommitment := []byte("old-batch")
	batchData := []byte("data")
	batchID, err := store.CreateBatch(ctx, []int64{blobID}, batchCommitment, batchData)
	if err != nil {
		t.Fatalf("CreateBatch failed: %v", err)
	}

	// Manually set submitted_at to 5 minutes ago
	store.GetDB().Exec("UPDATE batches SET submitted_at = datetime('now', '-5 minutes'), celestia_height = 12345 WHERE batch_id = ?", batchID)

	// Create mock that returns blob on Get (we now try Get with each trusted signer's commitment)
	mock := &mockCelestiaAPI{
		getFunc: func(ctx context.Context, height uint64, ns libshare.Namespace, commitment blob.Commitment) (*blob.Blob, error) {
			// Return blob to simulate successful Get with matching commitment
			dummySigner := make([]byte, 20)
			b, _ := blob.NewBlobV1(namespace, batchData, dummySigner)
			return b, nil
		},
	}

	logger := log.NewLogger(log.DiscardHandler())
	workerCfg := DefaultConfig()
	// Add a trusted signer - use a valid bech32 address format for celestia
	workerCfg.TrustedSigners = []string{"celestia1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqzf30as"}
	listener := NewEventListener(store, mock, namespace, workerCfg, nil, logger)

	// Run reconciliation
	err = listener.reconcileUnconfirmed(ctx)
	if err != nil {
		t.Fatalf("reconcileUnconfirmed failed: %v", err)
	}

	// Verify batch is now confirmed
	b, err := store.GetBlobByCommitment(ctx, testBlob.Commitment)
	if err != nil {
		t.Fatalf("GetBlobByCommitment failed: %v", err)
	}

	if b.Status != "confirmed" {
		t.Errorf("Expected status confirmed after reconciliation, got %s", b.Status)
	}
}

// TestEventListener_ContextCancellation tests reconciliation worker stops on context cancel
func TestEventListener_ContextCancellation(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	mock := &mockCelestiaAPI{}

	logger := log.NewLogger(log.DiscardHandler())
	workerCfg := DefaultConfig()
	workerCfg.ReconcilePeriod = 10 * time.Millisecond
	listener := NewEventListener(store, mock, namespace, workerCfg, nil, logger)

	// Create context with cancel
	ctx, cancel := context.WithCancel(context.Background())

	// Start listener in goroutine
	done := make(chan error, 1)
	go func() {
		done <- listener.Run(ctx)
	}()

	// Cancel after 50ms
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Wait for listener to stop
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Expected context.Canceled, got: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("Reconciliation worker did not stop after context cancellation")
	}
}
