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
	WriterURL    string
	ReaderURL    string
	BlobSize     int
	PutInterval  time.Duration
	ReaderLag    time.Duration
	PollInterval time.Duration // How often to poll for confirmation (should be small)
	Duration     time.Duration
}

// DefaultConfig returns sensible defaults
func DefaultConfig() Config {
	return Config{
		WriterURL:    "http://localhost:3100",
		ReaderURL:    "http://localhost:3100",
		BlobSize:     1200 * 1024, // ~1MB
		PutInterval:  1000 * time.Millisecond,
		ReaderLag:    30 * time.Second, // Shorter initial lag
		PollInterval: 2 * time.Second,  // Poll frequently for accurate timing
		Duration:     100 * time.Second,
	}
}

// BlobRecord stores a blob and its metadata for verification
type BlobRecord struct {
	Commitment   []byte
	OriginalData []byte
	BlobNum      int

	// Timing measurements
	PutStartTime  time.Time     // When PUT request was initiated
	PutEndTime    time.Time     // When PUT response was received
	PutLatency    time.Duration // PUT request duration (PutEndTime - PutStartTime)
	ConfirmedTime time.Time     // When GET returned 200 OK
	TotalTime     time.Duration // Time from PUT start to GET 200 (end-to-end round-trip)
	DAConfirmTime time.Duration // Time from PUT response to GET 200 (DA layer processing)
	PollAttempts  int           // How many GET attempts before success
	Confirmed     bool
}

// BlobStore is a thread-safe store for blob records
type BlobStore struct {
	mu    sync.Mutex
	blobs []BlobRecord
}

func NewBlobStore() *BlobStore {
	return &BlobStore{}
}

func (s *BlobStore) Add(record BlobRecord) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := len(s.blobs)
	s.blobs = append(s.blobs, record)
	return idx
}

func (s *BlobStore) Get(index int) *BlobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < len(s.blobs) {
		return &s.blobs[index]
	}
	return nil
}

func (s *BlobStore) MarkConfirmed(index int, confirmedTime time.Time, pollAttempts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < len(s.blobs) {
		b := &s.blobs[index]
		b.Confirmed = true
		b.ConfirmedTime = confirmedTime
		b.TotalTime = confirmedTime.Sub(b.PutStartTime)
		b.DAConfirmTime = confirmedTime.Sub(b.PutEndTime)
		b.PollAttempts = pollAttempts
	}
}

func (s *BlobStore) Snapshot() []BlobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := make([]BlobRecord, len(s.blobs))
	copy(snapshot, s.blobs)
	return snapshot
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

	// PUT latency (client → server → response)
	PutLatencyCount int
	PutLatencySum   time.Duration
	PutLatencyMin   time.Duration
	PutLatencyMax   time.Duration

	// Total time / end-to-end (PUT start → GET 200)
	TotalTimeCount int
	TotalTimeSum   time.Duration
	TotalTimeMin   time.Duration
	TotalTimeMax   time.Duration

	// DA confirmation time (PUT response → GET 200) = batching + Celestia submit + confirmation
	DAConfirmCount int
	DAConfirmSum   time.Duration
	DAConfirmMin   time.Duration
	DAConfirmMax   time.Duration

	// Poll attempts before success
	TotalPollAttempts int
}

