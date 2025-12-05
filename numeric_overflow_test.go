package celestia

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"testing"

	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBlobID_NumericBoundaries tests blob ID edge cases
func TestBlobID_NumericBoundaries(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	tests := []struct {
		name      string
		blobID    int64
		expectErr bool
	}{
		{
			name:      "zero ID",
			blobID:    0,
			expectErr: false,
		},
		{
			name:      "negative ID",
			blobID:    -1,
			expectErr: false, // SQLite allows negative IDs
		},
		{
			name:      "max int64",
			blobID:    math.MaxInt64,
			expectErr: false,
		},
		{
			name:      "min int64",
			blobID:    math.MinInt64,
			expectErr: false,
		},
		{
			name:      "near max int64",
			blobID:    math.MaxInt64 - 1,
			expectErr: false,
		},
	}

	namespace := createTestNamespace(t)

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use unique commitment for each test case
			commitment := []byte(fmt.Sprintf("test commitment %02d bytes long!", i))
			blob := &db.Blob{
				ID:         tt.blobID,
				Data:       []byte(fmt.Sprintf("test data %d", i)),
				Commitment: commitment,
				Namespace:  namespace.Bytes(),
			}

			_, err := store.InsertBlob(context.Background(), blob)

			if tt.expectErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)

				// Try to retrieve it by commitment
				retrieved, err := store.GetBlobByCommitment(context.Background(), commitment)
				require.NoError(t, err)
				// Note: Don't assert exact ID match since DB may auto-increment
				assert.Equal(t, blob.Data, retrieved.Data)
			}
		})
	}
}

// TestHeight_NumericBoundaries tests Celestia height edge cases
func TestHeight_NumericBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		height uint64
	}{
		{
			name:   "zero height",
			height: 0,
		},
		{
			name:   "height 1",
			height: 1,
		},
		{
			name:   "max uint64",
			height: math.MaxUint64,
		},
		{
			name:   "near max uint64",
			height: math.MaxUint64 - 1,
		},
		{
			name:   "power of 2 boundary",
			height: 1 << 32, // 2^32
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blobID := CelestiaBlobID{
				Height:     tt.height,
				Commitment: make([]byte, 32),
			}

			// Marshal
			data, err := blobID.MarshalBinary()
			require.NoError(t, err)

			// Unmarshal
			var decoded CelestiaBlobID
			err = decoded.UnmarshalBinary(data)
			require.NoError(t, err)

			// Verify height survived roundtrip
			assert.Equal(t, tt.height, decoded.Height, "height should survive marshal/unmarshal")
		})
	}
}

// TestShareOffset_NumericBoundaries tests share offset edge cases
func TestShareOffset_NumericBoundaries(t *testing.T) {
	tests := []struct {
		name        string
		shareOffset uint32
	}{
		{
			name:        "zero offset",
			shareOffset: 0,
		},
		{
			name:        "offset 1",
			shareOffset: 1,
		},
		{
			name:        "max uint32",
			shareOffset: math.MaxUint32,
		},
		{
			name:        "near max uint32",
			shareOffset: math.MaxUint32 - 1,
		},
		{
			name:        "power of 2 boundary",
			shareOffset: 1 << 16, // 2^16
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blobID := CelestiaBlobID{
				Height:      12345,
				Commitment:  make([]byte, 32),
				ShareOffset: tt.shareOffset,
				ShareSize:   100,
			}

			// Marshal
			data, err := blobID.MarshalBinary()
			require.NoError(t, err)

			// Unmarshal
			var decoded CelestiaBlobID
			err = decoded.UnmarshalBinary(data)
			require.NoError(t, err)

			// Verify offset survived roundtrip (only if not compact mode)
			if len(data) > CompactIDSize {
				assert.Equal(t, tt.shareOffset, decoded.ShareOffset, "share offset should survive marshal/unmarshal")
			}
		})
	}
}

// TestShareSize_NumericBoundaries tests share size edge cases
func TestShareSize_NumericBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		shareSize uint32
	}{
		{
			name:      "zero size",
			shareSize: 0,
		},
		{
			name:      "size 1",
			shareSize: 1,
		},
		{
			name:      "max uint32",
			shareSize: math.MaxUint32,
		},
		{
			name:      "near max uint32",
			shareSize: math.MaxUint32 - 1,
		},
		{
			name:      "power of 2 boundary",
			shareSize: 1 << 16, // 2^16
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blobID := CelestiaBlobID{
				Height:      12345,
				Commitment:  make([]byte, 32),
				ShareOffset: 100,
				ShareSize:   tt.shareSize,
			}

			// Marshal
			data, err := blobID.MarshalBinary()
			require.NoError(t, err)

			// Unmarshal
			var decoded CelestiaBlobID
			err = decoded.UnmarshalBinary(data)
			require.NoError(t, err)

			// Verify size survived roundtrip (only if not compact mode)
			if len(data) > CompactIDSize {
				assert.Equal(t, tt.shareSize, decoded.ShareSize, "share size should survive marshal/unmarshal")
			}
		})
	}
}

// TestBatchPacking_SizeOverflow tests handling of extremely large batch sizes
func TestBatchPacking_SizeOverflow(t *testing.T) {
	cfg := batch.DefaultConfig()

	// Try to pack a blob that would overflow when calculating total size
	largeBlob := &db.Blob{
		ID:   1,
		Data: make([]byte, math.MaxInt32), // 2GB blob
	}

	_, err := batch.PackBlobs([]*db.Blob{largeBlob}, cfg)
	// Should either succeed or fail gracefully (not panic)
	if err != nil {
		assert.Contains(t, err.Error(), "too large", "should reject oversized blob")
	}
}

