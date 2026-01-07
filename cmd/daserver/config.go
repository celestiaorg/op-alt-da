package main

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	awskeyring "github.com/celestiaorg/aws-kms-keyring"
	celestia "github.com/celestiaorg/op-alt-da"
)

// Config represents the simplified application configuration for stateless DA.
// This configuration removes batch, worker, backfill, and S3 sections.
type Config struct {
	Addr      string `toml:"addr"`
	Port      int    `toml:"port"`
	LogLevel  string `toml:"log_level"`
	LogFormat string `toml:"log_format"`

	// HTTP server timeouts (protection against Slowloris and slow POST attacks)
	HTTPReadTimeout  string `toml:"read_timeout"`  // Max time to read request headers + body
	HTTPWriteTimeout string `toml:"write_timeout"` // Max time to write response
	HTTPIdleTimeout  string `toml:"idle_timeout"`  // Max time for idle keep-alive connections

	Celestia   CelestiaConfig   `toml:"celestia"`
	Submission SubmissionConfig `toml:"submission"`
	Read       ReadConfig       `toml:"read"`
	Metrics    MetricsConfig    `toml:"metrics"`
	Fallback   FallbackConfig   `toml:"fallback"`
}

// CelestiaConfig holds Celestia connection settings for the clientTX architecture.
type CelestiaConfig struct {
	// Namespace (29 bytes hex)
	Namespace string `toml:"namespace"`

	// Use compact blob IDs (height + commitment only)
	BlobIDCompact bool `toml:"blobid_compact"`

	// Bridge node settings (for reading blobs via JSON-RPC)
	BridgeAddr       string `toml:"bridge_addr"`
	BridgeAuthToken  string `toml:"bridge_auth_token"`
	BridgeTLSEnabled bool   `toml:"bridge_tls_enabled"`

	// CoreGRPC settings (for submitting blobs to consensus)
	CoreGRPCAddr       string `toml:"core_grpc_addr"`
	CoreGRPCAuthToken  string `toml:"core_grpc_auth_token"`
	CoreGRPCTLSEnabled bool   `toml:"core_grpc_tls_enabled"`

	// Keyring settings (for signing transactions)
	KeyringPath    string `toml:"keyring_path"`
	DefaultKeyName string `toml:"default_key_name"`
	P2PNetwork     string `toml:"p2p_network"`

	// Parallel submission settings
	// 0 = immediate submission (no queue, default)
	// 1 = synchronous submission (queued, single signer)
	// >1 = parallel submission (multiple worker accounts)
	TxWorkerAccounts int `toml:"tx_worker_accounts"`

	// Optional AWS KMS-backed keyring configuration
	KMS CelestiaKMSConfig `toml:"kms"`
}

// CelestiaKMSConfig configures the optional AWS KMS backend for signing.
type CelestiaKMSConfig struct {
	Enabled     bool   `toml:"enabled"`
	Region      string `toml:"region"`
	Endpoint    string `toml:"endpoint"`
	AliasPrefix string `toml:"alias_prefix"`

	// Import configuration - specify a key to import on startup
	ImportKeyName string `toml:"import_key_name"`
	ImportKeyHex  string `toml:"import_key_hex"`
}

// SubmissionConfig holds submission settings for blob writes.
type SubmissionConfig struct {
	// Timeout for blob submission (fail immediately if exceeded)
	Timeout string `toml:"timeout"`

	// Transaction priority: 1=low, 2=medium, 3=high
	// Higher priority = faster inclusion, higher gas cost
	TxPriority int `toml:"tx_priority"`

	// Maximum blob size accepted (protection against memory exhaustion)
	// Supports human-readable formats: "2MB", "2097152", "1024KB", etc.
	MaxBlobSize string `toml:"max_blob_size"`
}

// ReadConfig holds read settings for blob retrieval.
type ReadConfig struct {
	// Timeout for blob.Get operations
	Timeout string `toml:"timeout"`
}

// MetricsConfig holds metrics settings.
type MetricsConfig struct {
	Enabled bool `toml:"enabled"`
	Port    int  `toml:"port"`
}

// FallbackConfig holds fallback storage provider settings.
type FallbackConfig struct {
	// Enable fallback storage
	Enabled bool `toml:"enabled"`

	// Provider type: "s3"
	Provider string `toml:"provider"`

	// S3 configuration (when provider = "s3")
	S3 S3Config `toml:"s3"`
}

