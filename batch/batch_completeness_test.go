package batch

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/celestiaorg/op-alt-da/db"
)

// TestBatchConfig_Validation tests batch config validation
func TestBatchConfig_Validation(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name:    "default config is valid",
			config:  DefaultConfig(),
			wantErr: false,
		},
		{
			name: "zero min blobs",
			config: &Config{
				MinBlobs:          0,
				MaxBlobs:          100,
				MinBatchSizeBytes: 1000,
				MaxBatchSizeBytes: 1000000,
			},
			wantErr: true,
		},
		{
			name: "max blobs less than min blobs",
			config: &Config{
				MinBlobs:          50,
				MaxBlobs:          10,
				MinBatchSizeBytes: 1000,
				MaxBatchSizeBytes: 1000000,
			},
			wantErr: true,
		},
		{
			name: "max size less than min size",
			config: &Config{
				MinBlobs:          10,
				MaxBlobs:          100,
				MinBatchSizeBytes: 10000,
				MaxBatchSizeBytes: 1000,
			},
			wantErr: true,
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

// TestBatchPacking_Corruption tests handling of corrupted batch data
func TestBatchPacking_Corruption(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name          string
		corruptData   func([]byte) []byte
		expectError   bool
		errorContains string
	}{
		{
			name: "truncated count field",
			corruptData: func(data []byte) []byte {
				return data[:2] // Only 2 bytes instead of 4 for count
			},
			expectError:   true,
			errorContains: "read blob count",
		},
		{
			name: "truncated size field",
			corruptData: func(data []byte) []byte {
				// Keep count, but truncate first size field
				return data[:6] // 4 bytes count + 2 bytes of size
			},
			expectError:   true,
			errorContains: "read blob",
		},
		{
			name: "truncated blob data",
			corruptData: func(data []byte) []byte {
				// Corrupt by truncating actual blob data
				if len(data) > 10 {
					return data[:len(data)-5]
				}
				return data
			},
			expectError:   true,
			errorContains: "read blob",
		},
		{
			name: "modified count to exceed max",
			corruptData: func(data []byte) []byte {
				corrupted := make([]byte, len(data))
				copy(corrupted, data)
				// Set count to impossibly high value
				binary.BigEndian.PutUint32(corrupted[0:4], uint32(cfg.MaxBlobs+100))
				return corrupted
			},
			expectError:   true,
			errorContains: "invalid blob count",
		},
		{
			name: "zero count",
			corruptData: func(data []byte) []byte {
				corrupted := make([]byte, len(data))
				copy(corrupted, data)
				// Set count to zero
				binary.BigEndian.PutUint32(corrupted[0:4], 0)
				return corrupted
			},
			expectError:   true,
			errorContains: "invalid blob count",
		},
	}

	// Create valid batch first
	blobs := []*db.Blob{
		{ID: 1, Data: []byte("blob1")},
		{ID: 2, Data: []byte("blob2")},
	}
	validPacked, err := PackBlobs(blobs, cfg)
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			corruptedData := tt.corruptData(validPacked)

			_, err := UnpackBlobs(corruptedData, cfg)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestBatchPacking_EdgeCaseSizes tests edge cases in blob sizes
func TestBatchPacking_EdgeCaseSizes(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name      string
		blobSizes []int
		wantErr   bool
	}{
		{
			name:      "single byte blobs",
			blobSizes: []int{1, 1, 1},
			wantErr:   false,
		},
		{
			name:      "exactly max batch size",
			blobSizes: []int{cfg.MaxBatchSizeBytes - 100}, // Account for headers
			wantErr:   false,
		},
		{
			name:      "mixed with zero-length",
			blobSizes: []int{100, 0, 200}, // Zero-length blob in middle
			wantErr:   true,               // Should reject empty blobs
		},
		{
			name:      "all zeros",
			blobSizes: []int{0, 0, 0},
			wantErr:   true, // Should reject empty blobs
		},
		{
			name:      "max blobs exact",
			blobSizes: func() []int {
				sizes := make([]int, cfg.MaxBlobs)
				for i := range sizes {
					sizes[i] = 1 // Each blob must have at least 1 byte
				}
				return sizes
			}(),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blobs := make([]*db.Blob, len(tt.blobSizes))
			for i, size := range tt.blobSizes {
				data := make([]byte, size)
				for j := range data {
					data[j] = byte(j % 256)
				}
				blobs[i] = &db.Blob{
					ID:   int64(i),
					Data: data,
				}
			}

			packed, err := PackBlobs(blobs, cfg)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			// Try to unpack
			unpacked, err := UnpackBlobs(packed, cfg)
			require.NoError(t, err)

			// Verify count and data
			assert.Equal(t, len(blobs), len(unpacked))
			for i, blob := range blobs {
				assert.Equal(t, blob.Data, unpacked[i], "blob %d data mismatch", i)
			}
		})
	}
}

