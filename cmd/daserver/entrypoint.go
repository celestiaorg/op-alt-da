package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v2"
	"golang.org/x/sync/errgroup"

	celestia "github.com/celestiaorg/op-alt-da"
	"github.com/celestiaorg/op-alt-da/backup"
	"github.com/celestiaorg/op-alt-da/batch"
	"github.com/celestiaorg/op-alt-da/db"
	s3store "github.com/celestiaorg/op-alt-da/s3"
	"github.com/celestiaorg/op-alt-da/worker"
	oplog "github.com/ethereum-optimism/optimism/op-service/log"
)

const (
	ConfigFileFlagName     = "config"
	DBPathFlagName         = "db.path"
	BackupEnabledFlagName  = "backup.enabled"
	BackupIntervalFlagName = "backup.interval"

	// Batch configuration
	BatchMinBlobsFlagName    = "batch.min-blobs"
	BatchMaxBlobsFlagName    = "batch.max-blobs"
	BatchTargetBlobsFlagName = "batch.target-blobs"
	BatchMaxSizeFlagName     = "batch.max-size-mb"
	BatchMinSizeFlagName     = "batch.min-size-kb"

	// Worker configuration
	WorkerSubmitPeriodFlagName    = "worker.submit-period"
	WorkerSubmitTimeoutFlagName   = "worker.submit-timeout"
	WorkerMaxRetriesFlagName      = "worker.max-retries"
	WorkerMaxBlobWaitTimeFlagName = "worker.max-blob-wait-time"
	WorkerReconcilePeriodFlagName = "worker.reconcile-period"
	WorkerReconcileAgeFlagName    = "worker.reconcile-age"
	WorkerGetTimeoutFlagName      = "worker.get-timeout"

	// Read-only configuration
	ReadOnlyFlagName      = "read-only"
	TrustedSignerFlagName = "trusted-signer"

	// Backfill configuration
	BackfillEnabledFlagName     = "backfill.enabled"
	BackfillStartHeightFlagName = "backfill.start-height"
	BackfillPeriodFlagName      = "backfill.period"
)

