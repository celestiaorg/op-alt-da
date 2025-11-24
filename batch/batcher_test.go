package batch

import (
	"bytes"
	"testing"

	"github.com/celestiaorg/op-alt-da/db"
)

func testConfig() *Config {
	return DefaultConfig()
}

func TestPackBlobs_SingleBlob(t *testing.T) {
	cfg := testConfig()
	blobs := []*db.Blob{
		{
			ID:   1,
			Data: []byte("test data"),
		},
	}

	packed, err := PackBlobs(blobs, cfg)
	if err != nil {
		t.Fatalf("PackBlobs failed: %v", err)
	}

	// Verify format: [count(4)] [size(4)] [data]
	if len(packed) < 8 {
		t.Fatal("Packed data too short")
	}

	// Count should be 1
	count := uint32(packed[0])<<24 | uint32(packed[1])<<16 | uint32(packed[2])<<8 | uint32(packed[3])
	if count != 1 {
		t.Errorf("Expected count 1, got %d", count)
	}
}

func TestPackBlobs_MultipleBlobs(t *testing.T) {
	cfg := testConfig()
	blobs := []*db.Blob{
		{ID: 1, Data: []byte("blob1")},
		{ID: 2, Data: []byte("blob2")},
		{ID: 3, Data: []byte("blob3")},
	}

	packed, err := PackBlobs(blobs, cfg)
	if err != nil {
		t.Fatalf("PackBlobs failed: %v", err)
	}

	if len(packed) == 0 {
		t.Fatal("Packed data is empty")
	}
}

func TestPackUnpack_RoundTrip(t *testing.T) {
	cfg := testConfig()
	originalBlobs := []*db.Blob{
		{ID: 1, Data: []byte("first blob data")},
		{ID: 2, Data: []byte("second blob data")},
		{ID: 3, Data: []byte("third blob data")},
	}

	// Pack
	packed, err := PackBlobs(originalBlobs, cfg)
	if err != nil {
		t.Fatalf("PackBlobs failed: %v", err)
	}

	// Unpack
	unpacked, err := UnpackBlobs(packed, cfg)
	if err != nil {
		t.Fatalf("UnpackBlobs failed: %v", err)
	}

	// Verify count
	if len(unpacked) != len(originalBlobs) {
		t.Errorf("Expected %d blobs, got %d", len(originalBlobs), len(unpacked))
	}

	// Verify data
	for i, original := range originalBlobs {
		if !bytes.Equal(unpacked[i], original.Data) {
			t.Errorf("Blob %d: expected %q, got %q", i, original.Data, unpacked[i])
		}
	}
}

func TestPackBlobs_EmptyList(t *testing.T) {
	cfg := testConfig()
	blobs := []*db.Blob{}

	_, err := PackBlobs(blobs, cfg)
	if err == nil {
		t.Error("Expected error for empty blob list")
	}
}

func TestPackBlobs_TooManyBlobs(t *testing.T) {
	cfg := testConfig()
	// Create more than MaxBlobs
	blobs := make([]*db.Blob, cfg.MaxBlobs+1)
	for i := range blobs {
		blobs[i] = &db.Blob{
			ID:   int64(i),
			Data: []byte("data"),
		}
	}

	_, err := PackBlobs(blobs, cfg)
	if err == nil {
		t.Error("Expected error for too many blobs")
	}
}

func TestPackBlobs_TooLarge(t *testing.T) {
	cfg := testConfig()
	// Create blob that exceeds MaxBatchSizeBytes
	largeData := make([]byte, cfg.MaxBatchSizeBytes+1)
	blobs := []*db.Blob{
		{ID: 1, Data: largeData},
	}

	_, err := PackBlobs(blobs, cfg)
	if err == nil {
		t.Error("Expected error for oversized batch")
	}
}

