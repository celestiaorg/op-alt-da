# Manual Testing

Quick setup for testing the Celestia Alt-DA server locally against Mocha-4 testnet.

## Quick Start

```bash
# Terminal 1: Start writer (port 3100)
./bin/da-server --config tests/manual/config-writer.toml

# Terminal 2: Start reader (port 3101)
./bin/da-server --config tests/manual/config-reader.toml

# Terminal 3: Run test
cd tests/manual
go run test-writer-reader.go -duration 60s
```

## What's Included

- **`config-writer.toml`** - Writer config (400KB/s, 1MB blocks, 8s max wait)
- **`config-reader.toml`** - Reader config (1s scan period, 5 blocks/scan)
- **`test-writer-reader.go`** - Integration test with configurable duration/interval/size
- **`*.db`** - SQLite databases (auto-created, gitignored)

## Configuration

Current settings target **400KB/s throughput**:

**Writer** - Batches 2-3 execution blocks per Celestia submission:
```toml
[batch]
min_blobs = 2
max_size_kb = 1843  # 1.8MB (under 2MB Celestia limit)

[worker]
max_blob_wait_time = "8s"  # Force submission after 8s
reconcile_period = "3s"
```

**Reader** - Scans Celestia every 1s:
```toml
[worker]
reconcile_period = "1s"

[backfill]
blocks_per_scan = 5  # 300 blocks/min capacity (30x safety margin)
```

## Expected Performance

| Metric | Value |
|--------|-------|
| **Latency** | 30-50s (batching + Celestia + scan) |
| **Throughput** | 400 KB/s sustained |
| **Batches/min** | 7-10 |

## Tuning

Adjust these based on your needs:

**Lower latency** (faster availability):
```toml
# config-writer.toml
max_blob_wait_time = "3s"  # Submit more frequently
```

**Higher throughput** (more data):
```toml
# config-writer.toml
max_size_kb = 1843  # Already at Celestia limit
min_blobs = 1       # Batch more aggressively
```

**Faster reader indexing**:
```toml
# config-reader.toml
reconcile_period = "500ms"  # Scan twice per second
blocks_per_scan = 10        # Catch up faster
```

## Health & Stats

```bash
curl http://localhost:3100/health  # Writer
curl http://localhost:3101/health  # Reader
curl http://localhost:3100/stats   # Detailed stats
```

## Troubleshooting

**Reader not indexing?**
- Check namespace matches writer: `grep namespace tests/manual/config-*.toml`
- Verify trusted signer: Get from writer logs, add to reader config

**High latency?**
- Reduce `max_blob_wait_time` in writer config
- Check Celestia network status

**404 on GET?**
- Wait 30-40s for Celestia confirmation
- Check writer logs for batch submission

## Cleanup

```bash
rm tests/manual/*.db*
```
