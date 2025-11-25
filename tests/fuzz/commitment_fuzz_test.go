package fuzz

import (
	"bytes"
	"testing"

	"github.com/celestiaorg/op-alt-da/commitment"
	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/stretchr/testify/require"
)

// Helper to create test namespace
func testNamespace() libshare.Namespace {
	nsBytes := []byte{
		0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
	}
	ns, _ := libshare.NewNamespaceFromBytes(nsBytes)
	return ns
}

// FuzzCommitmentDeterminism tests that commitment computation is deterministic
// for any input data
func FuzzCommitmentDeterminism(f *testing.F) {
	ns := testNamespace()

	// Seed corpus with diverse inputs
	f.Add([]byte("test data"))
	f.Add([]byte(""))                                    // Empty
	f.Add([]byte{0x00})                                  // Single null
	f.Add([]byte{0xFF})                                  // Single 0xFF
	f.Add(bytes.Repeat([]byte{0x00}, 1000))              // All zeros
	f.Add(bytes.Repeat([]byte{0xFF}, 1000))              // All 0xFF
	f.Add([]byte{0xAA, 0x55, 0xAA, 0x55})                // Alternating
	f.Add([]byte("unicode: 世界 🌍"))                     // Unicode
	f.Add([]byte{0x01, 0x02, 0x03, 0x04, 0x05})          // Sequential
	f.Add([]byte("\x00\x01\x02\x03\x04\x05\x06\x07"))   // Low bytes
	f.Add([]byte("\xF8\xF9\xFA\xFB\xFC\xFD\xFE\xFF"))   // High bytes
	f.Add([]byte("normal text with spaces"))             // Normal
	f.Add([]byte("<script>alert('xss')</script>"))       // XSS-like
	f.Add([]byte("../../../etc/passwd"))                 // Path traversal
	f.Add([]byte("\r\n\r\n"))                            // CRLF
	f.Add(bytes.Repeat([]byte("a"), 1024*1024))          // 1MB
	f.Add(bytes.Repeat([]byte{0x01}, 1024*1024))         // 1MB binary
	f.Add([]byte{0})                                     // Single zero
	f.Add(make([]byte, 100000))                          // Large zeros

	f.Fuzz(func(t *testing.T, data []byte) {
		// Compute commitment twice
		commitment1, err1 := commitment.ComputeCommitment(data, ns, make([]byte, 20))
		commitment2, err2 := commitment.ComputeCommitment(data, ns, make([]byte, 20))

		// Both should have same error status
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("inconsistent errors: err1=%v, err2=%v", err1, err2)
		}

		// If successful, commitments must be identical (determinism)
		if err1 == nil {
			if !bytes.Equal(commitment1, commitment2) {
				t.Fatalf("non-deterministic commitment: %x != %x", commitment1, commitment2)
			}

			// Commitment should be non-empty
			if len(commitment1) == 0 {
				t.Fatal("commitment is empty")
			}

			// Commitment should be exactly 32 bytes (for Celestia)
			if len(commitment1) != 32 {
				t.Fatalf("commitment wrong size: got %d, want 32", len(commitment1))
			}
		}
	})
}

// FuzzCommitmentUniqueness tests that different data produces different commitments
func FuzzCommitmentUniqueness(f *testing.F) {
	ns := testNamespace()

	// Seed with pairs of similar but different data
	f.Add([]byte("data1"), []byte("data2"))
	f.Add([]byte("a"), []byte("b"))
	f.Add([]byte{0x00}, []byte{0x01})
	f.Add([]byte{0xFF}, []byte{0xFE})
	f.Add([]byte("test"), []byte("test2"))
	f.Add([]byte("TEST"), []byte("test"))                    // Case difference
	f.Add([]byte("data "), []byte("data"))                    // Trailing space
	f.Add([]byte{0x00, 0x01}, []byte{0x01, 0x00})            // Byte order
	f.Add([]byte("abc"), []byte("abcd"))                      // Length diff
	f.Add(bytes.Repeat([]byte{0x00}, 10), bytes.Repeat([]byte{0x01}, 10))

	f.Fuzz(func(t *testing.T, data1, data2 []byte) {
		// Skip if data is identical
		if bytes.Equal(data1, data2) {
			t.Skip("identical data")
		}

		commitment1, err1 := commitment.ComputeCommitment(data1, ns, make([]byte, 20))
		commitment2, err2 := commitment.ComputeCommitment(data2, ns, make([]byte, 20))

		// Both should succeed or both should fail
		if err1 != nil || err2 != nil {
			return // Skip if either fails
		}

		// Different data should produce different commitments
		// Note: hash collisions are theoretically possible but astronomically unlikely
		if bytes.Equal(commitment1, commitment2) {
			t.Fatalf("collision detected! Different data produced same commitment:\n"+
				"data1: %x (len=%d)\n"+
				"data2: %x (len=%d)\n"+
				"commitment: %x",
				data1, len(data1), data2, len(data2), commitment1)
		}
	})
}

