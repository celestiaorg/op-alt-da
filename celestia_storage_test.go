package celestia

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/rand"
	"testing"

	"github.com/celestiaorg/celestia-node/blob"
	blobAPI "github.com/celestiaorg/celestia-node/nodebuilder/blob"
	libshare "github.com/celestiaorg/go-square/v2/share"
	altda "github.com/ethereum-optimism/optimism/op-alt-da"
	"github.com/ethereum/go-ethereum/crypto"
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

	// Test case 3: Legacy id format (height, commitment) 8 + 32 = 40 bytes
	commitment3 := make([]byte, 32)
	rnd.Read(commitment1)
	id3 := CelestiaBlobID{
		Height:      rnd.Uint64(),
		Commitment:  commitment3,
		ShareOffset: rnd.Uint32(),
		ShareSize:   rnd.Uint32(),
	}
	marshaled3, err := id3.MarshalBinary()
	require.NoError(t, err)
	var unmarshaled3 CelestiaBlobID
	err = unmarshaled3.UnmarshalBinary(marshaled3[:40])
	require.NoError(t, err)
	require.Equal(t, unmarshaled3.ShareOffset, uint32(0))
	require.Equal(t, unmarshaled3.ShareSize, uint32(0))

	// Test case 4: Invalid length for UnmarshalBinary
	invalidData := make([]byte, 32) // Too short
	var invalidID CelestiaBlobID
	err = invalidID.UnmarshalBinary(invalidData)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ID length")
}

func TestCelestiaBlobHandlign(t *testing.T) {
	data := make([]byte, 256)
	for i := 0; i < 256; i++ {
		data[i] = byte(i)
	}
	var blobClient blobAPI.Module
	var submitFunc = func(_ context.Context, _ blobAPI.Module, _ []*blob.Blob) (uint64, error) {
		return 10, nil
	}
	ns, _ := hex.DecodeString("00000000000000000000000000000000000000ca1de12a9fbe8a44f662")
	var namespace, _ = libshare.NewNamespaceFromBytes(ns)

	c, blobData, _ := SubmitAndCreateBlobID(context.Background(), blobClient, submitFunc, namespace, data, false)
	fmt.Printf("c: %v\n", c)
	fmt.Printf("d: %v\n", blobData)
	commitment := altda.NewGenericCommitment(append([]byte{VersionByte}, c...))
	fmt.Printf("commitment.Encode(): %v\n", commitment.Encode())
	fmt.Printf("blobData: %v\n", blobData)

	//key for s3
	key := crypto.Keccak256(commitment.Encode())
	fmt.Printf("s3 key: %v\n", key)
	s3Key := hex.EncodeToString(key)
	fmt.Printf("s3 key encoded: %v\n", s3Key)
}
