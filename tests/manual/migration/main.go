package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// =============================================================================
// CONFIGURATION
// =============================================================================

type Config struct {
	ServerURL       string
	Phase           string // "write", "read", or "both"
	BlobCount       int
	BlobSize        int
	CommitmentsFile string
	HTTPTimeout     time.Duration
	Verbose         bool
}

func DefaultConfig() Config {
	return Config{
		ServerURL:       "http://localhost:3100",
		Phase:           "write",
		BlobCount:       5,
		BlobSize:        128 * 1024, // 128KB
		CommitmentsFile: "commitments.json",
		HTTPTimeout:     120 * time.Second,
		Verbose:         false,
	}
}

// =============================================================================
// COMMITMENTS FILE FORMAT
// =============================================================================

// CommitmentsFile stores blob commitments and data hashes between phases
type CommitmentsFile struct {
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	S3Prefix  string    `json:"s3_prefix"`
	Blobs     []BlobRef `json:"blobs"`
}

// BlobRef stores a single blob's commitment and data hash for verification
type BlobRef struct {
	Commitment string `json:"commitment"` // Hex-encoded commitment from server
	DataHash   string `json:"data_hash"`  // SHA256 of original data for verification
	Size       int    `json:"size"`       // Blob size in bytes
}

// =============================================================================
// HTTP CLIENT
// =============================================================================

type Client struct {
	httpClient *http.Client
	serverURL  string
	verbose    bool
}

func NewClient(serverURL string, timeout time.Duration, verbose bool) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: timeout},
		serverURL:  serverURL,
		verbose:    verbose,
	}
}

