package main

import (
	"fmt"
	"strings"
	"time"

	celestia "github.com/celestiaorg/op-alt-da"
	"github.com/celestiaorg/op-alt-da/batch"
	s3 "github.com/celestiaorg/op-alt-da/s3"
	"github.com/celestiaorg/op-alt-da/worker"
	"github.com/urfave/cli/v2"
)

// BuildConfigFromTOML builds runtime configuration directly from TOML config
// This bypasses the environment variable conversion layer for cleaner, more idiomatic Go code
func BuildConfigFromTOML(tomlCfg *Config) (*RuntimeConfig, error) {
	// Parse durations
	submitPeriod, err := time.ParseDuration(tomlCfg.Worker.SubmitPeriod)
	if err != nil {
		return nil, fmt.Errorf("invalid worker.submit_period: %w", err)
	}
	submitTimeout, err := time.ParseDuration(tomlCfg.Worker.SubmitTimeout)
	if err != nil {
		return nil, fmt.Errorf("invalid worker.submit_timeout: %w", err)
	}
	maxBlobWaitTime, err := time.ParseDuration(tomlCfg.Worker.MaxBlobWaitTime)
	if err != nil {
		return nil, fmt.Errorf("invalid worker.max_blob_wait_time: %w", err)
	}
	reconcilePeriod, err := time.ParseDuration(tomlCfg.Worker.ReconcilePeriod)
	if err != nil {
		return nil, fmt.Errorf("invalid worker.reconcile_period: %w", err)
	}
	reconcileAge, err := time.ParseDuration(tomlCfg.Worker.ReconcileAge)
	if err != nil {
		return nil, fmt.Errorf("invalid worker.reconcile_age: %w", err)
	}
	getTimeout, err := time.ParseDuration(tomlCfg.Worker.GetTimeout)
	if err != nil {
		return nil, fmt.Errorf("invalid worker.get_timeout: %w", err)
	}
	backfillPeriod := 15 * time.Second // Default
	if tomlCfg.Backfill.Enabled {
		// Backfill period can be derived from reconcile period or set separately if needed
		backfillPeriod = reconcilePeriod
	}

	// Build batch config
	batchCfg := &batch.Config{
		MinBlobs:          tomlCfg.Batch.MinBlobs,
		MaxBlobs:          tomlCfg.Batch.MaxBlobs,
		TargetBlobs:       tomlCfg.Batch.TargetBlobs,
		MaxBatchSizeBytes: tomlCfg.Batch.MaxSizeKB * 1024, // Convert KB to bytes
		MinBatchSizeBytes: tomlCfg.Batch.MinSizeKB * 1024, // Convert KB to bytes
	}

	if err := batchCfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid batch configuration: %w", err)
	}

	// Calculate MaxTxSizeBytes from MaxTxSizeKB (default 1800 KB = 1.8 MB)
	maxTxSizeBytes := 1843200 // Default: 1800KB
	if tomlCfg.Worker.MaxTxSizeKB > 0 {
		maxTxSizeBytes = tomlCfg.Worker.MaxTxSizeKB * 1024
	}

	// Calculate MaxBlockSizeBytes from MaxBlockSizeKB (default 32768 KB = 32 MB)
	maxBlockSizeBytes := 33554432 // Default: 32MB
	if tomlCfg.Worker.MaxBlockSizeKB > 0 {
		maxBlockSizeBytes = tomlCfg.Worker.MaxBlockSizeKB * 1024
	}

	// Build worker config
	workerCfg := &worker.Config{
		SubmitPeriod:    submitPeriod,
		SubmitTimeout:   submitTimeout,
		MaxRetries:      tomlCfg.Worker.MaxRetries,
		MaxBlobWaitTime: maxBlobWaitTime,
		ReconcilePeriod: reconcilePeriod,
		ReconcileAge:    reconcileAge,
		GetTimeout:      getTimeout,
		ReadOnly:        tomlCfg.ReadOnly,
		TrustedSigners:  tomlCfg.Worker.TrustedSigners,
		BackfillEnabled: tomlCfg.Backfill.Enabled,
		StartHeight:     tomlCfg.Backfill.StartHeight,
		BackfillPeriod:    backfillPeriod,
		BlocksPerScan:     tomlCfg.Backfill.BlocksPerScan,
		MaxTxSizeBytes:    maxTxSizeBytes,
		MaxBlockSizeBytes: maxBlockSizeBytes,
	}

	if err := validateWorkerConfig(workerCfg); err != nil {
		return nil, fmt.Errorf("invalid worker configuration: %w", err)
	}

	return &RuntimeConfig{
		Addr:           tomlCfg.Addr,
		Port:           tomlCfg.Port,
		DBPath:         tomlCfg.DBPath,
		BatchConfig:    batchCfg,
		WorkerConfig:   workerCfg,
		CelestiaConfig: BuildCLIConfigFromTOML(tomlCfg),
		BackupEnabled:  tomlCfg.Backup.Enabled,
		BackupInterval: tomlCfg.Backup.Interval,
		TOMLConfig:     tomlCfg,
	}, nil
}

