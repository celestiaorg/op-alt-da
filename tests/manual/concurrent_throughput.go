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
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Config holds test configuration
type Config struct {
	ServerURL      string
	BlobSize       int
	ConcurrentPuts int           // Number of concurrent PUT workers
	TotalBlobs     int           // Total number of blobs to submit (0 = unlimited)
	Duration       time.Duration // Test duration (if TotalBlobs == 0)
	VerifyAfterPut bool          // Whether to verify GET after each PUT
	HTTPTimeout    time.Duration // Timeout for individual HTTP requests
	RampUpDelay    time.Duration // Delay between starting each worker
	Verbose        bool          // Show detailed per-blob logs
}

// DefaultConfig returns sensible defaults
func DefaultConfig() Config {
	return Config{
		ServerURL:      "http://localhost:3100",
		BlobSize:       1028 * 1024,            // 128KB - typical L2 batch size
		ConcurrentPuts: 4,                      // 4 concurrent workers
		TotalBlobs:     20,                     // Submit 20 blobs total
		Duration:       0,                      // Use TotalBlobs instead
		VerifyAfterPut: true,                   // Verify each blob after PUT
		HTTPTimeout:    120 * time.Second,      // 2 min timeout for Celestia confirmation
		RampUpDelay:    500 * time.Millisecond, // Stagger worker starts
		Verbose:        false,                  // Quiet mode by default
	}
}

// BlobResult stores result of a single blob PUT operation
type BlobResult struct {
	WorkerID   int
	BlobNum    int
	BlobSize   int
	Commitment []byte

	// Timing
	StartTime  time.Time
	EndTime    time.Time
	PutLatency time.Duration // Time to complete PUT (includes Celestia confirmation)
	GetLatency time.Duration // Time for verification GET (if enabled)

	// Status
	PutSuccess   bool
	GetSuccess   bool
	DataVerified bool
	Error        string
}

// Results aggregates all blob results
type Results struct {
	mu      sync.Mutex
	results []BlobResult

	// Counters for live progress
	putsStarted   int64
	putsCompleted int64
	putsFailed    int64
	getsSuccess   int64
	getsFailed    int64
}

func NewResults() *Results {
	return &Results{}
}

func (r *Results) Add(result BlobResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, result)
}

func (r *Results) IncrementStarted() {
	atomic.AddInt64(&r.putsStarted, 1)
}

func (r *Results) IncrementCompleted(success bool) {
	atomic.AddInt64(&r.putsCompleted, 1)
	if !success {
		atomic.AddInt64(&r.putsFailed, 1)
	}
}

func (r *Results) IncrementGet(success bool) {
	if success {
		atomic.AddInt64(&r.getsSuccess, 1)
	} else {
		atomic.AddInt64(&r.getsFailed, 1)
	}
}

func (r *Results) GetProgress() (started, completed, failed int64) {
	return atomic.LoadInt64(&r.putsStarted),
		atomic.LoadInt64(&r.putsCompleted),
		atomic.LoadInt64(&r.putsFailed)
}

func (r *Results) Snapshot() []BlobResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := make([]BlobResult, len(r.results))
	copy(snapshot, r.results)
	return snapshot
}

// Worker handles concurrent PUT requests
type Worker struct {
	id       int
	config   Config
	results  *Results
	client   *http.Client
	blobChan chan int // Channel to receive blob numbers to process
}

func NewWorker(id int, config Config, results *Results, blobChan chan int) *Worker {
	return &Worker{
		id:       id,
		config:   config,
		results:  results,
		client:   &http.Client{Timeout: config.HTTPTimeout},
		blobChan: blobChan,
	}
}

func (w *Worker) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case blobNum, ok := <-w.blobChan:
			if !ok {
				return // Channel closed
			}
			w.processBlob(ctx, blobNum)
		}
	}
}