// Put submits a blob and returns the commitment
func (c *Client) Put(ctx context.Context, data []byte) ([]byte, error) {
	url := c.serverURL + "/put"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// Get retrieves a blob by commitment
func (c *Client) Get(ctx context.Context, commitment []byte) ([]byte, error) {
	url := fmt.Sprintf("%s/get/0x%s", c.serverURL, hex.EncodeToString(commitment))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// =============================================================================
// PHASE 1: WRITE
// =============================================================================

func runWritePhase(ctx context.Context, config Config) error {
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Println("           PHASE 1: WRITE (Submit blobs to v0.9.0)")
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Printf("Server:      %s\n", config.ServerURL)
	fmt.Printf("Blob count:  %d\n", config.BlobCount)
	fmt.Printf("Blob size:   %d bytes (~%d KB)\n", config.BlobSize, config.BlobSize/1024)
	fmt.Printf("Output file: %s\n", config.CommitmentsFile)
	fmt.Println("────────────────────────────────────────────────────────────────")
	fmt.Println()

	client := NewClient(config.ServerURL, config.HTTPTimeout, config.Verbose)

	commitments := CommitmentsFile{
		Version:   "0.9.0",
		CreatedAt: time.Now().UTC(),
		S3Prefix:  "migration/",
		Blobs:     make([]BlobRef, 0, config.BlobCount),
	}

	successCount := 0
	failCount := 0

	for i := 1; i <= config.BlobCount; i++ {
		select {
		case <-ctx.Done():
			fmt.Println("\n⚠️  Interrupted! Saving partial results...")
			return saveCommitments(config.CommitmentsFile, commitments)
		default:
		}

		// Generate random data
		data := make([]byte, config.BlobSize)
		if _, err := rand.Read(data); err != nil {
			fmt.Printf("[%d/%d] ❌ Failed to generate random data: %v\n", i, config.BlobCount, err)
			failCount++
			continue
		}

		// Compute hash for later verification
		hash := sha256.Sum256(data)
		dataHash := fmt.Sprintf("sha256:%s", hex.EncodeToString(hash[:]))

		// Submit blob
		fmt.Printf("[%d/%d] Submitting blob (%d bytes)...", i, config.BlobCount, config.BlobSize)
		start := time.Now()

		commitment, err := client.Put(ctx, data)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf(" ❌ Failed after %v: %v\n", elapsed.Round(time.Millisecond), err)
			failCount++
			continue
		}

		commitmentHex := hex.EncodeToString(commitment)
		fmt.Printf(" ✅ OK in %v\n", elapsed.Round(time.Millisecond))
		if config.Verbose {
			fmt.Printf("    Commitment: %s\n", commitmentHex)
			fmt.Printf("    Data hash:  %s\n", dataHash)
		}

		commitments.Blobs = append(commitments.Blobs, BlobRef{
			Commitment: commitmentHex,
			DataHash:   dataHash,
			Size:       config.BlobSize,
		})
		successCount++
	}

	// Save commitments to file
	if err := saveCommitments(config.CommitmentsFile, commitments); err != nil {
		return fmt.Errorf("failed to save commitments: %w", err)
	}

	fmt.Println()
	fmt.Println("────────────────────────────────────────────────────────────────")
	fmt.Printf("✅ WRITE PHASE COMPLETE\n")
	fmt.Printf("   Success: %d\n", successCount)
	fmt.Printf("   Failed:  %d\n", failCount)
	fmt.Printf("   Saved:   %s\n", config.CommitmentsFile)
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1. Stop the v0.9.0 server")
	fmt.Println("  2. Start v0.10.0 server with fake Celestia endpoint")
	fmt.Println("  3. Run: ./migration-test -phase read")
	fmt.Println()

	return nil
}

func saveCommitments(path string, commitments CommitmentsFile) error {
	data, err := json.MarshalIndent(commitments, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// =============================================================================
// PHASE 2: READ
// =============================================================================

func runReadPhase(ctx context.Context, config Config) error {
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Println("           PHASE 2: READ (Verify blobs from S3 fallback)")
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Printf("Server:      %s\n", config.ServerURL)
	fmt.Printf("Input file:  %s\n", config.CommitmentsFile)
	fmt.Println("────────────────────────────────────────────────────────────────")
	fmt.Println()

	// Load commitments
	commitments, err := loadCommitments(config.CommitmentsFile)
	if err != nil {
		return fmt.Errorf("failed to load commitments: %w", err)
	}

	fmt.Printf("Loaded %d commitments from %s\n", len(commitments.Blobs), config.CommitmentsFile)
	fmt.Printf("Written by version: %s at %s\n", commitments.Version, commitments.CreatedAt.Format(time.RFC3339))
	fmt.Printf("S3 prefix: %s\n", commitments.S3Prefix)
	fmt.Println()

	client := NewClient(config.ServerURL, config.HTTPTimeout, config.Verbose)

	successCount := 0
	failCount := 0
	hashMismatchCount := 0

blobLoop:
	for i, blob := range commitments.Blobs {
		select {
		case <-ctx.Done():
			fmt.Println("\n⚠️  Interrupted!")
			break blobLoop
		default:
		}

		commitment, err := hex.DecodeString(blob.Commitment)
		if err != nil {
			fmt.Printf("[%d/%d] ❌ Invalid commitment hex: %v\n", i+1, len(commitments.Blobs), err)
			failCount++
			continue
		}

		fmt.Printf("[%d/%d] Reading blob (commitment: %s...)...", i+1, len(commitments.Blobs), blob.Commitment[:16])
		start := time.Now()

		data, err := client.Get(ctx, commitment)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf(" ❌ Failed after %v: %v\n", elapsed.Round(time.Millisecond), err)
			failCount++
			continue
		}

		// Verify data hash
		hash := sha256.Sum256(data)
		actualHash := fmt.Sprintf("sha256:%s", hex.EncodeToString(hash[:]))

		if actualHash != blob.DataHash {
			fmt.Printf(" ⚠️  HASH MISMATCH after %v\n", elapsed.Round(time.Millisecond))
			fmt.Printf("    Expected: %s\n", blob.DataHash)
			fmt.Printf("    Got:      %s\n", actualHash)
			hashMismatchCount++
			continue
		}

		fmt.Printf(" ✅ OK in %v (verified)\n", elapsed.Round(time.Millisecond))
		successCount++
	}

	fmt.Println()
	fmt.Println("────────────────────────────────────────────────────────────────")
	fmt.Printf("📥 READ PHASE RESULTS\n")
	fmt.Printf("   Success (verified): %d\n", successCount)
	fmt.Printf("   Failed:             %d\n", failCount)
	fmt.Printf("   Hash mismatch:      %d\n", hashMismatchCount)
	fmt.Println("────────────────────────────────────────────────────────────────")

	if failCount == 0 && hashMismatchCount == 0 && successCount == len(commitments.Blobs) {
		fmt.Println("✅ MIGRATION COMPATIBILITY: PASSED")
		fmt.Println("   All blobs written by v0.9.0 are readable by v0.10.0")
	} else {
		fmt.Println("❌ MIGRATION COMPATIBILITY: FAILED")
		fmt.Printf("   %d/%d blobs could not be verified\n", failCount+hashMismatchCount, len(commitments.Blobs))
	}
	fmt.Println("════════════════════════════════════════════════════════════════")

	if failCount > 0 || hashMismatchCount > 0 {
		return fmt.Errorf("verification failed: %d errors", failCount+hashMismatchCount)
	}

	return nil
}

func loadCommitments(path string) (*CommitmentsFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var commitments CommitmentsFile
	if err := json.Unmarshal(data, &commitments); err != nil {
		return nil, err
	}

	return &commitments, nil
}

// =============================================================================
// PHASE: CONTINUE (Optional - post new blobs with current branch)
// =============================================================================

func runContinuePhase(ctx context.Context, config Config) error {
	fmt.Println()
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Println("           PHASE 3: CONTINUE (Post new blobs with v0.10.0)")
	fmt.Println("════════════════════════════════════════════════════════════════")

	// Load existing commitments
	commitments, err := loadCommitments(config.CommitmentsFile)
	if err != nil {
		return fmt.Errorf("failed to load commitments: %w", err)
	}

	originalCount := len(commitments.Blobs)
	fmt.Printf("Loaded %d existing commitments\n", originalCount)
	fmt.Printf("Will add %d new blobs\n", config.BlobCount)
	fmt.Println()

	client := NewClient(config.ServerURL, config.HTTPTimeout, config.Verbose)

	successCount := 0
	failCount := 0

	for i := 1; i <= config.BlobCount; i++ {
		select {
		case <-ctx.Done():
			fmt.Println("\n⚠️  Interrupted! Saving partial results...")
			commitments.CreatedAt = time.Now().UTC()
			return saveCommitments(config.CommitmentsFile, *commitments)
		default:
		}

		// Generate random data
		data := make([]byte, config.BlobSize)
		if _, err := rand.Read(data); err != nil {
			fmt.Printf("[%d/%d] ❌ Failed to generate random data: %v\n", i, config.BlobCount, err)
			failCount++
			continue
		}

		// Compute hash
		hash := sha256.Sum256(data)
		dataHash := fmt.Sprintf("sha256:%s", hex.EncodeToString(hash[:]))

		// Submit blob
		fmt.Printf("[%d/%d] Submitting new blob...", i, config.BlobCount)
		start := time.Now()

		commitment, err := client.Put(ctx, data)
		elapsed := time.Since(start)

		if err != nil {
			fmt.Printf(" ❌ Failed after %v: %v\n", elapsed.Round(time.Millisecond), err)
			failCount++
			continue
		}

		commitmentHex := hex.EncodeToString(commitment)
		fmt.Printf(" ✅ OK in %v\n", elapsed.Round(time.Millisecond))

		commitments.Blobs = append(commitments.Blobs, BlobRef{
			Commitment: commitmentHex,
			DataHash:   dataHash,
			Size:       config.BlobSize,
		})
		successCount++
	}

	// Update and save
	commitments.Version = "0.10.0"
	commitments.CreatedAt = time.Now().UTC()
	if err := saveCommitments(config.CommitmentsFile, *commitments); err != nil {
		return fmt.Errorf("failed to save commitments: %w", err)
	}

	fmt.Println()
	fmt.Println("────────────────────────────────────────────────────────────────")
	fmt.Printf("✅ CONTINUE PHASE COMPLETE\n")
	fmt.Printf("   New blobs added: %d (success: %d, failed: %d)\n", config.BlobCount, successCount, failCount)
	fmt.Printf("   Total blobs:     %d\n", len(commitments.Blobs))
	fmt.Printf("   Saved:           %s\n", config.CommitmentsFile)
	fmt.Println("════════════════════════════════════════════════════════════════")

	return nil
}

// =============================================================================
// MAIN
// =============================================================================

func main() {
	config := parseFlags()

	// Setup graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var err error

	switch config.Phase {
	case "write":
		err = runWritePhase(ctx, config)
	case "read":
		err = runReadPhase(ctx, config)
	case "continue":
		err = runContinuePhase(ctx, config)
	case "both":
		// Run write, then prompt user to switch servers, then read
		err = runWritePhase(ctx, config)
		if err != nil {
			break
		}
		fmt.Println()
		fmt.Println("═══════════════════════════════════════════════════════════════════")
		fmt.Println("  SWITCH SERVERS: v0.9.0 → v0.10.0")
		fmt.Println("  1. Stop the v0.9.0 server (Ctrl+C)")
		fmt.Println("  2. Start v0.10.0 server with fake Celestia endpoint")
		fmt.Println("  3. Press Enter to continue with read phase...")
		fmt.Println("═══════════════════════════════════════════════════════════════════")
		fmt.Println()
		fmt.Scanln()
		err = runReadPhase(ctx, config)
	default:
		fmt.Fprintf(os.Stderr, "Unknown phase: %s (use 'write', 'read', 'continue', or 'both')\n", config.Phase)
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags() Config {
	config := DefaultConfig()

	flag.StringVar(&config.ServerURL, "url", config.ServerURL, "DA server URL")
	flag.StringVar(&config.Phase, "phase", config.Phase, "Phase to run: write, read, continue, or both")
	flag.IntVar(&config.BlobCount, "blobs", config.BlobCount, "Number of blobs to submit")
	flag.IntVar(&config.BlobSize, "size", config.BlobSize, "Blob size in bytes")
	flag.StringVar(&config.CommitmentsFile, "commitments", config.CommitmentsFile, "Path to commitments file")
	flag.BoolVar(&config.Verbose, "v", config.Verbose, "Verbose output")
	timeoutSecs := flag.Int("timeout", 120, "HTTP timeout in seconds")
	flag.Parse()

	config.HTTPTimeout = time.Duration(*timeoutSecs) * time.Second

	return config
}
