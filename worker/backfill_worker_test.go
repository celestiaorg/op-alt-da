package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/celestiaorg/celestia-node/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/ethereum/go-ethereum/log"

	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/celestiaorg/op-alt-da/sdkconfig"
)

func init() {
	// Configure SDK to use Celestia Bech32 prefix
	sdkconfig.InitCelestiaPrefix()
}

// TestBackfillWorker_IndexBatch tests indexing a discovered batch
func TestBackfillWorker_IndexBatch(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create test data
	testData1 := []byte("test blob 1")
	testData2 := []byte("test blob 2")
	blobs := []*db.Blob{
		{Data: testData1},
		{Data: testData2},
	}

	// Pack blobs into batch
	batchCfg := batch.DefaultConfig()
	packedData, err := batch.PackBlobs(blobs, batchCfg)
	if err != nil {
		t.Fatalf("PackBlobs failed: %v", err)
	}

	// Create celestia blob with dummy signer for V1
	dummySigner := make([]byte, 20)
	celestiaBlob, err := blob.NewBlobV1(namespace, packedData, dummySigner)
	if err != nil {
		t.Fatalf("NewBlobV1 failed: %v", err)
	}

	// Create backfill worker
	mock := &mockCelestiaAPI{}
	workerCfg := DefaultConfig()
	workerCfg.StartHeight = 1
	logger := log.NewLogger(log.DiscardHandler())
	worker := NewBackfillWorker(store, mock, nil, namespace, batchCfg, workerCfg, nil, logger)

	// Index the batch
	height := uint64(12345)
	err = worker.indexBatch(ctx, celestiaBlob, height)
	if err != nil {
		t.Fatalf("indexBatch failed: %v", err)
	}

	// Verify batch was indexed
	batch, err := store.GetBatchByCommitment(ctx, celestiaBlob.Commitment)
	if err != nil {
		t.Fatalf("GetBatchByCommitment failed: %v", err)
	}
	if batch == nil {
		t.Fatal("Batch not found after indexing")
	}
	if batch.Status != "confirmed" {
		t.Errorf("Expected batch status 'confirmed', got '%s'", batch.Status)
	}
	if batch.BlobCount != len(blobs) {
		t.Errorf("Expected blob count %d, got %d", len(blobs), batch.BlobCount)
	}
}

