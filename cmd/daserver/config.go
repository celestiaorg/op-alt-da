package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/celestiaorg/celestia-app/v6/app/params"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// Config represents the complete application configuration
type Config struct {
	Addr      string `toml:"addr"`
	Port      int    `toml:"port"`
	DBPath    string `toml:"db_path"`
	LogLevel  string `toml:"log_level"`
	LogFormat string `toml:"log_format"`
	LogColor  bool   `toml:"log_color"`
	LogPID    bool   `toml:"log_pid"`
	ReadOnly  bool   `toml:"read_only"`

	Celestia CelestiaConfig  `toml:"celestia"`
	Batch    BatchConfig     `toml:"batch"`
	Worker   WorkerConfig    `toml:"worker"`
	Backfill BackfillConfig  `toml:"backfill"`
	Backup   BackupConfig    `toml:"backup"`
	S3       S3Config        `toml:"s3"`
	Metrics  MetricsConfig   `toml:"metrics"`
}

// CelestiaConfig holds Celestia connection settings
type CelestiaConfig struct {
	Namespace     string         `toml:"namespace"`
	DARPCServer   string         `toml:"da_rpc_server"`
	AuthToken     string         `toml:"auth_token"`
	TLSEnabled    bool           `toml:"tls_enabled"`
	BlobIDCompact bool           `toml:"blobid_compact"`
	TxClient      TxClientConfig `toml:"tx_client"`
}

// TxClientConfig holds transaction client settings (Option B: Service Provider)
type TxClientConfig struct {
	CoreGRPCAddr       string `toml:"core_grpc_addr"`
	CoreGRPCAuthToken  string `toml:"core_grpc_auth_token"`
	CoreGRPCTLSEnabled bool   `toml:"core_grpc_tls_enabled"`
	DefaultKeyName     string `toml:"default_key_name"`
	KeyringPath        string `toml:"keyring_path"`
	P2PNetwork         string `toml:"p2p_network"`
}

// BatchConfig holds batch processing settings
type BatchConfig struct {
	MinBlobs    int `toml:"min_blobs"`
	MaxBlobs    int `toml:"max_blobs"`
	TargetBlobs int `toml:"target_blobs"`
	MaxSizeKB   int `toml:"max_size_kb"`  // Changed from MB to KB for precision
	MinSizeKB   int `toml:"min_size_kb"`
}

// WorkerConfig holds worker timing and retry settings
type WorkerConfig struct {
	SubmitPeriod     string   `toml:"submit_period"`
	SubmitTimeout    string   `toml:"submit_timeout"`
	MaxRetries       int      `toml:"max_retries"`
	MaxBlobWaitTime  string   `toml:"max_blob_wait_time"`
	ReconcilePeriod  string   `toml:"reconcile_period"`
	ReconcileAge     string   `toml:"reconcile_age"`
	GetTimeout       string   `toml:"get_timeout"`
	TrustedSigners   []string `toml:"trusted_signers"`
}

// BackfillConfig holds backfill settings
type BackfillConfig struct {
	Enabled          bool   `toml:"enabled"`
	StartHeight      uint64 `toml:"start_height"`
	EndHeight        uint64 `toml:"end_height"`
	BlocksPerScan    int    `toml:"blocks_per_scan"`    // How many blocks to scan per iteration
}

// BackupConfig holds backup settings
type BackupConfig struct {
	Enabled  bool   `toml:"enabled"`
	Interval string `toml:"interval"`
}

// S3Config holds S3 settings
type S3Config struct {
	CredentialType  string `toml:"credential_type"`
	Bucket          string `toml:"bucket"`
	Path            string `toml:"path"`
	Endpoint        string `toml:"endpoint"`
	AccessKeyID     string `toml:"access_key_id"`
	AccessKeySecret string `toml:"access_key_secret"`
	Timeout         string `toml:"timeout"`
}

// MetricsConfig holds metrics settings
type MetricsConfig struct {
	Enabled bool `toml:"enabled"`
	Port    int  `toml:"port"`
}

// LoadConfig loads configuration from a TOML file
func LoadConfig(path string) (*Config, error) {
	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("config file not found: %s", path)
	}

	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("failed to decode TOML config: %w", err)
	}

	return &cfg, nil
}

