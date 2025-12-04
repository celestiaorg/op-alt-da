package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// Config holds all test configuration parameters
type Config struct {
	WriterURL   string
	ReaderURL   string
	BlobSize    int
	PutInterval time.Duration
	ReaderLag   time.Duration
	GetInterval time.Duration
	Duration    time.Duration
}

// DefaultConfig returns sensible defaults
func DefaultConfig() Config {
	return Config{
		WriterURL:   "http://localhost:3100",
		ReaderURL:   "http://localhost:3100",
		BlobSize:    1000 * 1024, // ~1MB
		PutInterval: 1000 * time.Millisecond,
		ReaderLag:   25 * time.Second,
		GetInterval: 20 * time.Second,
		Duration:    100 * time.Second,
	}
}

// BlobRecord stores a blob and its metadata for verification
type BlobRecord struct {
	Commitment   []byte
	OriginalData []byte
	PutStartTime time.Time // When PUT request was initiated
	BlobNum      int
	Confirmed    bool          // Whether GET returned 200 OK
	RoundTrip    time.Duration // Time from PUT start to first successful GET
}

// BlobStore is a thread-safe store for blob records
type BlobStore struct {
	mu    sync.Mutex
	blobs []BlobRecord
}

func NewBlobStore() *BlobStore {
	return &BlobStore{}
}

func (s *BlobStore) Add(record BlobRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs = append(s.blobs, record)
}

func (s *BlobStore) Snapshot() []BlobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := make([]BlobRecord, len(s.blobs))
	copy(snapshot, s.blobs)
	return snapshot
}

func (s *BlobStore) MarkConfirmed(index int, roundTrip time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < len(s.blobs) {
		s.blobs[index].Confirmed = true
		s.blobs[index].RoundTrip = roundTrip
	}
}

func (s *BlobStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.blobs)
}

// Stats tracks test statistics
type Stats struct {
	mu               sync.Mutex
	PutsTotal        int
	PutsFailed       int
	GetsTotal        int
	GetsSuccess      int
	GetsFailed       int
	GetsNotFound     int
	GetsDataMismatch int

	// Round-trip latency tracking
	RoundTripCount int
	RoundTripSum   time.Duration
	RoundTripMin   time.Duration
	RoundTripMax   time.Duration
}

func (s *Stats) RecordPut(success bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PutsTotal++
	if !success {
		s.PutsFailed++
	}
}

func (s *Stats) RecordGet(status GetStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.GetsTotal++
	switch status {
	case GetSuccess:
		s.GetsSuccess++
	case GetNotFound:
		s.GetsNotFound++
	case GetFailed:
		s.GetsFailed++
	case GetDataMismatch:
		s.GetsDataMismatch++
	}
}

func (s *Stats) RecordRoundTrip(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RoundTripCount++
	s.RoundTripSum += d
	if s.RoundTripMin == 0 || d < s.RoundTripMin {
		s.RoundTripMin = d
	}
	if d > s.RoundTripMax {
		s.RoundTripMax = d
	}
}