// TestBatchPacking_BinaryFormat tests binary format details
func TestBatchPacking_BinaryFormat(t *testing.T) {
	cfg := DefaultConfig()

	blobs := []*db.Blob{
		{ID: 1, Data: []byte("abc")},
		{ID: 2, Data: []byte("defgh")},
	}

	packed, err := PackBlobs(blobs, cfg)
	require.NoError(t, err)

	// Manually parse and verify format
	buf := bytes.NewReader(packed)

	// Read count
	var count uint32
	err = binary.Read(buf, binary.BigEndian, &count)
	require.NoError(t, err)
	assert.Equal(t, uint32(2), count, "should have 2 blobs")

	// Read first blob size
	var size1 uint32
	err = binary.Read(buf, binary.BigEndian, &size1)
	require.NoError(t, err)
	assert.Equal(t, uint32(3), size1, "first blob should be 3 bytes")

	// Read first blob data
	data1 := make([]byte, size1)
	_, err = buf.Read(data1)
	require.NoError(t, err)
	assert.Equal(t, []byte("abc"), data1)

	// Read second blob size
	var size2 uint32
	err = binary.Read(buf, binary.BigEndian, &size2)
	require.NoError(t, err)
	assert.Equal(t, uint32(5), size2, "second blob should be 5 bytes")

	// Read second blob data
	data2 := make([]byte, size2)
	_, err = buf.Read(data2)
	require.NoError(t, err)
	assert.Equal(t, []byte("defgh"), data2)

	// Should be at end
	remaining, _ := io.ReadAll(buf)
	assert.Empty(t, remaining, "should have read all data")
}

// TestBatchPacking_SizeAccounting tests size calculations
func TestBatchPacking_SizeAccounting(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name       string
		blobSizes  []int
		expectSize int
	}{
		{
			name:       "single blob",
			blobSizes:  []int{10},
			expectSize: 4 + 4 + 10, // count + size + data
		},
		{
			name:       "two blobs",
			blobSizes:  []int{5, 7},
			expectSize: 4 + 4 + 5 + 4 + 7, // count + (size+data)*2
		},
		{
			name:       "three blobs",
			blobSizes:  []int{1, 2, 3},
			expectSize: 4 + 4 + 1 + 4 + 2 + 4 + 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blobs := make([]*db.Blob, len(tt.blobSizes))
			for i, size := range tt.blobSizes {
				blobs[i] = &db.Blob{
					ID:   int64(i),
					Data: make([]byte, size),
				}
			}

			packed, err := PackBlobs(blobs, cfg)
			require.NoError(t, err)

			assert.Equal(t, tt.expectSize, len(packed), "packed size should match calculation")
		})
	}
}

// TestBatchPacking_Stress tests with large numbers of blobs
func TestBatchPacking_Stress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	cfg := DefaultConfig()

	// Test with maximum number of blobs
	blobs := make([]*db.Blob, cfg.MaxBlobs)
	for i := range blobs {
		blobs[i] = &db.Blob{
			ID:   int64(i),
			Data: []byte{byte(i % 256)},
		}
	}

	packed, err := PackBlobs(blobs, cfg)
	require.NoError(t, err)

	unpacked, err := UnpackBlobs(packed, cfg)
	require.NoError(t, err)

	assert.Equal(t, len(blobs), len(unpacked))
	for i := range blobs {
		assert.Equal(t, blobs[i].Data, unpacked[i])
	}
}

