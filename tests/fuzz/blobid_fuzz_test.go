package fuzz

import (
	"bytes"
	"testing"

	celestia "github.com/celestiaorg/op-alt-da"
)

// FuzzBlobIDRoundTrip tests that any blob ID can be marshaled and unmarshaled
func FuzzBlobIDRoundTrip(f *testing.F) {
	// Seed with interesting values
	f.Add(uint64(0), []byte{0x00}, uint32(0), uint32(0))                    // All zeros
	f.Add(uint64(1), []byte{0x01}, uint32(1), uint32(1))                    // All ones
	f.Add(uint64(12345), []byte{0xAB, 0xCD}, uint32(100), uint32(200))     // Normal values
	f.Add(uint64(0xFFFFFFFFFFFFFFFF), []byte{0xFF}, uint32(0xFFFFFFFF), uint32(0xFFFFFFFF)) // Max values
	f.Add(uint64(1), make([]byte, 32), uint32(0), uint32(0))               // 32-byte commitment
	f.Add(uint64(999999), make([]byte, 32), uint32(12345), uint32(67890))  // Realistic values

	f.Fuzz(func(t *testing.T, height uint64, commitmentSeed []byte, shareOffset uint32, shareSize uint32) {
		// Create 32-byte commitment from seed
		commitment := make([]byte, 32)
		for i := 0; i < 32 && i < len(commitmentSeed); i++ {
			commitment[i] = commitmentSeed[i%len(commitmentSeed)]
		}

		// Create blob ID
		original := celestia.CelestiaBlobID{
			Height:      height,
			Commitment:  commitment,
			ShareOffset: shareOffset,
			ShareSize:   shareSize,
		}

		// Marshal
		marshaled, err := original.MarshalBinary()
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		// Should be either 40 bytes (compact) or 48 bytes (full)
		if len(marshaled) != 40 && len(marshaled) != 48 {
			t.Fatalf("unexpected marshaled size: %d", len(marshaled))
		}

		// Unmarshal
		var unmarshaled celestia.CelestiaBlobID
		err = unmarshaled.UnmarshalBinary(marshaled)
		if err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		// Verify roundtrip
		if unmarshaled.Height != original.Height {
			t.Fatalf("height mismatch: got %d, want %d", unmarshaled.Height, original.Height)
		}

		if !bytes.Equal(unmarshaled.Commitment, original.Commitment) {
			t.Fatalf("commitment mismatch: got %x, want %x", unmarshaled.Commitment, original.Commitment)
		}

		// Note: ShareOffset/ShareSize might be 0 if compact format was used
		if len(marshaled) == 48 {
			if unmarshaled.ShareOffset != original.ShareOffset {
				t.Fatalf("share offset mismatch: got %d, want %d", unmarshaled.ShareOffset, original.ShareOffset)
			}
			if unmarshaled.ShareSize != original.ShareSize {
				t.Fatalf("share size mismatch: got %d, want %d", unmarshaled.ShareSize, original.ShareSize)
			}
		}
	})
}

// FuzzBlobIDUnmarshalCorrupted tests that unmarshaling corrupted data fails gracefully
func FuzzBlobIDUnmarshalCorrupted(f *testing.F) {
	// Seed with various corrupted data
	f.Add([]byte{})                                        // Empty
	f.Add([]byte{0x00})                                   // Too short
	f.Add(make([]byte, 10))                               // Way too short
	f.Add(make([]byte, 39))                               // Just under compact size
	f.Add(make([]byte, 40))                               // Exactly compact size
	f.Add(make([]byte, 41))                               // Between sizes
	f.Add(make([]byte, 47))                               // Just under full size
	f.Add(make([]byte, 48))                               // Exactly full size
	f.Add(make([]byte, 49))                               // Oversized
	f.Add(make([]byte, 100))                              // Way oversized
	f.Add(bytes.Repeat([]byte{0xFF}, 48))                 // All 0xFF
	f.Add(bytes.Repeat([]byte{0x00}, 48))                 // All zeros

	// Create valid then corrupt
	validID := celestia.CelestiaBlobID{
		Height:      12345,
		Commitment:  make([]byte, 32),
		ShareOffset: 100,
		ShareSize:   200,
	}
	validMarshaled, _ := validID.MarshalBinary()
	f.Add(validMarshaled[:10])                            // Truncated
	f.Add(append(validMarshaled, 0xFF))                   // Extra byte

	f.Fuzz(func(t *testing.T, data []byte) {
		var blobID celestia.CelestiaBlobID
		err := blobID.UnmarshalBinary(data)

		// Should either succeed or return error, never panic
		if err != nil {
			// Expected for invalid data
			return
		}

		// If successful, verify basic sanity
		if len(blobID.Commitment) != 32 {
			t.Fatalf("commitment wrong size: %d", len(blobID.Commitment))
		}
	})
}

