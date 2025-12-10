// s3migrate migrates S3 blobs from v0.9.0 key format to v0.10.0 key format.
//
// v0.9.0 stored blobs using: key = keccak256(commitment)
// v0.10.0 stores blobs using: key = hex(commitment)
//
// Usage:
//
//	# Migrate within same bucket, different prefix
//	s3migrate \
//	  --src-bucket my-bucket --src-prefix old/ \
//	  --dst-bucket my-bucket --dst-prefix new/ \
//	  --commitments commitments.txt
//
//	# Migrate to different bucket
//	s3migrate \
//	  --src-bucket old-bucket --src-prefix blobs/ \
//	  --dst-bucket new-bucket --dst-prefix blobs/ \
//	  --commitments commitments.txt
//
//	# Pipe commitments from another source
//	cat commitments.txt | s3migrate --src-bucket ... --dst-bucket ...
//
// Commitments file: one hex-encoded commitment per line (or JSON format from migration test)
package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/ethereum/go-ethereum/crypto"
)

type Config struct {
	// Source (v0.9.0 format)
	SrcBucket string
	SrcPrefix string

	// Destination (v0.10.0 format)
	DstBucket string
	DstPrefix string

	// AWS
	Region          string
	AccessKeyID     string
	AccessKeySecret string

	// Input
	CommitmentsFile string // If empty, read from stdin

	// Options
	DryRun    bool
	DeleteOld bool
	Verbose   bool
}

func main() {
	cfg := parseFlags()

	if err := validateConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Load commitments
	commitments, err := loadCommitments(cfg.CommitmentsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading commitments: %v\n", err)
		os.Exit(1)
	}

	if len(commitments) == 0 {
		fmt.Fprintln(os.Stderr, "Error: no commitments provided")
		os.Exit(1)
	}

	printHeader(cfg, len(commitments))

	// Initialize S3 client
	ctx := context.Background()
	client, err := initS3Client(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing S3 client: %v\n", err)
		os.Exit(1)
	}

	// Migrate
	stats := migrate(ctx, client, cfg, commitments)
	printSummary(stats, cfg.DryRun)

	if stats.Failed > 0 {
		os.Exit(1)
	}
}

type Stats struct {
	Migrated int
	Skipped  int
	Failed   int
}

func migrate(ctx context.Context, client *s3.Client, cfg Config, commitments [][]byte) Stats {
	var stats Stats

	for i, commitment := range commitments {
		srcKey := makeSrcKey(cfg.SrcPrefix, commitment)
		dstKey := makeDstKey(cfg.DstPrefix, commitment)

		if cfg.Verbose {
			fmt.Printf("[%d/%d] %s\n", i+1, len(commitments), hex.EncodeToString(commitment)[:16])
			fmt.Printf("       src: %s/%s\n", cfg.SrcBucket, srcKey)
			fmt.Printf("       dst: %s/%s\n", cfg.DstBucket, dstKey)
		}

		// Check source exists
		srcExists, err := objectExists(ctx, client, cfg.SrcBucket, srcKey)
		if err != nil {
			fmt.Printf("[%d/%d] ❌ Error checking source: %v\n", i+1, len(commitments), err)
			stats.Failed++
			continue
		}
		if !srcExists {
			if cfg.Verbose {
				fmt.Printf("[%d/%d] ⏭️  Source not found, skipping\n", i+1, len(commitments))
			}
			stats.Skipped++
			continue
		}

		// Check destination exists
		dstExists, err := objectExists(ctx, client, cfg.DstBucket, dstKey)
		if err != nil {
			fmt.Printf("[%d/%d] ❌ Error checking destination: %v\n", i+1, len(commitments), err)
			stats.Failed++
			continue
		}
		if dstExists {
			if cfg.Verbose {
				fmt.Printf("[%d/%d] ⏭️  Destination exists, skipping\n", i+1, len(commitments))
			}
			stats.Skipped++
			continue
		}

		// Copy
		if !cfg.DryRun {
			if err := copyObject(ctx, client, cfg.SrcBucket, srcKey, cfg.DstBucket, dstKey); err != nil {
				fmt.Printf("[%d/%d] ❌ Copy failed: %v\n", i+1, len(commitments), err)
				stats.Failed++
				continue
			}

			if cfg.DeleteOld {
				if err := deleteObject(ctx, client, cfg.SrcBucket, srcKey); err != nil {
					fmt.Printf("[%d/%d] ⚠️  Copied but delete failed: %v\n", i+1, len(commitments), err)
				}
			}
		}

		fmt.Printf("[%d/%d] ✅ %s\n", i+1, len(commitments), hex.EncodeToString(commitment)[:16])
		stats.Migrated++
	}

	return stats
}

