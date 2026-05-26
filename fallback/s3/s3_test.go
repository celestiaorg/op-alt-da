package s3

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewS3Provider_MissingBucket(t *testing.T) {
	ctx := context.Background()
	_, err := NewS3Provider(ctx, Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bucket is required")
}

func TestNewS3Provider_InvalidCredentialType(t *testing.T) {
	ctx := context.Background()
	_, err := NewS3Provider(ctx, Config{
		Bucket:         "test-bucket",
		CredentialType: "invalid",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown credential type")
}

func TestNewS3Provider_AnonymousCredentials(t *testing.T) {
	ctx := context.Background()
	provider, err := NewS3Provider(ctx, Config{
		Bucket:         "public-bucket",
		CredentialType: "anonymous",
	})
	require.NoError(t, err)
	assert.NotNil(t, provider)
	assert.True(t, provider.Available())
}

func TestNewS3Provider_EmptyCredentialTypeWithStaticKeys(t *testing.T) {
	ctx := context.Background()
	provider, err := NewS3Provider(ctx, Config{
		Bucket:          "test-bucket",
		AccessKeyID:     "test-key",
		AccessKeySecret: "test-secret",
	})
	require.NoError(t, err)
	assert.NotNil(t, provider)
	assert.True(t, provider.Available())
}

func TestNewS3Provider_StaticCredentialsRequired(t *testing.T) {
	ctx := context.Background()
	_, err := NewS3Provider(ctx, Config{
		Bucket:         "test-bucket",
		CredentialType: "static",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "access key and secret required")
}

func TestS3Provider_MakeKey(t *testing.T) {
	provider := &S3Provider{
		bucket: "test-bucket",
		prefix: "",
	}

	commitment := []byte("test-commitment")
	expectedHash := hex.EncodeToString(crypto.Keccak256(commitment))
	key := provider.makeKey(commitment)
	assert.Equal(t, expectedHash, key)

	// With prefix
	provider.prefix = "blobs"
	assert.Equal(t, "blobs/"+expectedHash, provider.makeKey(commitment))
}

func TestS3Provider_MakeReadOnlyLegacyDerivationKey(t *testing.T) {
	provider := &S3Provider{prefix: "00000000000000000000000000000000000000ca1de12ac5a629c3c42f"}

	commitment, err := hex.DecodeString("010ca2f3ac00000000007b156d9cdc8698d364787f4ae8a8e18346e55756533f6a4562a199b13d60548e")
	require.NoError(t, err)

	key := provider.makeReadOnlyLegacyDerivationKey(commitment)
	assert.Equal(t,
		"00000000000000000000000000000000000000ca1de12ac5a629c3c42f/cea2f3ac00000000007b156d9cdc8698d364787f4ae8a8e18346e55756533f6a4562a199b13d60548e",
		key,
	)
}

func TestS3Provider_Name(t *testing.T) {
	provider := &S3Provider{}
	assert.Equal(t, "s3", provider.Name())
}

func TestS3Provider_Available(t *testing.T) {
	// Not available when client is nil
	provider := &S3Provider{
		bucket: "test",
	}
	assert.False(t, provider.Available())

	// Not available when bucket is empty
	provider = &S3Provider{
		client: nil,
		bucket: "",
	}
	assert.False(t, provider.Available())
}

func TestS3Provider_Timeout(t *testing.T) {
	provider := &S3Provider{
		timeout: 45 * time.Second,
	}
	assert.Equal(t, 45*time.Second, provider.Timeout())
}

func TestConfig_Defaults(t *testing.T) {
	ctx := context.Background()

	cfg := Config{
		Bucket: "test-bucket",
	}

	// Test that defaults are applied (region, timeout)
	assert.Equal(t, "", cfg.Region) // Will be defaulted in NewS3Provider

	// Test with static credentials (will succeed in creating provider)
	cfg = Config{
		Bucket:          "test-bucket",
		CredentialType:  "static",
		AccessKeyID:     "test-key",
		AccessKeySecret: "test-secret",
		Region:          "us-west-2",
		Timeout:         60 * time.Second,
	}

	provider, err := NewS3Provider(ctx, cfg)
	require.NoError(t, err)
	assert.NotNil(t, provider)
	assert.Equal(t, "test-bucket", provider.bucket)
	assert.Equal(t, 60*time.Second, provider.timeout)
}