// Validate checks that the configuration is valid
func (c *Config) Validate() error {
	// Validate basic settings
	if c.Addr == "" {
		return fmt.Errorf("addr is required")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if c.DBPath == "" {
		return fmt.Errorf("db_path is required")
	}

	// Validate Celestia settings
	if c.Celestia.Namespace == "" {
		return fmt.Errorf("celestia.namespace is required")
	}
	if len(c.Celestia.Namespace) != 58 { // 29 bytes = 58 hex chars
		return fmt.Errorf("celestia.namespace must be 58 hex characters (29 bytes)")
	}
	if c.Celestia.DARPCServer == "" {
		return fmt.Errorf("celestia.da_rpc_server is required")
	}

	// Validate deployment mode
	hasTxClient := c.Celestia.TxClient.CoreGRPCAddr != ""
	hasAuthToken := c.Celestia.AuthToken != ""

	if hasTxClient && hasAuthToken {
		return fmt.Errorf("cannot configure both tx_client (Option B) and auth_token (Option A) - choose one deployment mode")
	}

	// Option B validation (Service Provider with TxClient)
	if hasTxClient {
		if c.Celestia.TxClient.KeyringPath == "" {
			return fmt.Errorf("celestia.tx_client.keyring_path is required for Option B")
		}
		if c.Celestia.TxClient.P2PNetwork == "" {
			return fmt.Errorf("celestia.tx_client.p2p_network is required for Option B")
		}
	}

	// Read-only mode validation
	if c.ReadOnly {
		if len(c.Worker.TrustedSigners) == 0 {
			return fmt.Errorf("read-only mode requires worker.trusted_signers to be configured for security (prevents Woods attack)")
		}
		// Validate trusted signers are valid Bech32 addresses
		if err := validateTrustedSigners(c.Worker.TrustedSigners); err != nil {
			return fmt.Errorf("invalid worker.trusted_signers: %w", err)
		}
	}

	// Validate batch settings
	if c.Batch.MinBlobs <= 0 {
		return fmt.Errorf("batch.min_blobs must be positive")
	}
	if c.Batch.MaxBlobs < c.Batch.MinBlobs {
		return fmt.Errorf("batch.max_blobs must be >= batch.min_blobs")
	}
	if c.Batch.TargetBlobs < c.Batch.MinBlobs || c.Batch.TargetBlobs > c.Batch.MaxBlobs {
		return fmt.Errorf("batch.target_blobs must be between min_blobs and max_blobs")
	}

	// Validate worker duration strings
	if _, err := time.ParseDuration(c.Worker.SubmitPeriod); err != nil {
		return fmt.Errorf("worker.submit_period is invalid: %w", err)
	}
	if _, err := time.ParseDuration(c.Worker.SubmitTimeout); err != nil {
		return fmt.Errorf("worker.submit_timeout is invalid: %w", err)
	}
	if _, err := time.ParseDuration(c.Worker.MaxBlobWaitTime); err != nil {
		return fmt.Errorf("worker.max_blob_wait_time is invalid: %w", err)
	}
	if _, err := time.ParseDuration(c.Worker.ReconcilePeriod); err != nil {
		return fmt.Errorf("worker.reconcile_period is invalid: %w", err)
	}
	if _, err := time.ParseDuration(c.Worker.ReconcileAge); err != nil {
		return fmt.Errorf("worker.reconcile_age is invalid: %w", err)
	}
	if _, err := time.ParseDuration(c.Worker.GetTimeout); err != nil {
		return fmt.Errorf("worker.get_timeout is invalid: %w", err)
	}

	// Validate backfill settings
	if c.Backfill.Enabled {
		if c.Backfill.BlocksPerScan < 1 {
			return fmt.Errorf("backfill.blocks_per_scan must be at least 1")
		}
		if c.Backfill.BlocksPerScan > 100 {
			return fmt.Errorf("backfill.blocks_per_scan too large (max 100, got %d)", c.Backfill.BlocksPerScan)
		}
	}

	// Validate backup settings
	if c.Backup.Enabled {
		if _, err := time.ParseDuration(c.Backup.Interval); err != nil {
			return fmt.Errorf("backup.interval is invalid: %w", err)
		}
		if c.S3.Bucket == "" {
			return fmt.Errorf("s3.bucket is required when backup is enabled")
		}
		if c.S3.CredentialType != "iam" && c.S3.CredentialType != "static" {
			return fmt.Errorf("s3.credential_type must be 'iam' or 'static'")
		}
		if c.S3.CredentialType == "static" {
			if c.S3.AccessKeyID == "" || c.S3.AccessKeySecret == "" {
				return fmt.Errorf("s3.access_key_id and s3.access_key_secret are required for static credentials")
			}
		}
	}

	return nil
}

// Note: ConvertToEnvVars() has been removed as part of idiomatic Go refactor
// TOML config is now used directly without environment variable conversion

// validateTrustedSigners validates that all signers are valid Celestia Bech32 addresses
// Only accepts Bech32 format with the official Celestia prefix from celestia-app
func validateTrustedSigners(signers []string) error {
	for i, signer := range signers {
		signer = strings.TrimSpace(signer)

		// Must be a Celestia Bech32 address with the official prefix
		if !strings.HasPrefix(signer, params.Bech32PrefixAccAddr) {
			return fmt.Errorf("signer[%d] must be a Celestia Bech32 address starting with %q, got: %q",
				i, params.Bech32PrefixAccAddr, signer)
		}

		// Validate it's a valid Bech32 address by trying to decode it
		addr, err := sdk.AccAddressFromBech32(signer)
		if err != nil {
			return fmt.Errorf("signer[%d] invalid Bech32 address %q: %w", i, signer, err)
		}

		// Verify it decodes to 20 bytes (Celestia address format)
		if len(addr) != 20 {
			return fmt.Errorf("signer[%d] Bech32 address %q decoded to %d bytes (expected 20)", i, signer, len(addr))
		}
	}

	return nil
}