// BuildConfigFromCLI builds runtime configuration from CLI context (env vars + flags)
// Used when no TOML config file is specified
func BuildConfigFromCLI(cliCtx *cli.Context) (*RuntimeConfig, error) {
	// Parse trusted signers from comma-separated string
	var trustedSigners []string
	trustedSignersStr := cliCtx.String(TrustedSignersFlagName)
	if trustedSignersStr != "" {
		for _, signer := range strings.Split(trustedSignersStr, ",") {
			trimmed := strings.TrimSpace(signer)
			if trimmed != "" {
				trustedSigners = append(trustedSigners, trimmed)
			}
		}
	}

	// Build batch config
	batchCfg := &batch.Config{
		MinBlobs:          cliCtx.Int(BatchMinBlobsFlagName),
		MaxBlobs:          cliCtx.Int(BatchMaxBlobsFlagName),
		TargetBlobs:       cliCtx.Int(BatchTargetBlobsFlagName),
		MaxBatchSizeBytes: cliCtx.Int(BatchMaxSizeFlagName) * 1024, // Convert KB to bytes
		MinBatchSizeBytes: cliCtx.Int(BatchMinSizeFlagName) * 1024, // Convert KB to bytes
	}

	if err := batchCfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid batch configuration: %w", err)
	}

	// Build worker config
	workerCfg := &worker.Config{
		SubmitPeriod:    cliCtx.Duration(WorkerSubmitPeriodFlagName),
		SubmitTimeout:   cliCtx.Duration(WorkerSubmitTimeoutFlagName),
		MaxRetries:      cliCtx.Int(WorkerMaxRetriesFlagName),
		MaxBlobWaitTime: cliCtx.Duration(WorkerMaxBlobWaitTimeFlagName),
		ReconcilePeriod: cliCtx.Duration(WorkerReconcilePeriodFlagName),
		ReconcileAge:    cliCtx.Duration(WorkerReconcileAgeFlagName),
		GetTimeout:      cliCtx.Duration(WorkerGetTimeoutFlagName),
		ReadOnly:        cliCtx.Bool(ReadOnlyFlagName),
		TrustedSigners:  trustedSigners,
		BackfillEnabled: cliCtx.Bool(BackfillEnabledFlagName),
		StartHeight:     cliCtx.Uint64(BackfillStartHeightFlagName),
		BackfillPeriod:  cliCtx.Duration(BackfillPeriodFlagName),
		BlocksPerScan:   cliCtx.Int(BackfillBlocksPerScanFlagName),
	}

	if err := validateWorkerConfig(workerCfg); err != nil {
		return nil, fmt.Errorf("invalid worker configuration: %w", err)
	}

	return &RuntimeConfig{
		Addr:           cliCtx.String(ListenAddrFlagName),
		Port:           cliCtx.Int(PortFlagName),
		DBPath:         cliCtx.String(DBPathFlagName),
		BatchConfig:    batchCfg,
		WorkerConfig:   workerCfg,
		CelestiaConfig: ReadCLIConfig(cliCtx),
		BackupEnabled:  cliCtx.Bool(BackupEnabledFlagName),
		BackupInterval: cliCtx.Duration(BackupIntervalFlagName).String(),
		TOMLConfig:     nil, // No TOML when using CLI
	}, nil
}

// RuntimeConfig holds the actual runtime configuration used by the server
type RuntimeConfig struct {
	Addr          string
	Port          int
	DBPath        string
	BatchConfig   *batch.Config
	WorkerConfig  *worker.Config
	CelestiaConfig CLIConfig
	BackupEnabled bool
	BackupInterval string
	TOMLConfig    *Config // Optional: original TOML if loaded
}

// BuildCLIConfigFromTOML builds the Celestia CLI config from TOML
func BuildCLIConfigFromTOML(tomlCfg *Config) CLIConfig {
	// Parse S3 timeout if provided
	var s3Timeout time.Duration
	if tomlCfg.S3.Timeout != "" {
		var err error
		s3Timeout, err = time.ParseDuration(tomlCfg.S3.Timeout)
		if err != nil {
			// Use default timeout if parsing fails
			s3Timeout = 5 * time.Minute
		}
	}

	return CLIConfig{
		CelestiaEndpoint:      tomlCfg.Celestia.DARPCServer,
		CelestiaTLSEnabled:    tomlCfg.Celestia.TLSEnabled,
		CelestiaAuthToken:     tomlCfg.Celestia.AuthToken,
		CelestiaNamespace:     tomlCfg.Celestia.Namespace,
		CelestiaCompactBlobID: tomlCfg.Celestia.BlobIDCompact,
		TxClientConfig: celestia.TxClientConfig{
			DefaultKeyName:     tomlCfg.Celestia.TxClient.DefaultKeyName,
			KeyringPath:        tomlCfg.Celestia.TxClient.KeyringPath,
			CoreGRPCAddr:       tomlCfg.Celestia.TxClient.CoreGRPCAddr,
			CoreGRPCTLSEnabled: tomlCfg.Celestia.TxClient.CoreGRPCTLSEnabled,
			CoreGRPCAuthToken:  tomlCfg.Celestia.TxClient.CoreGRPCAuthToken,
			P2PNetwork:         tomlCfg.Celestia.TxClient.P2PNetwork,
		},
		S3Config: s3.S3Config{
			S3CredentialType: toS3CredentialType(tomlCfg.S3.CredentialType),
			Bucket:           tomlCfg.S3.Bucket,
			Path:             tomlCfg.S3.Path,
			Endpoint:         tomlCfg.S3.Endpoint,
			AccessKeyID:      tomlCfg.S3.AccessKeyID,
			AccessKeySecret:  tomlCfg.S3.AccessKeySecret,
			Timeout:          s3Timeout,
		},
		MetricsEnabled: tomlCfg.Metrics.Enabled,
		MetricsPort:    tomlCfg.Metrics.Port,
	}
}

// Note: toS3CredentialType is defined in flags.go
