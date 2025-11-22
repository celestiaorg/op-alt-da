package sanity

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

var (
	testDuration = flag.Duration("duration", 5*time.Minute, "Test duration (e.g., 2m, 5m, 10m)")
)

const (
	serverURL     = "http://localhost:3100"
	metricsURL    = "http://localhost:6060/metrics"
	putInterval   = 2 * time.Second  // Send PUT every 2 seconds
	blobSizeMin   = 50 * 1024        // 50KB
	blobSizeMax   = 500 * 1024       // 500KB
	getTimeout    = 2 * time.Minute  // Max time to wait for GET 200
	getRetryDelay = 5 * time.Second  // Retry GET every 5 seconds
)

type TestResult struct {
	TotalPuts           int
	SuccessfulPuts      int
	TotalGets           int
	Successful200Gets   int
	Failed404Gets       int
	TimedOutGets        int
	AverageTimeToGet200 time.Duration

	// E2E pipeline metrics
	AverageE2ETime      time.Duration // PUT -> batch -> DA submission -> confirmation -> GET 200

	// Metrics from server
	SubmissionsTotal    float64
	SubmissionErrors    float64
	RetrievalsTotal     float64
	RetrievalErrors     float64

	// HTTP metrics
	GetRequests2xx      float64
	GetRequests4xx      float64
}

func TestMetricsSanity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), *testDuration+2*time.Minute)
	defer cancel()

	t.Logf("🚀 Starting metrics sanity test")
	t.Logf("Server: %s", serverURL)
	t.Logf("Metrics: %s", metricsURL)
	t.Logf("Duration: %s", *testDuration)
	t.Logf("PUT interval: %s", putInterval)

	// Check server is running
	resp, err := http.Get(serverURL + "/health")
	if err != nil {
		t.Fatalf("Server not responding: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("Server health check failed: %d", resp.StatusCode)
	}

	// Check metrics endpoint is accessible
	resp, err = http.Get(metricsURL)
	if err != nil {
		t.Fatalf("Metrics endpoint not responding: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("Metrics endpoint not accessible: %d", resp.StatusCode)
	}

	t.Logf("✅ Server and metrics endpoint are healthy")

	// Shared result tracking
	result := &TestResult{}
	var resultMu sync.Mutex

	// Track PUT timestamps for E2E measurement
	putTimestamps := make(map[string]time.Time)
	var timestampsMu sync.Mutex

	// Start background PUT sender
	commitments := make(chan string, 1000)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		sendPuts(ctx, t, commitments, result, &resultMu, putTimestamps, &timestampsMu)
	}()

	// Start background GET poller
	timeToGet200 := make(chan time.Duration, 1000)
	e2eTimes := make(chan time.Duration, 1000)
	wg.Add(1)
	go func() {
		defer wg.Done()
		pollGets(ctx, t, commitments, timeToGet200, e2eTimes, putTimestamps, &timestampsMu)
	}()

	// Wait for test duration
	time.Sleep(*testDuration)
	cancel() // Stop workers
	wg.Wait()

	close(timeToGet200)
	close(e2eTimes)

	// Collect GET results
	collectResults(t, timeToGet200, e2eTimes, result)

	// Try to get final metrics from server (optional - don't fail if it doesn't work)
	scrapeMetrics(t, result)

	// Print report
	printReport(t, result)

	// Validate results
	validateResults(t, result)
}