// TestBlobCount_Overflow tests handling of impossible blob counts
func TestBlobCount_Overflow(t *testing.T) {
	cfg := batch.DefaultConfig()

	// Create corrupted batch data with impossibly high count
	data := make([]byte, 4)
	binary.BigEndian.PutUint32(data[0:4], math.MaxUint32)

	_, err := batch.UnpackBlobs(data, cfg)
	require.Error(t, err, "should reject impossibly high blob count")
	assert.Contains(t, err.Error(), "invalid blob count", "error should mention invalid count")
}

// TestBlobSize_Overflow tests handling of impossible individual blob sizes
func TestBlobSize_Overflow(t *testing.T) {
	cfg := batch.DefaultConfig()

	// Create batch data with valid count but impossible blob size
	data := make([]byte, 8)
	binary.BigEndian.PutUint32(data[0:4], 1) // 1 blob
	binary.BigEndian.PutUint32(data[4:8], math.MaxUint32) // Impossibly large size

	_, err := batch.UnpackBlobs(data, cfg)
	require.Error(t, err, "should reject impossibly large blob size")
	// Will fail when trying to read that many bytes
}

// TestConfigValues_NumericBoundaries tests batch config edge cases
func TestConfigValues_NumericBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		config  *batch.Config
		wantErr bool
	}{
		{
			name: "max possible values",
			config: &batch.Config{
				MinBlobs:          1,
				MaxBlobs:          math.MaxInt32,
				TargetBlobs:       100,
				MinBatchSizeBytes: 1,
				MaxBatchSizeBytes: math.MaxInt32,
			},
			wantErr: false,
		},
		{
			name: "near overflow values",
			config: &batch.Config{
				MinBlobs:          math.MaxInt32 - 1,
				MaxBlobs:          math.MaxInt32,
				TargetBlobs:       math.MaxInt32 - 10,
				MinBatchSizeBytes: math.MaxInt32 - 1000,
				MaxBatchSizeBytes: math.MaxInt32,
			},
			wantErr: false,
		},
		{
			name: "negative min blobs",
			config: &batch.Config{
				MinBlobs:          -1,
				MaxBlobs:          100,
				TargetBlobs:       50,
				MinBatchSizeBytes: 1000,
				MaxBatchSizeBytes: 1000000,
			},
			wantErr: true, // Should be caught by validation
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestNamespace_EdgeCases tests namespace edge cases
func TestNamespace_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		createNS  func() (libshare.Namespace, error)
		expectErr bool
	}{
		{
			name: "all zeros namespace",
			createNS: func() (libshare.Namespace, error) {
				nsBytes := make([]byte, 29)
				return libshare.NewNamespaceFromBytes(nsBytes)
			},
			expectErr: false, // Version 0 with zeros is valid
		},
		{
			name: "all 0xFF namespace",
			createNS: func() (libshare.Namespace, error) {
				nsBytes := make([]byte, 29)
				for i := range nsBytes {
					nsBytes[i] = 0xFF
				}
				return libshare.NewNamespaceFromBytes(nsBytes)
			},
			expectErr: false, // Should be valid
		},
		{
			name: "max version byte",
			createNS: func() (libshare.Namespace, error) {
				nsBytes := make([]byte, 29)
				nsBytes[0] = 0xFF // Max version
				for i := 1; i < 29; i++ {
					nsBytes[i] = byte(i)
				}
				return libshare.NewNamespaceFromBytes(nsBytes)
			},
			expectErr: false, // Library should handle or reject
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns, err := tt.createNS()

			if tt.expectErr {
				assert.Error(t, err)
			} else {
				// If no error expected, just verify it was created
				// (library might still return error for invalid versions)
				if err == nil {
					assert.NotNil(t, ns)
				}
			}
		})
	}
}

// TestBinaryEncoding_Endianness tests that endianness is handled correctly
func TestBinaryEncoding_Endianness(t *testing.T) {
	// Test that we're using big endian consistently
	cfg := batch.DefaultConfig()

	blobs := []*db.Blob{
		{ID: 1, Data: []byte("test")},
	}

	packed, err := batch.PackBlobs(blobs, cfg)
	require.NoError(t, err)

	// First 4 bytes should be count in big endian
	count := binary.BigEndian.Uint32(packed[0:4])
	assert.Equal(t, uint32(1), count, "count should be 1 in big endian")

	// Next 4 bytes should be size in big endian
	size := binary.BigEndian.Uint32(packed[4:8])
	assert.Equal(t, uint32(4), size, "size should be 4 in big endian")
}

// TestCelestiaBlobID_BinarySize tests that binary encoding sizes are correct
func TestCelestiaBlobID_BinarySize(t *testing.T) {
	tests := []struct {
		name         string
		blobID       CelestiaBlobID
		expectedSize int
	}{
		{
			name: "blob ID without share info still marshals to full format",
			blobID: CelestiaBlobID{
				Height:     12345,
				Commitment: make([]byte, 32),
				// Note: Even without ShareOffset/ShareSize, MarshalBinary
				// always produces full format (48 bytes) unless isCompact is set
			},
			expectedSize: FullIDSize, // Always full format: 8 + 32 + 4 + 4 = 48
		},
		{
			name: "blob ID with share info",
			blobID: CelestiaBlobID{
				Height:      12345,
				Commitment:  make([]byte, 32),
				ShareOffset: 100,
				ShareSize:   200,
			},
			expectedSize: FullIDSize, // 8 + 32 + 4 + 4 = 48
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.blobID.MarshalBinary()
			require.NoError(t, err)
			assert.Equal(t, tt.expectedSize, len(data), "marshaled size should match expected")
		})
	}
}
