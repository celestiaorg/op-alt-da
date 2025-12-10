# Manual Throughput Tests

This directory contains manual tests for measuring DA server throughput and latency.

## Concurrent Throughput Test

Tests the stateless DA server with concurrent PUT requests to measure maximum throughput, latency, and roundtrip times.

### Usage

```bash
# Build the test
cd tests/manual
go build -o throughput-test concurrent_throughput.go

# Run with defaults (4 workers, 20 blobs, ~1MB each)
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
  -timeout 180 \
  -v
```

### Flags

| Flag       | Default                 | Description                         |
| ---------- | ----------------------- | ----------------------------------- |
| `-url`     | `http://localhost:3100` | DA server URL                       |
| `-workers` | `4`                     | Number of concurrent PUT workers    |
| `-blobs`   | `20`                    | Total number of blobs to submit     |
| `-size`    | `1052672` (~1MB)        | Blob size in bytes                  |
| `-verify`  | `true`                  | Verify GET after each PUT           |
| `-timeout` | `120`                   | HTTP timeout in seconds             |
| `-rampup`  | `500`                   | Delay between starting workers (ms) |
| `-v`       | `false`                 | Verbose output (per-blob logs)      |

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

[MAIN] Started 4 workers
[PROGRESS] 5s elapsed: 0/20 completed (0 failed)
...

════════════════════════════════════════════════════════════════
                     FINAL TEST RESULTS
════════════════════════════════════════════════════════════════

📤 PUT Results:
   Success: 20
   Failed:  0
   Total:   20

⏱️  PUT Latency (includes Celestia confirmation):
   Min:  12.5s
   Max:  18.2s
   Avg:  15.1s
   P50:  14.8s
   P95:  17.5s
   P99:  18.0s

📥 GET Verification:
   Success: 20
   Failed:  0

⏱️  GET Latency:
   Min:  85ms
   Max:  245ms
   Avg:  124ms
   P50:  118ms
   P95:  210ms
   P99:  238ms

🔄 ROUNDTRIP Latency (PUT + GET per blob):
   Blobs measured: 20
   Min:  12.6s
   Max:  18.4s
   Avg:  15.2s
   P50:  14.9s
   P95:  17.7s
   P99:  18.2s

────────────────────────────────────────────────────────────────
                    📈 THROUGHPUT METRICS
────────────────────────────────────────────────────────────────

   Test duration:      75s
   Total data:         2.50 MB (2621440 bytes)
   Concurrent workers: 4

   🚀 Throughput:      34.13 KB/s (0.03 MB/s)
   📦 Blob rate:       0.27 blobs/s
   ⚡ Effective concurrency: 4.0

────────────────────────────────────────────────────────────────
                    PER-WORKER STATISTICS
────────────────────────────────────────────────────────────────

Worker     Blobs      Success    Avg Latency    
────────────────────────────────────────────────────────────────
1          5          5          15.2s          
2          5          5          14.8s          
3          5          5          15.5s          
4          5          5          14.9s          

────────────────────────────────────────────────────────────────
✅ DATA INTEGRITY: All blobs verified successfully
════════════════════════════════════════════════════════════════
```

### Metrics Explained

| Metric                 | Description                                                        |
| ---------------------- | ------------------------------------------------------------------ |
| **PUT Latency**        | Time for a single PUT request (includes Celestia block inclusion)  |
| **GET Latency**        | Time to retrieve and verify a blob after PUT                       |
| **Roundtrip Latency**  | PUT + GET combined for each blob (end-to-end latency)              |
| **Throughput**         | Data rate in KB/s and MB/s                                         |
| **Blob rate**          | Number of blobs successfully submitted per second                  |
| **Effective concurrency** | Average number of requests in-flight (should ≈ worker count)    |

### Test Scenarios

#### High Throughput Test
```bash
go run concurrent_throughput.go -workers 8 -blobs 100 -size 131072
```

#### Large Blob Test
```bash
go run concurrent_throughput.go -workers 2 -blobs 10 -size 1048576  # 1MB blobs
```

#### Stress Test
```bash
go run concurrent_throughput.go -workers 16 -blobs 200 -timeout 300
```

#### Quick Verification
```bash
go run concurrent_throughput.go -workers 2 -blobs 5 -v
```