var (
	ConfigFileFlag = &cli.StringFlag{
		Name:    ConfigFileFlagName,
		Usage:   "path to TOML configuration file (takes precedence over env vars and CLI flags)",
		Value:   "",
		EnvVars: prefixEnvVars("CONFIG"),
	}
	DBPathFlag = &cli.StringFlag{
		Name:    DBPathFlagName,
		Usage:   "path to SQLite database file",
		Value:   "./data/blobs.db",
		EnvVars: prefixEnvVars("DB_PATH"),
	}
	BackupEnabledFlag = &cli.BoolFlag{
		Name:    BackupEnabledFlagName,
		Usage:   "enable periodic S3 database backups",
		Value:   false,
		EnvVars: prefixEnvVars("BACKUP_ENABLED"),
	}
	BackupIntervalFlag = &cli.DurationFlag{
		Name:    BackupIntervalFlagName,
		Usage:   "interval between database backups",
		Value:   1 * time.Hour,
		EnvVars: prefixEnvVars("BACKUP_INTERVAL"),
	}
	BatchMinBlobsFlag = &cli.IntFlag{
		Name:    BatchMinBlobsFlagName,
		Usage:   "minimum number of blobs before creating a batch",
		Value:   10,
		EnvVars: prefixEnvVars("BATCH_MIN_BLOBS"),
	}
	BatchMaxBlobsFlag = &cli.IntFlag{
		Name:    BatchMaxBlobsFlagName,
		Usage:   "maximum number of blobs per batch",
		Value:   50,
		EnvVars: prefixEnvVars("BATCH_MAX_BLOBS"),
	}
	BatchTargetBlobsFlag = &cli.IntFlag{
		Name:    BatchTargetBlobsFlagName,
		Usage:   "target number of blobs to fetch for batching",
		Value:   20,
		EnvVars: prefixEnvVars("BATCH_TARGET_BLOBS"),
	}
	BatchMaxSizeFlag = &cli.IntFlag{
		Name:    BatchMaxSizeFlagName,
		Usage:   "maximum batch size in MB",
		Value:   1,
		EnvVars: prefixEnvVars("BATCH_MAX_SIZE_MB"),
	}
	BatchMinSizeFlag = &cli.IntFlag{
		Name:    BatchMinSizeFlagName,
		Usage:   "minimum batch size in KB before forcing submission",
		Value:   500,
		EnvVars: prefixEnvVars("BATCH_MIN_SIZE_KB"),
	}
	WorkerSubmitPeriodFlag = &cli.DurationFlag{
		Name:    WorkerSubmitPeriodFlagName,
		Usage:   "how often submission worker checks for pending blobs",
		Value:   2 * time.Second,
		EnvVars: prefixEnvVars("WORKER_SUBMIT_PERIOD"),
	}
	WorkerSubmitTimeoutFlag = &cli.DurationFlag{
		Name:    WorkerSubmitTimeoutFlagName,
		Usage:   "timeout for submitting batch to Celestia",
		Value:   60 * time.Second,
		EnvVars: prefixEnvVars("WORKER_SUBMIT_TIMEOUT"),
	}
	WorkerMaxRetriesFlag = &cli.IntFlag{
		Name:    WorkerMaxRetriesFlagName,
		Usage:   "maximum retries for failed submissions",
		Value:   10,
		EnvVars: prefixEnvVars("WORKER_MAX_RETRIES"),
	}
	WorkerMaxBlobWaitTimeFlag = &cli.DurationFlag{
		Name:    WorkerMaxBlobWaitTimeFlagName,
		Usage:   "max time a blob waits before forced submission (time-based batching)",
		Value:   30 * time.Second,
		EnvVars: prefixEnvVars("WORKER_MAX_BLOB_WAIT_TIME"),
	}
	WorkerReconcilePeriodFlag = &cli.DurationFlag{
		Name:    WorkerReconcilePeriodFlagName,
		Usage:   "how often event listener reconciles unconfirmed batches",
		Value:   30 * time.Second,
		EnvVars: prefixEnvVars("WORKER_RECONCILE_PERIOD"),
	}
	WorkerReconcileAgeFlag = &cli.DurationFlag{
		Name:    WorkerReconcileAgeFlagName,
		Usage:   "age threshold for reconciling unconfirmed batches",
		Value:   2 * time.Minute,
		EnvVars: prefixEnvVars("WORKER_RECONCILE_AGE"),
	}
	WorkerGetTimeoutFlag = &cli.DurationFlag{
		Name:    WorkerGetTimeoutFlagName,
		Usage:   "timeout for Celestia Get operations during reconciliation",
		Value:   30 * time.Second,
		EnvVars: prefixEnvVars("WORKER_GET_TIMEOUT"),
	}
	ReadOnlyFlag = &cli.BoolFlag{
		Name:    ReadOnlyFlagName,
		Usage:   "run server in read-only mode (blocks PUT requests, disables submission worker)",
		Value:   false,
		EnvVars: prefixEnvVars("READ_ONLY"),
	}
	TrustedSignerFlag = &cli.StringFlag{
		Name:    TrustedSignerFlagName,
		Usage:   "Comma-separated Celestia addresses of trusted write servers (CIP-21 signer verification for HA failover)",
		Value:   "",
		EnvVars: prefixEnvVars("TRUSTED_SIGNER"),
	}
	BackfillEnabledFlag = &cli.BoolFlag{
		Name:    BackfillEnabledFlagName,
		Usage:   "enable backfill worker for read-only servers (syncs from Celestia)",
		Value:   false,
		EnvVars: prefixEnvVars("BACKFILL_ENABLED"),
	}
	BackfillStartHeightFlag = &cli.Uint64Flag{
		Name:    BackfillStartHeightFlagName,
		Usage:   "Celestia block height to start backfilling from (0 = latest)",
		Value:   0,
		EnvVars: prefixEnvVars("BACKFILL_START_HEIGHT"),
	}
	BackfillPeriodFlag = &cli.DurationFlag{
		Name:    BackfillPeriodFlagName,
		Usage:   "how often backfill worker scans for new Celestia blocks",
		Value:   15 * time.Second,
		EnvVars: prefixEnvVars("BACKFILL_PERIOD"),
	}
)

// LoadTOMLConfigIfPresent loads TOML config and sets environment variables BEFORE flag parsing
// This is called as app.Before hook to ensure env vars are set before CLI context is created
func LoadTOMLConfigIfPresent(cliCtx *cli.Context) error {
	configFile := cliCtx.String(ConfigFileFlagName)
	if configFile == "" {
		// No TOML config, use env vars and CLI flags
		return nil
	}

	// Load and validate TOML config
	tomlCfg, err := LoadConfig(configFile)
	if err != nil {
		return fmt.Errorf("failed to load config file '%s': %w", configFile, err)
	}

	// Validate TOML config
	if err := tomlCfg.Validate(); err != nil {
		return fmt.Errorf("invalid config in '%s': %w", configFile, err)
	}

	// Convert TOML to env vars and set them BEFORE CLI context processes flags
	envVars := tomlCfg.ConvertToEnvVars()
	for key, value := range envVars {
		os.Setenv(key, value)
	}

	return nil
}