func (w *Worker) processBlob(ctx context.Context, blobNum int) {
	result := BlobResult{
		WorkerID:  w.id,
		BlobNum:   blobNum,
		BlobSize:  w.config.BlobSize,
		StartTime: time.Now(),
	}

	w.results.IncrementStarted()

	// Generate random blob data
	data := make([]byte, w.config.BlobSize)
	if _, err := rand.Read(data); err != nil {
		result.Error = fmt.Sprintf("failed to generate data: %v", err)
		result.EndTime = time.Now()
		result.PutLatency = result.EndTime.Sub(result.StartTime)
		w.results.Add(result)
		w.results.IncrementCompleted(false)
		return
	}

	// PUT request
	putStart := time.Now()
	commitment, err := w.putBlob(ctx, data)
	result.PutLatency = time.Since(putStart)
	result.EndTime = time.Now()

	if err != nil {
		result.Error = fmt.Sprintf("PUT failed: %v", err)
		w.results.Add(result)
		w.results.IncrementCompleted(false)
		fmt.Printf("[W%d] ❌ Blob #%d: PUT failed after %v - %v\n",
			w.id, blobNum, result.PutLatency.Round(time.Millisecond), err)
		return
	}

	result.Commitment = commitment
	result.PutSuccess = true
	w.results.IncrementCompleted(true)

	if w.config.Verbose {
		fmt.Printf("[W%d] ✅ Blob #%d: PUT OK in %v (commitment: %s...)\n",
			w.id, blobNum, result.PutLatency.Round(time.Millisecond),
			hex.EncodeToString(commitment[:8]))
	}

	// Verify GET if enabled
	if w.config.VerifyAfterPut {
		getStart := time.Now()
		retrievedData, err := w.getBlob(ctx, commitment)
		result.GetLatency = time.Since(getStart)

		if err != nil {
			result.Error = fmt.Sprintf("GET verification failed: %v", err)
			w.results.IncrementGet(false)
			fmt.Printf("[W%d] ⚠️  Blob #%d: GET failed - %v\n", w.id, blobNum, err)
		} else if !bytes.Equal(retrievedData, data) {
			result.Error = "data mismatch"
			w.results.IncrementGet(false)
			fmt.Printf("[W%d] ⚠️  Blob #%d: DATA MISMATCH!\n", w.id, blobNum)
		} else {
			result.GetSuccess = true
			result.DataVerified = true
			w.results.IncrementGet(true)
			if w.config.Verbose {
				fmt.Printf("[W%d] ✓  Blob #%d: GET verified in %v\n",
					w.id, blobNum, result.GetLatency.Round(time.Millisecond))
			}
		}
	}

	w.results.Add(result)
}

func (w *Worker) putBlob(ctx context.Context, data []byte) ([]byte, error) {
	url := w.config.ServerURL + "/put"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	commitment, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(commitment))
	}

	return commitment, nil
}

func (w *Worker) getBlob(ctx context.Context, commitment []byte) ([]byte, error) {
	url := fmt.Sprintf("%s/get/0x%s", w.config.ServerURL, hex.EncodeToString(commitment))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	return data, nil
}

// Test orchestrates the concurrent throughput test
type Test struct {
	config    Config
	results   *Results
	startTime time.Time
}

func NewTest(config Config) *Test {
	return &Test{
		config:  config,
		results: NewResults(),
	}
}

func (t *Test) PrintConfig() {
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Println("           Concurrent Throughput Test (Stateless DA)")
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Printf("Server:           %s\n", t.config.ServerURL)
	fmt.Printf("Blob size:        %d bytes (~%d KB)\n", t.config.BlobSize, t.config.BlobSize/1024)
	fmt.Printf("Concurrent PUTs:  %d workers\n", t.config.ConcurrentPuts)
	fmt.Printf("Total blobs:      %d\n", t.config.TotalBlobs)
	fmt.Printf("HTTP timeout:     %v (for Celestia confirmation)\n", t.config.HTTPTimeout)
	fmt.Printf("Verify GETs:      %v\n", t.config.VerifyAfterPut)
	fmt.Printf("Ramp-up delay:    %v between workers\n", t.config.RampUpDelay)
	fmt.Println("────────────────────────────────────────────────────────────────")
	fmt.Println("Note: Each PUT blocks until Celestia confirms (~10-60s)")
	fmt.Println("      Concurrent workers maximize throughput")
	fmt.Println("────────────────────────────────────────────────────────────────")
	fmt.Println("Press Ctrl+C to stop gracefully")
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Println()
}