func (s *Stats) Print() {
	s.mu.Lock()
	defer s.mu.Unlock()

	fmt.Println("\n========================================")
	fmt.Println("FINAL TEST STATISTICS")
	fmt.Println("========================================")
	fmt.Printf("PUT Requests:\n")
	fmt.Printf("  Total:   %d\n", s.PutsTotal)
	fmt.Printf("  Failed:  %d\n", s.PutsFailed)
	fmt.Printf("  Success: %d\n", s.PutsTotal-s.PutsFailed)
	fmt.Printf("\nGET Requests:\n")
	fmt.Printf("  Total:          %d\n", s.GetsTotal)
	fmt.Printf("  Success:        %d\n", s.GetsSuccess)
	fmt.Printf("  Not Found:      %d (pending confirmation)\n", s.GetsNotFound)
	fmt.Printf("  Data Mismatch:  %d ⚠️\n", s.GetsDataMismatch)
	fmt.Printf("  Failed:         %d\n", s.GetsFailed)

	fmt.Printf("\nRound-Trip Latency (PUT start → GET 200 OK):\n")
	if s.RoundTripCount > 0 {
		avg := s.RoundTripSum / time.Duration(s.RoundTripCount)
		fmt.Printf("  Confirmed:  %d blobs\n", s.RoundTripCount)
		fmt.Printf("  Min:        %v\n", s.RoundTripMin.Round(time.Millisecond))
		fmt.Printf("  Max:        %v\n", s.RoundTripMax.Round(time.Millisecond))
		fmt.Printf("  Avg:        %v\n", avg.Round(time.Millisecond))
	} else {
		fmt.Printf("  No confirmed round-trips yet\n")
	}

	if s.GetsDataMismatch > 0 {
		fmt.Printf("\n⚠️  WARNING: Data mismatch detected - blob data corrupted!\n")
	} else if s.GetsSuccess > 0 {
		fmt.Printf("\n✓ All retrieved blobs match original data (integrity verified)\n")
	}
	fmt.Println("========================================")
}

// GetStatus represents the result of a GET request
type GetStatus int

const (
	GetSuccess GetStatus = iota
	GetNotFound
	GetFailed
	GetDataMismatch
)

// Writer handles blob writing operations
type Writer struct {
	config Config
	store  *BlobStore
	stats  *Stats
	client *http.Client
}

func NewWriter(config Config, store *BlobStore, stats *Stats) *Writer {
	return &Writer{
		config: config,
		store:  store,
		stats:  stats,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (w *Writer) Run(ctx context.Context) {
	fmt.Println("[WRITER] Starting writer goroutine...")

	blobNum := 0
	lastReportTime := time.Now()
	batchCount := 0

	for {
		select {
		case <-ctx.Done():
			fmt.Printf("[WRITER] Shutting down (wrote %d blobs)\n", blobNum)
			return
		default:
		}

		blobNum++
		batchCount++

		// Record time BEFORE PUT starts - this is the round-trip start
		putStartTime := time.Now()

		commitment, data, err := w.putBlob(ctx, blobNum)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Printf("[WRITER] Shutting down (wrote %d blobs)\n", blobNum-1)
				return
			}
			fmt.Printf("[WRITER] ERROR: %v\n", err)
			w.stats.RecordPut(false)
			if !sleep(ctx, w.config.PutInterval) {
				return
			}
			continue
		}

		w.store.Add(BlobRecord{
			Commitment:   commitment,
			OriginalData: data,
			PutStartTime: putStartTime, // Time when PUT was initiated
			BlobNum:      blobNum,
			Confirmed:    false,
		})
		w.stats.RecordPut(true)

		// Log progress every 10 blobs or 10 seconds
		if batchCount >= 10 || time.Since(lastReportTime) >= 10*time.Second {
			fmt.Printf("[WRITER] Progress: %d blobs written (latest: #%d, commitment: %x...)\n",
				blobNum, blobNum, commitment[:min(8, len(commitment))])
			lastReportTime = time.Now()
			batchCount = 0
		}

		if !sleep(ctx, w.config.PutInterval) {
			fmt.Printf("[WRITER] Shutting down (wrote %d blobs)\n", blobNum)
			return
		}
	}
}

func (w *Writer) putBlob(ctx context.Context, blobNum int) ([]byte, []byte, error) {
	// Generate random data
	data := make([]byte, w.config.BlobSize)
	if _, err := rand.Read(data); err != nil {
		return nil, nil, fmt.Errorf("failed to generate random data for blob %d: %w", blobNum, err)
	}

	// PUT request with context
	url := w.config.WriterURL + "/put"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request for blob %d: %w", blobNum, err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("PUT request failed for blob %d: %w", blobNum, err)
	}
	defer resp.Body.Close()

	commitment, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read commitment for blob %d: %w", blobNum, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("PUT returned status %d for blob %d", resp.StatusCode, blobNum)
	}

	return commitment, data, nil
}

