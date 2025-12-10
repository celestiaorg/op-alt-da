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

// =============================================================================
// CONFIGURATION
// =============================================================================

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
		BlobSize:       1028 * 1024,            // ~1MB - typical L2 batch size
		ConcurrentPuts: 4,                      // 4 concurrent workers
		TotalBlobs:     20,                     // Submit 20 blobs total
		Duration:       0,                      // Use TotalBlobs instead
		VerifyAfterPut: true,                   // Verify each blob after PUT
		HTTPTimeout:    120 * time.Second,      // 2 min timeout for Celestia confirmation
		RampUpDelay:    500 * time.Millisecond, // Stagger worker starts
		Verbose:        false,                  // Quiet mode by default
	}
}

// =============================================================================
// BLOB RESULT - Single blob operation result
// =============================================================================

// BlobResult stores result of a single blob PUT (and optional GET) operation
type BlobResult struct {
	WorkerID   int
	BlobNum    int
	BlobSize   int
	Commitment []byte

	// Timing
	StartTime        time.Time
	EndTime          time.Time
	PutLatency       time.Duration // Time to complete PUT (includes Celestia confirmation)
	GetLatency       time.Duration // Time for verification GET (if enabled)
	RoundtripLatency time.Duration // Total roundtrip: PUT + GET (only valid if both succeeded)

	// Status
	PutSuccess   bool
	GetSuccess   bool
	DataVerified bool
	Error        string
}

// =============================================================================
// RESULTS COLLECTOR - Thread-safe result aggregation
// =============================================================================

// ResultsCollector aggregates all blob results in a thread-safe manner
type ResultsCollector struct {
	mu      sync.Mutex
	results []BlobResult

	// Atomic counters for live progress reporting
	putsStarted   int64
	putsCompleted int64
	putsFailed    int64
	getsSuccess   int64
	getsFailed    int64
}

func NewResultsCollector() *ResultsCollector {
	return &ResultsCollector{}
}

func (r *ResultsCollector) Add(result BlobResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, result)
}

func (r *ResultsCollector) IncrementStarted() {
	atomic.AddInt64(&r.putsStarted, 1)
}

func (r *ResultsCollector) IncrementPutCompleted(success bool) {
	atomic.AddInt64(&r.putsCompleted, 1)
	if !success {
		atomic.AddInt64(&r.putsFailed, 1)
	}
}

func (r *ResultsCollector) IncrementGetCompleted(success bool) {
	if success {
		atomic.AddInt64(&r.getsSuccess, 1)
	} else {
		atomic.AddInt64(&r.getsFailed, 1)
	}
}

func (r *ResultsCollector) GetProgress() (started, completed, failed int64) {
	return atomic.LoadInt64(&r.putsStarted),
		atomic.LoadInt64(&r.putsCompleted),
		atomic.LoadInt64(&r.putsFailed)
}

func (r *ResultsCollector) Snapshot() []BlobResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := make([]BlobResult, len(r.results))
	copy(snapshot, r.results)
	return snapshot
}

// =============================================================================
// LATENCY STATISTICS - Reusable stats calculation
// =============================================================================

// LatencyStats holds computed statistics for a set of latencies
type LatencyStats struct {
	Count int
	Min   time.Duration
	Max   time.Duration
	Avg   time.Duration
	P50   time.Duration
	P95   time.Duration
	P99   time.Duration
	Sum   time.Duration
}

// ComputeLatencyStats calculates statistics from a slice of durations
// Returns nil if the input slice is empty
func ComputeLatencyStats(latencies []time.Duration) *LatencyStats {
	if len(latencies) == 0 {
		return nil
	}

	// Sort for percentile calculations
	sorted := make([]time.Duration, len(latencies))
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	// Calculate sum
	var sum time.Duration
	for _, l := range sorted {
		sum += l
	}

	// Calculate percentile indices (with bounds checking)
	p50Idx := len(sorted) / 2
	p95Idx := min(int(float64(len(sorted))*0.95), len(sorted)-1)
	p99Idx := min(int(float64(len(sorted))*0.99), len(sorted)-1)

	return &LatencyStats{
		Count: len(sorted),
		Min:   sorted[0],
		Max:   sorted[len(sorted)-1],
		Avg:   sum / time.Duration(len(sorted)),
		P50:   sorted[p50Idx],
		P95:   sorted[p95Idx],
		P99:   sorted[p99Idx],
		Sum:   sum,
	}
}