// TestBackfillWorker_SignerVerification tests trusted signer verification
func TestBackfillWorker_SignerVerification(t *testing.T) {
	_, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create test data
	testData := []byte("test blob")
	blobs := []*db.Blob{{Data: testData}}

	// Pack blobs into batch
	batchCfg := batch.DefaultConfig()
	packedData, err := batch.PackBlobs(blobs, batchCfg)
	if err != nil {
		t.Fatalf("PackBlobs failed: %v", err)
	}

	// Create celestia blob with specific signer
	trustedSigner := make([]byte, 20)
	trustedSigner[0] = 0xAA
	trustedSigner[19] = 0xBB
	celestiaBlob, err := blob.NewBlobV1(namespace, packedData, trustedSigner)
	if err != nil {
		t.Fatalf("NewBlobV1 failed: %v", err)
	}

	// Convert test signer to Bech32 format
	trustedSignerAddr := sdk.AccAddress(trustedSigner)
	trustedSignerBech32 := trustedSignerAddr.String()

	tests := []struct {
		name           string
		trustedSigners []string
		expectError    bool
	}{
		{
			name:           "no trusted signers - accept all",
			trustedSigners: []string{},
			expectError:    false,
		},
		{
			name:           "matching trusted signer (Bech32)",
			trustedSigners: []string{trustedSignerBech32},
			expectError:    false,
		},
		{
			name:           "non-matching trusted signer",
			trustedSigners: []string{"celestia1qqgjyv6y24n80zye42aueh0wluqsyqcyf07sls"},
			expectError:    true,
		},
		{
			name:           "multiple signers with match",
			trustedSigners: []string{"celestia1qqgjyv6y24n80zye42aueh0wluqsyqcyf07sls", trustedSignerBech32},
			expectError:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fresh store for each test
			testStore, _, testCleanup := setupWorkerTest(t)
			defer testCleanup()

			mock := &mockCelestiaAPI{}
			workerCfg := DefaultConfig()
			workerCfg.StartHeight = 1
			workerCfg.TrustedSigners = tt.trustedSigners
			logger := log.NewLogger(log.DiscardHandler())
			worker := NewBackfillWorker(testStore, mock, nil, namespace, batchCfg, workerCfg, nil, logger)

			err := worker.indexBatch(ctx, celestiaBlob, 12345)
			if tt.expectError && err == nil {
				t.Error("Expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			// If no error expected, verify batch was indexed
			if !tt.expectError {
				batch, err := testStore.GetBatchByCommitment(ctx, celestiaBlob.Commitment)
				if err != nil {
					t.Fatalf("GetBatchByCommitment failed: %v", err)
				}
				if batch == nil {
					t.Error("Batch not found after successful indexing")
				}
			}
		})
	}
}

// TestBackfillWorker_RejectUnsignedBlob tests rejection of unsigned blobs
func TestBackfillWorker_RejectUnsignedBlob(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create test data
	testData := []byte("test blob")
	blobs := []*db.Blob{{Data: testData}}

	// Pack blobs into batch
	batchCfg := batch.DefaultConfig()
	packedData, err := batch.PackBlobs(blobs, batchCfg)
	if err != nil {
		t.Fatalf("PackBlobs failed: %v", err)
	}

	// Create celestia blob with zero signer (unsigned)
	zeroSigner := make([]byte, 20)
	celestiaBlob, err := blob.NewBlobV1(namespace, packedData, zeroSigner)
	if err != nil {
		t.Fatalf("NewBlobV1 failed: %v", err)
	}

	// Create backfill worker with trusted signers configured
	mock := &mockCelestiaAPI{}
	workerCfg := DefaultConfig()
	workerCfg.StartHeight = 1
	// Use a Bech32 address
	workerCfg.TrustedSigners = []string{"celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc"}
	logger := log.NewLogger(log.DiscardHandler())
	worker := NewBackfillWorker(store, mock, nil, namespace, batchCfg, workerCfg, nil, logger)

	// Index should fail due to untrusted signer
	err = worker.indexBatch(ctx, celestiaBlob, 12345)
	if err == nil {
		t.Error("Expected error for unsigned blob with trusted signers configured")
	}

	// Verify batch was NOT indexed
	batch, _ := store.GetBatchByCommitment(ctx, celestiaBlob.Commitment)
	if batch != nil {
		t.Error("Batch should not be indexed when signer verification fails")
	}
}

// TestBackfillWorker_RejectMalformedBatch tests rejection of malformed batches
func TestBackfillWorker_RejectMalformedBatch(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create malformed batch data (not properly packed)
	malformedData := []byte("this is not a valid packed batch")

	// Create celestia blob with dummy signer
	dummySigner := make([]byte, 20)
	celestiaBlob, err := blob.NewBlobV1(namespace, malformedData, dummySigner)
	if err != nil {
		t.Fatalf("NewBlobV1 failed: %v", err)
	}

	// Create backfill worker
	mock := &mockCelestiaAPI{}
	workerCfg := DefaultConfig()
	workerCfg.StartHeight = 1
	batchCfg := batch.DefaultConfig()
	logger := log.NewLogger(log.DiscardHandler())
	worker := NewBackfillWorker(store, mock, nil, namespace, batchCfg, workerCfg, nil, logger)

	// Index should fail due to malformed data
	err = worker.indexBatch(ctx, celestiaBlob, 12345)
	if err == nil {
		t.Error("Expected error for malformed batch data")
	}

	// Verify batch was NOT indexed
	batch, _ := store.GetBatchByCommitment(ctx, celestiaBlob.Commitment)
	if batch != nil {
		t.Error("Batch should not be indexed when data is malformed")
	}
}

// TestBackfillWorker_ScanAndIndexBlocks tests the scan logic
func TestBackfillWorker_ScanAndIndexBlocks(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create test batch data
	testData := []byte("test blob")
	blobs := []*db.Blob{{Data: testData}}
	batchCfg := batch.DefaultConfig()
	packedData, err := batch.PackBlobs(blobs, batchCfg)
	if err != nil {
		t.Fatalf("PackBlobs failed: %v", err)
	}

	dummySigner := make([]byte, 20)
	celestiaBlob, err := blob.NewBlobV1(namespace, packedData, dummySigner)
	if err != nil {
		t.Fatalf("NewBlobV1 failed: %v", err)
	}

	// Mock GetAll to return a blob at height 2
	mock := &mockCelestiaAPI{
		getAllFunc: func(ctx context.Context, height uint64, namespaces []libshare.Namespace) ([]*blob.Blob, error) {
			if height == 2 {
				return []*blob.Blob{celestiaBlob}, nil
			}
			// Return error for heights beyond 2 (simulating tip)
			if height > 2 {
				return nil, fmt.Errorf("height not found")
			}
			return []*blob.Blob{}, nil
		},
	}

	// Mock header API to return tip at height 3
	mockHeader := &mockHeaderAPI{tipHeight: 3}

	workerCfg := DefaultConfig()
	workerCfg.StartHeight = 1
	logger := log.NewLogger(log.DiscardHandler())
	worker := NewBackfillWorker(store, mock, mockHeader, namespace, batchCfg, workerCfg, nil, logger)

	// Run scan using doBackfill
	worker.doBackfill(ctx)

	// Verify the blob was discovered and indexed
	batch, err := store.GetBatchByCommitment(ctx, celestiaBlob.Commitment)
	if err != nil {
		t.Fatalf("GetBatchByCommitment failed: %v", err)
	}
	if batch == nil {
		t.Fatal("Batch not found after scan")
	}
	if batch.Status != "confirmed" {
		t.Errorf("Expected batch status 'confirmed', got '%s'", batch.Status)
	}

	// Verify height was updated
	if worker.backfillHeight != 3 {
		t.Errorf("Expected backfill height 3, got %d", worker.backfillHeight)
	}
}

// TestBackfillWorker_DuplicateBatch tests handling of duplicate batches
func TestBackfillWorker_DuplicateBatch(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create test data
	testData := []byte("test blob")
	blobs := []*db.Blob{{Data: testData}}
	batchCfg := batch.DefaultConfig()
	packedData, err := batch.PackBlobs(blobs, batchCfg)
	if err != nil {
		t.Fatalf("PackBlobs failed: %v", err)
	}

	dummySigner := make([]byte, 20)
	celestiaBlob, err := blob.NewBlobV1(namespace, packedData, dummySigner)
	if err != nil {
		t.Fatalf("NewBlobV1 failed: %v", err)
	}

	mock := &mockCelestiaAPI{}
	workerCfg := DefaultConfig()
	workerCfg.StartHeight = 1
	logger := log.NewLogger(log.DiscardHandler())
	worker := NewBackfillWorker(store, mock, nil, namespace, batchCfg, workerCfg, nil, logger)

	// Index the batch first time
	height := uint64(12345)
	err = worker.indexBatch(ctx, celestiaBlob, height)
	if err != nil {
		t.Fatalf("First indexBatch failed: %v", err)
	}

	// Index the same batch again (should be idempotent)
	err = worker.indexBatch(ctx, celestiaBlob, height)
	if err != nil {
		t.Fatalf("Second indexBatch failed: %v", err)
	}

	// Verify only one batch exists
	batch, err := store.GetBatchByCommitment(ctx, celestiaBlob.Commitment)
	if err != nil {
		t.Fatalf("GetBatchByCommitment failed: %v", err)
	}
	if batch == nil {
		t.Fatal("Batch not found")
	}

	// Count batches in DB
	var count int
	err = store.GetDB().QueryRow("SELECT COUNT(*) FROM batches WHERE batch_commitment = ?", celestiaBlob.Commitment).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count batches: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 batch, found %d", count)
	}
}

// TestBackfillWorker_ContextCancellation tests worker stops on context cancel
func TestBackfillWorker_ContextCancellation(t *testing.T) {
	store, namespace, cleanup := setupWorkerTest(t)
	defer cleanup()

	mock := &mockCelestiaAPI{
		getAllFunc: func(ctx context.Context, height uint64, namespaces []libshare.Namespace) ([]*blob.Blob, error) {
			// Simulate slow operation
			time.Sleep(100 * time.Millisecond)
			return []*blob.Blob{}, nil
		},
	}

	workerCfg := DefaultConfig()
	workerCfg.StartHeight = 1
	workerCfg.BackfillPeriod = 10 * time.Millisecond
	batchCfg := batch.DefaultConfig()
	logger := log.NewLogger(log.DiscardHandler())
	worker := NewBackfillWorker(store, mock, nil, namespace, batchCfg, workerCfg, nil, logger)

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

	// Should return within reasonable time
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Worker did not stop after context cancellation")
	}
}
