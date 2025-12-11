package s3

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/celestiaorg/op-alt-da/fallback"
	"github.com/ethereum/go-ethereum/crypto"
)

// Config holds S3 provider configuration.
type Config struct {
	// Bucket is the S3 bucket name (required)
	Bucket string
	// Prefix is the key prefix for stored objects (optional)
	Prefix string
	// Endpoint is the S3 endpoint URL (optional, for S3-compatible services)
	Endpoint string
	// Region is the AWS region (default: us-east-1)
	Region string
	// AccessKeyID is the AWS access key (optional, uses default credential chain if empty)
	AccessKeyID string
	// AccessKeySecret is the AWS secret key (optional, uses default credential chain if empty)
	AccessKeySecret string
	// CredentialType specifies how to authenticate: "static", "environment", "iam" (default: auto-detect)
	CredentialType string
	// Timeout is the timeout for S3 operations (default: 30s)
	Timeout time.Duration
}

// S3Provider implements the fallback.Provider interface using AWS S3.
type S3Provider struct {
	client  *s3.Client
	bucket  string
	prefix  string
	timeout time.Duration
}

// NewS3Provider creates a new S3 fallback provider.
func NewS3Provider(ctx context.Context, cfg Config) (*S3Provider, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3: bucket is required")
	}

	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	// Build AWS config options
	var opts []func(*config.LoadOptions) error
	opts = append(opts, config.WithRegion(cfg.Region))

	// Configure credentials based on credential type
	switch cfg.CredentialType {
	case "static":
		if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
			return nil, errors.New("s3: access key and secret required for static credentials")
		}
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.AccessKeySecret, ""),
		))
	case "environment", "iam", "":
		// Use default credential chain (environment, IAM role, etc.)
		// If static credentials are provided, use them
		if cfg.AccessKeyID != "" && cfg.AccessKeySecret != "" {
			opts = append(opts, config.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.AccessKeySecret, ""),
			))
		}
	default:
		return nil, fmt.Errorf("s3: unknown credential type: %s", cfg.CredentialType)
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: failed to load AWS config: %w", err)
	}

	// Build S3 client options
	var s3Opts []func(*s3.Options)
	if cfg.Endpoint != "" {
		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true // Required for most S3-compatible services
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)

	return &S3Provider{
		client:  client,
		bucket:  cfg.Bucket,
		prefix:  cfg.Prefix,
		timeout: cfg.Timeout,
	}, nil
}

// Name returns the provider identifier.
func (p *S3Provider) Name() string {
	return "s3"
}

// Put stores blob data with commitment as key.
func (p *S3Provider) Put(ctx context.Context, commitment []byte, data []byte) error {
	key := p.makeKey(commitment)

	_, err := p.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return fmt.Errorf("s3: failed to put object: %w", err)
	}

	return nil
}

// Get retrieves blob data by commitment.
func (p *S3Provider) Get(ctx context.Context, commitment []byte) ([]byte, error) {
	data, err := p.getObject(ctx, p.makeKey(commitment))
	if err != nil {
		if isNotFoundError(err) {
			return nil, fallback.ErrNotFound
		}
		return nil, err
	}
	return data, nil
}

// getObject retrieves an object by key.
func (p *S3Provider) getObject(ctx context.Context, key string) ([]byte, error) {
	result, err := p.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(p.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer result.Body.Close()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, fmt.Errorf("s3: failed to read object body: %w", err)
	}

	return data, nil
}

// Available returns true if the provider is configured and ready.
func (p *S3Provider) Available() bool {
	return p.client != nil && p.bucket != ""
}

// Timeout returns the configured timeout for operations.
func (p *S3Provider) Timeout() time.Duration {
	return p.timeout
}

// makeKey generates the canonical S3 object key from a commitment.
// Format: prefix/hex(keccak256(commitment))
func (p *S3Provider) makeKey(commitment []byte) string {
	keccakKey := crypto.Keccak256(commitment)
	return path.Join(p.prefix, hex.EncodeToString(keccakKey))
}

// isNotFoundError checks if the error indicates the object was not found.
// Uses proper AWS SDK v2 error types instead of string matching.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	// Check for NoSuchKey or NotFound error types (AWS SDK v2)
	var noSuchKey *types.NoSuchKey
	var notFound *types.NotFound
	return errors.As(err, &noSuchKey) || errors.As(err, &notFound)
}
