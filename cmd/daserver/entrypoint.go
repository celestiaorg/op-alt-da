package main

import (
	"context"
	"fmt"
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
	DBPathFlagName          = "db.path"
	BackupEnabledFlagName   = "backup.enabled"
	BackupIntervalFlagName  = "backup.interval"

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
	WorkerReconcilePeriodFlagName = "worker.reconcile-period"
	WorkerReconcileAgeFlagName    = "worker.reconcile-age"
	WorkerGetTimeoutFlagName      = "worker.get-timeout"
)

var (
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
)

func StartDAServer(cliCtx *cli.Context) error {
	if err := CheckRequired(cliCtx); err != nil {
		return err
	}

	cfg := ReadCLIConfig(cliCtx)
	if err := cfg.Check(); err != nil {
		return err
	}

	logCfg := oplog.ReadCLIConfig(cliCtx)
	l := oplog.NewLogger(oplog.AppOut(cliCtx), logCfg)
	oplog.SetGlobalLogHandler(l.Handler())

	l.Info("Initializing Async Alt-DA server...")

	// Initialize database
	dbPath := cliCtx.String(DBPathFlagName)
	l.Info("Opening database", "path", dbPath)

	store, err := db.NewBlobStore(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer store.Close()

	// Initialize Celestia store (for client access)
	l.Info("Connecting to Celestia", "url", cfg.CelestiaConfig().URL)
	celestiaStore, err := celestia.NewCelestiaStore(cfg.CelestiaConfig())
	if err != nil {
		return fmt.Errorf("failed to connect to Celestia: %w", err)
	}

	// Create batch configuration
	batchCfg := &batch.Config{
		MinBlobs:          cliCtx.Int(BatchMinBlobsFlagName),
		MaxBlobs:          cliCtx.Int(BatchMaxBlobsFlagName),
		TargetBlobs:       cliCtx.Int(BatchTargetBlobsFlagName),
		MaxBatchSizeBytes: cliCtx.Int(BatchMaxSizeFlagName) * 1024 * 1024, // Convert MB to bytes
		MinBatchSizeBytes: cliCtx.Int(BatchMinSizeFlagName) * 1024,        // Convert KB to bytes
	}

	// Validate batch configuration
	if err := batchCfg.Validate(); err != nil {
		return fmt.Errorf("invalid batch configuration: %w", err)
	}

	l.Info("Batch configuration",
		"min_blobs", batchCfg.MinBlobs,
		"max_blobs", batchCfg.MaxBlobs,
		"target_blobs", batchCfg.TargetBlobs,
		"max_size_mb", cliCtx.Int(BatchMaxSizeFlagName),
		"min_size_kb", cliCtx.Int(BatchMinSizeFlagName))

	// Create worker configuration
	workerCfg := &worker.Config{
		SubmitPeriod:    cliCtx.Duration(WorkerSubmitPeriodFlagName),
		SubmitTimeout:   cliCtx.Duration(WorkerSubmitTimeoutFlagName),
		MaxRetries:      cliCtx.Int(WorkerMaxRetriesFlagName),
		ReconcilePeriod: cliCtx.Duration(WorkerReconcilePeriodFlagName),
		ReconcileAge:    cliCtx.Duration(WorkerReconcileAgeFlagName),
		GetTimeout:      cliCtx.Duration(WorkerGetTimeoutFlagName),
	}

	l.Info("Worker configuration",
		"submit_period", workerCfg.SubmitPeriod,
		"submit_timeout", workerCfg.SubmitTimeout,
		"max_retries", workerCfg.MaxRetries,
		"reconcile_period", workerCfg.ReconcilePeriod,
		"reconcile_age", workerCfg.ReconcileAge,
		"get_timeout", workerCfg.GetTimeout)

	// Create server
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

	// Start server
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
		<-cliCtx.Done()
		l.Info("Received shutdown signal")
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
