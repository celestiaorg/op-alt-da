# Metrics Sanity Test

This test validates that worker metrics are being recorded correctly by the DA server.

## What it does

1. **Sends random PUT requests** every 2 seconds with random blob data (50KB to 500KB)
2. **Polls GET requests** for each commitment until it returns 200 OK (or times out after 2 minutes)
3. **Scrapes metrics endpoint** at the end to verify worker metrics were recorded
4. **Reports results** showing roundtrip times and metric values

## Prerequisites

1. DA server running on `localhost:3100`
2. Metrics enabled and exposed on `localhost:6060`
3. Server connected to Celestia (testnet or localestia)

## Running the test

### Default (5 minutes):

```bash
cd tests/sanity
go test -v -timeout 10m
```

### Custom duration (2 minutes):

```bash
go test -v -timeout 5m -duration 2m
```

### Custom duration (10 minutes):

```bash
go test -v -timeout 15m -duration 10m
```

**Important:** The `-timeout` flag should always be at least `duration + 5m` to allow time for cleanup and reporting.

### Against different server:

Edit the URLs in the test file:

```go
const (
    serverURL  = "http://your-server:3100"
    metricsURL = "http://your-server:6060/metrics"
)
```

## What gets validated

✅ **Functionality:**
- PUT requests succeed
- GET requests return 200 OK after confirmation
- Average time to confirmation is reasonable

✅ **Worker Metrics:**
- `celestia_submissions_total` > 0
- `celestia_retrievals_total` > 0
- Submission/retrieval error rates < 10%

✅ **HTTP Metrics:**
- `op_altda_get_requests_total{status="2xx"}` > 0
- `op_altda_get_requests_total{status="4xx"}` recorded

## Example output

```
=== RUN   TestMetricsSanity
    metrics_sanity_test.go:45: 🚀 Starting metrics sanity test
    metrics_sanity_test.go:46: Server: http://localhost:3100
    metrics_sanity_test.go:47: Metrics: http://localhost:6060/metrics
    metrics_sanity_test.go:48: Duration: 5m0s
    ...
    metrics_sanity_test.go:120: ✅ PUT #1: 0x0c1234567890... (size: 50 KB)
    metrics_sanity_test.go:120: ✅ PUT #2: 0x0cabcdef1234... (size: 150 KB)
    ...
    metrics_sanity_test.go:165: ✅ GET 0x0c1234567890... 200 OK after 45s (9 attempts)
    ...
    metrics_sanity_test.go:265: ================================================================================
    metrics_sanity_test.go:266: METRICS SANITY TEST REPORT
    metrics_sanity_test.go:267: ================================================================================
    metrics_sanity_test.go:269: 📥 GET Results:
    metrics_sanity_test.go:270:   - Successful 200 OK: 35
    metrics_sanity_test.go:271:   - Average time to 200: 52s
    metrics_sanity_test.go:273: 📊 Worker Metrics (from http://localhost:6060/metrics):
    metrics_sanity_test.go:274:   - celestia_submissions_total: 8
    metrics_sanity_test.go:275:   - celestia_submission_errors_total: 0
    metrics_sanity_test.go:276:   - celestia_retrievals_total: 8
    metrics_sanity_test.go:277:   - celestia_retrieval_errors_total: 0
    metrics_sanity_test.go:279: 📊 HTTP Metrics:
    metrics_sanity_test.go:280:   - op_altda_get_requests_total{status="2xx"}: 35
    metrics_sanity_test.go:281:   - op_altda_get_requests_total{status="4xx"}: 287
    ...
    metrics_sanity_test.go:292: ✅ PASS: Got 35 successful 200 OK responses
    metrics_sanity_test.go:299: ✅ PASS: Worker submissions recorded: 8
    metrics_sanity_test.go:306: ✅ PASS: Worker retrievals recorded: 8
    metrics_sanity_test.go:313: ✅ PASS: HTTP GET 2xx metrics recorded: 35
    metrics_sanity_test.go:332: ✅ PASS: No submission errors
    metrics_sanity_test.go:345: ✅ PASS: No retrieval errors
    metrics_sanity_test.go:348: 🏁 Test complete!
--- PASS: TestMetricsSanity (300.12s)
PASS
```

## Troubleshooting

**No 200 OK responses:**
- Check server logs for worker activity
- Verify server is connected to Celestia
- Check batching configuration (might need more blobs or longer wait)

**Worker metrics are 0:**
- Check `--metrics.enabled` flag is set
- Verify workers are running (check logs for "Starting submission worker")
- Check if batches are being created (might need more PUTs)

**High error rates:**
- Check server logs for errors
- Verify Celestia node connectivity
- Check gas balance for transactions
