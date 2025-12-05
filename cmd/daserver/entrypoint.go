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
	BatchMaxSizeFlagName     = "batch.max-size-kb"
	BatchMinSizeFlagName     = "batch.min-size-kb"

	// Worker configuration
	WorkerSubmitTimeoutFlagName          = "worker.submit-timeout"
	WorkerMaxRetriesFlagName             = "worker.max-retries"
	WorkerMaxParallelSubmissionsFlagName = "worker.max-parallel-submissions"
	WorkerReconcilePeriodFlagName        = "worker.reconcile-period"
	WorkerReconcileAgeFlagName           = "worker.reconcile-age"
	WorkerGetTimeoutFlagName             = "worker.get-timeout"

	// Read-only configuration
	ReadOnlyFlagName       = "read-only"
	TrustedSignersFlagName = "trusted-signers"

	// Backfill configuration (for historical data migration)
	BackfillEnabledFlagName       = "backfill.enabled"
	BackfillStartHeightFlagName   = "backfill.start-height"
	BackfillTargetHeightFlagName  = "backfill.target-height"
	BackfillPeriodFlagName        = "backfill.period"
	BackfillBlocksPerScanFlagName = "backfill.blocks-per-scan"
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
		Usage:   "maximum batch size in KB (use KB for precision, e.g., 1800 for 1.8MB)",
		Value:   1024,
		EnvVars: prefixEnvVars("BATCH_MAX_SIZE_KB"),
	}
	BatchMinSizeFlag = &cli.IntFlag{
		Name:    BatchMinSizeFlagName,
		Usage:   "minimum batch size in KB before forcing submission",
		Value:   500,
		EnvVars: prefixEnvVars("BATCH_MIN_SIZE_KB"),
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
	WorkerMaxParallelSubmissionsFlag = &cli.IntFlag{
		Name:    WorkerMaxParallelSubmissionsFlagName,
		Usage:   "number of parallel Submit() calls to Celestia (should match TxWorkerAccounts on node)",
		Value:   1,
		EnvVars: prefixEnvVars("WORKER_MAX_PARALLEL_SUBMISSIONS"),
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
	TrustedSignersFlag = &cli.StringFlag{
		Name:    TrustedSignersFlagName,
		Usage:   "Comma-separated Celestia addresses of trusted write servers (CIP-21 signer verification for HA failover)",
		Value:   "",
		EnvVars: prefixEnvVars("TRUSTED_SIGNERS"),
	}
	BackfillEnabledFlag = &cli.BoolFlag{
		Name:    BackfillEnabledFlagName,
		Usage:   "enable backfill worker for read-only servers (syncs from Celestia)",
		Value:   false,
		EnvVars: prefixEnvVars("BACKFILL_ENABLED"),
	}
	BackfillStartHeightFlag = &cli.Uint64Flag{
		Name:    BackfillStartHeightFlagName,
		Usage:   "Celestia block height to start backfilling from",
		Value:   0,
		EnvVars: prefixEnvVars("BACKFILL_START_HEIGHT"),
	}
	BackfillTargetHeightFlag = &cli.Uint64Flag{
		Name:    BackfillTargetHeightFlagName,
		Usage:   "Celestia block height to backfill to (0 = disabled)",
		Value:   0,
		EnvVars: prefixEnvVars("BACKFILL_TARGET_HEIGHT"),
	}
	BackfillPeriodFlag = &cli.DurationFlag{
		Name:    BackfillPeriodFlagName,
		Usage:   "how often backfill worker scans for new Celestia blocks",
		Value:   15 * time.Second,
		EnvVars: prefixEnvVars("BACKFILL_PERIOD"),
	}
	BackfillBlocksPerScanFlag = &cli.IntFlag{
		Name:    BackfillBlocksPerScanFlagName,
		Usage:   "how many blocks to scan per backfill iteration (Celestia Mocha: ~10 blocks/min, default: 10)",
		Value:   10,
		EnvVars: prefixEnvVars("BACKFILL_BLOCKS_PER_SCAN"),
	}
)

// Note: LoadTOMLConfigIfPresent() has been removed as part of idiomatic Go refactor
// TOML config is now loaded directly in StartDAServer() without environment variable conversion

func StartDAServer(cliCtx *cli.Context) error {
	// Initialize logger early for warnings
	logCfg := oplog.ReadCLIConfig(cliCtx)
	l := oplog.NewLogger(oplog.AppOut(cliCtx), logCfg)

	configFile := cliCtx.String(ConfigFileFlagName)
	var runtimeCfg *RuntimeConfig
	var cfg CLIConfig
	var err error

	if configFile != "" {
		l.Info("Loading configuration from TOML file", "path", configFile)

		tomlCfg, err := LoadConfig(configFile)
		if err != nil {
			return fmt.Errorf("failed to load config file '%s': %w", configFile, err)
		}
		if err := tomlCfg.Validate(); err != nil {
			return fmt.Errorf("invalid config in '%s': %w", configFile, err)
		}

		runtimeCfg, err = BuildConfigFromTOML(tomlCfg)
		if err != nil {
			return fmt.Errorf("failed to build config from TOML: %w", err)
		}

		cfg = runtimeCfg.CelestiaConfig

		l.Info("✓ TOML configuration loaded and validated successfully")
		l.Info("Configuration source: TOML file", "path", configFile)
	} else {
		l.Info("Configuration source: Environment variables and CLI flags")
		l.Info("Tip: Use --config config.toml for production deployments")

		if err := CheckRequired(cliCtx); err != nil {
			return err
		}

		runtimeCfg, err = BuildConfigFromCLI(cliCtx)
		if err != nil {
			return fmt.Errorf("failed to build config from CLI: %w", err)
		}

		cfg = runtimeCfg.CelestiaConfig
	}

	// Check CLI config
	if err := cfg.Check(false); err != nil {
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

	// Use runtime config that was built from either TOML or CLI
	l.Info("Opening database", "path", runtimeCfg.DBPath)

	store, err := db.NewBlobStore(runtimeCfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Initialize Celestia store (for client access)
	l.Info("Connecting to Celestia", "bridge_addr", cfg.CelestiaConfig.BridgeAddr, "grpc_addr", cfg.CelestiaConfig.CoreGRPCAddr)
	celestiaStore, err := celestia.NewCelestiaStore(cliCtx.Context, cfg.CelestiaConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to Celestia: %w", err)
	}

	// Log batch configuration
	l.Info("Batch configuration",
		"min_blobs", runtimeCfg.BatchConfig.MinBlobs,
		"max_blobs", runtimeCfg.BatchConfig.MaxBlobs,
		"target_blobs", runtimeCfg.BatchConfig.TargetBlobs,
		"max_size_kb", runtimeCfg.BatchConfig.MaxBatchSizeBytes/1024,
		"min_size_kb", runtimeCfg.BatchConfig.MinBatchSizeBytes/1024)

	// Log worker configuration (continuous mode - no tick period)
	l.Info("Worker configuration",
		"mode", "continuous",
		"trusted_signers", strings.Join(runtimeCfg.WorkerConfig.TrustedSigners, ","),
		"submit_timeout", runtimeCfg.WorkerConfig.SubmitTimeout,
		"max_retries", runtimeCfg.WorkerConfig.MaxRetries,
		"max_parallel_submissions", runtimeCfg.WorkerConfig.MaxParallelSubmissions,
		"reconcile_period", runtimeCfg.WorkerConfig.ReconcilePeriod,
		"reconcile_age", runtimeCfg.WorkerConfig.ReconcileAge,
		"get_timeout", runtimeCfg.WorkerConfig.GetTimeout)

	// Log backfill configuration if enabled
	if runtimeCfg.WorkerConfig.BackfillEnabled && runtimeCfg.WorkerConfig.BackfillTargetHeight > 0 {
		l.Info("Backfill configuration",
			"start_height", runtimeCfg.WorkerConfig.StartHeight,
			"target_height", runtimeCfg.WorkerConfig.BackfillTargetHeight,
			"period", runtimeCfg.WorkerConfig.BackfillPeriod,
			"blocks_per_scan", runtimeCfg.WorkerConfig.BlocksPerScan)
	}

	server := celestia.NewCelestiaServer(
		runtimeCfg.Addr,
		runtimeCfg.Port,
		store,
		celestiaStore,
		runtimeCfg.BatchConfig,
		runtimeCfg.WorkerConfig,
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
	if runtimeCfg.BackupEnabled {
		// Check if S3 is configured (bucket must be set)
		if cfg.S3Config.Bucket != "" {
			backupInterval, err := time.ParseDuration(runtimeCfg.BackupInterval)
			if err != nil {
				return fmt.Errorf("invalid backup interval: %w", err)
			}

			l.Info("S3 backup enabled", "interval", backupInterval)

			s3Store, err := s3store.NewS3(cfg.S3Config)
			if err != nil {
				return fmt.Errorf("failed to initialize S3: %w", err)
			}

			// Get underlying sql.DB from store
			sqlDB := store.GetDB()

			backupService := backup.NewS3BackupService(
				sqlDB,
				s3Store,
				runtimeCfg.DBPath,
				backupInterval,
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
	// Validate backfill configuration
	if cfg.BackfillEnabled {
		if cfg.BackfillTargetHeight == 0 {
			return fmt.Errorf("backfill.target_height is required when backfill is enabled")
		}
		if cfg.StartHeight > cfg.BackfillTargetHeight {
			return fmt.Errorf("backfill.start_height (%d) cannot be greater than target_height (%d)",
				cfg.StartHeight, cfg.BackfillTargetHeight)
		}
		// Warn if backfill has trusted signers configured (for verification)
		if len(cfg.TrustedSigners) == 0 {
			// Just a warning - without trusted signers, all blobs in namespace are accepted
		}
	}

	return nil
}