func TestUnpackBlobs_InvalidData(t *testing.T) {
	cfg := testConfig()
	// Too short
	_, err := UnpackBlobs([]byte{0, 0, 0}, cfg)
	if err == nil {
		t.Error("Expected error for invalid data")
	}

	// Zero count
	invalidCount := []byte{0, 0, 0, 0}
	_, err = UnpackBlobs(invalidCount, cfg)
	if err == nil {
		t.Error("Expected error for zero count")
	}

	// Count too large
	tooManyBlobs := []byte{0, 0, 0, 100} // 100 blobs but no data
	_, err = UnpackBlobs(tooManyBlobs, cfg)
	if err == nil {
		t.Error("Expected error for incomplete data")
	}
}

func TestShouldBatch_EnoughBlobs(t *testing.T) {
	cfg := testConfig()
	// MinBlobs (10) should trigger batch
	if !cfg.ShouldBatch(cfg.MinBlobs, 1000) {
		t.Errorf("Expected batch for %d blobs", cfg.MinBlobs)
	}

	// Less than MinBlobs should not trigger
	if cfg.ShouldBatch(cfg.MinBlobs-1, 1000) {
		t.Errorf("Expected no batch for %d blobs", cfg.MinBlobs-1)
	}
}

func TestShouldBatch_LargeSize(t *testing.T) {
	cfg := testConfig()
	// >= MinBatchSizeBytes should trigger batch even with few blobs
	if !cfg.ShouldBatch(3, cfg.MinBatchSizeBytes) {
		t.Error("Expected batch for large size")
	}

	// < MinBatchSizeBytes with few blobs should not trigger
	if cfg.ShouldBatch(3, cfg.MinBatchSizeBytes-1) {
		t.Error("Expected no batch for small size")
	}
}

func TestPackUnpack_VariousSizes(t *testing.T) {
	cfg := testConfig()
	testCases := []struct {
		name      string
		blobSizes []int
	}{
		{"tiny", []int{1, 2, 3}},
		{"small", []int{100, 200, 300}},
		{"medium", []int{1024, 2048, 4096}},
		{"large", []int{10000, 20000, 30000}},
		{"mixed", []int{1, 1000, 10000, 100}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			blobs := make([]*db.Blob, len(tc.blobSizes))
			for i, size := range tc.blobSizes {
				data := make([]byte, size)
				for j := range data {
					data[j] = byte(j % 256)
				}
				blobs[i] = &db.Blob{
					ID:   int64(i),
					Data: data,
				}
			}

			// Pack
			packed, err := PackBlobs(blobs, cfg)
			if err != nil {
				t.Fatalf("PackBlobs failed: %v", err)
			}

			// Unpack
			unpacked, err := UnpackBlobs(packed, cfg)
			if err != nil {
				t.Fatalf("UnpackBlobs failed: %v", err)
			}

			// Verify
			if len(unpacked) != len(blobs) {
				t.Fatalf("Expected %d blobs, got %d", len(blobs), len(unpacked))
			}

			for i, blob := range blobs {
				if !bytes.Equal(unpacked[i], blob.Data) {
					t.Errorf("Blob %d data mismatch", i)
				}
			}
		})
	}
}

func TestPackUnpack_MaxBlobs(t *testing.T) {
	cfg := testConfig()
	// Test with exactly MaxBlobs
	blobs := make([]*db.Blob, cfg.MaxBlobs)
	for i := range blobs {
		blobs[i] = &db.Blob{
			ID:   int64(i),
			Data: []byte{byte(i)},
		}
	}

	packed, err := PackBlobs(blobs, cfg)
	if err != nil {
		t.Fatalf("PackBlobs failed: %v", err)
	}

	unpacked, err := UnpackBlobs(packed, cfg)
	if err != nil {
		t.Fatalf("UnpackBlobs failed: %v", err)
	}

	if len(unpacked) != cfg.MaxBlobs {
		t.Errorf("Expected %d blobs, got %d", cfg.MaxBlobs, len(unpacked))
	}
}