func sendPuts(ctx context.Context, t *testing.T, commitments chan<- string, result *TestResult, mu *sync.Mutex, putTimestamps map[string]time.Time, tsMu *sync.Mutex) {
	ticker := time.NewTicker(putInterval)
	defer ticker.Stop()

	putCount := 0
	for {
		select {
		case <-ctx.Done():
			t.Logf("📤 PUT sender stopping after %d PUTs", putCount)
			return
		case <-ticker.C:
			putCount++
			mu.Lock()
			result.TotalPuts++
			mu.Unlock()

			// Generate random blob data
			size := blobSizeMin + (putCount % (blobSizeMax - blobSizeMin))
			data := make([]byte, size)
			rand.Read(data)

			// Record timestamp BEFORE sending PUT
			putTime := time.Now()

			// Send PUT request
			resp, err := http.Post(
				serverURL+"/put",
				"application/octet-stream",
				bytes.NewReader(data),
			)
			if err != nil {
				t.Logf("❌ PUT #%d failed: %v", putCount, err)
				continue
			}

			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode != 200 {
				t.Logf("❌ PUT #%d returned %d: %s", putCount, resp.StatusCode, string(body))
				continue
			}

			mu.Lock()
			result.SuccessfulPuts++
			mu.Unlock()

			// Commitment is binary data, hex-encode it for URL usage
			commitment := hex.EncodeToString(body)
			t.Logf("✅ PUT #%d: %s (size: %d KB)", putCount, commitment[:16]+"...", size/1024)

			// Store PUT timestamp for E2E tracking
			tsMu.Lock()
			putTimestamps[commitment] = putTime
			tsMu.Unlock()

			// Send commitment to GET poller
			select {
			case commitments <- commitment:
			case <-ctx.Done():
				return
			}
		}
	}
}

func pollGets(ctx context.Context, t *testing.T, commitments <-chan string, timeToGet200 chan<- time.Duration, e2eTimes chan<- time.Duration, putTimestamps map[string]time.Time, tsMu *sync.Mutex) {
	for {
		select {
		case <-ctx.Done():
			t.Logf("📥 GET poller stopping")
			return
		case commitment := <-commitments:
			// Poll this commitment until 200 or timeout
			go pollSingleGet(ctx, t, commitment, timeToGet200, e2eTimes, putTimestamps, tsMu)
		}
	}
}

func pollSingleGet(ctx context.Context, t *testing.T, commitment string, timeToGet200 chan<- time.Duration, e2eTimes chan<- time.Duration, putTimestamps map[string]time.Time, tsMu *sync.Mutex) {
	startTime := time.Now()
	getCtx, cancel := context.WithTimeout(ctx, getTimeout)
	defer cancel()

	ticker := time.NewTicker(getRetryDelay)
	defer ticker.Stop()

	attemptCount := 0
	for {
		attemptCount++

		req, err := http.NewRequestWithContext(getCtx, "GET", serverURL+"/get/"+commitment, nil)
		if err != nil {
			t.Logf("Failed to create request: %v", err)
			return
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if getCtx.Err() != nil {
				// Timed out
				elapsed := time.Since(startTime)
				t.Logf("⏱️  GET %s... TIMEOUT after %s (%d attempts)", commitment[:16], elapsed.Round(time.Second), attemptCount)
				return
			}
			t.Logf("❌ GET %s... error: %v", commitment[:16], err)
			continue
		}

		if resp.StatusCode == 200 {
			resp.Body.Close()
			elapsed := time.Since(startTime)

			// Calculate E2E time (PUT to GET 200)
			tsMu.Lock()
			putTime, hasPutTime := putTimestamps[commitment]
			if hasPutTime {
				delete(putTimestamps, commitment) // Clean up
			}
			tsMu.Unlock()

			if hasPutTime {
				e2eTime := time.Since(putTime)
				t.Logf("✅ GET %s... 200 OK after %s (%d attempts) | E2E: %s", commitment[:16], elapsed.Round(time.Second), attemptCount, e2eTime.Round(time.Second))
				e2eTimes <- e2eTime
			} else {
				t.Logf("✅ GET %s... 200 OK after %s (%d attempts)", commitment[:16], elapsed.Round(time.Second), attemptCount)
			}

			timeToGet200 <- elapsed
			return
		}

		resp.Body.Close()

		if resp.StatusCode == 404 {
			// Still not confirmed, keep retrying
			select {
			case <-getCtx.Done():
				elapsed := time.Since(startTime)
				t.Logf("⏱️  GET %s... TIMEOUT after %s (%d attempts, last: 404)", commitment[:16], elapsed.Round(time.Second), attemptCount)
				return
			case <-ticker.C:
				continue
			}
		} else {
			t.Logf("❌ GET %s... unexpected status %d", commitment[:16], resp.StatusCode)
			return
		}
	}
}