// Reader handles blob reading and verification
type Reader struct {
	config             Config
	store              *BlobStore
	stats              *Stats
	client             *http.Client
	lastConfirmedIndex int
}

func NewReader(config Config, store *BlobStore, stats *Stats) *Reader {
	return &Reader{
		config:             config,
		store:              store,
		stats:              stats,
		client:             &http.Client{Timeout: 30 * time.Second},
		lastConfirmedIndex: -1,
	}
}

func (r *Reader) Run(ctx context.Context) {
	fmt.Printf("[READER] Waiting %v before starting GET requests...\n", r.config.ReaderLag)
	if !sleep(ctx, r.config.ReaderLag) {
		fmt.Println("[READER] Shutting down during initial wait")
		return
	}
	fmt.Println("[READER] Starting reader goroutine...")

	ticker := time.NewTicker(r.config.GetInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("[READER] Shutting down")
			return
		case <-ticker.C:
			r.checkBlobs(ctx)
		}
	}
}

func (r *Reader) checkBlobs(ctx context.Context) {
	blobs := r.store.Snapshot()
	totalBlobs := len(blobs)

	if totalBlobs == 0 {
		fmt.Println("[READER] No blobs available yet, waiting...")
		return
	}

	// Sequential checking: start from first unconfirmed blob
	startIdx := r.lastConfirmedIndex + 1
	endIdx := min(startIdx+10, totalBlobs)

	if startIdx >= totalBlobs {
		fmt.Printf("[READER] All %d blobs confirmed! Waiting for new blobs...\n", totalBlobs)
		return
	}

	fmt.Printf("\n[READER] ===== GET Check Round (checking blobs %d-%d of %d) =====\n",
		startIdx+1, endIdx, totalBlobs)

	result := r.checkBlobRange(ctx, blobs[startIdx:endIdx], startIdx)

	fmt.Printf("[READER] Round summary: ✓ %d success, ⏳ %d pending, ⚠️  %d mismatch, ✗ %d failed (last confirmed: blob #%d)\n",
		result.success, result.notFound, result.mismatch, result.errors, r.lastConfirmedIndex+1)
}

type checkResult struct {
	success  int
	notFound int
	mismatch int
	errors   int
}

func (r *Reader) checkBlobRange(ctx context.Context, blobs []BlobRecord, startIdx int) checkResult {
	var result checkResult

	for i, blob := range blobs {
		// Check for cancellation between blobs
		select {
		case <-ctx.Done():
			return result
		default:
		}

		status, roundTrip, msg := r.verifyBlob(ctx, blob)

		switch status {
		case GetSuccess:
			fmt.Printf("[READER] ✓ Blob #%d: %s\n", blob.BlobNum, msg)
			r.stats.RecordGet(GetSuccess)
			r.stats.RecordRoundTrip(roundTrip)
			r.store.MarkConfirmed(startIdx+i, roundTrip)
			result.success++
			r.lastConfirmedIndex = startIdx + i

		case GetNotFound:
			fmt.Printf("[READER] ⏳ Blob #%d: %s\n", blob.BlobNum, msg)
			r.stats.RecordGet(GetNotFound)
			result.notFound++
			return result // Stop here - wait for this blob

		case GetDataMismatch:
			fmt.Printf("[READER] ⚠️  Blob #%d: %s\n", blob.BlobNum, msg)
			r.stats.RecordGet(GetDataMismatch)
			result.mismatch++
			return result // Stop on mismatch

		case GetFailed:
			fmt.Printf("[READER] ✗ Blob #%d: %s\n", blob.BlobNum, msg)
			r.stats.RecordGet(GetFailed)
			result.errors++
			return result // Stop on error
		}
	}

	return result
}

