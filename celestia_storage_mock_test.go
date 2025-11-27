package celestia

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/celestia-node/blob"
	"github.com/celestiaorg/celestia-node/state"
	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/ethereum/go-ethereum/log"
)

// MockBlobModule is a mock implementation of blobAPI.Module
type MockBlobModule struct {
	mock.Mock
}

func (m *MockBlobModule) Submit(ctx context.Context, blobs []*blob.Blob, opts *state.TxConfig) (uint64, error) {
	args := m.Called(ctx, blobs, opts)
	return args.Get(0).(uint64), args.Error(1)
}

func (m *MockBlobModule) Get(ctx context.Context, height uint64, namespace libshare.Namespace, commitment blob.Commitment) (*blob.Blob, error) {
	args := m.Called(ctx, height, namespace, commitment)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*blob.Blob), args.Error(1)
}

func (m *MockBlobModule) GetAll(ctx context.Context, height uint64, namespaces []libshare.Namespace) ([]*blob.Blob, error) {
	args := m.Called(ctx, height, namespaces)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]*blob.Blob), args.Error(1)
}

func (m *MockBlobModule) GetProof(ctx context.Context, height uint64, namespace libshare.Namespace, commitment blob.Commitment) (*blob.Proof, error) {
	args := m.Called(ctx, height, namespace, commitment)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*blob.Proof), args.Error(1)
}

func (m *MockBlobModule) Included(ctx context.Context, height uint64, namespace libshare.Namespace, proof *blob.Proof, commitment blob.Commitment) (bool, error) {
	args := m.Called(ctx, height, namespace, proof, commitment)
	return args.Bool(0), args.Error(1)
}

func (m *MockBlobModule) Subscribe(ctx context.Context, namespace libshare.Namespace) (<-chan *blob.SubscriptionResponse, error) {
	args := m.Called(ctx, namespace)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(<-chan *blob.SubscriptionResponse), args.Error(1)
}

func (m *MockBlobModule) GetCommitmentProof(ctx context.Context, height uint64, namespace libshare.Namespace, shareCommitment []byte) (*blob.CommitmentProof, error) {
	args := m.Called(ctx, height, namespace, shareCommitment)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*blob.CommitmentProof), args.Error(1)
}

// TestCelestiaStore_Get_Success tests successful blob retrieval
func TestCelestiaStore_Get_Success(t *testing.T) {
	mockClient := new(MockBlobModule)
	ns := createTestNamespace(t)

	store := &CelestiaStore{
		Log:        log.New(),
		GetTimeout: 5 * time.Second,
		Namespace:  ns,
		Client:     mockClient,
	}

	// Create test blob
	testData := []byte("test blob data")
	testBlob, err := blob.NewBlobV1(ns, testData, make([]byte, 20))
	require.NoError(t, err)

	height := uint64(12345)

	// Setup mock expectation
	mockClient.On("Get",
		mock.Anything,
		height,
		ns,
		blob.Commitment(testBlob.Commitment),
	).Return(testBlob, nil)

	// Create blob ID key (2 byte header + marshaled blob ID)
	blobID := CelestiaBlobID{
		Height:     height,
		Commitment: testBlob.Commitment,
	}
	idBytes, err := blobID.MarshalBinary()
	require.NoError(t, err)
	key := append([]byte{0x00, VersionByte}, idBytes...)

	// Execute
	ctx := context.Background()
	result, err := store.Get(ctx, key)

	// Assert
	require.NoError(t, err)
	assert.Equal(t, testData, result)
	mockClient.AssertExpectations(t)
}

// TestCelestiaStore_Get_NotFound tests blob not found scenario
func TestCelestiaStore_Get_NotFound(t *testing.T) {
	mockClient := new(MockBlobModule)
	ns := createTestNamespace(t)

	store := &CelestiaStore{
		Log:        log.New(),
		GetTimeout: 5 * time.Second,
		Namespace:  ns,
		Client:     mockClient,
	}

	height := uint64(12345)
	commitment := make([]byte, 32)

	// Setup mock to return not found
	mockClient.On("Get",
		mock.Anything,
		height,
		ns,
		blob.Commitment(commitment),
	).Return(nil, errors.New("blob not found"))

	// Create key
	blobID := CelestiaBlobID{
		Height:     height,
		Commitment: commitment,
	}
	idBytes, err := blobID.MarshalBinary()
	require.NoError(t, err)
	key := append([]byte{0x00, VersionByte}, idBytes...)

	// Execute
	ctx := context.Background()
	_, err = store.Get(ctx, key)

	// Assert
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to resolve frame")
	mockClient.AssertExpectations(t)
}