// FuzzCommitmentSize tests that commitment is always exactly 32 bytes
func FuzzCommitmentSize(f *testing.F) {
	ns := testNamespace()

	// Seed with various sizes
	f.Add([]byte{0x01})
	f.Add(make([]byte, 0))       // Empty
	f.Add(make([]byte, 1))        // 1 byte
	f.Add(make([]byte, 31))       // Just under 32
	f.Add(make([]byte, 32))       // Exactly 32
	f.Add(make([]byte, 33))       // Just over 32
	f.Add(make([]byte, 100))      // Small
	f.Add(make([]byte, 1024))     // 1KB
	f.Add(make([]byte, 10000))    // 10KB
	f.Add(make([]byte, 100000))   // 100KB
	f.Add(make([]byte, 1000000))  // 1MB

	f.Fuzz(func(t *testing.T, data []byte) {
		commitment, err := commitment.ComputeCommitment(data, ns, make([]byte, 20))

		if err != nil {
			// Some sizes might be rejected, which is fine
			return
		}

		// Invariant: commitment is ALWAYS 32 bytes
		if len(commitment) != 32 {
			t.Fatalf("commitment size invariant violated: got %d bytes, want 32 (data size: %d)",
				len(commitment), len(data))
		}

		// Commitment should not be all zeros (unless data is specifically crafted)
		allZeros := true
		for _, b := range commitment {
			if b != 0 {
				allZeros = false
				break
			}
		}
		if allZeros && len(data) > 0 {
			t.Logf("warning: commitment is all zeros for non-empty data (len=%d)", len(data))
		}
	})
}

// FuzzCommitmentStability tests that commitment remains stable across calls
// This catches any hidden state or randomness
func FuzzCommitmentStability(f *testing.F) {
	ns := testNamespace()

	f.Add([]byte("stability test"))
	f.Add(make([]byte, 1000))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Compute commitment multiple times in sequence
		commitments := make([][]byte, 10)
		for i := 0; i < 10; i++ {
			c, err := commitment.ComputeCommitment(data, ns, make([]byte, 20))
			if err != nil {
				return
			}
			commitments[i] = c
		}

		// All should be identical
		reference := commitments[0]
		for i := 1; i < 10; i++ {
			if !bytes.Equal(reference, commitments[i]) {
				t.Fatalf("commitment unstable: call 0 (%x) != call %d (%x)",
					reference, i, commitments[i])
			}
		}
	})
}

// FuzzCommitmentBytePatterns tests special byte patterns that might cause issues
func FuzzCommitmentBytePatterns(f *testing.F) {
	ns := testNamespace()

	// Seed with problematic patterns
	f.Add([]byte{0x00}, uint8(100))               // Repeated nulls
	f.Add([]byte{0xFF}, uint8(100))               // Repeated 0xFF
	f.Add([]byte{0xAA}, uint8(100))               // 10101010 pattern
	f.Add([]byte{0x55}, uint8(100))               // 01010101 pattern
	f.Add([]byte{0x01}, uint8(255))               // Max repeats
	f.Add([]byte{0x00, 0xFF}, uint8(100))         // Alternating 0/255
	f.Add([]byte{0x01, 0x02, 0x04, 0x08}, uint8(50)) // Powers of 2

	f.Fuzz(func(t *testing.T, pattern []byte, repeats uint8) {
		if len(pattern) == 0 {
			t.Skip()
		}

		// Create data by repeating pattern
		data := bytes.Repeat(pattern, int(repeats))

		comm, err := commitment.ComputeCommitment(data, ns, make([]byte, 20))
		if err != nil {
			return // Some patterns might be invalid
		}

		// Should always be 32 bytes
		require.Len(t, comm, 32)

		// Should be deterministic
		comm2, _ := commitment.ComputeCommitment(data, ns, make([]byte, 20))
		require.Equal(t, comm, comm2)
	})
}