func (t *Test) Run(ctx context.Context) {
	t.startTime = time.Now()

	// Create blob channel
	blobChan := make(chan int, t.config.TotalBlobs)

	// Queue all blobs
	for i := 1; i <= t.config.TotalBlobs; i++ {
		blobChan <- i
	}
	close(blobChan)

	// Start workers with ramp-up delay
	var wg sync.WaitGroup
	for i := 0; i < t.config.ConcurrentPuts; i++ {
		wg.Add(1)
		worker := NewWorker(i+1, t.config, t.results, blobChan)
		go worker.Run(ctx, &wg)

		// Ramp-up delay (stagger worker starts)
		if i < t.config.ConcurrentPuts-1 && t.config.RampUpDelay > 0 {
			select {
			case <-ctx.Done():
				break
			case <-time.After(t.config.RampUpDelay):
			}
		}
	}
	fmt.Printf("[MAIN] Started %d workers\n", t.config.ConcurrentPuts)

	// Progress reporter
	go t.progressReporter(ctx)

	// Wait for all workers to complete
	wg.Wait()

	t.printResults()
}

func (t *Test) progressReporter(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, completed, failed := t.results.GetProgress()
			elapsed := time.Since(t.startTime).Round(time.Second)
			fmt.Printf("[PROGRESS] %v elapsed: %d/%d completed (%d failed)\n",
				elapsed, completed, t.config.TotalBlobs, failed)
		}
	}
}