func collectResults(t *testing.T, timeToGet200 <-chan time.Duration, e2eTimes <-chan time.Duration, result *TestResult) {
	var totalTime time.Duration
	for elapsed := range timeToGet200 {
		result.Successful200Gets++
		totalTime += elapsed
	}

	if result.Successful200Gets > 0 {
		result.AverageTimeToGet200 = totalTime / time.Duration(result.Successful200Gets)
	}

	// Collect E2E times
	var totalE2E time.Duration
	e2eCount := 0
	for e2e := range e2eTimes {
		e2eCount++
		totalE2E += e2e
	}

	if e2eCount > 0 {
		result.AverageE2ETime = totalE2E / time.Duration(e2eCount)
	}
}

func scrapeMetrics(t *testing.T, result *TestResult) {
	resp, err := http.Get(metricsURL)
	if err != nil {
		t.Logf("⚠️  Failed to scrape metrics: %v", err)
		return
	}
	defer resp.Body.Close()

	// Use Prometheus parser with UTF8 validation scheme
	parser := expfmt.NewTextParser(model.UTF8Validation)
	metricFamilies, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		t.Logf("⚠️  Failed to parse metrics: %v", err)
		return
	}

	// Extract worker metrics
	if mf, ok := metricFamilies["celestia_submissions_total"]; ok {
		if len(mf.Metric) > 0 {
			result.SubmissionsTotal = float64(*mf.Metric[0].Counter.Value)
		}
	}
	if mf, ok := metricFamilies["celestia_submission_errors_total"]; ok {
		if len(mf.Metric) > 0 {
			result.SubmissionErrors = float64(*mf.Metric[0].Counter.Value)
		}
	}
	if mf, ok := metricFamilies["celestia_retrievals_total"]; ok {
		if len(mf.Metric) > 0 {
			result.RetrievalsTotal = float64(*mf.Metric[0].Counter.Value)
		}
	}
	if mf, ok := metricFamilies["celestia_retrieval_errors_total"]; ok {
		if len(mf.Metric) > 0 {
			result.RetrievalErrors = float64(*mf.Metric[0].Counter.Value)
		}
	}

	// Extract GET request metrics by status
	if mf, ok := metricFamilies["op_altda_get_requests_total"]; ok {
		for _, m := range mf.Metric {
			for _, label := range m.Label {
				if label.GetName() == "status" {
					value := float64(*m.Counter.Value)
					switch label.GetValue() {
					case "2xx":
						result.GetRequests2xx = value
					case "4xx":
						result.GetRequests4xx = value
					}
				}
			}
		}
	}

	t.Logf("📊 Metrics scraped successfully")
}