// TestBatchPacking_ConcurrentPackUnpack tests thread safety
func TestBatchPacking_ConcurrentPackUnpack(t *testing.T) {
	cfg := DefaultConfig()

	// Create test data
	blobs := []*db.Blob{
		{ID: 1, Data: []byte("concurrent test 1")},
		{ID: 2, Data: []byte("concurrent test 2")},
	}

	// Pack once
	packed, err := PackBlobs(blobs, cfg)
	require.NoError(t, err)

	// Unpack concurrently many times
	numGoroutines := 100
	done := make(chan bool, numGoroutines)
	errors := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func() {
			unpacked, err := UnpackBlobs(packed, cfg)
			if err != nil {
				errors <- err
				done <- false
				return
			}

			// Verify
			if len(unpacked) != len(blobs) {
				errors <- assert.AnError
				done <- false
				return
			}

			for j := range blobs {
				if !bytes.Equal(blobs[j].Data, unpacked[j]) {
					errors <- assert.AnError
					done <- false
					return
				}
			}

			done <- true
		}()
	}

	// Wait for all to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Check for errors
	select {
	case err := <-errors:
		t.Fatalf("concurrent unpack failed: %v", err)
	default:
		// No errors
	}
}

// TestShouldBatch_AllConditions tests batch triggering logic
func TestShouldBatch_AllConditions(t *testing.T) {
	cfg := DefaultConfig()

	tests := []struct {
		name      string
		blobCount int
		totalSize int
		want      bool
	}{
		{
			name:      "below both thresholds",
			blobCount: cfg.MinBlobs - 1,
			totalSize: cfg.MinBatchSizeBytes - 1,
			want:      false,
		},
		{
			name:      "exact min blobs",
			blobCount: cfg.MinBlobs,
			totalSize: 100,
			want:      true,
		},
		{
			name:      "exact min size",
			blobCount: 1,
			totalSize: cfg.MinBatchSizeBytes,
			want:      true,
		},
		{
			name:      "exceeds both",
			blobCount: cfg.MinBlobs + 10,
			totalSize: cfg.MinBatchSizeBytes + 10000,
			want:      true,
		},
		{
			name:      "zero blobs",
			blobCount: 0,
			totalSize: 1000000,
			want:      false,
		},
		{
			name:      "zero size",
			blobCount: 100,
			totalSize: 0,
			want:      true, // Blob count threshold met
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.ShouldBatch(tt.blobCount, tt.totalSize)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestBatchPacking_DataIntegrity tests data isn't corrupted during pack/unpack
func TestBatchPacking_DataIntegrity(t *testing.T) {
	cfg := DefaultConfig()

	// Create blobs with specific patterns
	blobs := []*db.Blob{
		{ID: 1, Data: bytes.Repeat([]byte{0xFF}, 100)},
		{ID: 2, Data: bytes.Repeat([]byte{0x00}, 100)},
		{ID: 3, Data: bytes.Repeat([]byte{0xAA}, 100)},
		{ID: 4, Data: bytes.Repeat([]byte{0x55}, 100)},
	}

	// Pack
	packed, err := PackBlobs(blobs, cfg)
	require.NoError(t, err)

	// Unpack
	unpacked, err := UnpackBlobs(packed, cfg)
	require.NoError(t, err)

	// Verify each blob's pattern is intact
	for i, blob := range blobs {
		assert.Equal(t, blob.Data, unpacked[i], "blob %d pattern corrupted", i)

		// Extra check: verify the pattern
		expectedByte := blob.Data[0]
		for _, b := range unpacked[i] {
			assert.Equal(t, expectedByte, b, "blob %d pattern not uniform", i)
		}
	}
}


