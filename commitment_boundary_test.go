package celestia

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/celestiaorg/op-alt-da/commitment"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommitment_LengthBoundaries tests various commitment lengths
func TestCommitment_LengthBoundaries(t *testing.T) {
	namespace := createTestNamespace(t)

	tests := []struct {
		name      string
		dataSize  int
		expectErr bool
	}{
		{
			name:      "empty data",
			dataSize:  0,
			expectErr: true, // Celestia doesn't allow empty blobs
		},
		{
			name:      "1 byte",
			dataSize:  1,
			expectErr: false,
		},
		{
			name:      "32 bytes",
			dataSize:  32,
			expectErr: false,
		},
		{
			name:      "1 KB",
			dataSize:  1024,
			expectErr: false,
		},
		{
			name:      "1 MB",
			dataSize:  1024 * 1024,
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := make([]byte, tt.dataSize)
			for i := range data {
				data[i] = byte(i % 256)
			}

			commitment, err := commitment.ComputeCommitment(data, namespace)

			if tt.expectErr {
				assert.Error(t, err, "should reject invalid data")
			} else {
				require.NoError(t, err, "should compute commitment")
				assert.Equal(t, 32, len(commitment), "commitment should be 32 bytes")
			}
		})
	}
}

// TestCelestiaBlobID_MarshalBoundaries tests marshaling edge cases
func TestCelestiaBlobID_MarshalBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		blobID CelestiaBlobID
	}{
		{
			name: "zero height",
			blobID: CelestiaBlobID{
				Height:     0,
				Commitment: make([]byte, 32),
			},
		},
		{
			name: "max uint64 height",
			blobID: CelestiaBlobID{
				Height:     ^uint64(0), // MaxUint64
				Commitment: make([]byte, 32),
			},
		},
		{
			name: "zero share offset",
			blobID: CelestiaBlobID{
				Height:      12345,
				Commitment:  make([]byte, 32),
				ShareOffset: 0,
				ShareSize:   100,
			},
		},
		{
			name: "max uint32 share offset",
			blobID: CelestiaBlobID{
				Height:      12345,
				Commitment:  make([]byte, 32),
				ShareOffset: ^uint32(0), // MaxUint32
				ShareSize:   100,
			},
		},
		{
			name: "zero share size",
			blobID: CelestiaBlobID{
				Height:      12345,
				Commitment:  make([]byte, 32),
				ShareOffset: 100,
				ShareSize:   0,
			},
		},
		{
			name: "max uint32 share size",
			blobID: CelestiaBlobID{
				Height:      12345,
				Commitment:  make([]byte, 32),
				ShareOffset: 100,
				ShareSize:   ^uint32(0), // MaxUint32
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal
			data, err := tt.blobID.MarshalBinary()
			require.NoError(t, err, "marshal should succeed")
			assert.NotEmpty(t, data, "marshaled data should not be empty")

			// Unmarshal
			var decoded CelestiaBlobID
			err = decoded.UnmarshalBinary(data)
			require.NoError(t, err, "unmarshal should succeed")

			// Verify roundtrip
			assert.Equal(t, tt.blobID.Height, decoded.Height, "height mismatch")
			assert.Equal(t, tt.blobID.Commitment, decoded.Commitment, "commitment mismatch")

			// Only check offset/size if not compact mode
			if len(data) > CompactIDSize {
				assert.Equal(t, tt.blobID.ShareOffset, decoded.ShareOffset, "share offset mismatch")
				assert.Equal(t, tt.blobID.ShareSize, decoded.ShareSize, "share size mismatch")
			}
		})
	}
}

// TestCelestiaBlobID_UnmarshalInvalidData tests unmarshaling with invalid data
func TestCelestiaBlobID_UnmarshalInvalidData(t *testing.T) {
	tests := []struct {
		name      string
		data      []byte
		expectErr bool
	}{
		{
			name:      "empty data",
			data:      []byte{},
			expectErr: true,
		},
		{
			name:      "single byte",
			data:      []byte{0x01},
			expectErr: true,
		},
		{
			name:      "31 bytes (just under compact size)",
			data:      make([]byte, 31),
			expectErr: true,
		},
		{
			name:      "exactly compact size (40 bytes)",
			data:      make([]byte, CompactIDSize),
			expectErr: false,
		},
		{
			name:      "41 bytes (between compact and full)",
			data:      make([]byte, 41),
			expectErr: false, // Should succeed, just won't have offset/size
		},
		{
			name:      "exactly full size (48 bytes)",
			data:      make([]byte, FullIDSize),
			expectErr: false,
		},
		{
			name:      "more than full size",
			data:      make([]byte, 100),
			expectErr: false, // Extra bytes ignored
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var blobID CelestiaBlobID
			err := blobID.UnmarshalBinary(tt.data)

			if tt.expectErr {
				assert.Error(t, err, "should reject invalid data")
			} else {
				assert.NoError(t, err, "should accept valid data")
			}
		})
	}
}

// TestCommitment_Collisions tests that different data produces different commitments
func TestCommitment_Collisions(t *testing.T) {
	namespace := createTestNamespace(t)

	// Generate 1000 unique blobs and verify no collision
	commitments := make(map[string]bool)

	for i := 0; i < 1000; i++ {
		// Create unique data
		data := make([]byte, 100)
		binary.BigEndian.PutUint64(data, uint64(i))

		commitment, err := commitment.ComputeCommitment(data, namespace)
		require.NoError(t, err)

		commitmentStr := string(commitment)
		assert.False(t, commitments[commitmentStr], "commitment collision detected at iteration %d", i)
		commitments[commitmentStr] = true
	}

	assert.Equal(t, 1000, len(commitments), "should have 1000 unique commitments")
}

// TestCommitment_Determinism tests that same data always produces same commitment
func TestCommitment_Determinism(t *testing.T) {
	namespace := createTestNamespace(t)
	data := []byte("test data for determinism")

	// Compute commitment multiple times
	var commitments [][]byte
	for i := 0; i < 10; i++ {
		commitment, err := commitment.ComputeCommitment(data, namespace)
		require.NoError(t, err)
		commitments = append(commitments, commitment)
	}

	// Verify all are identical
	for i := 1; i < len(commitments); i++ {
		assert.True(t, bytes.Equal(commitments[0], commitments[i]),
			"commitment %d differs from first commitment", i)
	}
}
