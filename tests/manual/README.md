# Manual Integration Test: Writer/Reader Round-Trip

This test measures end-to-end round-trip times for blobs submitted to the DA server and retrieved after confirmation on Celestia.

## Quick Start

```bash
cd tests/manual

# Run with default settings (100 seconds, 2s polling)
go run test-writer-reader.go

# Run for 5 minutes with aggressive polling (500ms)
go run test-writer-reader.go -duration 300 -poll 500

# Graceful shutdown anytime with Ctrl+C
```

## What This Test Does

1. **Writer** sends PUT requests to store blobs (1 per second by default)
2. **Reader** polls GET requests to retrieve blobs after DA confirmation
3. **Verifies** data integrity (original data matches retrieved data)
4. **Measures** timing at each stage

## Timing Measurements Explained

```
┌────────────────────────────────────────────────────────────────────────────┐
│                              TIMELINE                                       │
├────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│   Client                           Server                      Celestia    │
│     │                                │                            │        │
│     │──── PUT Request ──────────────►│                            │        │
│     │                                │ Store in DB                │        │
│     │◄─── PUT Response ─────────────│ (pending_submission)       │        │
│     │                                │                            │        │
│     │                                │ [Batching wait...]         │        │
│     │                                │                            │        │
│     │                                │──── Submit Batch ─────────►│        │
│     │                                │                            │        │
│     │                                │◄─── Confirmed ────────────│        │
│     │                                │ Mark as "confirmed"        │        │
│     │                                │                            │        │
│     │──── GET Request ──────────────►│                            │        │
│     │◄─── GET 200 OK ───────────────│                            │        │
│     │                                │                            │        │
└────────────────────────────────────────────────────────────────────────────┘
```

### Metrics

| Metric          | What It Measures      | Start                 | End                   |
| --------------- | --------------------- | --------------------- | --------------------- |
| **PUT Latency** | HTTP request time     | PUT request sent      | PUT response received |
| **DA Confirm**  | DA layer processing   | PUT response received | GET returns 200 OK    |
| **Total Time**  | End-to-end round-trip | PUT request sent      | GET returns 200 OK    |

### DA Confirm Time Breakdown

The **DA Confirm** time includes:

1. **Batching Wait** - Time waiting for the submission worker to pick up the blob

   - Server batches blobs periodically (e.g., every 10 seconds)
   - A blob submitted just after a batch runs waits longer

2. **Celestia Submission** - Time to submit the batch to Celestia

   - Network latency to Celestia node
   - Block inclusion time

3. **Confirmation** - Time for the server to mark the blob as confirmed
   - Event listener detects on-chain confirmation
   - Database update

## Sample Output

```
════════════════════════════════════════════════════════════════
                     FINAL TEST STATISTICS
════════════════════════════════════════════════════════════════

📤 PUT Requests:
   Total:   60
   Success: 60
   Failed:  0

⏱️  PUT Latency (client → server → response):
   Min: 12ms
   Max: 45ms
   Avg: 23ms

📥 GET Requests:
   Total:         420
   Success (200): 60
   Not Found:     360
   Data Mismatch: 0
   Failed:        0

────────────────────────────────────────────────────────────────
                    ROUND-TRIP MEASUREMENTS
────────────────────────────────────────────────────────────────

🔄 Total Time (PUT start → GET 200 OK):
   Confirmed: 60 blobs
   Min:       8.234s
   Max:       18.567s
   Avg:       12.345s

🌐 DA Confirm Time (PUT response → GET 200 OK):
   Includes: batching wait + Celestia submission + on-chain confirmation
   Min: 8.212s
   Max: 18.522s
   Avg: 12.322s

📊 Polling Stats:
   Avg polls per blob: 6.0

────────────────────────────────────────────────────────────────
                    PER-BLOB TIMING DETAILS
────────────────────────────────────────────────────────────────
Blob#  PUT Latency  DA Confirm   Total Time   Polls  Status
────────────────────────────────────────────────────────────────
1      23ms         12.345s      12.368s      7      ✅
2      18ms         11.234s      11.252s      6      ✅
3      21ms         10.123s      10.144s      5      ✅
...
```

## Configuration

| Flag        | Default | Description                   |
| ----------- | ------- | ----------------------------- |
| `-duration` | 100     | Test duration in seconds      |
| `-poll`     | 2000    | Poll interval in milliseconds |

### Tuning Tips

- **Smaller poll interval** = More accurate timing, more GET requests
- **Larger poll interval** = Less accurate, fewer requests (timing includes poll delay)

For accurate DA Confirm measurements, use `-poll 500` or lower.

## Understanding Results

### High DA Confirm Times

If DA Confirm times are high, check:

1. **Server batching period** - Blobs wait up to this period before submission
2. **Celestia network** - Block times, congestion
3. **Server configuration** - Submission worker settings

### Variance in DA Confirm Times

Variance is expected due to:

1. **Batching timing** - Blobs arriving just before vs. just after a batch
2. **Celestia block times** - ~12 seconds on mainnet
3. **Polling granularity** - Adds up to `poll_interval` variance

### Data Mismatch Errors

If you see data mismatch errors:

1. Check if the server is correctly storing/retrieving blobs
2. Verify commitment computation matches between PUT and GET
3. Check for any data corruption in transit

### 🔴 STUCK Blobs (Reliability Issue)

If you see blobs marked as `🔴 STUCK` with later blobs confirmed, this indicates a **critical reliability issue**:

```
Blob#  PUT Latency  DA Confirm   Total Time   Polls  Status
62     26ms         ...          2m3.243s     ...    🔴 STUCK
63     21ms         ...          2m2.214s     ...    🔴 STUCK
...
69     30ms         18.117s      18.147s      10     ✅
```

**What this means:** The reader server skipped a batch. Blobs 62-68 were submitted to Celestia but the reader never fetched them.

**Possible causes:**

1. **Backfill worker missed a height range** - Check reader logs for gaps
2. **Batch submission partially failed** - Check writer logs
3. **Race condition in height tracking** - Event listener vs backfill worker
4. **Celestia node issues** - Missed blocks or reorg

**Debug steps:**

1. Check writer server logs for batch submission around stuck blob times
2. Check reader server logs for backfill errors
3. Compare `latest_height` between writer and reader `/stats` endpoints
4. Query Celestia directly to verify batch exists at expected height

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     Test Process                             │
├─────────────────────────────────────────────────────────────┤
│                                                              │
│  ┌──────────┐          ┌───────────┐         ┌───────────┐  │
│  │  Writer  │          │ BlobStore │         │  Reader   │  │
│  │          │──add────►│  (shared) │◄──poll──│           │  │
│  │ PUT /put │          │           │         │ GET /get  │  │
│  └──────────┘          └───────────┘         └───────────┘  │
│       │                      │                     │        │
│       │                      │                     │        │
│       ▼                      ▼                     ▼        │
│  ┌─────────────────────────────────────────────────────┐   │
│  │                      Stats                           │   │
│  │  - PUT latency                                       │   │
│  │  - DA Confirm time                                   │   │
│  │  - Total time                                        │   │
│  │  - Poll attempts                                     │   │
│  └─────────────────────────────────────────────────────┘   │
│                                                              │
└─────────────────────────────────────────────────────────────┘
```

## Graceful Shutdown

Press `Ctrl+C` or send `SIGTERM` to gracefully stop the test. The test will:

1. Stop sending new PUT requests
2. Finish current GET poll cycle
3. Print final statistics
4. Show per-blob timing details

This allows you to stop early while still getting meaningful results.
