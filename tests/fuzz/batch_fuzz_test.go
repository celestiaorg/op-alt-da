package fuzz

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/db"
)

// FuzzBatchPackUnpack tests that any packed batch can be unpacked without panics
// and that roundtrip preserves data integrity
func FuzzBatchPackUnpack(f *testing.F) {
	cfg := batch.DefaultConfig()

	// Seed corpus with interesting cases
	f.Add([]byte("single blob"), uint8(1))                                    // Single blob
	f.Add([]byte(""), uint8(1))                                               // Empty blob
	f.Add([]byte("a"), uint8(1))                                              // Single byte
	f.Add(bytes.Repeat([]byte{0xFF}, 100), uint8(1))                          // All 0xFF
	f.Add(bytes.Repeat([]byte{0x00}, 100), uint8(1))                          // All zeros
	f.Add([]byte("blob1\x00blob2\x00blob3"), uint8(3))                        // Multiple with nulls
	f.Add(bytes.Repeat([]byte("test"), 250), uint8(1))                        // 1KB blob
	f.Add([]byte("\xFF\xFE\xFD\xFC"), uint8(1))                               // High bytes
	f.Add([]byte{0x00, 0x01, 0x02, 0x03, 0x04}, uint8(1))                     // Sequential
	f.Add([]byte("unicode: 世界 🌍 ñ"), uint8(1))                              // Unicode
	f.Add(bytes.Repeat([]byte{0xAA, 0x55}, 50), uint8(1))                     // Alternating pattern
	f.Add([]byte("<script>alert('xss')</script>"), uint8(1))                  // XSS-like
	f.Add([]byte("../../../etc/passwd"), uint8(1))                            // Path traversal
	f.Add([]byte("\r\n\r\n"), uint8(1))                                       // CRLF
	f.Add(make([]byte, cfg.MaxBatchSizeBytes/10), uint8(1))                   // Large blob
	f.Add([]byte("a"), uint8(uint8(cfg.MaxBlobs)))                            // Max blobs
	f.Add(bytes.Repeat([]byte("x"), 100), uint8(10))                          // Many medium blobs
	f.Add([]byte{0x00}, uint8(100))                                           // Many tiny blobs
	f.Add([]byte("normal text with spaces and punctuation!"), uint8(1))       // Normal text
	f.Add([]byte("\t\n\r\v\f"), uint8(1))                                     // Whitespace chars

	f.Fuzz(func(t *testing.T, data []byte, blobCount uint8) {
		// Limit blob count to reasonable range
		if blobCount == 0 || blobCount > uint8(cfg.MaxBlobs) {
			t.Skip("invalid blob count")
		}

		// Create blobs from fuzzed data
		blobs := make([]*db.Blob, blobCount)
		dataLen := len(data)
		chunkSize := dataLen / int(blobCount)
		if chunkSize == 0 && dataLen > 0 {
			chunkSize = 1
		}

		for i := uint8(0); i < blobCount; i++ {
			start := int(i) * chunkSize
			end := start + chunkSize
			if i == blobCount-1 {
				end = dataLen // Last blob gets remainder
			}
			if start > dataLen {
				start = dataLen
			}
			if end > dataLen {
				end = dataLen
			}

			blobs[i] = &db.Blob{
				ID:   int64(i),
				Data: data[start:end],
			}
		}

		// Test packing - should not panic
		packed, err := batch.PackBlobs(blobs, cfg)
		if err != nil {
			// Some errors are expected (e.g., too large)
			// But should not panic
			return
		}

		// Test unpacking - should not panic
		unpacked, err := batch.UnpackBlobs(packed, cfg)
		if err != nil {
			t.Fatalf("unpack failed after successful pack: %v", err)
		}

		// Verify roundtrip integrity
		if len(unpacked) != len(blobs) {
			t.Fatalf("blob count mismatch: got %d, want %d", len(unpacked), len(blobs))
		}

		for i, blob := range blobs {
			if !bytes.Equal(unpacked[i], blob.Data) {
				t.Fatalf("blob %d data mismatch: got %x, want %x", i, unpacked[i], blob.Data)
			}
		}
	})
}