// FuzzBlobIDBackwardCompatibility tests that compact format can be read
func FuzzBlobIDBackwardCompatibility(f *testing.F) {
	f.Add(uint64(12345), []byte{0x01, 0x02, 0x03})

	f.Fuzz(func(t *testing.T, height uint64, commitmentSeed []byte) {
		// Create 32-byte commitment
		commitment := make([]byte, 32)
		for i := 0; i < 32 && i < len(commitmentSeed); i++ {
			commitment[i] = commitmentSeed[i%len(commitmentSeed)]
		}

		// Create full format ID
		fullID := celestia.CelestiaBlobID{
			Height:      height,
			Commitment:  commitment,
			ShareOffset: 999,
			ShareSize:   888,
		}

		fullMarshaled, err := fullID.MarshalBinary()
		if err != nil {
			t.Fatalf("full marshal failed: %v", err)
		}

		// Take only first 40 bytes (compact format)
		if len(fullMarshaled) < 40 {
			t.Skip("marshaled data too short")
		}
		compactData := fullMarshaled[:40]

		// Should unmarshal as compact format
		var compactID celestia.CelestiaBlobID
		err = compactID.UnmarshalBinary(compactData)
		if err != nil {
			t.Fatalf("compact unmarshal failed: %v", err)
		}

		// Height and commitment should match
		if compactID.Height != fullID.Height {
			t.Fatalf("height mismatch: got %d, want %d", compactID.Height, fullID.Height)
		}

		if !bytes.Equal(compactID.Commitment, fullID.Commitment) {
			t.Fatalf("commitment mismatch")
		}

		// Share fields should be zero for compact format
		if compactID.ShareOffset != 0 || compactID.ShareSize != 0 {
			t.Logf("note: compact format has non-zero share fields (offset=%d, size=%d)",
				compactID.ShareOffset, compactID.ShareSize)
		}
	})
}

// FuzzBlobIDCommitmentSize tests commitment size handling
func FuzzBlobIDCommitmentSize(f *testing.F) {
	f.Add([]byte{})                          // Empty
	f.Add([]byte{0x01})                      // Too short
	f.Add(make([]byte, 31))                  // Just under 32
	f.Add(make([]byte, 32))                  // Exactly 32
	f.Add(make([]byte, 33))                  // Just over 32
	f.Add(make([]byte, 64))                  // Double size
	f.Add(bytes.Repeat([]byte{0xFF}, 32))    // All 0xFF
	f.Add(bytes.Repeat([]byte{0x00}, 32))    // All zeros

	f.Fuzz(func(t *testing.T, commitment []byte) {
		// BlobID expects 32-byte commitment
		// Test what happens with other sizes

		blobID := celestia.CelestiaBlobID{
			Height:      12345,
			Commitment:  commitment,
			ShareOffset: 100,
			ShareSize:   200,
		}

		// Try to marshal
		marshaled, err := blobID.MarshalBinary()

		if len(commitment) != 32 {
			// Non-32-byte commitments might fail or be padded/truncated
			// Either behavior is acceptable, just shouldn't panic
			if err != nil {
				return
			}
			// If it succeeds, verify we can unmarshal
			var unmarshaled celestia.CelestiaBlobID
			err = unmarshaled.UnmarshalBinary(marshaled)
			if err != nil {
				t.Fatalf("unmarshal failed after successful marshal: %v", err)
			}
		} else {
			// 32-byte commitments should work perfectly
			if err != nil {
				t.Fatalf("marshal failed for 32-byte commitment: %v", err)
			}

			var unmarshaled celestia.CelestiaBlobID
			err = unmarshaled.UnmarshalBinary(marshaled)
			if err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			if !bytes.Equal(unmarshaled.Commitment, commitment) {
				t.Fatalf("commitment mismatch")
			}
		}
	})
}

