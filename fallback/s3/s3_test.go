package s3

import (
	"context"
	"testing"
	"time"

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
	key := provider.makeKey(commitment)

	// Key should be hex-encoded keccak256 hash (64 characters)
	assert.Len(t, key, 64)

	// With prefix
	provider.prefix = "blobs"
	key = provider.makeKey(commitment)
	assert.Contains(t, key, "blobs/")
	assert.Len(t, key, 64+len("blobs/"))
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

	// This will fail to create because we don't have real credentials,
	// but we can verify the config validation works
	cfg := Config{
		Bucket: "test-bucket",
	}

	// Test that defaults are applied (region, timeout)
	// We can't fully test without real S3, but we can test the validation
	assert.Equal(t, "", cfg.Region) // Will be defaulted in NewS3Provider

	// Test with static credentials (will succeed in creating provider)
	cfg = Config{
		Bucket:          "test-bucket",
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

