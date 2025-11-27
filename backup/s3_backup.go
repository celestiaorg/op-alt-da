package backup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"golang.org/x/sync/errgroup"

	s3store "github.com/celestiaorg/op-alt-da/s3"
)

type S3BackupService struct {
	db         *sql.DB
	s3         *s3store.S3Store
	dbPath     string
	backupPath string
	interval   time.Duration
	log        log.Logger
}

func NewS3BackupService(
	db *sql.DB,
	s3 *s3store.S3Store,
	dbPath string,
	interval time.Duration,
	log log.Logger,
) *S3BackupService {
	return &S3BackupService{
		db:         db,
		s3:         s3,
		dbPath:     dbPath,
		backupPath: "backups/",
		interval:   interval,
		log:        log,
	}
}

func (b *S3BackupService) Run(ctx context.Context) error {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	b.log.Info("S3 backup service started", "interval", b.interval)

	// Run initial backup
	if err := b.performBackup(ctx); err != nil {
		b.log.Error("Initial backup failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			b.log.Info("S3 backup service stopping")
			return ctx.Err()
		case <-ticker.C:
			if err := b.performBackup(ctx); err != nil {
				b.log.Error("Backup failed", "error", err)
			}
		}
	}
}

func (b *S3BackupService) performBackup(ctx context.Context) error {
	startTime := time.Now()
	timestamp := time.Now().Format("20060102-150405")

	b.log.Info("Starting database backup", "timestamp", timestamp)

	// Create temporary backup file using SQLite VACUUM INTO
	tempDir := filepath.Dir(b.dbPath)
	tempBackupPath := filepath.Join(tempDir, fmt.Sprintf("backup-%s.db", timestamp))

	// Use VACUUM INTO to create a consistent snapshot
	_, err := b.db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", tempBackupPath))
	if err != nil {
		return fmt.Errorf("vacuum into failed: %w", err)
	}
	defer os.Remove(tempBackupPath)

	b.log.Info("Database snapshot created", "path", tempBackupPath)

	// Read the backup file
	backupData, err := os.ReadFile(tempBackupPath)
	if err != nil {
		return fmt.Errorf("read backup file: %w", err)
	}

	// Upload to S3
	// S3 Put expects []byte key, so we'll use the timestamp as the key
	s3Key := []byte(fmt.Sprintf("db-%s.db", timestamp))
	if err := b.s3.Put(ctx, s3Key, backupData); err != nil {
		return fmt.Errorf("s3 upload failed: %w", err)
	}

	s3KeyStr := string(s3Key)

	b.log.Info("Backup completed",
		"s3_key", s3KeyStr,
		"size_mb", len(backupData)/1024/1024,
		"duration_s", time.Since(startTime).Seconds())

	return nil
}

// RunWithErrgroup runs the backup service as part of an errgroup
func (b *S3BackupService) RunWithErrgroup(g *errgroup.Group, ctx context.Context) {
	g.Go(func() error {
		if err := b.Run(ctx); err != nil && err != context.Canceled {
			return fmt.Errorf("s3 backup service error: %w", err)
		}
		return nil
	})
}