// FuzzCommitmentBoundaries tests boundary conditions
func FuzzCommitmentBoundaries(f *testing.F) {
	ns := testNamespace()

	// Seed with size boundaries
	f.Add(uint32(0))          // Empty
	f.Add(uint32(1))          // Single byte
	f.Add(uint32(31))         // Just under commitment size
	f.Add(uint32(32))         // Equal to commitment size
	f.Add(uint32(33))         // Just over commitment size
	f.Add(uint32(63))         // Double commitment size - 1
	f.Add(uint32(64))         // Double commitment size
	f.Add(uint32(65))         // Double commitment size + 1
	f.Add(uint32(127))        // Power of 2 - 1
	f.Add(uint32(128))        // Power of 2
	f.Add(uint32(255))        // uint8 max
	f.Add(uint32(256))        // Just over uint8
	f.Add(uint32(1023))       // Just under 1KB
	f.Add(uint32(1024))       // 1KB
	f.Add(uint32(1025))       // Just over 1KB
	f.Add(uint32(65535))      // uint16 max
	f.Add(uint32(65536))      // Just over uint16
	f.Add(uint32(1024 * 1024)) // 1MB

	f.Fuzz(func(t *testing.T, size uint32) {
		// Cap size for fuzzing performance
		if size > 10*1024*1024 {
			t.Skip("size too large for fuzzing")
		}

		data := make([]byte, size)
		// Fill with predictable pattern
		for i := range data {
			data[i] = byte(i % 256)
		}

		commitment, err := commitment.ComputeCommitment(data, ns, make([]byte, 20))
		if err != nil {
			return
		}

		require.Len(t, commitment, 32, "commitment size invariant violated for data size %d", size)
	})
}

// FuzzCommitmentConcurrent tests concurrent commitment computation
func FuzzCommitmentConcurrent(f *testing.F) {
	ns := testNamespace()

	f.Add([]byte("concurrent test"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			t.Skip()
		}

		// Compute commitment once to get reference
		reference, err := commitment.ComputeCommitment(data, ns, make([]byte, 20))
		if err != nil {
			t.Skip()
		}

		// Compute concurrently
		results := make(chan []byte, 20)
		errors := make(chan error, 20)

		for i := 0; i < 20; i++ {
			go func() {
				c, err := commitment.ComputeCommitment(data, ns, make([]byte, 20))
				if err != nil {
					errors <- err
					return
				}
				results <- c
			}()
		}

		// Collect results
		for i := 0; i < 20; i++ {
			select {
			case c := <-results:
				if !bytes.Equal(reference, c) {
					t.Fatalf("concurrent computation mismatch: %x != %x", reference, c)
				}
			case err := <-errors:
				t.Fatalf("concurrent computation error: %v", err)
			}
		}
	})
}

// FuzzCommitmentMemoryCorruption tests for buffer overruns and memory issues
func FuzzCommitmentMemoryCorruption(f *testing.F) {
	ns := testNamespace()

	// Seed with patterns that might cause issues
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
	f.Add([]byte{0x00, 0x00, 0x00, 0x00, 0x00})
	f.Add([]byte{0x7F, 0xFF, 0xFF, 0xFF, 0xFF}) // Max int32
	f.Add([]byte{0x80, 0x00, 0x00, 0x00, 0x00}) // Min int32

	f.Fuzz(func(t *testing.T, data []byte) {
		// Try to trigger memory issues
		for i := 0; i < 3; i++ {
			commitment, err := commitment.ComputeCommitment(data, ns, make([]byte, 20))
			if err == nil {
				// Basic sanity checks
				if len(commitment) != 32 {
					t.Fatal("commitment size changed")
				}
				
				// Ensure we can read all bytes without panic
				sum := 0
				for _, b := range commitment {
					sum += int(b)
				}
			}
		}
	})
}

