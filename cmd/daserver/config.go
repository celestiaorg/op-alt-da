package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
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
	MaxSizeMB   int `toml:"max_size_mb"`
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
	Enabled     bool   `toml:"enabled"`
	StartHeight uint64 `toml:"start_height"`
	EndHeight   uint64 `toml:"end_height"`
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
		// Validate trusted signers format (should be 40-char hex)
		for i, signer := range c.Worker.TrustedSigners {
			if len(signer) != 40 {
				return fmt.Errorf("worker.trusted_signers[%d] must be 40 hex characters (20 bytes)", i)
			}
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

// ConvertToEnvVars converts TOML config to environment variables map
// This allows us to use existing CLI flag parsing infrastructure
func (c *Config) ConvertToEnvVars() map[string]string {
	env := make(map[string]string)

	prefix := "OP_ALTDA_"

	// Basic settings
	env[prefix+"ADDR"] = c.Addr
	env[prefix+"PORT"] = fmt.Sprintf("%d", c.Port)
	env[prefix+"DB_PATH"] = c.DBPath

	// Only set log settings if they're explicitly configured (non-default values)
	if c.LogLevel != "" {
		env[prefix+"LOG_LEVEL"] = c.LogLevel
	}
	if c.LogFormat != "" {
		env[prefix+"LOG_FORMAT"] = c.LogFormat
	}
	if c.LogColor {
		env[prefix+"LOG_COLOR"] = "true"
	}
	if c.LogPID {
		env[prefix+"LOG_PID"] = "true"
	}
	if c.ReadOnly {
		env[prefix+"READ_ONLY"] = "true"
	}

	// Celestia settings
	env[prefix+"CELESTIA_NAMESPACE"] = c.Celestia.Namespace
	env[prefix+"CELESTIA_SERVER"] = c.Celestia.DARPCServer
	if c.Celestia.AuthToken != "" {
		env[prefix+"CELESTIA_AUTH_TOKEN"] = c.Celestia.AuthToken
	}
	env[prefix+"CELESTIA_TLS_ENABLED"] = fmt.Sprintf("%t", c.Celestia.TLSEnabled)
	env[prefix+"CELESTIA_BLOBID_COMPACT"] = fmt.Sprintf("%t", c.Celestia.BlobIDCompact)

	// TxClient settings (Option B)
	if c.Celestia.TxClient.CoreGRPCAddr != "" {
		env[prefix+"CELESTIA_TX_CLIENT_CORE_GRPC_ADDR"] = c.Celestia.TxClient.CoreGRPCAddr
		if c.Celestia.TxClient.CoreGRPCAuthToken != "" {
			env[prefix+"CELESTIA_TX_CLIENT_CORE_GRPC_AUTH_TOKEN"] = c.Celestia.TxClient.CoreGRPCAuthToken
		}
		env[prefix+"CELESTIA_TX_CLIENT_CORE_GRPC_TLS_ENABLED"] = fmt.Sprintf("%t", c.Celestia.TxClient.CoreGRPCTLSEnabled)
		env[prefix+"CELESTIA_TX_CLIENT_KEY_NAME"] = c.Celestia.TxClient.DefaultKeyName
		env[prefix+"CELESTIA_TX_CLIENT_KEYRING_PATH"] = c.Celestia.TxClient.KeyringPath
		env[prefix+"CELESTIA_TX_CLIENT_P2P_NETWORK"] = c.Celestia.TxClient.P2PNetwork
	}

	// Batch settings
	env[prefix+"BATCH_MIN_BLOBS"] = fmt.Sprintf("%d", c.Batch.MinBlobs)
	env[prefix+"BATCH_MAX_BLOBS"] = fmt.Sprintf("%d", c.Batch.MaxBlobs)
	env[prefix+"BATCH_TARGET_BLOBS"] = fmt.Sprintf("%d", c.Batch.TargetBlobs)
	env[prefix+"BATCH_MAX_SIZE_MB"] = fmt.Sprintf("%d", c.Batch.MaxSizeMB)
	env[prefix+"BATCH_MIN_SIZE_KB"] = fmt.Sprintf("%d", c.Batch.MinSizeKB)

	// Worker settings
	env[prefix+"WORKER_SUBMIT_PERIOD"] = c.Worker.SubmitPeriod
	env[prefix+"WORKER_SUBMIT_TIMEOUT"] = c.Worker.SubmitTimeout
	env[prefix+"WORKER_MAX_RETRIES"] = fmt.Sprintf("%d", c.Worker.MaxRetries)
	env[prefix+"WORKER_MAX_BLOB_WAIT_TIME"] = c.Worker.MaxBlobWaitTime
	env[prefix+"WORKER_RECONCILE_PERIOD"] = c.Worker.ReconcilePeriod
	env[prefix+"WORKER_RECONCILE_AGE"] = c.Worker.ReconcileAge
	env[prefix+"WORKER_GET_TIMEOUT"] = c.Worker.GetTimeout
	if len(c.Worker.TrustedSigners) > 0 {
		env[prefix+"WORKER_TRUSTED_SIGNERS"] = strings.Join(c.Worker.TrustedSigners, ",")
	}

	// Backfill settings
	env[prefix+"BACKFILL_ENABLED"] = fmt.Sprintf("%t", c.Backfill.Enabled)
	if c.Backfill.Enabled {
		env[prefix+"BACKFILL_START_HEIGHT"] = fmt.Sprintf("%d", c.Backfill.StartHeight)
		env[prefix+"BACKFILL_END_HEIGHT"] = fmt.Sprintf("%d", c.Backfill.EndHeight)
	}

	// Backup settings
	env[prefix+"BACKUP_ENABLED"] = fmt.Sprintf("%t", c.Backup.Enabled)
	if c.Backup.Enabled {
		env[prefix+"BACKUP_INTERVAL"] = c.Backup.Interval
	}

	// S3 settings
	if c.Backup.Enabled {
		env[prefix+"S3_CREDENTIAL_TYPE"] = c.S3.CredentialType
		env[prefix+"S3_BUCKET"] = c.S3.Bucket
		env[prefix+"S3_PATH"] = c.S3.Path
		if c.S3.Endpoint != "" {
			env[prefix+"S3_ENDPOINT"] = c.S3.Endpoint
		}
		if c.S3.CredentialType == "static" {
			env[prefix+"S3_ACCESS_KEY_ID"] = c.S3.AccessKeyID
			env[prefix+"S3_ACCESS_KEY_SECRET"] = c.S3.AccessKeySecret
		}
		env[prefix+"S3_TIMEOUT"] = c.S3.Timeout
	}

	// Metrics settings
	env[prefix+"METRICS_ENABLED"] = fmt.Sprintf("%t", c.Metrics.Enabled)
	if c.Metrics.Enabled {
		env[prefix+"METRICS_PORT"] = fmt.Sprintf("%d", c.Metrics.Port)
	}

	return env
}
