package main

import (
	"encoding/hex"
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
	submitTimeout, err := time.ParseDuration(tomlCfg.Worker.SubmitTimeout)
	if err != nil {
		return nil, fmt.Errorf("invalid worker.submit_timeout: %w", err)
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
	backfillPeriod := 5 * time.Second // Default: check every 5s
	if tomlCfg.Backfill.Period != "" {
		parsed, err := time.ParseDuration(tomlCfg.Backfill.Period)
		if err != nil {
			return nil, fmt.Errorf("invalid backfill.period: %w", err)
		}
		backfillPeriod = parsed
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

	// Build worker config
	maxParallelSubmissions := 1
	if tomlCfg.Worker.MaxParallelSubmissions > 0 {
		maxParallelSubmissions = tomlCfg.Worker.MaxParallelSubmissions
	}

	retryBackoff := 1 * time.Second // Default: 1s linear backoff
	if tomlCfg.Worker.RetryBackoff != "" {
		parsed, err := time.ParseDuration(tomlCfg.Worker.RetryBackoff)
		if err != nil {
			return nil, fmt.Errorf("invalid worker.retry_backoff: %w", err)
		}
		retryBackoff = parsed
	}

	workerCfg := &worker.Config{
		SubmitTimeout:          submitTimeout,
		MaxRetries:             tomlCfg.Worker.MaxRetries,
		RetryBackoff:           retryBackoff,
		MaxParallelSubmissions: maxParallelSubmissions,
		ReconcilePeriod:        reconcilePeriod,
		ReconcileAge:           reconcileAge,
		GetTimeout:             getTimeout,
		TrustedSigners:         tomlCfg.Worker.TrustedSigners,
		BackfillEnabled:        tomlCfg.Backfill.Enabled,
		StartHeight:            tomlCfg.Backfill.StartHeight,
		BackfillTargetHeight:   tomlCfg.Backfill.TargetHeight,
		BackfillPeriod:         backfillPeriod,
		BlocksPerScan:          tomlCfg.Backfill.BlocksPerScan,
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
		SubmitTimeout:          cliCtx.Duration(WorkerSubmitTimeoutFlagName),
		MaxRetries:             cliCtx.Int(WorkerMaxRetriesFlagName),
		MaxParallelSubmissions: cliCtx.Int(WorkerMaxParallelSubmissionsFlagName),
		ReconcilePeriod:        cliCtx.Duration(WorkerReconcilePeriodFlagName),
		ReconcileAge:           cliCtx.Duration(WorkerReconcileAgeFlagName),
		GetTimeout:             cliCtx.Duration(WorkerGetTimeoutFlagName),
		TrustedSigners:         trustedSigners,
		BackfillEnabled:        cliCtx.Bool(BackfillEnabledFlagName),
		StartHeight:            cliCtx.Uint64(BackfillStartHeightFlagName),
		BackfillTargetHeight:   cliCtx.Uint64(BackfillTargetHeightFlagName),
		BackfillPeriod:         cliCtx.Duration(BackfillPeriodFlagName),
		BlocksPerScan:          cliCtx.Int(BackfillBlocksPerScanFlagName),
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
	Addr           string
	Port           int
	DBPath         string
	BatchConfig    *batch.Config
	WorkerConfig   *worker.Config
	CelestiaConfig CLIConfig
	BackupEnabled  bool
	BackupInterval string
	TOMLConfig     *Config // Optional: original TOML if loaded
}

// BuildCLIConfigFromTOML builds the CLI config from TOML
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

	// Decode namespace from hex string
	ns, _ := hex.DecodeString(tomlCfg.Celestia.Namespace)

	return CLIConfig{
		CelestiaConfig: celestia.CelestiaClientConfig{
			// Bridge node settings
			BridgeAddr:       tomlCfg.Celestia.BridgeAddr,
			BridgeAuthToken:  tomlCfg.Celestia.BridgeAuthToken,
			BridgeTLSEnabled: tomlCfg.Celestia.BridgeTLSEnabled,
			// CoreGRPC settings
			CoreGRPCAddr:       tomlCfg.Celestia.CoreGRPCAddr,
			CoreGRPCAuthToken:  tomlCfg.Celestia.CoreGRPCAuthToken,
			CoreGRPCTLSEnabled: tomlCfg.Celestia.CoreGRPCTLSEnabled,
			// Keyring settings
			KeyringPath:    tomlCfg.Celestia.KeyringPath,
			DefaultKeyName: tomlCfg.Celestia.DefaultKeyName,
			P2PNetwork:     tomlCfg.Celestia.P2PNetwork,
			// Parallel submission settings
			TxWorkerAccounts: tomlCfg.Celestia.TxWorkerAccounts,
			// Blob settings
			Namespace:     ns,
			CompactBlobID: tomlCfg.Celestia.BlobIDCompact,
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
