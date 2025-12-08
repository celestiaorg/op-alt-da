package celestia

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCelestiaBlobIDMarshalUnmarshal(t *testing.T) {
	rnd := rand.New(rand.NewSource(123))

	// Test case 1: Full format data
	commitment1 := make([]byte, CommitmentSize)
	rnd.Read(commitment1)
	id1 := CelestiaBlobID{
		Height:      rnd.Uint64(),
		Commitment:  commitment1,
		ShareOffset: rnd.Uint32(),
		ShareSize:   rnd.Uint32(),
	}

	marshaled1, err := id1.MarshalBinary()
	require.NoError(t, err)
	require.Len(t, marshaled1, FullBlobIDSize)

	var unmarshaled1 CelestiaBlobID
	err = unmarshaled1.UnmarshalBinary(marshaled1)
	require.NoError(t, err)
	assert.Equal(t, id1.Height, unmarshaled1.Height)
	assert.Equal(t, id1.ShareOffset, unmarshaled1.ShareOffset)
	assert.Equal(t, id1.ShareSize, unmarshaled1.ShareSize)
	assert.True(t, bytes.Equal(id1.Commitment, unmarshaled1.Commitment))

	// Test case 2: Zero values (still valid)
	commitment2 := make([]byte, CommitmentSize)
	id2 := CelestiaBlobID{
		Height:      0,
		Commitment:  commitment2,
		ShareOffset: 0,
		ShareSize:   0,
	}

	marshaled2, err := id2.MarshalBinary()
	require.NoError(t, err)
	require.Len(t, marshaled2, FullBlobIDSize)

	var unmarshaled2 CelestiaBlobID
	err = unmarshaled2.UnmarshalBinary(marshaled2)
	require.NoError(t, err)
	assert.Equal(t, id2.Height, unmarshaled2.Height)
	assert.Equal(t, id2.ShareOffset, unmarshaled2.ShareOffset)
	assert.Equal(t, id2.ShareSize, unmarshaled2.ShareSize)
	assert.True(t, bytes.Equal(id2.Commitment, unmarshaled2.Commitment))

	// Test case 3: Compact format (height + commitment only = 40 bytes)
	commitment3 := make([]byte, CommitmentSize)
	rnd.Read(commitment3)
	id3 := CelestiaBlobID{
		Height:      rnd.Uint64(),
		Commitment:  commitment3,
		ShareOffset: rnd.Uint32(),
		ShareSize:   rnd.Uint32(),
	}
	marshaled3, err := id3.MarshalBinary()
	require.NoError(t, err)

	// Unmarshal only the compact portion (first 40 bytes)
	var unmarshaled3 CelestiaBlobID
	err = unmarshaled3.UnmarshalBinary(marshaled3[:CompactBlobIDSize])
	require.NoError(t, err)
	assert.Equal(t, id3.Height, unmarshaled3.Height)
	assert.True(t, bytes.Equal(id3.Commitment, unmarshaled3.Commitment))
	// Compact format should have zero share offset/size
	assert.Equal(t, uint32(0), unmarshaled3.ShareOffset)
	assert.Equal(t, uint32(0), unmarshaled3.ShareSize)
}

func TestCelestiaBlobIDValidation(t *testing.T) {
	// Test case 1: Nil commitment should fail validation
	id1 := CelestiaBlobID{
		Height:     12345,
		Commitment: nil,
	}
	err := id1.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commitment cannot be nil")

	// Test case 2: Wrong commitment size should fail validation
	id2 := CelestiaBlobID{
		Height:     12345,
		Commitment: make([]byte, 16), // Wrong size
	}
	err = id2.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commitment must be")

	// Test case 3: Valid commitment should pass validation
	id3 := CelestiaBlobID{
		Height:     12345,
		Commitment: make([]byte, CommitmentSize),
	}
	err = id3.Validate()
	require.NoError(t, err)
}

func TestCelestiaBlobIDMarshalValidationErrors(t *testing.T) {
	// Test case 1: Nil commitment should fail marshal
	id1 := CelestiaBlobID{
		Height:     12345,
		Commitment: nil,
	}
	_, err := id1.MarshalBinary()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal validation failed")

	// Test case 2: Wrong commitment size should fail marshal
	id2 := CelestiaBlobID{
		Height:     12345,
		Commitment: make([]byte, 16), // Wrong size
	}
	_, err = id2.MarshalBinary()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal validation failed")
}

func TestCelestiaBlobIDUnmarshalErrors(t *testing.T) {
	// Test case 1: Data too short for compact format
	shortData := make([]byte, CompactBlobIDSize-1) // 39 bytes, need 40
	var id1 CelestiaBlobID
	err := id1.UnmarshalBinary(shortData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ID length")

	// Test case 2: Very short data
	veryShortData := make([]byte, 10)
	var id2 CelestiaBlobID
	err = id2.UnmarshalBinary(veryShortData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ID length")

	// Test case 3: Empty data
	var id3 CelestiaBlobID
	err = id3.UnmarshalBinary([]byte{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ID length")
}

func TestCelestiaBlobIDConstants(t *testing.T) {
	// Verify constants have expected values
	assert.Equal(t, 8, HeightSize, "Height should be 8 bytes (uint64)")
	assert.Equal(t, 32, CommitmentSize, "Commitment should be 32 bytes")
	assert.Equal(t, 4, ShareOffsetSize, "ShareOffset should be 4 bytes (uint32)")
	assert.Equal(t, 4, ShareSizeSize, "ShareSize should be 4 bytes (uint32)")

	// Verify computed sizes
	assert.Equal(t, 40, CompactBlobIDSize, "Compact format: 8 + 32 = 40 bytes")
	assert.Equal(t, 48, FullBlobIDSize, "Full format: 8 + 32 + 4 + 4 = 48 bytes")
}

