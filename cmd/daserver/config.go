package main

import (
	"encoding/hex"
	"fmt"
	"time"

	celestia "github.com/celestiaorg/op-alt-da"
)

// Config represents the simplified application configuration for stateless DA.
// This configuration removes batch, worker, backfill, and S3 sections.
type Config struct {
	Addr      string `toml:"addr"`
	Port      int    `toml:"port"`
	LogLevel  string `toml:"log_level"`
	LogFormat string `toml:"log_format"`

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
}

// SubmissionConfig holds submission settings for blob writes.
type SubmissionConfig struct {
	// Timeout for blob submission (fail immediately if exceeded)
	Timeout string `toml:"timeout"`

	// Transaction priority: 1=low, 2=medium, 3=high
	// Higher priority = faster inclusion, higher gas cost
	TxPriority int `toml:"tx_priority"`
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
		Addr:      "127.0.0.1",
		Port:      3100,
		LogLevel:  "info",
		LogFormat: "text",
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
		},
		Submission: SubmissionConfig{
			Timeout:    "60s",
			TxPriority: 2,
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

	if _, err := hex.DecodeString(c.Celestia.Namespace); err != nil {
		return fmt.Errorf("celestia.namespace must be valid hex: %w", err)
	}

	if _, err := c.GetSubmissionTimeout(); err != nil {
		return fmt.Errorf("submission.timeout: %w", err)
	}

	if _, err := c.GetReadTimeout(); err != nil {
		return fmt.Errorf("read.timeout: %w", err)
	}

	if c.Submission.TxPriority < 1 || c.Submission.TxPriority > 3 {
		return fmt.Errorf("submission.tx_priority must be 1 (low), 2 (medium), or 3 (high)")
	}

	// If CoreGRPC is configured, keyring settings are required
	if c.Celestia.CoreGRPCAddr != "" {
		if c.Celestia.KeyringPath == "" {
			return fmt.Errorf("celestia.keyring_path is required when core_grpc_addr is set")
		}
		if c.Celestia.DefaultKeyName == "" {
			return fmt.Errorf("celestia.default_key_name is required when core_grpc_addr is set")
		}
		if c.Celestia.P2PNetwork == "" {
			return fmt.Errorf("celestia.p2p_network is required when core_grpc_addr is set")
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

// GetReadTimeout parses and returns the read timeout duration.
func (c *Config) GetReadTimeout() (time.Duration, error) {
	if c.Read.Timeout == "" {
		return 30 * time.Second, nil
	}
	return time.ParseDuration(c.Read.Timeout)
}

// TxClientEnabled returns true if the TX client (CoreGRPC) is configured.
func (c *Config) TxClientEnabled() bool {
	return c.Celestia.CoreGRPCAddr != "" && c.Celestia.KeyringPath != ""
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