// FuzzBlobIDEndianness tests byte order handling
func FuzzBlobIDEndianness(f *testing.F) {
	f.Add(uint64(0x0102030405060708), uint32(0x090A0B0C), uint32(0x0D0E0F10))

	f.Fuzz(func(t *testing.T, height uint64, shareOffset uint32, shareSize uint32) {
		commitment := make([]byte, 32)
		for i := range commitment {
			commitment[i] = byte(i)
		}

		blobID := celestia.CelestiaBlobID{
			Height:      height,
			Commitment:  commitment,
			ShareOffset: shareOffset,
			ShareSize:   shareSize,
		}

		marshaled, err := blobID.MarshalBinary()
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		var unmarshaled celestia.CelestiaBlobID
		err = unmarshaled.UnmarshalBinary(marshaled)
		if err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		// Verify all numeric fields are correctly decoded
		if unmarshaled.Height != height {
			t.Fatalf("height endianness issue: got %d, want %d", unmarshaled.Height, height)
		}

		if len(marshaled) == 48 {
			if unmarshaled.ShareOffset != shareOffset {
				t.Fatalf("shareOffset endianness issue: got %d, want %d", 
					unmarshaled.ShareOffset, shareOffset)
			}
			if unmarshaled.ShareSize != shareSize {
				t.Fatalf("shareSize endianness issue: got %d, want %d", 
					unmarshaled.ShareSize, shareSize)
			}
		}
	})
}

// FuzzBlobIDZeroValues tests handling of zero/default values
func FuzzBlobIDZeroValues(f *testing.F) {
	f.Add(uint8(0b00000001)) // Only height non-zero
	f.Add(uint8(0b00000010)) // Only commitment non-zero
	f.Add(uint8(0b00000100)) // Only shareOffset non-zero
	f.Add(uint8(0b00001000)) // Only shareSize non-zero
	f.Add(uint8(0b00001111)) // All non-zero
	f.Add(uint8(0b00000000)) // All zero

	f.Fuzz(func(t *testing.T, mask uint8) {
		height := uint64(0)
		if mask&0x01 != 0 {
			height = 12345
		}

		commitment := make([]byte, 32)
		if mask&0x02 != 0 {
			for i := range commitment {
				commitment[i] = 0xFF
			}
		}

		shareOffset := uint32(0)
		if mask&0x04 != 0 {
			shareOffset = 100
		}

		shareSize := uint32(0)
		if mask&0x08 != 0 {
			shareSize = 200
		}

		blobID := celestia.CelestiaBlobID{
			Height:      height,
			Commitment:  commitment,
			ShareOffset: shareOffset,
			ShareSize:   shareSize,
		}

		// Should handle zero values gracefully
		marshaled, err := blobID.MarshalBinary()
		if err != nil {
			t.Fatalf("marshal failed for mask %08b: %v", mask, err)
		}

		var unmarshaled celestia.CelestiaBlobID
		err = unmarshaled.UnmarshalBinary(marshaled)
		if err != nil {
			t.Fatalf("unmarshal failed for mask %08b: %v", mask, err)
		}

		// Verify roundtrip
		if unmarshaled.Height != height {
			t.Fatalf("height mismatch: got %d, want %d", unmarshaled.Height, height)
		}
		if !bytes.Equal(unmarshaled.Commitment, commitment) {
			t.Fatal("commitment mismatch")
		}
	})
}

// FuzzBlobIDMaxValues tests maximum value handling
func FuzzBlobIDMaxValues(f *testing.F) {
	f.Add(uint8(0xFF)) // Use as seed for all max values

	f.Fuzz(func(t *testing.T, seed uint8) {
		// Create values based on seed to vary the test
		height := uint64(0xFFFFFFFFFFFFFFFF)
		if seed&0x01 == 0 {
			height = 0
		}

		commitment := bytes.Repeat([]byte{seed}, 32)

		shareOffset := uint32(0xFFFFFFFF)
		if seed&0x02 == 0 {
			shareOffset = 0
		}

		shareSize := uint32(0xFFFFFFFF)
		if seed&0x04 == 0 {
			shareSize = 0
		}

		blobID := celestia.CelestiaBlobID{
			Height:      height,
			Commitment:  commitment,
			ShareOffset: shareOffset,
			ShareSize:   shareSize,
		}

		// Should handle max values without overflow
		marshaled, err := blobID.MarshalBinary()
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		var unmarshaled celestia.CelestiaBlobID
		err = unmarshaled.UnmarshalBinary(marshaled)
		if err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		// Verify no overflow occurred
		if unmarshaled.Height != height {
			t.Fatalf("height overflow: got %d, want %d", unmarshaled.Height, height)
		}
	})
}