// PrintLatencyStats prints formatted latency statistics with a title
func PrintLatencyStats(title string, stats *LatencyStats, showSum bool) {
	if stats == nil {
		return
	}

	fmt.Printf("\n%s:\n", title)
	fmt.Printf("   Min:  %v\n", stats.Min.Round(time.Millisecond))
	fmt.Printf("   Max:  %v\n", stats.Max.Round(time.Millisecond))
	fmt.Printf("   Avg:  %v\n", stats.Avg.Round(time.Millisecond))
	fmt.Printf("   P50:  %v\n", stats.P50.Round(time.Millisecond))
	fmt.Printf("   P95:  %v\n", stats.P95.Round(time.Millisecond))
	fmt.Printf("   P99:  %v\n", stats.P99.Round(time.Millisecond))
	if showSum {
		fmt.Printf("   Sum:  %v (total time for all operations)\n", stats.Sum.Round(time.Millisecond))
	}
}

// =============================================================================
// AGGREGATED RESULTS - Computed summary from raw results
// =============================================================================

// AggregatedResults holds all computed metrics from raw blob results
type AggregatedResults struct {
	// Counts
	PutSuccess int
	PutFailed  int
	GetSuccess int
	GetFailed  int
	TotalBytes int64

	// Latency statistics
	PutStats       *LatencyStats
	GetStats       *LatencyStats
	RoundtripStats *LatencyStats

	// Per-worker breakdown
	WorkerStats map[int]*WorkerStats
}

// WorkerStats holds per-worker metrics
type WorkerStats struct {
	BlobCount    int
	SuccessCount int
	TotalLatency time.Duration
}

// AggregateResults processes raw blob results into computed metrics
func AggregateResults(results []BlobResult, verifyEnabled bool) *AggregatedResults {
	agg := &AggregatedResults{
		WorkerStats: make(map[int]*WorkerStats),
	}

	var putLatencies, getLatencies, roundtripLatencies []time.Duration

	for _, r := range results {
		// Initialize worker stats if needed
		if agg.WorkerStats[r.WorkerID] == nil {
			agg.WorkerStats[r.WorkerID] = &WorkerStats{}
		}
		ws := agg.WorkerStats[r.WorkerID]
		ws.BlobCount++

		// PUT results
		if r.PutSuccess {
			agg.PutSuccess++
			agg.TotalBytes += int64(r.BlobSize)
			putLatencies = append(putLatencies, r.PutLatency)
			ws.SuccessCount++
			ws.TotalLatency += r.PutLatency
		} else {
			agg.PutFailed++
		}

		// GET results (only if verification was enabled)
		if r.GetSuccess {
			agg.GetSuccess++
			getLatencies = append(getLatencies, r.GetLatency)
			if r.RoundtripLatency > 0 {
				roundtripLatencies = append(roundtripLatencies, r.RoundtripLatency)
			}
		} else if r.PutSuccess && verifyEnabled {
			agg.GetFailed++
		}
	}

	// Compute statistics
	agg.PutStats = ComputeLatencyStats(putLatencies)
	agg.GetStats = ComputeLatencyStats(getLatencies)
	agg.RoundtripStats = ComputeLatencyStats(roundtripLatencies)

	return agg
}

// =============================================================================
// WORKER - Processes blobs concurrently
// =============================================================================

// Worker handles concurrent PUT/GET requests
type Worker struct {
	id        int
	config    Config
	collector *ResultsCollector
	client    *http.Client
	blobChan  chan int
}