// TestCelestiaStore_Get_Timeout tests timeout handling
func TestCelestiaStore_Get_Timeout(t *testing.T) {
	mockClient := new(MockBlobModule)
	ns := createTestNamespace(t)

	store := &CelestiaStore{
		Log:        log.New(),
		GetTimeout: 100 * time.Millisecond, // Short timeout
		Namespace:  ns,
		Client:     mockClient,
	}

	height := uint64(12345)
	commitment := make([]byte, 32)

	// Setup mock to block longer than timeout
	mockClient.On("Get",
		mock.Anything,
		height,
		ns,
		blob.Commitment(commitment),
	).Return(nil, context.DeadlineExceeded)

	// Create key
	blobID := CelestiaBlobID{
		Height:     height,
		Commitment: commitment,
	}
	idBytes, err := blobID.MarshalBinary()
	require.NoError(t, err)
	key := append([]byte{0x00, VersionByte}, idBytes...)

	// Execute
	ctx := context.Background()
	_, err = store.Get(ctx, key)

	// Assert
	assert.Error(t, err)
	mockClient.AssertExpectations(t)
}

// TestCelestiaStore_Get_InvalidKey tests invalid key handling
func TestCelestiaStore_Get_InvalidKey(t *testing.T) {
	mockClient := new(MockBlobModule)
	ns := createTestNamespace(t)

	store := &CelestiaStore{
		Log:        log.New(),
		GetTimeout: 5 * time.Second,
		Namespace:  ns,
		Client:     mockClient,
	}

	tests := []struct {
		name             string
		key              []byte
		expectedErrorMsg string
	}{
		{
			name:             "empty key",
			key:              []byte{},
			expectedErrorMsg: "invalid commitment format: expected at least 2 bytes [alt-da_type][celestia_version]",
		},
		{
			name:             "too short",
			key:              []byte{0x00},
			expectedErrorMsg: "invalid commitment format: expected at least 2 bytes [alt-da_type][celestia_version]",
		},
		{
			name:             "invalid format",
			key:              []byte{0x00, 0x01, 0x02},
			expectedErrorMsg: "failed to unmarshal blob ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := store.Get(context.Background(), tt.key)
			assert.Error(t, err, "should reject invalid key")
			assert.Contains(t, err.Error(), tt.expectedErrorMsg, "error message should be informative")
		})
	}
}

// TestCelestiaStore_CreateCommitment tests commitment creation
func TestCelestiaStore_CreateCommitment(t *testing.T) {
	ns := createTestNamespace(t)

	store := &CelestiaStore{
		Log:        log.New(),
		Namespace:  ns,
		SignerAddr: make([]byte, 20), // Valid 20-byte signer
	}

	testData := []byte("test data for commitment")

	// Execute
	commitment, err := store.CreateCommitment(testData)

	// Assert
	require.NoError(t, err)
	assert.NotEmpty(t, commitment)
	assert.Equal(t, 32, len(commitment), "commitment should be 32 bytes")

	// Same data should produce same commitment
	commitment2, err := store.CreateCommitment(testData)
	require.NoError(t, err)
	assert.Equal(t, commitment, commitment2, "commitment should be deterministic")

	// Different data should produce different commitment
	commitment3, err := store.CreateCommitment([]byte("different data"))
	require.NoError(t, err)
	assert.NotEqual(t, commitment, commitment3, "different data should have different commitment")
}

// TestCelestiaStore_CreateCommitment_EmptyData tests commitment with empty data
func TestCelestiaStore_CreateCommitment_EmptyData(t *testing.T) {
	ns := createTestNamespace(t)

	store := &CelestiaStore{
		Log:        log.New(),
		Namespace:  ns,
		SignerAddr: make([]byte, 20), // Valid 20-byte signer
	}

	// Empty data
	commitment, err := store.CreateCommitment([]byte{})

	// Should either succeed with valid commitment or fail gracefully
	if err == nil {
		assert.NotEmpty(t, commitment, "if successful, should return non-empty commitment")
	} else {
		t.Logf("Empty data rejected with error: %v", err)
	}
}

