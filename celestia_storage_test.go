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

	// Test case 1: Full data
	commitment1 := make([]byte, 32)
	rnd.Read(commitment1)
	id1 := CelestiaBlobID{
		Height:      rnd.Uint64(),
		Commitment:  commitment1,
		ShareOffset: rnd.Uint32(),
		ShareSize:   rnd.Uint32(),
	}

	marshaled1, err := id1.MarshalBinary()
	require.NoError(t, err)
	require.Len(t, marshaled1, 48)

	var unmarshaled1 CelestiaBlobID
	err = unmarshaled1.UnmarshalBinary(marshaled1)
	require.NoError(t, err)
	assert.Equal(t, id1.Height, unmarshaled1.Height)
	assert.Equal(t, id1.ShareOffset, unmarshaled1.ShareOffset)
	assert.Equal(t, id1.ShareSize, unmarshaled1.ShareSize)
	assert.True(t, bytes.Equal(id1.Commitment, unmarshaled1.Commitment))

	// Test case 2: Zero values
	commitment2 := make([]byte, 32)
	id2 := CelestiaBlobID{
		Height:      0,
		Commitment:  commitment2,
		ShareOffset: 0,
		ShareSize:   0,
	}

	marshaled2, err := id2.MarshalBinary()
	require.NoError(t, err)
	require.Len(t, marshaled2, 48)

	var unmarshaled2 CelestiaBlobID
	err = unmarshaled2.UnmarshalBinary(marshaled2)
	require.NoError(t, err)
	assert.Equal(t, id2.Height, unmarshaled2.Height)
	assert.Equal(t, id2.ShareOffset, unmarshaled2.ShareOffset)
	assert.Equal(t, id2.ShareSize, unmarshaled2.ShareSize)
	assert.True(t, bytes.Equal(id2.Commitment, unmarshaled2.Commitment))

	// Test case 3: Invalid length for UnmarshalBinary
	invalidData := make([]byte, 32) // Too short
	var invalidID CelestiaBlobID
	err = invalidID.UnmarshalBinary(invalidData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ID length")
}