// FuzzBatchUnpackCorrupted tests that unpacking invalid data fails gracefully
// without panics or infinite loops
func FuzzBatchUnpackCorrupted(f *testing.F) {
	cfg := batch.DefaultConfig()

	// Seed with various corruption patterns
	f.Add([]byte{0x00, 0x00, 0x00, 0x00})                                      // Zero count
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF})                                      // Max count
	f.Add([]byte{0x00, 0x00, 0x00, 0x01})                                      // Count=1, no data
	f.Add([]byte{0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x0A})             // Count=1, size=10, no data
	f.Add([]byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x05, 0x01, 0x02}) // Truncated
	
	// Create a valid batch then corrupt it
	validBlob := &db.Blob{ID: 1, Data: []byte("valid data")}
	validPacked, _ := batch.PackBlobs([]*db.Blob{validBlob}, cfg)
	f.Add(validPacked[:len(validPacked)/2])                                    // Truncated valid
	f.Add(append(validPacked, []byte{0xFF, 0xFF}...))                          // Extra bytes
	
	// Corrupt count field
	corruptedCount := make([]byte, len(validPacked))
	copy(corruptedCount, validPacked)
	binary.BigEndian.PutUint32(corruptedCount[0:4], 999999)
	f.Add(corruptedCount)
	
	// Corrupt size field
	corruptedSize := make([]byte, len(validPacked))
	copy(corruptedSize, validPacked)
	if len(corruptedSize) > 8 {
		binary.BigEndian.PutUint32(corruptedSize[4:8], 999999)
	}
	f.Add(corruptedSize)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Should handle any corrupted data gracefully
		// No panics, no infinite loops, no crashes
		unpacked, err := batch.UnpackBlobs(data, cfg)

		// Either succeeds or returns an error, never panics
		if err != nil {
			// This is fine - corrupted data should be rejected
			return
		}

		// If it succeeds, verify basic sanity
		if len(unpacked) > cfg.MaxBlobs {
			t.Fatalf("unpacked too many blobs: %d > %d", len(unpacked), cfg.MaxBlobs)
		}

		for i, blob := range unpacked {
			if len(blob) > cfg.MaxBatchSizeBytes {
				t.Fatalf("blob %d too large: %d > %d", i, len(blob), cfg.MaxBatchSizeBytes)
			}
		}
	})
}

// FuzzBatchBinaryFormat tests the binary format parsing for edge cases
func FuzzBatchBinaryFormat(f *testing.F) {
	cfg := batch.DefaultConfig()

	// Seed with format-specific edge cases
	f.Add(uint32(0), uint32(100))          // Zero count, any size
	f.Add(uint32(1), uint32(0))            // One blob, zero size
	f.Add(uint32(1), uint32(1))            // One blob, one byte
	f.Add(uint32(100), uint32(1))          // Max blobs, small size
	f.Add(uint32(10), uint32(1000))        // Medium blobs, medium size
	f.Add(uint32(1), uint32(1024*1024))    // One blob, 1MB
	f.Add(uint32(0xFFFFFFFF), uint32(100)) // Max uint32 count
	f.Add(uint32(100), uint32(0xFFFFFFFF)) // Max uint32 size

	f.Fuzz(func(t *testing.T, blobCount uint32, blobSize uint32) {
		// Construct a batch with fuzzed count and size
		buf := new(bytes.Buffer)

		// Write count
		binary.Write(buf, binary.BigEndian, blobCount)

		// Write blob entries
		for i := uint32(0); i < blobCount && i < 1000; i++ { // Cap iterations
			binary.Write(buf, binary.BigEndian, blobSize)
			
			// Write actual data (or zeros)
			if blobSize < 1024*1024 { // Cap data size for fuzzing
				buf.Write(make([]byte, blobSize))
			} else {
				buf.Write(make([]byte, 1024)) // Use small data for large size values
			}
		}

		// Try to unpack - should handle gracefully
		data := buf.Bytes()
		unpacked, err := batch.UnpackBlobs(data, cfg)

		if err == nil {
			// If successful, verify sanity
			if len(unpacked) > cfg.MaxBlobs {
				t.Fatalf("unpacked too many blobs: %d", len(unpacked))
			}
		}
		// Errors are fine - fuzzed data is often invalid
	})
}

