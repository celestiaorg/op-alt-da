package commitment

import (
	"fmt"

	"github.com/celestiaorg/celestia-node/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
)

// ComputeCommitment computes a deterministic commitment for a blob
// using the celestia-node blob API with V1 (signed blobs for CIP-21)
func ComputeCommitment(data []byte, namespace libshare.Namespace) ([]byte, error) {
	// Create blob using BlobV1 for signed blobs (CIP-21)
	// BlobV1 requires a 20-byte signer (Celestia address format)
	// Use dummy signer for commitment computation (actual signer added during Submit)
	dummySigner := make([]byte, 20) // 20-byte zero address

	b, err := blob.NewBlobV1(namespace, data, dummySigner)
	if err != nil {
		return nil, fmt.Errorf("create blob: %w", err)
	}

	// Return the commitment (already computed by NewBlobV1)
	return b.Commitment, nil
}