func StartDAServer(cliCtx *cli.Context) error {
	// Initialize logger early for warnings
	logCfg := oplog.ReadCLIConfig(cliCtx)
	l := oplog.NewLogger(oplog.AppOut(cliCtx), logCfg)

	// Check if TOML config file is specified
	configFile := cliCtx.String(ConfigFileFlagName)
	if configFile != "" {
		l.Info("Loading configuration from TOML file", "path", configFile)

		// Config was already loaded in Before hook, just re-load for validation messages
		tomlCfg, err := LoadConfig(configFile)
		if err != nil {
			return fmt.Errorf("failed to load config file '%s': %w", configFile, err)
		}
		if err := tomlCfg.Validate(); err != nil {
			return fmt.Errorf("invalid config in '%s': %w", configFile, err)
		}

		l.Info("✓ TOML configuration loaded and validated successfully")
		l.Info("Configuration source: TOML file", "path", configFile)
		l.Info("Note: Environment variables were set from TOML in main() before CLI parsing")

		// Skip CheckRequired when using TOML - we already validated the config
		// CheckRequired uses ctx.IsSet() which only checks command-line flags,
		// not environment variables set programmatically
	} else {
		l.Info("Configuration source: Environment variables and CLI flags")
		l.Info("Tip: Use --config config.toml for production deployments")

		// Only check required flags when NOT using TOML config
		if err := CheckRequired(cliCtx); err != nil {
			return err
		}
	}

	cfg := ReadCLIConfig(cliCtx)
	if err := cfg.Check(); err != nil {
		return err
	}

	// Set global log handler (logger already initialized above)
	oplog.SetGlobalLogHandler(l.Handler())

	l.Info("Initializing Async Alt-DA server...")

	// Display metrics configuration prominently
	l.Info("========================================")
	if cfg.MetricsEnabled {
		l.Info("Prometheus Metrics: ENABLED", "port", cfg.MetricsPort)
	} else {
		l.Info("Prometheus Metrics: DISABLED", "note", "Set --metrics.enabled or OP_ALTDA_METRICS_ENABLED=true to enable")
	}
	l.Info("========================================")

	// Display detected Celestia connection mode prominently
	l.Info("========================================")
	l.Info("Celestia Connection Mode Detected", "mode", cfg.GetCelestiaMode())

	// Log detailed configuration
	details := cfg.GetCelestiaModeDetails()
	for key, value := range details {
		l.Info("  "+key, "value", value)
	}
	l.Info("========================================")

	dbPath := cliCtx.String(DBPathFlagName)
	l.Info("Opening database", "path", dbPath)

	store, err := db.NewBlobStore(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	// Initialize Celestia store (for client access)
	l.Info("Connecting to Celestia", "url", cfg.CelestiaConfig().URL)
	celestiaStore, err := celestia.NewCelestiaStore(cliCtx.Context, cfg.CelestiaConfig())
	if err != nil {
		return fmt.Errorf("failed to connect to Celestia: %w", err)
	}

	batchCfg := &batch.Config{
		MinBlobs:          cliCtx.Int(BatchMinBlobsFlagName),
		MaxBlobs:          cliCtx.Int(BatchMaxBlobsFlagName),
		TargetBlobs:       cliCtx.Int(BatchTargetBlobsFlagName),
		MaxBatchSizeBytes: cliCtx.Int(BatchMaxSizeFlagName) * 1024 * 1024, // Convert MB to bytes
		MinBatchSizeBytes: cliCtx.Int(BatchMinSizeFlagName) * 1024,        // Convert KB to bytes
	}

	if err := batchCfg.Validate(); err != nil {
		return fmt.Errorf("invalid batch configuration: %w", err)
	}

	l.Info("Batch configuration",
		"min_blobs", batchCfg.MinBlobs,
		"max_blobs", batchCfg.MaxBlobs,
		"target_blobs", batchCfg.TargetBlobs,
		"max_size_mb", cliCtx.Int(BatchMaxSizeFlagName),
		"min_size_kb", cliCtx.Int(BatchMinSizeFlagName))

	// Parse trusted signers from comma-separated string
	var trustedSigners []string
	trustedSignerStr := cliCtx.String(TrustedSignerFlagName)
	if trustedSignerStr != "" {
		// Split by comma and trim whitespace
		for _, signer := range strings.Split(trustedSignerStr, ",") {
			trimmed := strings.TrimSpace(signer)
			if trimmed != "" {
				trustedSigners = append(trustedSigners, trimmed)
			}
		}
	}

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
	}

	if err := validateWorkerConfig(workerCfg); err != nil {
		return fmt.Errorf("invalid worker configuration: %w", err)
	}

	l.Info("Worker configuration",
		"read_only", workerCfg.ReadOnly,
		"trusted_signers", strings.Join(workerCfg.TrustedSigners, ","),
		"submit_period", workerCfg.SubmitPeriod,
		"submit_timeout", workerCfg.SubmitTimeout,
		"max_retries", workerCfg.MaxRetries,
		"max_blob_wait_time", workerCfg.MaxBlobWaitTime,
		"reconcile_period", workerCfg.ReconcilePeriod,
		"reconcile_age", workerCfg.ReconcileAge,
		"get_timeout", workerCfg.GetTimeout,
		"backfill_enabled", workerCfg.BackfillEnabled,
		"start_height", workerCfg.StartHeight,
		"backfill_period", workerCfg.BackfillPeriod)

	server := celestia.NewCelestiaServer(
		cliCtx.String(ListenAddrFlagName),
		cliCtx.Int(PortFlagName),
		store,
		celestiaStore,
		batchCfg,
		workerCfg,
		cfg.MetricsEnabled,
		cfg.MetricsPort,
		l,
	)

	// Create context with cancel
	ctx, cancel := context.WithCancel(cliCtx.Context)
	defer cancel()

	// Use errgroup for proper goroutine management
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		l.Info("Starting server")
		if err := server.Start(ctx); err != nil && err != context.Canceled {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	})

	// Start S3 backup service if enabled
	if cliCtx.Bool(BackupEnabledFlagName) {
		// Check if S3 is configured (bucket must be set)
		if cfg.S3Config.Bucket != "" {
			l.Info("S3 backup enabled", "interval", cliCtx.Duration(BackupIntervalFlagName))

			s3Store, err := s3store.NewS3(cfg.S3Config)
			if err != nil {
				return fmt.Errorf("failed to initialize S3: %w", err)
			}

			// Get underlying sql.DB from store
			sqlDB := store.GetDB()

			backupService := backup.NewS3BackupService(
				sqlDB,
				s3Store,
				dbPath,
				cliCtx.Duration(BackupIntervalFlagName),
				l.New("component", "s3_backup"),
			)

			g.Go(func() error {
				if err := backupService.Run(ctx); err != nil && err != context.Canceled {
					return fmt.Errorf("s3 backup error: %w", err)
				}
				return nil
			})
		} else {
			l.Warn("Backup enabled but S3 not configured, skipping backups")
		}
	}

	// Wait for interrupt signal
	g.Go(func() error {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

		select {
		case sig := <-sigChan:
			l.Info("Received shutdown signal", "signal", sig)
		case <-cliCtx.Done():
			l.Info("Context cancelled")
		}

		cancel()
		return nil
	})

	// Wait for all goroutines to finish
	if err := g.Wait(); err != nil && err != context.Canceled {
		l.Error("Server stopped with error", "error", err)
		return err
	}

	l.Info("Server stopped gracefully")
	return nil
}

// validateWorkerConfig validates worker configuration for common mistakes
func validateWorkerConfig(cfg *worker.Config) error {
	// Warn if read-only mode is enabled but backfill is not
	if cfg.ReadOnly && !cfg.BackfillEnabled {
		return fmt.Errorf("read-only mode requires backfill to be enabled (set --backfill.enabled=true)")
	}

	// Strongly recommend trusted signers in read-only mode for security (CIP-21)
	if cfg.ReadOnly && len(cfg.TrustedSigners) == 0 {
		return fmt.Errorf("read-only mode requires trusted signers for security (set --trusted-signer=<address1>,<address2>,...). Without it, malicious actors can inject junk data into your namespace")
	}

	// Warn if backfill is enabled without specifying a start height
	if cfg.BackfillEnabled && cfg.StartHeight == 0 {
		// This is just a warning - starting from 0 means "latest"
		// We don't return an error here
	}

	return nil
}
