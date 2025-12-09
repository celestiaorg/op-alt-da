# Manual Throughput Tests

This directory contains manual tests for measuring DA server throughput and latency.

## Concurrent Throughput Test

Tests the stateless DA server with concurrent PUT requests to measure maximum throughput.

### Usage

```bash
# Build the test
cd tests/manual
go build -o throughput-test concurrent_throughput.go

# Run with defaults (4 workers, 20 blobs, 128KB each)
./throughput-test

# Or run directly
go run concurrent_throughput.go

# Run with custom settings
go run concurrent_throughput.go \
  -url http://localhost:3100 \
  -workers 8 \
  -blobs 50 \
  -size 131072 \
  -verify=true \
  -timeout 180
```

### Flags

| Flag       | Default                 | Description                         |
| ---------- | ----------------------- | ----------------------------------- |
| `-url`     | `http://localhost:3100` | DA server URL                       |
| `-workers` | `4`                     | Number of concurrent PUT workers    |
| `-blobs`   | `20`                    | Total number of blobs to submit     |
| `-size`    | `131072` (128KB)        | Blob size in bytes                  |
| `-verify`  | `true`                  | Verify GET after each PUT           |
| `-timeout` | `120`                   | HTTP timeout in seconds             |
| `-rampup`  | `500`                   | Delay between starting workers (ms) |

### Expected Output

```
════════════════════════════════════════════════════════════════
           Concurrent Throughput Test (Stateless DA)
════════════════════════════════════════════════════════════════
Server:           http://localhost:3100
Blob size:        131072 bytes (~128 KB)
Concurrent PUTs:  4 workers
Total blobs:      20
HTTP timeout:     2m0s (for Celestia confirmation)
Verify GETs:      true
────────────────────────────────────────────────────────────────

[MAIN] Started worker 1/4
[MAIN] Started worker 2/4
[MAIN] Started worker 3/4
[MAIN] Started worker 4/4
[W1] ✅ Blob #1: PUT OK in 15.234s (commitment: 010c0100...)
[W2] ✅ Blob #2: PUT OK in 16.102s (commitment: 010c0200...)
[W1] ✓  Blob #1: GET verified in 124ms
...

════════════════════════════════════════════════════════════════
                     FINAL TEST RESULTS
════════════════════════════════════════════════════════════════

📤 PUT Results:
   Success: 20
   Failed:  0

⏱️  PUT Latency (includes Celestia confirmation):
   Min:  12.5s
   Max:  18.2s
   Avg:  15.1s
   P50:  14.8s
   P95:  17.5s
   P99:  18.0s

────────────────────────────────────────────────────────────────
                    📈 THROUGHPUT METRICS
────────────────────────────────────────────────────────────────

   Test duration:     75s
   Total data:        2.50 MB
   Concurrent workers: 4

   🚀 Throughput:     34.13 KB/s (0.03 MB/s)
   📦 Blob rate:      0.27 blobs/s
   ⚡ Effective concurrency: 4.0

✅ DATA INTEGRITY: All blobs verified successfully
════════════════════════════════════════════════════════════════
```