// FuzzBatchSizeCalculations tests size accounting for overflow/underflow
func FuzzBatchSizeCalculations(f *testing.F) {
	cfg := batch.DefaultConfig()

	// Seed with size boundary conditions
	f.Add(uint8(1), uint32(0))
	f.Add(uint8(1), uint32(1))
	f.Add(uint8(1), uint32(cfg.MaxBatchSizeBytes))
	f.Add(uint8(cfg.MaxBlobs), uint32(1))
	f.Add(uint8(100), uint32(10000))

	f.Fuzz(func(t *testing.T, count uint8, size uint32) {
		if count == 0 || count > uint8(cfg.MaxBlobs) {
			t.Skip()
		}
		if size > uint32(cfg.MaxBatchSizeBytes) {
			t.Skip()
		}

		// Create blobs with specific size
		blobs := make([]*db.Blob, count)
		data := make([]byte, size)
		for i := range blobs {
			blobs[i] = &db.Blob{
				ID:   int64(i),
				Data: data,
			}
		}

		packed, err := batch.PackBlobs(blobs, cfg)
		
		if err != nil {
			// Expected for sizes that exceed limits
			return
		}

		// Verify size calculation
		// Format: [count(4)] + [size(4) + data(size)] * count
		expectedMinSize := 4 + int(count)*(4+int(size))
		if len(packed) < expectedMinSize {
			t.Fatalf("packed size %d less than expected minimum %d", 
				len(packed), expectedMinSize)
		}

		// Verify unpacks correctly
		unpacked, err := batch.UnpackBlobs(packed, cfg)
		if err != nil {
			t.Fatalf("unpack failed: %v", err)
		}

		if len(unpacked) != int(count) {
			t.Fatalf("count mismatch: got %d, want %d", len(unpacked), count)
		}

		for i, blob := range unpacked {
			if len(blob) != int(size) {
				t.Fatalf("blob %d size mismatch: got %d, want %d", i, len(blob), size)
			}
		}
	})
}

// FuzzBatchConcurrentAccess tests thread safety of pack/unpack
func FuzzBatchConcurrentAccess(f *testing.F) {
	cfg := batch.DefaultConfig()

	f.Add([]byte("concurrent test data"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			t.Skip()
		}

		blob := &db.Blob{ID: 1, Data: data}
		packed, err := batch.PackBlobs([]*db.Blob{blob}, cfg)
		if err != nil {
			t.Skip()
		}

		// Unpack concurrently multiple times
		// Should not have race conditions
		done := make(chan bool, 10)
		for i := 0; i < 10; i++ {
			go func() {
				unpacked, err := batch.UnpackBlobs(packed, cfg)
				if err != nil {
					t.Errorf("concurrent unpack failed: %v", err)
				}
				if len(unpacked) != 1 {
					t.Errorf("wrong blob count: %d", len(unpacked))
				}
				if len(unpacked) > 0 && !bytes.Equal(unpacked[0], data) {
					t.Errorf("data corruption in concurrent access")
				}
				done <- true
			}()
		}

		// Wait for all goroutines
		for i := 0; i < 10; i++ {
			<-done
		}
	})
}

// FuzzBatchEmptyAndBoundary tests empty and boundary conditions
func FuzzBatchEmptyAndBoundary(f *testing.F) {
	cfg := batch.DefaultConfig()

	// Seed with boundary cases
	f.Add([]byte{}, uint8(1))                        // Empty data
	f.Add([]byte{0x00}, uint8(1))                    // Single null
	f.Add([]byte{0xFF}, uint8(1))                    // Single 0xFF
	f.Add(make([]byte, 1), uint8(1))                 // One zero byte
	f.Add(make([]byte, cfg.MaxBatchSizeBytes/2), uint8(1)) // Half max size

	f.Fuzz(func(t *testing.T, data []byte, count uint8) {
		if count == 0 || count > 10 { // Keep small for fuzzing speed
			t.Skip()
		}

		blobs := make([]*db.Blob, count)
		for i := uint8(0); i < count; i++ {
			blobs[i] = &db.Blob{
				ID:   int64(i),
				Data: data, // All use same data
			}
		}

		// Should handle any data size gracefully
		packed, err := batch.PackBlobs(blobs, cfg)
		if err != nil {
			return // Expected for too-large data
		}

		unpacked, err := batch.UnpackBlobs(packed, cfg)
		if err != nil {
			t.Fatalf("unpack failed after successful pack: %v", err)
		}

		// Verify all blobs match
		for i := range unpacked {
			if !bytes.Equal(unpacked[i], data) {
				t.Fatalf("blob %d mismatch", i)
			}
		}
	})
}