func (t *Test) printResults() {
	results := t.results.Snapshot()
	totalDuration := time.Since(t.startTime)

	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Println("                     FINAL TEST RESULTS")
	fmt.Println("════════════════════════════════════════════════════════════════")

	// Count successes/failures
	putSuccess := 0
	putFailed := 0
	getSuccess := 0
	getFailed := 0
	var putLatencies []time.Duration
	var getLatencies []time.Duration
	totalBytes := int64(0)

	for _, r := range results {
		if r.PutSuccess {
			putSuccess++
			putLatencies = append(putLatencies, r.PutLatency)
			totalBytes += int64(r.BlobSize)
		} else {
			putFailed++
		}
		if r.GetSuccess {
			getSuccess++
			getLatencies = append(getLatencies, r.GetLatency)
		} else if r.PutSuccess && t.config.VerifyAfterPut {
			getFailed++
		}
	}

	fmt.Printf("\n📤 PUT Results:\n")
	fmt.Printf("   Success: %d\n", putSuccess)
	fmt.Printf("   Failed:  %d\n", putFailed)
	fmt.Printf("   Total:   %d\n", putSuccess+putFailed)

	if len(putLatencies) > 0 {
		sort.Slice(putLatencies, func(i, j int) bool { return putLatencies[i] < putLatencies[j] })

		var sum time.Duration
		for _, l := range putLatencies {
			sum += l
		}
		avg := sum / time.Duration(len(putLatencies))
		p50 := putLatencies[len(putLatencies)/2]
		p95 := putLatencies[int(float64(len(putLatencies))*0.95)]
		p99 := putLatencies[int(float64(len(putLatencies))*0.99)]

		fmt.Printf("\n⏱️  PUT Latency (includes Celestia confirmation):\n")
		fmt.Printf("   Min:  %v\n", putLatencies[0].Round(time.Millisecond))
		fmt.Printf("   Max:  %v\n", putLatencies[len(putLatencies)-1].Round(time.Millisecond))
		fmt.Printf("   Avg:  %v\n", avg.Round(time.Millisecond))
		fmt.Printf("   P50:  %v\n", p50.Round(time.Millisecond))
		fmt.Printf("   P95:  %v\n", p95.Round(time.Millisecond))
		fmt.Printf("   P99:  %v\n", p99.Round(time.Millisecond))
	}

	if t.config.VerifyAfterPut {
		fmt.Printf("\n📥 GET Verification:\n")
		fmt.Printf("   Success: %d\n", getSuccess)
		fmt.Printf("   Failed:  %d\n", getFailed)

		if len(getLatencies) > 0 {
			sort.Slice(getLatencies, func(i, j int) bool { return getLatencies[i] < getLatencies[j] })
			var sum time.Duration
			for _, l := range getLatencies {
				sum += l
			}
			avg := sum / time.Duration(len(getLatencies))

			fmt.Printf("\n⏱️  GET Latency:\n")
			fmt.Printf("   Min:  %v\n", getLatencies[0].Round(time.Millisecond))
			fmt.Printf("   Max:  %v\n", getLatencies[len(getLatencies)-1].Round(time.Millisecond))
			fmt.Printf("   Avg:  %v\n", avg.Round(time.Millisecond))
		}
	}

	// Throughput calculation
	fmt.Println("\n────────────────────────────────────────────────────────────────")
	fmt.Println("                    📈 THROUGHPUT METRICS")
	fmt.Println("────────────────────────────────────────────────────────────────")

	fmt.Printf("\n   Test duration:     %v\n", totalDuration.Round(time.Second))
	fmt.Printf("   Total data:        %.2f MB (%d bytes)\n",
		float64(totalBytes)/(1024*1024), totalBytes)
	fmt.Printf("   Concurrent workers: %d\n", t.config.ConcurrentPuts)

	if totalDuration.Seconds() > 0 && totalBytes > 0 {
		bytesPerSec := float64(totalBytes) / totalDuration.Seconds()
		blobsPerSec := float64(putSuccess) / totalDuration.Seconds()

		fmt.Printf("\n   🚀 Throughput:     %.2f KB/s (%.2f MB/s)\n",
			bytesPerSec/1024, bytesPerSec/(1024*1024))
		fmt.Printf("   📦 Blob rate:      %.2f blobs/s\n", blobsPerSec)

		// Effective concurrency (how many PUTs were in flight on average)
		if len(putLatencies) > 0 {
			var totalLatency time.Duration
			for _, l := range putLatencies {
				totalLatency += l
			}
			avgLatency := totalLatency / time.Duration(len(putLatencies))
			effectiveConcurrency := float64(putSuccess) * avgLatency.Seconds() / totalDuration.Seconds()
			fmt.Printf("   ⚡ Effective concurrency: %.1f\n", effectiveConcurrency)
		}
	}

	// Per-worker stats
	fmt.Println("\n────────────────────────────────────────────────────────────────")
	fmt.Println("                    PER-WORKER STATISTICS")
	fmt.Println("────────────────────────────────────────────────────────────────")

	workerStats := make(map[int]struct {
		count   int
		success int
		sumLat  time.Duration
	})

	for _, r := range results {
		ws := workerStats[r.WorkerID]
		ws.count++
		if r.PutSuccess {
			ws.success++
			ws.sumLat += r.PutLatency
		}
		workerStats[r.WorkerID] = ws
	}

	fmt.Printf("\n%-10s %-10s %-10s %-15s\n", "Worker", "Blobs", "Success", "Avg Latency")
	fmt.Println("────────────────────────────────────────────────────────────────")
	for i := 1; i <= t.config.ConcurrentPuts; i++ {
		ws := workerStats[i]
		avgLat := time.Duration(0)
		if ws.success > 0 {
			avgLat = ws.sumLat / time.Duration(ws.success)
		}
		fmt.Printf("%-10d %-10d %-10d %-15v\n", i, ws.count, ws.success, avgLat.Round(time.Millisecond))
	}

	// Data integrity
	if t.config.VerifyAfterPut {
		fmt.Println("\n────────────────────────────────────────────────────────────────")
		if getFailed == 0 && getSuccess > 0 {
			fmt.Println("✅ DATA INTEGRITY: All blobs verified successfully")
		} else if getFailed > 0 {
			fmt.Printf("⚠️  DATA INTEGRITY: %d verification failures\n", getFailed)
		}
	}

	fmt.Println("════════════════════════════════════════════════════════════════")
}

func main() {
	config := DefaultConfig()

	// Parse flags
	flag.StringVar(&config.ServerURL, "url", config.ServerURL, "DA server URL")
	flag.IntVar(&config.BlobSize, "size", config.BlobSize, "Blob size in bytes")
	flag.IntVar(&config.ConcurrentPuts, "workers", config.ConcurrentPuts, "Number of concurrent PUT workers")
	flag.IntVar(&config.TotalBlobs, "blobs", config.TotalBlobs, "Total number of blobs to submit")
	flag.BoolVar(&config.VerifyAfterPut, "verify", config.VerifyAfterPut, "Verify GET after each PUT")
	flag.BoolVar(&config.Verbose, "v", config.Verbose, "Verbose output (show per-blob logs)")
	timeoutSecs := flag.Int("timeout", 120, "HTTP timeout in seconds")
	rampUpMs := flag.Int("rampup", 500, "Ramp-up delay between workers in milliseconds")
	flag.Parse()

	config.HTTPTimeout = time.Duration(*timeoutSecs) * time.Second
	config.RampUpDelay = time.Duration(*rampUpMs) * time.Millisecond

	// Setup signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	test := NewTest(config)
	test.PrintConfig()
	test.Run(ctx)
}