func printReport(t *testing.T, result *TestResult) {
	sep := strings.Repeat("=", 80)
	t.Logf("\n%s", sep)
	t.Logf("METRICS SANITY TEST REPORT")
	t.Logf("%s", sep)
	t.Logf("")
	t.Logf("📤 PUT Results:")
	t.Logf("  - Total PUT attempts: %d", result.TotalPuts)
	t.Logf("  - Successful PUTs: %d", result.SuccessfulPuts)
	if result.TotalPuts > 0 {
		t.Logf("  - Success rate: %.1f%%", float64(result.SuccessfulPuts)/float64(result.TotalPuts)*100)
	}
	t.Logf("")
	t.Logf("📥 GET Results:")
	t.Logf("  - Successful 200 OK: %d", result.Successful200Gets)
	if result.AverageTimeToGet200 > 0 {
		t.Logf("  - Average time to 200: %s", result.AverageTimeToGet200.Round(time.Second))
	}
	t.Logf("")
	t.Logf("🔄 E2E Pipeline Metrics (PUT → Batch → DA Submission → Confirmation → GET 200):")
	if result.AverageE2ETime > 0 {
		t.Logf("  - Average E2E time: %s", result.AverageE2ETime.Round(time.Second))
		t.Logf("  - This includes: batching + Celestia submission + confirmation + retrieval")
	} else {
		t.Logf("  - No E2E data collected")
	}
	t.Logf("")
	if result.SubmissionsTotal > 0 || result.RetrievalsTotal > 0 {
		t.Logf("📊 Worker Metrics (from %s):", metricsURL)
		t.Logf("  - celestia_submissions_total: %.0f", result.SubmissionsTotal)
		t.Logf("  - celestia_submission_errors_total: %.0f", result.SubmissionErrors)
		t.Logf("  - celestia_retrievals_total: %.0f", result.RetrievalsTotal)
		t.Logf("  - celestia_retrieval_errors_total: %.0f", result.RetrievalErrors)
		t.Logf("")
	}
	if result.GetRequests2xx > 0 || result.GetRequests4xx > 0 {
		t.Logf("📊 HTTP Metrics:")
		t.Logf("  - op_altda_get_requests_total{status=\"2xx\"}: %.0f", result.GetRequests2xx)
		t.Logf("  - op_altda_get_requests_total{status=\"4xx\"}: %.0f", result.GetRequests4xx)
		t.Logf("")
	}
	t.Logf("%s", sep)
}

func validateResults(t *testing.T, result *TestResult) {
	t.Logf("\n🔍 Validating results...")

	// Check if we sent any PUTs
	if result.TotalPuts == 0 {
		t.Errorf("❌ FAIL: No PUT requests were sent!")
	} else {
		t.Logf("✅ PASS: Sent %d PUT requests", result.TotalPuts)
	}

	// Check PUT success rate
	if result.SuccessfulPuts == 0 {
		t.Errorf("❌ FAIL: No successful PUT requests!")
	} else if result.TotalPuts > 0 {
		successRate := float64(result.SuccessfulPuts) / float64(result.TotalPuts) * 100
		if successRate < 90 {
			t.Errorf("❌ FAIL: Low PUT success rate: %.1f%%", successRate)
		} else {
			t.Logf("✅ PASS: PUT success rate: %.1f%%", successRate)
		}
	}

	// Check if we got any successful 200 responses
	if result.Successful200Gets == 0 {
		t.Errorf("❌ FAIL: No successful 200 OK responses!")
	} else {
		t.Logf("✅ PASS: Got %d successful 200 OK responses", result.Successful200Gets)
	}

	// Check average time to confirmation (optional - might be 0 if no 200s)
	if result.AverageTimeToGet200 > 0 {
		t.Logf("📊 INFO: Average time to confirmation: %s", result.AverageTimeToGet200.Round(time.Second))
	}

	// Check worker metrics if available (optional)
	if result.SubmissionsTotal > 0 {
		t.Logf("📊 INFO: Worker submissions recorded: %.0f", result.SubmissionsTotal)
		if result.SubmissionErrors > 0 {
			errorRate := result.SubmissionErrors / (result.SubmissionsTotal + result.SubmissionErrors) * 100
			if errorRate > 10 {
				t.Errorf("❌ FAIL: High submission error rate: %.1f%%", errorRate)
			} else {
				t.Logf("⚠️  WARN: Some submission errors: %.1f%%", errorRate)
			}
		}
	}

	if result.RetrievalsTotal > 0 {
		t.Logf("📊 INFO: Worker retrievals recorded: %.0f", result.RetrievalsTotal)
		if result.RetrievalErrors > 0 {
			errorRate := result.RetrievalErrors / (result.RetrievalsTotal + result.RetrievalErrors) * 100
			if errorRate > 10 {
				t.Errorf("❌ FAIL: High retrieval error rate: %.1f%%", errorRate)
			} else {
				t.Logf("⚠️  WARN: Some retrieval errors: %.1f%%", errorRate)
			}
		}
	}

	t.Logf("\n🏁 Test complete!")
}