// S3Config holds S3-specific fallback settings.
type S3Config struct {
	Bucket          string `toml:"bucket"`
	Prefix          string `toml:"prefix"`
	Endpoint        string `toml:"endpoint"`
	Region          string `toml:"region"`
	CredentialType  string `toml:"credential_type"`
	AccessKeyID     string `toml:"access_key_id"`
	AccessKeySecret string `toml:"access_key_secret"`
	Timeout         string `toml:"timeout"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Addr:             "127.0.0.1",
		Port:             3100,
		LogLevel:         "info",
		LogFormat:        "text",
		HTTPReadTimeout:  "", // Will default to 30s in parser
		HTTPWriteTimeout: "", // Will default to 120s in parser
		HTTPIdleTimeout:  "", // Will default to 60s in parser
		Celestia: CelestiaConfig{
			Namespace:          "",
			BlobIDCompact:      true,
			BridgeAddr:         "http://localhost:26658",
			BridgeAuthToken:    "",
			BridgeTLSEnabled:   false,
			CoreGRPCAddr:       "",
			CoreGRPCAuthToken:  "",
			CoreGRPCTLSEnabled: true,
			KeyringPath:        "",
			DefaultKeyName:     "my_celes_key",
			P2PNetwork:         "mocha-4",
			TxWorkerAccounts:   0,
			KMS: CelestiaKMSConfig{
				AliasPrefix: "alias/op-alt-da/",
			},
		},
		Submission: SubmissionConfig{
			Timeout:     "60s",
			TxPriority:  2,
			MaxBlobSize: "", // Will default to 2MB in parser
		},
		Read: ReadConfig{
			Timeout: "30s",
		},
		Metrics: MetricsConfig{
			Enabled: false,
			Port:    6060,
		},
		Fallback: FallbackConfig{
			Enabled:  false,
			Provider: "s3",
			S3: S3Config{
				Region:  "us-east-1",
				Timeout: "30s",
			},
		},
	}
}

// GetS3Timeout parses and returns the S3 timeout duration.
func (c *Config) GetS3Timeout() (time.Duration, error) {
	if c.Fallback.S3.Timeout == "" {
		return 30 * time.Second, nil
	}
	return time.ParseDuration(c.Fallback.S3.Timeout)
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	if c.Celestia.Namespace == "" {
		return fmt.Errorf("celestia.namespace is required")
	}

	ns, err := hex.DecodeString(c.Celestia.Namespace)
	if err != nil {
		return fmt.Errorf("celestia.namespace must be valid hex: %w", err)
	}
	if len(ns) != 29 {
		return fmt.Errorf("celestia.namespace must be 29 bytes (58 hex chars), got %d bytes", len(ns))
	}

	// Validate HTTP server timeouts
	if _, err := c.GetHTTPReadTimeout(); err != nil {
		return fmt.Errorf("read_timeout: %w", err)
	}
	if _, err := c.GetHTTPWriteTimeout(); err != nil {
		return fmt.Errorf("write_timeout: %w", err)
	}
	if _, err := c.GetHTTPIdleTimeout(); err != nil {
		return fmt.Errorf("idle_timeout: %w", err)
	}

	if _, err := c.GetSubmissionTimeout(); err != nil {
		return fmt.Errorf("submission.timeout: %w", err)
	}

	if _, err := c.GetReadTimeout(); err != nil {
		return fmt.Errorf("read.timeout: %w", err)
	}

	if _, err := c.GetMaxBlobSize(); err != nil {
		return fmt.Errorf("submission.max_blob_size: %w", err)
	}

	if c.Submission.TxPriority < 1 || c.Submission.TxPriority > 3 {
		return fmt.Errorf("submission.tx_priority must be 1 (low), 2 (medium), or 3 (high)")
	}

	// If CoreGRPC is configured, signing settings are required
	if c.Celestia.CoreGRPCAddr != "" {
		signingConfigured := c.Celestia.KeyringPath != "" || c.Celestia.KMS.Enabled
		if !signingConfigured {
			return fmt.Errorf("configure either celestia.keyring_path or celestia.kms when core_grpc_addr is set")
		}
		if c.Celestia.DefaultKeyName == "" {
			return fmt.Errorf("celestia.default_key_name is required when core_grpc_addr is set")
		}
		if c.Celestia.P2PNetwork == "" {
			return fmt.Errorf("celestia.p2p_network is required when core_grpc_addr is set")
		}

		if c.Celestia.KMS.Enabled {
			if c.Celestia.KMS.Region == "" {
				return fmt.Errorf("celestia.kms.region is required when kms is enabled")
			}
			if c.Celestia.KMS.AliasPrefix == "" {
				return fmt.Errorf("celestia.kms.alias_prefix is required when kms is enabled")
			}

			// Validate import configuration
			hasImportName := c.Celestia.KMS.ImportKeyName != ""
			hasImportHex := c.Celestia.KMS.ImportKeyHex != ""

			if hasImportName != hasImportHex {
				return fmt.Errorf("celestia.kms: both import_key_name and import_key_hex must be specified together, or neither")
			}

			if hasImportHex {
				// Validate hex format
				keyHex := strings.TrimPrefix(c.Celestia.KMS.ImportKeyHex, "0x")
				if _, err := hex.DecodeString(keyHex); err != nil {
					return fmt.Errorf("celestia.kms.import_key_hex: invalid hex format: %w", err)
				}
				// Validate length (32 bytes = 64 hex chars)
				if len(keyHex) != 64 {
					return fmt.Errorf("celestia.kms.import_key_hex must be 32 bytes (64 hex characters), got %d", len(keyHex))
				}
			}
		}
	}

	return nil
}

// GetSubmissionTimeout parses and returns the submission timeout duration.
func (c *Config) GetSubmissionTimeout() (time.Duration, error) {
	if c.Submission.Timeout == "" {
		return 60 * time.Second, nil
	}
	return time.ParseDuration(c.Submission.Timeout)
}

// GetReadTimeout parses and returns the read timeout duration for blob retrieval.
func (c *Config) GetReadTimeout() (time.Duration, error) {
	if c.Read.Timeout == "" {
		return 30 * time.Second, nil
	}
	return time.ParseDuration(c.Read.Timeout)
}

// GetHTTPReadTimeout parses and returns the HTTP server read timeout.
func (c *Config) GetHTTPReadTimeout() (time.Duration, error) {
	if c.HTTPReadTimeout == "" {
		return 30 * time.Second, nil
	}
	return time.ParseDuration(c.HTTPReadTimeout)
}

// GetHTTPWriteTimeout parses and returns the HTTP server write timeout.
func (c *Config) GetHTTPWriteTimeout() (time.Duration, error) {
	if c.HTTPWriteTimeout == "" {
		return 120 * time.Second, nil
	}
	return time.ParseDuration(c.HTTPWriteTimeout)
}

// GetHTTPIdleTimeout parses and returns the HTTP server idle timeout.
func (c *Config) GetHTTPIdleTimeout() (time.Duration, error) {
	if c.HTTPIdleTimeout == "" {
		return 60 * time.Second, nil
	}
	return time.ParseDuration(c.HTTPIdleTimeout)
}

// GetMaxBlobSize parses and returns the maximum blob size in bytes.
func (c *Config) GetMaxBlobSize() (int64, error) {
	if c.Submission.MaxBlobSize == "" {
		return 2 * 1024 * 1024, nil // Default 2MB (Celestia max)
	}
	return parseByteSize(c.Submission.MaxBlobSize)
}

// parseByteSize parses human-readable byte sizes like "2MB", "1024KB", "2097152"
func parseByteSize(s string) (int64, error) {
	const (
		base    = 10 // decimal
		bitSize = 64 // int64
	)

	s = strings.TrimSpace(strings.ToUpper(s))

	units := []struct {
		suffix string
		mult   int64
	}{
		{"GB", 1024 * 1024 * 1024},
		{"MB", 1024 * 1024},
		{"KB", 1024},
		{"B", 1},
	}

	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			numStr := strings.TrimSuffix(s, u.suffix)
			num, err := strconv.ParseInt(strings.TrimSpace(numStr), base, bitSize)
			if err != nil {
				return 0, fmt.Errorf("invalid number in byte size: %w", err)
			}
			return num * u.mult, nil
		}
	}

	// Plain number (bytes)
	return strconv.ParseInt(s, base, bitSize)
}

// TxClientEnabled returns true if the TX client (CoreGRPC) is configured.
func (c *Config) TxClientEnabled() bool {
	if c.Celestia.CoreGRPCAddr == "" {
		return false
	}
	if c.Celestia.KeyringPath != "" {
		return true
	}
	return c.Celestia.KMS.Enabled
}

// ToCelestiaRPCConfig converts the Config to a celestia.RPCClientConfig.
func (c *Config) ToCelestiaRPCConfig() celestia.RPCClientConfig {
	ns, _ := hex.DecodeString(c.Celestia.Namespace)

	var txCfg *celestia.TxClientConfig
	if c.TxClientEnabled() {
		txCfg = &celestia.TxClientConfig{
			DefaultKeyName:     c.Celestia.DefaultKeyName,
			KeyringPath:        c.Celestia.KeyringPath,
			CoreGRPCAddr:       c.Celestia.CoreGRPCAddr,
			CoreGRPCTLSEnabled: c.Celestia.CoreGRPCTLSEnabled,
			CoreGRPCAuthToken:  c.Celestia.CoreGRPCAuthToken,
			P2PNetwork:         c.Celestia.P2PNetwork,
			TxWorkerAccounts:   c.Celestia.TxWorkerAccounts,
		}
		if c.Celestia.KMS.Enabled {
			aliasPrefix := c.Celestia.KMS.AliasPrefix
			if aliasPrefix == "" {
				aliasPrefix = "alias/op-alt-da/"
			}
			txCfg.KMS = &awskeyring.Config{
				Region:        c.Celestia.KMS.Region,
				Endpoint:      c.Celestia.KMS.Endpoint,
				AliasPrefix:   aliasPrefix,
				ImportKeyName: c.Celestia.KMS.ImportKeyName,
				ImportKeyHex:  c.Celestia.KMS.ImportKeyHex,
			}
		}
	}

	return celestia.RPCClientConfig{
		URL:            c.Celestia.BridgeAddr,
		TLSEnabled:     c.Celestia.BridgeTLSEnabled,
		AuthToken:      c.Celestia.BridgeAuthToken,
		Namespace:      ns,
		CompactBlobID:  c.Celestia.BlobIDCompact,
		TxClientConfig: txCfg,
	}
}
