package commitment

import (
	"fmt"

	"github.com/celestiaorg/celestia-node/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
)

// ComputeCommitment computes a deterministic commitment for a blob
// using the celestia-node blob API
func ComputeCommitment(data []byte, namespace libshare.Namespace) ([]byte, error) {
	// Create blob using celestia-node's NewBlob function
	// This automatically computes the commitment
	b, err := blob.NewBlob(libshare.ShareVersionZero, namespace, data, nil)
	if err != nil {
		return nil, fmt.Errorf("create blob: %w", err)
	}

	// Return the commitment (already computed by NewBlob)
	return b.Commitment, nil
}