func (s *Stats) RecordPut(success bool, latency time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PutsTotal++
	if !success {
		s.PutsFailed++
		return
	}
	s.PutLatencyCount++
	s.PutLatencySum += latency
	if s.PutLatencyMin == 0 || latency < s.PutLatencyMin {
		s.PutLatencyMin = latency
	}
	if latency > s.PutLatencyMax {
		s.PutLatencyMax = latency
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

func (s *Stats) RecordConfirmation(totalTime, daConfirmTime time.Duration, pollAttempts int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Total time (end-to-end)
	s.TotalTimeCount++
	s.TotalTimeSum += totalTime
	if s.TotalTimeMin == 0 || totalTime < s.TotalTimeMin {
		s.TotalTimeMin = totalTime
	}
	if totalTime > s.TotalTimeMax {
		s.TotalTimeMax = totalTime
	}

	// DA confirmation time
	s.DAConfirmCount++
	s.DAConfirmSum += daConfirmTime
	if s.DAConfirmMin == 0 || daConfirmTime < s.DAConfirmMin {
		s.DAConfirmMin = daConfirmTime
	}
	if daConfirmTime > s.DAConfirmMax {
		s.DAConfirmMax = daConfirmTime
	}

	s.TotalPollAttempts += pollAttempts
}

func (s *Stats) Print() {
	s.mu.Lock()
	defer s.mu.Unlock()

	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Println("                     FINAL TEST STATISTICS")
	fmt.Println("════════════════════════════════════════════════════════════════")

	fmt.Printf("\n📤 PUT Requests:\n")
	fmt.Printf("   Total:   %d\n", s.PutsTotal)
	fmt.Printf("   Success: %d\n", s.PutsTotal-s.PutsFailed)
	fmt.Printf("   Failed:  %d\n", s.PutsFailed)

	if s.PutLatencyCount > 0 {
		avg := s.PutLatencySum / time.Duration(s.PutLatencyCount)
		fmt.Printf("\n⏱️  PUT Latency (client → server → response):\n")
		fmt.Printf("   Min: %v\n", s.PutLatencyMin.Round(time.Millisecond))
		fmt.Printf("   Max: %v\n", s.PutLatencyMax.Round(time.Millisecond))
		fmt.Printf("   Avg: %v\n", avg.Round(time.Millisecond))
	}

	fmt.Printf("\n📥 GET Requests:\n")
	fmt.Printf("   Total:         %d\n", s.GetsTotal)
	fmt.Printf("   Success (200): %d\n", s.GetsSuccess)
	fmt.Printf("   Not Found:     %d\n", s.GetsNotFound)
	fmt.Printf("   Data Mismatch: %d\n", s.GetsDataMismatch)
	fmt.Printf("   Failed:        %d\n", s.GetsFailed)

	fmt.Println("\n────────────────────────────────────────────────────────────────")
	fmt.Println("                    ROUND-TRIP MEASUREMENTS")
	fmt.Println("────────────────────────────────────────────────────────────────")

	if s.TotalTimeCount > 0 {
		avgTotal := s.TotalTimeSum / time.Duration(s.TotalTimeCount)
		avgDA := s.DAConfirmSum / time.Duration(s.DAConfirmCount)
		avgPolls := float64(s.TotalPollAttempts) / float64(s.TotalTimeCount)

		fmt.Printf("\n🔄 Total Time (PUT start → GET 200 OK):\n")
		fmt.Printf("   Confirmed: %d blobs\n", s.TotalTimeCount)
		fmt.Printf("   Min:       %v\n", s.TotalTimeMin.Round(time.Millisecond))
		fmt.Printf("   Max:       %v\n", s.TotalTimeMax.Round(time.Millisecond))
		fmt.Printf("   Avg:       %v\n", avgTotal.Round(time.Millisecond))

		fmt.Printf("\n🌐 DA Confirm Time (PUT response → GET 200 OK):\n")
		fmt.Printf("   Includes: batching wait + Celestia submission + on-chain confirmation\n")
		fmt.Printf("   Min: %v\n", s.DAConfirmMin.Round(time.Millisecond))
		fmt.Printf("   Max: %v\n", s.DAConfirmMax.Round(time.Millisecond))
		fmt.Printf("   Avg: %v\n", avgDA.Round(time.Millisecond))

		fmt.Printf("\n📊 Polling Stats:\n")
		fmt.Printf("   Avg polls per blob: %.1f\n", avgPolls)
	} else {
		fmt.Printf("\n❌ No confirmations recorded\n")
	}

	if s.GetsDataMismatch > 0 {
		fmt.Printf("\n⚠️  WARNING: Data mismatch detected - blob data corrupted!\n")
	} else if s.GetsSuccess > 0 {
		fmt.Printf("\n✅ All retrieved blobs match original data (integrity verified)\n")
	}
	fmt.Println("════════════════════════════════════════════════════════════════")
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

		// Record time BEFORE PUT starts
		putStartTime := time.Now()

		commitment, data, err := w.putBlob(ctx, blobNum)
		putEndTime := time.Now()
		putLatency := putEndTime.Sub(putStartTime)

		if err != nil {
			if ctx.Err() != nil {
				fmt.Printf("[WRITER] Shutting down (wrote %d blobs)\n", blobNum-1)
				return
			}
			fmt.Printf("[WRITER] ERROR: %v\n", err)
			w.stats.RecordPut(false, 0)
			if !sleep(ctx, w.config.PutInterval) {
				return
			}
			continue
		}

		w.store.Add(BlobRecord{
			Commitment:   commitment,
			OriginalData: data,
			BlobNum:      blobNum,
			PutStartTime: putStartTime,
			PutEndTime:   putEndTime,
			PutLatency:   putLatency,
		})
		w.stats.RecordPut(true, putLatency)

		// Log progress every 10 blobs or 10 seconds
		if batchCount >= 10 || time.Since(lastReportTime) >= 10*time.Second {
			fmt.Printf("[WRITER] Progress: %d blobs written (latest: #%d, PUT latency: %v)\n",
				blobNum, blobNum, putLatency.Round(time.Millisecond))
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
	config Config
	store  *BlobStore
	stats  *Stats
	client *http.Client

	// Track poll attempts per blob
	pollAttempts map[int]int
	pollMu       sync.Mutex
}

func NewReader(config Config, store *BlobStore, stats *Stats) *Reader {
	return &Reader{
		config:       config,
		store:        store,
		stats:        stats,
		client:       &http.Client{Timeout: 30 * time.Second},
		pollAttempts: make(map[int]int),
	}
}

func (r *Reader) Run(ctx context.Context) {
	fmt.Printf("[READER] Waiting %v before starting GET requests...\n", r.config.ReaderLag)
	if !sleep(ctx, r.config.ReaderLag) {
		fmt.Println("[READER] Shutting down during initial wait")
		return
	}
	fmt.Printf("[READER] Starting reader goroutine (polling every %v)...\n", r.config.PollInterval)

	ticker := time.NewTicker(r.config.PollInterval)
	defer ticker.Stop()

	lastConfirmedIndex := -1

	for {
		select {
		case <-ctx.Done():
			fmt.Println("[READER] Shutting down")
			return
		case <-ticker.C:
			lastConfirmedIndex = r.checkBlobs(ctx, lastConfirmedIndex)
		}
	}
}

func (r *Reader) checkBlobs(ctx context.Context, lastConfirmedIndex int) int {
	blobs := r.store.Snapshot()
	totalBlobs := len(blobs)

	if totalBlobs == 0 {
		return lastConfirmedIndex
	}

	startIdx := lastConfirmedIndex + 1
	if startIdx >= totalBlobs {
		return lastConfirmedIndex
	}

	// Check all unconfirmed blobs (not just 10)
	endIdx := totalBlobs

	confirmedThisRound := 0
	notFoundThisRound := 0

	for i := startIdx; i < endIdx; i++ {
		select {
		case <-ctx.Done():
			return lastConfirmedIndex
		default:
		}

		blob := blobs[i]
		if blob.Confirmed {
			lastConfirmedIndex = i
			continue
		}

		// Increment poll attempts
		r.pollMu.Lock()
		r.pollAttempts[i]++
		attempts := r.pollAttempts[i]
		r.pollMu.Unlock()

		status, msg := r.verifyBlob(ctx, blob)

		switch status {
		case GetSuccess:
			confirmedTime := time.Now()
			r.store.MarkConfirmed(i, confirmedTime, attempts)

			totalTime := confirmedTime.Sub(blob.PutStartTime)
			daConfirmTime := confirmedTime.Sub(blob.PutEndTime)

			r.stats.RecordGet(GetSuccess)
			r.stats.RecordConfirmation(totalTime, daConfirmTime, attempts)

			fmt.Printf("[READER] ✅ Blob #%d: %s (polls: %d, total: %v, DA: %v)\n",
				blob.BlobNum, msg, attempts,
				totalTime.Round(time.Millisecond),
				daConfirmTime.Round(time.Millisecond))

			confirmedThisRound++
			lastConfirmedIndex = i

		case GetNotFound:
			r.stats.RecordGet(GetNotFound)
			notFoundThisRound++
			// Don't log every 404 - too noisy

		case GetDataMismatch:
			fmt.Printf("[READER] ⚠️  Blob #%d: %s\n", blob.BlobNum, msg)
			r.stats.RecordGet(GetDataMismatch)
			return lastConfirmedIndex

		case GetFailed:
			fmt.Printf("[READER] ❌ Blob #%d: %s\n", blob.BlobNum, msg)
			r.stats.RecordGet(GetFailed)
			// Continue checking others
		}
	}

	if confirmedThisRound > 0 || notFoundThisRound > 0 {
		fmt.Printf("[READER] Round: ✅ %d confirmed, ⏳ %d pending (total confirmed: %d/%d)\n",
			confirmedThisRound, notFoundThisRound, lastConfirmedIndex+1, totalBlobs)
	}

	return lastConfirmedIndex
}

func (r *Reader) verifyBlob(ctx context.Context, blob BlobRecord) (GetStatus, string) {
	url := fmt.Sprintf("%s/get/0x%s", r.config.ReaderURL, hex.EncodeToString(blob.Commitment))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return GetFailed, fmt.Sprintf("failed to create request: %v", err)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return GetFailed, "request cancelled"
		}
		return GetFailed, fmt.Sprintf("GET request failed: %v", err)
	}
	defer resp.Body.Close()

	retrievedData, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		if !bytes.Equal(retrievedData, blob.OriginalData) {
			return GetDataMismatch, fmt.Sprintf("DATA MISMATCH! (size: got %d, expected %d)",
				len(retrievedData), len(blob.OriginalData))
		}
		return GetSuccess, fmt.Sprintf("OK (size: %d bytes)", len(retrievedData))

	case http.StatusNotFound:
		return GetNotFound, "pending"

	default:
		return GetFailed, fmt.Sprintf("status %d", resp.StatusCode)
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
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Println("              Writer/Reader Integration Test")
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Printf("Writer endpoint:  %s\n", t.config.WriterURL)
	fmt.Printf("Reader endpoint:  %s\n", t.config.ReaderURL)
	fmt.Printf("Blob size:        %d bytes (~%d KB)\n", t.config.BlobSize, t.config.BlobSize/1024)
	fmt.Printf("PUT interval:     %v\n", t.config.PutInterval)
	fmt.Printf("Reader lag:       %v\n", t.config.ReaderLag)
	fmt.Printf("Poll interval:    %v (for accurate timing)\n", t.config.PollInterval)
	fmt.Printf("Test duration:    %v\n", t.config.Duration)
	fmt.Printf("Expected blobs:   ~%d\n", int(t.config.Duration.Seconds()))
	fmt.Println("────────────────────────────────────────────────────────────────")
	fmt.Println("Press Ctrl+C to stop gracefully")
	fmt.Println("════════════════════════════════════════════════════════════════")
}

func (t *Test) Run(ctx context.Context) {
	t.startTime = time.Now()

	// Create a context that cancels after duration OR on parent cancel
	ctx, cancel := context.WithTimeout(ctx, t.config.Duration+t.config.ReaderLag+30*time.Second)
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
	t.printBlobDetails()
	t.fetchServerStats()
	fmt.Printf("\nTest duration: %v\n", time.Since(t.startTime).Round(time.Second))
	fmt.Println("\nTest completed!")
}

// fetchServerStats queries both writer and reader servers for their internal stats
func (t *Test) fetchServerStats() {
	fmt.Println("\n────────────────────────────────────────────────────────────────")
	fmt.Println("                    SERVER STATS (for debugging)")
	fmt.Println("────────────────────────────────────────────────────────────────")

	client := &http.Client{Timeout: 5 * time.Second}

	// Fetch writer stats
	fmt.Printf("\n📝 Writer Server (%s):\n", t.config.WriterURL)
	writerStats, err := client.Get(t.config.WriterURL + "/stats")
	if err != nil {
		fmt.Printf("   Error fetching stats: %v\n", err)
	} else {
		defer writerStats.Body.Close()
		body, _ := io.ReadAll(writerStats.Body)
		fmt.Printf("   %s\n", string(body))
	}

	// Fetch reader stats (if different server)
	if t.config.ReaderURL != t.config.WriterURL {
		fmt.Printf("\n📖 Reader Server (%s):\n", t.config.ReaderURL)
		readerStats, err := client.Get(t.config.ReaderURL + "/stats")
		if err != nil {
			fmt.Printf("   Error fetching stats: %v\n", err)
		} else {
			defer readerStats.Body.Close()
			body, _ := io.ReadAll(readerStats.Body)
			fmt.Printf("   %s\n", string(body))
		}
	}
}

func (t *Test) printBlobDetails() {
	blobs := t.store.Snapshot()
	if len(blobs) == 0 {
		return
	}

	fmt.Println("\n────────────────────────────────────────────────────────────────")
	fmt.Println("                    PER-BLOB TIMING DETAILS")
	fmt.Println("────────────────────────────────────────────────────────────────")
	fmt.Printf("%-6s %-12s %-12s %-12s %-6s %-8s\n",
		"Blob#", "PUT Latency", "DA Confirm", "Total Time", "Polls", "Status")
	fmt.Println("────────────────────────────────────────────────────────────────")

	confirmed := 0
	pending := 0
	var stuckBlobs []int
	var stuckCommitments []string

	for _, b := range blobs {
		if b.Confirmed {
			confirmed++
			fmt.Printf("%-6d %-12v %-12v %-12v %-6d %-8s\n",
				b.BlobNum,
				b.PutLatency.Round(time.Millisecond),
				b.DAConfirmTime.Round(time.Millisecond),
				b.TotalTime.Round(time.Millisecond),
				b.PollAttempts,
				"✅")
		} else {
			pending++
			waitTime := time.Since(b.PutStartTime)
			// Mark as STUCK if waiting longer than 60 seconds
			status := "⏳"
			if waitTime > 60*time.Second {
				status = "🔴 STUCK"
				stuckBlobs = append(stuckBlobs, b.BlobNum)
				if len(b.Commitment) >= 8 {
					stuckCommitments = append(stuckCommitments, hex.EncodeToString(b.Commitment[:8]))
				}
			}
			fmt.Printf("%-6d %-12v %-12s %-12v %-6s %-8s\n",
				b.BlobNum,
				b.PutLatency.Round(time.Millisecond),
				"...",
				waitTime.Round(time.Millisecond),
				"...",
				status)
		}
	}

	fmt.Println("────────────────────────────────────────────────────────────────")
	fmt.Printf("Summary: %d confirmed, %d pending\n", confirmed, pending)

	// Detect and report gaps (stuck blobs surrounded by confirmed ones)
	t.reportGaps(blobs, stuckBlobs, stuckCommitments)
}

// reportGaps identifies blobs that are stuck while later blobs got confirmed (indicates a system issue)
func (t *Test) reportGaps(blobs []BlobRecord, stuckBlobs []int, stuckCommitments []string) {
	if len(stuckBlobs) == 0 {
		return
	}

	// Find if there are confirmed blobs AFTER stuck ones
	maxStuckIdx := 0
	for i, b := range blobs {
		if !b.Confirmed {
			if i > maxStuckIdx {
				maxStuckIdx = i
			}
		}
	}

	hasConfirmedAfterStuck := false
	for i := maxStuckIdx + 1; i < len(blobs); i++ {
		if blobs[i].Confirmed {
			hasConfirmedAfterStuck = true
			break
		}
	}

	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Println("                    ⚠️  RELIABILITY ISSUE DETECTED")
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Printf("\n🔴 STUCK BLOBS: %v\n", stuckBlobs)
	fmt.Printf("   These blobs have been waiting >60s and are not confirmed.\n")

	if hasConfirmedAfterStuck {
		fmt.Println("\n❌ CRITICAL: Later blobs were confirmed while earlier ones are stuck!")
		fmt.Println("   This indicates the reader server SKIPPED a batch.")
		fmt.Println("\n   Possible causes:")
		fmt.Println("   1. Backfill worker missed a Celestia height range")
		fmt.Println("   2. Batch submission partially failed")
		fmt.Println("   3. Race condition in height tracking")
		fmt.Println("   4. Event listener skipped certain blocks")
	}

	fmt.Println("\n📋 Stuck blob commitments (first 8 bytes):")
	for i, blobNum := range stuckBlobs {
		if i < len(stuckCommitments) {
			fmt.Printf("   Blob #%d: 0x%s...\n", blobNum, stuckCommitments[i])
		}
	}

	fmt.Println("\n💡 Debug steps:")
	fmt.Println("   1. Check writer server logs for batch submission around these blobs")
	fmt.Println("   2. Check reader server logs for backfill/event listener errors")
	fmt.Println("   3. Query Celestia to verify the batch was actually submitted")
	fmt.Println("   4. Check if the stuck blobs' batch height was processed by reader")
	fmt.Println("════════════════════════════════════════════════════════════════")
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
	pollMs := flag.Int("poll", 2000, "Poll interval in milliseconds (smaller = more accurate timing)")
	flag.Parse()

	config := DefaultConfig()
	config.Duration = time.Duration(*durationSecs) * time.Second
	config.PollInterval = time.Duration(*pollMs) * time.Millisecond

	// Setup signal handling for graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	test := NewTest(config)
	test.PrintConfig()
	test.Run(ctx)
}