func parseFlags() Config {
	cfg := Config{}

	// Source
	flag.StringVar(&cfg.SrcBucket, "src-bucket", "", "Source S3 bucket (v0.9.0 format)")
	flag.StringVar(&cfg.SrcPrefix, "src-prefix", "", "Source S3 prefix")

	// Destination
	flag.StringVar(&cfg.DstBucket, "dst-bucket", "", "Destination S3 bucket (v0.10.0 format)")
	flag.StringVar(&cfg.DstPrefix, "dst-prefix", "", "Destination S3 prefix")

	// AWS
	flag.StringVar(&cfg.Region, "region", "us-east-1", "AWS region")
	flag.StringVar(&cfg.AccessKeyID, "access-key-id", "", "AWS access key ID (or AWS_ACCESS_KEY_ID env)")
	flag.StringVar(&cfg.AccessKeySecret, "access-key-secret", "", "AWS secret key (or AWS_SECRET_ACCESS_KEY env)")

	// Input
	flag.StringVar(&cfg.CommitmentsFile, "commitments", "", "Commitments file (one per line, or JSON). If omitted, reads from stdin")

	// Options
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "Preview without making changes")
	flag.BoolVar(&cfg.DeleteOld, "delete-src", false, "Delete source objects after copying")
	flag.BoolVar(&cfg.Verbose, "v", false, "Verbose output")

	flag.Parse()

	// Environment fallbacks
	if cfg.AccessKeyID == "" {
		cfg.AccessKeyID = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	if cfg.AccessKeySecret == "" {
		cfg.AccessKeySecret = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}

	return cfg
}

func validateConfig(cfg Config) error {
	if cfg.SrcBucket == "" {
		return fmt.Errorf("--src-bucket is required")
	}
	if cfg.DstBucket == "" {
		return fmt.Errorf("--dst-bucket is required")
	}
	return nil
}

func loadCommitments(filePath string) ([][]byte, error) {
	var reader *bufio.Reader

	if filePath == "" {
		// Read from stdin
		stat, _ := os.Stdin.Stat()
		if (stat.Mode() & os.ModeCharDevice) != 0 {
			return nil, fmt.Errorf("no commitments file specified and stdin is empty. Use --commitments or pipe input")
		}
		reader = bufio.NewReader(os.Stdin)
	} else {
		file, err := os.Open(filePath)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		reader = bufio.NewReader(file)
	}

	// Try JSON first
	return parseCommitments(reader)
}

func parseCommitments(reader *bufio.Reader) ([][]byte, error) {
	// Read all content
	var lines []string
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}

	if len(lines) == 0 {
		return nil, nil
	}

	// Try JSON format first
	if strings.HasPrefix(lines[0], "{") {
		return parseJSON(strings.Join(lines, "\n"))
	}

	// Parse as plain hex commitments
	var commitments [][]byte
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue // Skip comments
		}
		line = strings.TrimPrefix(line, "0x")
		commitment, err := hex.DecodeString(line)
		if err != nil {
			return nil, fmt.Errorf("invalid hex: %s", line)
		}
		commitments = append(commitments, commitment)
	}

	return commitments, nil
}

func parseJSON(data string) ([][]byte, error) {
	// Try migration test format
	var jsonData struct {
		Blobs []struct {
			Commitment string `json:"commitment"`
		} `json:"blobs"`
	}

	if err := json.Unmarshal([]byte(data), &jsonData); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	var commitments [][]byte
	for _, blob := range jsonData.Blobs {
		commitment, err := hex.DecodeString(blob.Commitment)
		if err != nil {
			return nil, fmt.Errorf("invalid commitment hex: %s", blob.Commitment)
		}
		commitments = append(commitments, commitment)
	}

	return commitments, nil
}

func initS3Client(ctx context.Context, cfg Config) (*s3.Client, error) {
	var opts []func(*config.LoadOptions) error
	opts = append(opts, config.WithRegion(cfg.Region))

	if cfg.AccessKeyID != "" && cfg.AccessKeySecret != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.AccessKeySecret, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}

	return s3.NewFromConfig(awsCfg), nil
}

// makeSrcKey generates the v0.9.0 S3 key: prefix/keccak256(commitment)
func makeSrcKey(prefix string, commitment []byte) string {
	hash := crypto.Keccak256(commitment)
	return path.Join(prefix, hex.EncodeToString(hash))
}

// makeDstKey generates the v0.10.0 S3 key: prefix/hex(commitment)
func makeDstKey(prefix string, commitment []byte) string {
	return path.Join(prefix, hex.EncodeToString(commitment))
}

func objectExists(ctx context.Context, client *s3.Client, bucket, key string) (bool, error) {
	_, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "404") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func copyObject(ctx context.Context, client *s3.Client, srcBucket, srcKey, dstBucket, dstKey string) error {
	_, err := client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(dstBucket),
		CopySource: aws.String(srcBucket + "/" + srcKey),
		Key:        aws.String(dstKey),
	})
	return err
}

func deleteObject(ctx context.Context, client *s3.Client, bucket, key string) error {
	_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	return err
}

func printHeader(cfg Config, count int) {
	fmt.Println("S3 Key Migration: v0.9.0 → v0.10.0")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("Source:      s3://%s/%s\n", cfg.SrcBucket, cfg.SrcPrefix)
	fmt.Printf("Destination: s3://%s/%s\n", cfg.DstBucket, cfg.DstPrefix)
	fmt.Printf("Commitments: %d\n", count)
	fmt.Printf("Delete src:  %v\n", cfg.DeleteOld)
	fmt.Println("───────────────────────────────────────────────────────────")

	if cfg.DryRun {
		fmt.Println("⚠️  DRY RUN - no changes will be made")
		fmt.Println("───────────────────────────────────────────────────────────")
	}
	fmt.Println()
}

func printSummary(stats Stats, dryRun bool) {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("✅ Migrated: %d\n", stats.Migrated)
	fmt.Printf("⏭️  Skipped:  %d\n", stats.Skipped)
	fmt.Printf("❌ Failed:   %d\n", stats.Failed)

	if dryRun && stats.Migrated > 0 {
		fmt.Println()
		fmt.Println("Run without --dry-run to apply changes")
	}
}