func NewWorker(id int, config Config, collector *ResultsCollector, blobChan chan int) *Worker {
	return &Worker{
		id:        id,
		config:    config,
		collector: collector,
		client:    &http.Client{Timeout: config.HTTPTimeout},
		blobChan:  blobChan,
	}
}

// Run processes blobs from the channel until it's closed or context is cancelled
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

// processBlob handles a single blob: generate -> PUT -> optional GET verification
func (w *Worker) processBlob(ctx context.Context, blobNum int) {
	result := BlobResult{
		WorkerID:  w.id,
		BlobNum:   blobNum,
		BlobSize:  w.config.BlobSize,
		StartTime: time.Now(),
	}

	w.collector.IncrementStarted()

	// Step 1: Generate random blob data
	data, err := w.generateRandomData()
	if err != nil {
		w.finishWithError(&result, fmt.Sprintf("failed to generate data: %v", err))
		return
	}

	// Step 2: PUT the blob
	commitment, err := w.executePut(ctx, data)
	result.PutLatency = time.Since(result.StartTime)
	result.EndTime = time.Now()

	if err != nil {
		w.finishWithError(&result, fmt.Sprintf("PUT failed: %v", err))
		fmt.Printf("[W%d] ❌ Blob #%d: PUT failed after %v - %v\n",
			w.id, blobNum, result.PutLatency.Round(time.Millisecond), err)
		return
	}

	result.Commitment = commitment
	result.PutSuccess = true
	w.collector.IncrementPutCompleted(true)

	if w.config.Verbose {
		fmt.Printf("[W%d] ✅ Blob #%d: PUT OK in %v (commitment: %s...)\n",
			w.id, blobNum, result.PutLatency.Round(time.Millisecond),
			hex.EncodeToString(commitment[:8]))
	}

	// Step 3: Optional GET verification
	if w.config.VerifyAfterPut {
		w.verifyGet(ctx, &result, data, blobNum)
	}

	w.collector.Add(result)
}

func (w *Worker) generateRandomData() ([]byte, error) {
	data := make([]byte, w.config.BlobSize)
	_, err := rand.Read(data)
	return data, err
}

func (w *Worker) finishWithError(result *BlobResult, errMsg string) {
	result.Error = errMsg
	result.EndTime = time.Now()
	result.PutLatency = result.EndTime.Sub(result.StartTime)
	w.collector.Add(*result)
	w.collector.IncrementPutCompleted(false)
}

func (w *Worker) verifyGet(ctx context.Context, result *BlobResult, originalData []byte, blobNum int) {
	getStart := time.Now()
	retrievedData, err := w.executeGet(ctx, result.Commitment)
	result.GetLatency = time.Since(getStart)

	if err != nil {
		result.Error = fmt.Sprintf("GET verification failed: %v", err)
		w.collector.IncrementGetCompleted(false)
		fmt.Printf("[W%d] ⚠️  Blob #%d: GET failed - %v\n", w.id, blobNum, err)
		return
	}

	if !bytes.Equal(retrievedData, originalData) {
		result.Error = "data mismatch"
		w.collector.IncrementGetCompleted(false)
		fmt.Printf("[W%d] ⚠️  Blob #%d: DATA MISMATCH!\n", w.id, blobNum)
		return
	}

	// Success!
	result.GetSuccess = true
	result.DataVerified = true
	result.RoundtripLatency = result.PutLatency + result.GetLatency
	w.collector.IncrementGetCompleted(true)

	if w.config.Verbose {
		fmt.Printf("[W%d] ✓  Blob #%d: GET verified in %v (roundtrip: %v)\n",
			w.id, blobNum, result.GetLatency.Round(time.Millisecond),
			result.RoundtripLatency.Round(time.Millisecond))
	}
}

// =============================================================================
// HTTP CLIENT METHODS
// =============================================================================

