//go:build integration

package s3

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestS3Provider_PublicBucketIntegration(t *testing.T) {
	if os.Getenv("S3_INTEGRATION") == "" {
		t.Skip("set S3_INTEGRATION=1 to run")
	}

	ctx := context.Background()
	provider, err := NewS3Provider(ctx, Config{
		Bucket:         "caldera-celestia-cache-staging",
		Prefix:         "00000000000000000000000000000000000000ca1de12ac5a629c3c42f",
		Region:         "us-west-2",
		CredentialType: "anonymous",
		Timeout:        30 * time.Second,
	})
	require.NoError(t, err)
	require.True(t, provider.Available())

	// Known object from live bucket listing
	key := "00000000000000000000000000000000000000ca1de12ac5a629c3c42f/ce00002f000000000010e10b3a4ea8bd33f1a1d70f8872ef9611ad6af1051918e6b9ebe900680fb749"
	data, err := provider.getObject(ctx, key)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	t.Logf("retrieved %d bytes from %s", len(data), key)
}