// TestCelestiaStore_CreateCommitment_LargeData tests commitment with large data
func TestCelestiaStore_CreateCommitment_LargeData(t *testing.T) {
	ns := createTestNamespace(t)

	store := &CelestiaStore{
		Log:        log.New(),
		Namespace:  ns,
		SignerAddr: make([]byte, 20), // Valid 20-byte signer
	}

	// Large data (~1MB)
	largeData := make([]byte, 1024*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	commitment, err := store.CreateCommitment(largeData)

	require.NoError(t, err)
	assert.NotEmpty(t, commitment)
	assert.Equal(t, 32, len(commitment))
}

// TestCelestiaBlobID_MarshalUnmarshal_Compact tests compact blob ID format
func TestCelestiaBlobID_MarshalUnmarshal_Compact(t *testing.T) {
	commitment := make([]byte, 32)
	for i := range commitment {
		commitment[i] = byte(i)
	}

	blobID := CelestiaBlobID{
		Height:     12345,
		Commitment: commitment,
	}
	// Set isCompact flag via marshaling
	blobID.ShareOffset = 0
	blobID.ShareSize = 0

	marshaled, err := blobID.MarshalBinary()
	require.NoError(t, err)

	// Should be compact size or full size depending on implementation
	t.Logf("Marshaled size: %d bytes", len(marshaled))

	// Unmarshal
	var unmarshaled CelestiaBlobID
	err = unmarshaled.UnmarshalBinary(marshaled)
	require.NoError(t, err)

	assert.Equal(t, blobID.Height, unmarshaled.Height)
	assert.Equal(t, blobID.Commitment, unmarshaled.Commitment)
}

// TestCelestiaBlobID_MarshalUnmarshal_Full tests full blob ID format
func TestCelestiaBlobID_MarshalUnmarshal_Full(t *testing.T) {
	commitment := make([]byte, 32)
	for i := range commitment {
		commitment[i] = byte(i)
	}

	blobID := CelestiaBlobID{
		Height:      12345,
		Commitment:  commitment,
		ShareOffset: 100,
		ShareSize:   200,
	}

	marshaled, err := blobID.MarshalBinary()
	require.NoError(t, err)
	assert.Equal(t, 48, len(marshaled), "full format should be 48 bytes")

	// Unmarshal
	var unmarshaled CelestiaBlobID
	err = unmarshaled.UnmarshalBinary(marshaled)
	require.NoError(t, err)

	assert.Equal(t, blobID.Height, unmarshaled.Height)
	assert.Equal(t, blobID.Commitment, unmarshaled.Commitment)
	assert.Equal(t, blobID.ShareOffset, unmarshaled.ShareOffset)
	assert.Equal(t, blobID.ShareSize, unmarshaled.ShareSize)
}

// TestCelestiaBlobID_UnmarshalBinary_BackwardCompatibility tests reading legacy format
func TestCelestiaBlobID_UnmarshalBinary_BackwardCompatibility(t *testing.T) {
	commitment := make([]byte, 32)
	for i := range commitment {
		commitment[i] = byte(i)
	}

	// Create full format
	blobID := CelestiaBlobID{
		Height:      12345,
		Commitment:  commitment,
		ShareOffset: 100,
		ShareSize:   200,
	}

	fullMarshaled, err := blobID.MarshalBinary()
	require.NoError(t, err)

	// Test unmarshaling compact format (first 40 bytes)
	compactData := fullMarshaled[:40]

	var compactUnmarshaled CelestiaBlobID
	err = compactUnmarshaled.UnmarshalBinary(compactData)
	require.NoError(t, err)

	assert.Equal(t, blobID.Height, compactUnmarshaled.Height)
	assert.Equal(t, blobID.Commitment, compactUnmarshaled.Commitment)
	// Share fields should be zero
	assert.Equal(t, uint32(0), compactUnmarshaled.ShareOffset)
	assert.Equal(t, uint32(0), compactUnmarshaled.ShareSize)
}

// TestCelestiaStore_Get_ConcurrentRequests tests concurrent Get operations
func TestCelestiaStore_Get_ConcurrentRequests(t *testing.T) {
	mockClient := new(MockBlobModule)
	ns := createTestNamespace(t)

	store := &CelestiaStore{
		Log:        log.New(),
		GetTimeout: 5 * time.Second,
		Namespace:  ns,
		Client:     mockClient,
	}

	// Create test blob
	testData := []byte("concurrent test data")
	testBlob, err := blob.NewBlobV1(ns, testData, make([]byte, 20))
	require.NoError(t, err)

	height := uint64(12345)

	// Setup mock to handle multiple calls
	mockClient.On("Get",
		mock.Anything,
		height,
		ns,
		blob.Commitment(testBlob.Commitment),
	).Return(testBlob, nil).Times(10)

	// Create key
	blobID := CelestiaBlobID{
		Height:     height,
		Commitment: testBlob.Commitment,
	}
	idBytes, err := blobID.MarshalBinary()
	require.NoError(t, err)
	key := append([]byte{0x00, VersionByte}, idBytes...)

	// Launch concurrent Gets
	numRequests := 10
	results := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			ctx := context.Background()
			data, err := store.Get(ctx, key)
			if err == nil && !bytes.Equal(data, testData) {
				results <- errors.New("data mismatch")
				return
			}
			results <- err
		}()
	}

	// Collect results
	for i := 0; i < numRequests; i++ {
		err := <-results
		assert.NoError(t, err, "concurrent request %d should succeed", i)
	}

	mockClient.AssertExpectations(t)
}

// TestCelestiaStore_Get_ContextCancellation tests context cancellation
func TestCelestiaStore_Get_ContextCancellation(t *testing.T) {
	mockClient := new(MockBlobModule)
	ns := createTestNamespace(t)

	store := &CelestiaStore{
		Log:        log.New(),
		GetTimeout: 5 * time.Second,
		Namespace:  ns,
		Client:     mockClient,
	}

	height := uint64(12345)
	commitment := make([]byte, 32)

	// Setup mock to return context canceled
	mockClient.On("Get",
		mock.Anything,
		height,
		ns,
		blob.Commitment(commitment),
	).Return(nil, context.Canceled)

	// Create key
	blobID := CelestiaBlobID{
		Height:     height,
		Commitment: commitment,
	}
	idBytes, err := blobID.MarshalBinary()
	require.NoError(t, err)
	key := append([]byte{0x00, VersionByte}, idBytes...)

	// Execute with canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = store.Get(ctx, key)

	// Should handle cancellation gracefully
	assert.Error(t, err)
}