func (w *Worker) executePut(ctx context.Context, data []byte) ([]byte, error) {
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

func (w *Worker) executeGet(ctx context.Context, commitment []byte) ([]byte, error) {
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

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	return body, nil
}

// =============================================================================
// TEST ORCHESTRATOR
// =============================================================================

// Test orchestrates the concurrent throughput test
type Test struct {
	config    Config
	collector *ResultsCollector
	startTime time.Time
}

func NewTest(config Config) *Test {
	return &Test{
		config:    config,
		collector: NewResultsCollector(),
	}
}

// Run executes the test and prints results
func (t *Test) Run(ctx context.Context) {
	t.startTime = time.Now()

	t.startWorkers(ctx)
	t.printResults()
}

func (t *Test) startWorkers(ctx context.Context) {
	// Create blob channel and queue all work
	blobChan := make(chan int, t.config.TotalBlobs)
	for i := 1; i <= t.config.TotalBlobs; i++ {
		blobChan <- i
	}
	close(blobChan)

	// Start workers with ramp-up delay
	var wg sync.WaitGroup
	for i := 0; i < t.config.ConcurrentPuts; i++ {
		wg.Add(1)
		worker := NewWorker(i+1, t.config, t.collector, blobChan)
		go worker.Run(ctx, &wg)

		// Stagger worker starts (except for last worker)
		if i < t.config.ConcurrentPuts-1 && t.config.RampUpDelay > 0 {
			select {
			case <-ctx.Done():
				break
			case <-time.After(t.config.RampUpDelay):
			}
		}
	}
	fmt.Printf("[MAIN] Started %d workers\n", t.config.ConcurrentPuts)

	// Start progress reporter
	go t.reportProgress(ctx)

	// Wait for completion
	wg.Wait()
}

func (t *Test) reportProgress(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, completed, failed := t.collector.GetProgress()
			elapsed := time.Since(t.startTime).Round(time.Second)
			fmt.Printf("[PROGRESS] %v elapsed: %d/%d completed (%d failed)\n",
				elapsed, completed, t.config.TotalBlobs, failed)
		}
	}
}

// =============================================================================
// RESULTS PRINTING
// =============================================================================

func (t *Test) printResults() {
	results := t.collector.Snapshot()
	totalDuration := time.Since(t.startTime)
	agg := AggregateResults(results, t.config.VerifyAfterPut)

	t.printHeader()
	t.printPutResults(agg)
	t.printGetResults(agg)
	t.printRoundtripResults(agg)
	t.printThroughputMetrics(agg, totalDuration)
	t.printWorkerStats(agg)
	t.printDataIntegrity(agg)
	t.printFooter()
}

func (t *Test) printHeader() {
	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Println("                     FINAL TEST RESULTS")
	fmt.Println("════════════════════════════════════════════════════════════════")
}

func (t *Test) printFooter() {
	fmt.Println("════════════════════════════════════════════════════════════════")
}

func (t *Test) printPutResults(agg *AggregatedResults) {
	fmt.Printf("\n📤 PUT Results:\n")
	fmt.Printf("   Success: %d\n", agg.PutSuccess)
	fmt.Printf("   Failed:  %d\n", agg.PutFailed)
	fmt.Printf("   Total:   %d\n", agg.PutSuccess+agg.PutFailed)

	PrintLatencyStats("⏱️  PUT Latency (includes Celestia confirmation)", agg.PutStats, false)
}

func (t *Test) printGetResults(agg *AggregatedResults) {
	if !t.config.VerifyAfterPut {
		return
	}

	fmt.Printf("\n📥 GET Verification:\n")
	fmt.Printf("   Success: %d\n", agg.GetSuccess)
	fmt.Printf("   Failed:  %d\n", agg.GetFailed)

	PrintLatencyStats("⏱️  GET Latency", agg.GetStats, false)
}

