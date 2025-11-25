package commitment

import (
	"fmt"

	"github.com/celestiaorg/celestia-node/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
)

// ComputeCommitment computes a deterministic commitment for a blob
// using the celestia-node blob API with V1 (signed blobs for CIP-21)
// IMPORTANT: The signer address is part of the blob structure and affects the commitment.
// The same signer used here MUST be used when submitting to Celestia, otherwise commitments won't match.
func ComputeCommitment(data []byte, namespace libshare.Namespace, signerAddr []byte) ([]byte, error) {
	// Create blob using BlobV1 for signed blobs (CIP-21)
	// BlobV1 requires a 20-byte signer (Celestia address format)
	if len(signerAddr) != 20 {
		return nil, fmt.Errorf("invalid signer address: expected 20 bytes, got %d", len(signerAddr))
	}

	b, err := blob.NewBlobV1(namespace, data, signerAddr)
	if err != nil {
		return nil, fmt.Errorf("create blob: %w", err)
	}

	// Return the commitment (already computed by NewBlobV1)
	return b.Commitment, nil
}