func (r *Reader) verifyBlob(ctx context.Context, blob BlobRecord) (GetStatus, time.Duration, string) {
	url := fmt.Sprintf("%s/get/0x%s", r.config.ReaderURL, hex.EncodeToString(blob.Commitment))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return GetFailed, 0, fmt.Sprintf("failed to create request: %v", err)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return GetFailed, 0, "request cancelled"
		}
		return GetFailed, 0, fmt.Sprintf("GET request failed: %v", err)
	}
	defer resp.Body.Close()

	retrievedData, _ := io.ReadAll(resp.Body)

	// Calculate round-trip: from PUT start to NOW (successful GET)
	roundTrip := time.Since(blob.PutStartTime)
	waitTime := time.Since(blob.PutStartTime).Round(time.Second)

	switch resp.StatusCode {
	case http.StatusOK:
		if !bytes.Equal(retrievedData, blob.OriginalData) {
			return GetDataMismatch, 0, fmt.Sprintf("DATA MISMATCH! (waited: %v, size: got %d, expected %d)",
				waitTime, len(retrievedData), len(blob.OriginalData))
		}
		return GetSuccess, roundTrip, fmt.Sprintf("OK (round-trip: %v, size: %d bytes, data verified)",
			roundTrip.Round(time.Millisecond), len(retrievedData))

	case http.StatusNotFound:
		return GetNotFound, 0, fmt.Sprintf("Not found (waited: %v) - pending backfill, will retry", waitTime)

	default:
		return GetFailed, 0, fmt.Sprintf("Unexpected status %d (waited: %v)", resp.StatusCode, waitTime)
	}
}

// Test orchestrates the writer/reader test
type Test struct {
	config    Config
	store     *BlobStore
	stats     *Stats
	startTime time.Time
}

func NewTest(config Config) *Test {
	return &Test{
		config: config,
		store:  NewBlobStore(),
		stats:  &Stats{},
	}
}

func (t *Test) PrintConfig() {
	fmt.Println("========================================")
	fmt.Println("Writer/Reader Integration Test")
	fmt.Println("========================================")
	fmt.Printf("Writer endpoint: %s\n", t.config.WriterURL)
	fmt.Printf("Reader endpoint: %s\n", t.config.ReaderURL)
	fmt.Printf("Blob size: %d bytes (~%d KB)\n", t.config.BlobSize, t.config.BlobSize/1024)
	fmt.Printf("PUT interval: %v\n", t.config.PutInterval)
	fmt.Printf("Reader lag: %v\n", t.config.ReaderLag)
	fmt.Printf("GET interval: %v\n", t.config.GetInterval)
	fmt.Printf("Test duration: %v\n", t.config.Duration)
	fmt.Printf("Expected blobs: ~%d\n", int(t.config.Duration.Seconds()))
	fmt.Println("Press Ctrl+C to stop gracefully")
	fmt.Println("========================================")
}

func (t *Test) Run(ctx context.Context) {
	t.startTime = time.Now()

	// Create a context that cancels after duration OR on parent cancel
	ctx, cancel := context.WithTimeout(ctx, t.config.Duration+t.config.ReaderLag+3*t.config.GetInterval)
	defer cancel()

	var wg sync.WaitGroup

	// Start writer with its own timeout
	writerCtx, writerCancel := context.WithTimeout(ctx, t.config.Duration)
	writer := NewWriter(t.config, t.store, t.stats)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer writerCancel()
		writer.Run(writerCtx)
	}()

	// Start reader
	reader := NewReader(t.config, t.store, t.stats)
	wg.Add(1)
	go func() {
		defer wg.Done()
		reader.Run(ctx)
	}()

	wg.Wait()

	t.stats.Print()
	fmt.Printf("\nTest duration: %v\n", time.Since(t.startTime).Round(time.Second))
	fmt.Println("\nTest completed!")
}

// sleep sleeps for duration or until context is cancelled.
// Returns true if sleep completed, false if cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func main() {
	durationSecs := flag.Int("duration", 100, "Test duration in seconds")
	flag.Parse()

	config := DefaultConfig()
	config.Duration = time.Duration(*durationSecs) * time.Second

	// Setup signal handling for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	test := NewTest(config)
	test.PrintConfig()
	test.Run(ctx)
}