func (t *Test) printRoundtripResults(agg *AggregatedResults) {
	if !t.config.VerifyAfterPut || agg.RoundtripStats == nil {
		return
	}

	fmt.Printf("\n🔄 ROUNDTRIP Latency (PUT + GET per blob):\n")
	fmt.Printf("   Blobs measured: %d\n", agg.RoundtripStats.Count)
	fmt.Printf("   Min:  %v\n", agg.RoundtripStats.Min.Round(time.Millisecond))
	fmt.Printf("   Max:  %v\n", agg.RoundtripStats.Max.Round(time.Millisecond))
	fmt.Printf("   Avg:  %v\n", agg.RoundtripStats.Avg.Round(time.Millisecond))
	fmt.Printf("   P50:  %v\n", agg.RoundtripStats.P50.Round(time.Millisecond))
	fmt.Printf("   P95:  %v\n", agg.RoundtripStats.P95.Round(time.Millisecond))
	fmt.Printf("   P99:  %v\n", agg.RoundtripStats.P99.Round(time.Millisecond))
}

func (t *Test) printThroughputMetrics(agg *AggregatedResults, totalDuration time.Duration) {
	fmt.Println("\n────────────────────────────────────────────────────────────────")
	fmt.Println("                    📈 THROUGHPUT METRICS")
	fmt.Println("────────────────────────────────────────────────────────────────")

	fmt.Printf("\n   Test duration:      %v\n", totalDuration.Round(time.Second))
	fmt.Printf("   Total data:         %.2f MB (%d bytes)\n",
		float64(agg.TotalBytes)/(1024*1024), agg.TotalBytes)
	fmt.Printf("   Concurrent workers: %d\n", t.config.ConcurrentPuts)

	if totalDuration.Seconds() > 0 && agg.TotalBytes > 0 {
		bytesPerSec := float64(agg.TotalBytes) / totalDuration.Seconds()
		blobsPerSec := float64(agg.PutSuccess) / totalDuration.Seconds()

		fmt.Printf("\n   🚀 Throughput:      %.2f KB/s (%.2f MB/s)\n",
			bytesPerSec/1024, bytesPerSec/(1024*1024))
		fmt.Printf("   📦 Blob rate:       %.2f blobs/s\n", blobsPerSec)

		// Effective concurrency calculation
		if agg.PutStats != nil {
			effectiveConcurrency := float64(agg.PutSuccess) * agg.PutStats.Avg.Seconds() / totalDuration.Seconds()
			fmt.Printf("   ⚡ Effective concurrency: %.1f\n", effectiveConcurrency)
		}
	}
}

func (t *Test) printWorkerStats(agg *AggregatedResults) {
	fmt.Println("\n────────────────────────────────────────────────────────────────")
	fmt.Println("                    PER-WORKER STATISTICS")
	fmt.Println("────────────────────────────────────────────────────────────────")

	fmt.Printf("\n%-10s %-10s %-10s %-15s\n", "Worker", "Blobs", "Success", "Avg Latency")
	fmt.Println("────────────────────────────────────────────────────────────────")

	for i := 1; i <= t.config.ConcurrentPuts; i++ {
		ws := agg.WorkerStats[i]
		if ws == nil {
			ws = &WorkerStats{} // Handle workers that processed no blobs
		}

		avgLat := time.Duration(0)
		if ws.SuccessCount > 0 {
			avgLat = ws.TotalLatency / time.Duration(ws.SuccessCount)
		}
		fmt.Printf("%-10d %-10d %-10d %-15v\n", i, ws.BlobCount, ws.SuccessCount, avgLat.Round(time.Millisecond))
	}
}

func (t *Test) printDataIntegrity(agg *AggregatedResults) {
	if !t.config.VerifyAfterPut {
		return
	}

	fmt.Println("\n────────────────────────────────────────────────────────────────")
	if agg.GetFailed == 0 && agg.GetSuccess > 0 {
		fmt.Println("✅ DATA INTEGRITY: All blobs verified successfully")
	} else if agg.GetFailed > 0 {
		fmt.Printf("⚠️  DATA INTEGRITY: %d verification failures\n", agg.GetFailed)
	}
}

// PrintConfig displays the test configuration
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

// =============================================================================
// MAIN
// =============================================================================

func main() {
	config := parseFlags()

	// Setup graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Run test
	test := NewTest(config)
	test.PrintConfig()
	test.Run(ctx)
}

func parseFlags() Config {
	config := DefaultConfig()

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

	return config
}
