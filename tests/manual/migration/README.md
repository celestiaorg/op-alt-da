# S3 Migration Compatibility Test

This test verifies that blobs written to S3 fallback storage by op-alt-da v0.9.0 can be read by v0.10.0, ensuring backwards compatibility.

## Overview

The test runs in two distinct phases with a **mandatory server switch** between them:

```
╔═══════════════════════════════════════════════════════════════════════════════╗
║  PHASE 1: WRITE                        PHASE 2: READ                          ║
║  ─────────────────                     ──────────────                         ║
║  Server: v0.9.0                        Server: v0.10.0 (current branch)       ║
║  Action: PUT blobs                     Action: GET blobs                      ║
║  S3: write-through                     S3: read-fallback (forced)             ║
╠═══════════════════════════════════════════════════════════════════════════════╣
║                                                                               ║
║  ┌─────────────┐      ┌─────────────┐      ┌─────────────┐                   ║
║  │   v0.9.0    │─────▶│     S3      │◀─────│  v0.10.0    │                   ║
║  │   Server    │      │  (shared)   │      │   Server    │                   ║
║  └─────────────┘      └─────────────┘      └─────────────┘                   ║
║        │                    │                    │                            ║
║    PHASE 1              prefix:              PHASE 2                          ║
║   (write)             migration/              (read)                          ║
║                                                                               ║
╚═══════════════════════════════════════════════════════════════════════════════╝
```

---

## Prerequisites

### 1. Build Both Server Versions

```bash
# Build v0.9.0
git checkout v0.9.0
make build
mv bin/da-server bin/da-server-v0.9.0

# Build v0.10.0 (current branch)
git checkout <your-branch>   # e.g., main, develop, or your feature branch
make build
mv bin/da-server bin/da-server-v0.10.0
```

### 2. Build the Migration Test

```bash
cd tests/manual/migration
go build -o migration-test .
```

### 3. S3 Bucket Ready

Ensure your S3 bucket is configured with valid credentials.

---

## Step-by-Step Instructions

### ══════════════════════════════════════════════════════════════

### PHASE 1: WRITE BLOBS WITH v0.9.0

### ══════════════════════════════════════════════════════════════

**Server Version:** `v0.9.0`
**Purpose:** Submit blobs to Celestia with S3 write-through enabled

#### 1.1 Start v0.9.0 Server

v0.9.0 uses CLI flags (no config.toml support). Note the different flag names:

```bash
./bin/da-server-v0.9.0 \
  --addr 127.0.0.1 \
  --port 3100 \
  --celestia.namespace 0000000000000000000000000000000000000041ca0e22713b4d1c5912 \
  --celestia.server https://your-celestia-node \
  --celestia.tls-enabled=false \
  --routing.fallback \
  --s3.credential-type static \
  --s3.bucket your-bucket \
  --s3.path migration/ \
  --s3.endpoint s3.eu-west-1.amazonaws.com \
  --s3.access-key-id AKIA... \
  --s3.access-key-secret "..."
```

**Note:** v0.9.0 uses different flag names than v0.10.0:

- `--routing.fallback` instead of `--fallback.enabled`
- `--s3.path` instead of `--fallback.s3.prefix`
- `--s3.bucket` instead of `--fallback.s3.bucket`
- `--s3.endpoint` is required (no default)

#### 1.2 Run Write Phase

In a **separate terminal**:

```bash
cd tests/manual/migration
./migration-test -phase write -blobs 5 -size 131072
```

This will:

- Submit 5 blobs (~128KB each) to the v0.9.0 server
- Save commitments and data hashes to `commitments.json`

#### 1.3 Stop v0.9.0 Server

Press `Ctrl+C` in the server terminal to stop v0.9.0.

```
⚠️  v0.9.0 SERVER MUST BE STOPPED BEFORE PROCEEDING TO PHASE 2
```

---

### ══════════════════════════════════════════════════════════════

### SWITCH SERVERS: v0.9.0 → v0.10.0

### ══════════════════════════════════════════════════════════════

Before Phase 2, you must:

1. ✅ Stop the v0.9.0 server (Ctrl+C)
2. ✅ Start the v0.10.0 server with **fake Celestia endpoint**

The fake endpoint forces v0.10.0 to read from S3 fallback instead of Celestia.

---

### ══════════════════════════════════════════════════════════════

### PHASE 2: READ BLOBS WITH v0.10.0

### ══════════════════════════════════════════════════════════════

**Server Version:** `v0.10.0` (current branch)
**Purpose:** Read blobs from S3 fallback, verify data integrity

#### 2.1 Create config-migration.toml

```toml
addr = "127.0.0.1"
port = 3100

[celestia]
namespace = "0000000000000000000000000000000000000041ca0e22713b4d1c5912"
blobid_compact = true

# FAKE endpoint - forces S3 fallback on reads!
bridge_addr = "http://localhost:99999"
bridge_tls_enabled = false

[fallback]
enabled = true
provider = "s3"
mode = "both"

[fallback.s3]
bucket = "your-bucket"
region = "eu-west-1"
prefix = "migration/"          # MUST match v0.9.0 prefix!
credential_type = "static"
access_key_id = "AKIA..."
access_key_secret = "..."
timeout = "30s"
```

#### 2.2 Start v0.10.0 Server

```bash
./bin/da-server-v0.10.0 --config config-migration.toml
```

#### 2.3 Run Read Phase

In a **separate terminal**:

```bash
cd tests/manual/migration
./migration-test -phase read
```

This will:

- Load commitments from `commitments.json`
- GET each blob from v0.10.0 server (which reads from S3)
- Verify data integrity using SHA256 hashes
- Report compatibility results

---

## Quick Reference

| Phase        | Server Version | Config                | Purpose                    |
| ------------ | -------------- | --------------------- | -------------------------- |
| **1. Write** | v0.9.0         | CLI flags             | Submit blobs, populate S3  |
| _Switch_     | —              | —                     | Stop v0.9.0, start v0.10.0 |
| **2. Read**  | v0.10.0        | config-migration.toml | Read from S3, verify       |

---

## CLI Flags

| Flag           | Default                 | Description                                |
| -------------- | ----------------------- | ------------------------------------------ |
| `-url`         | `http://localhost:3100` | DA server URL                              |
| `-phase`       | `write`                 | Phase: `write`, `read`, `continue`, `both` |
| `-blobs`       | `5`                     | Number of blobs to submit                  |
| `-size`        | `131072` (128KB)        | Blob size in bytes                         |
| `-commitments` | `commitments.json`      | Path to commitments file                   |
| `-timeout`     | `120`                   | HTTP timeout in seconds                    |
| `-v`           | `false`                 | Verbose output                             |

---

## Phases Explained

### `write`

Submits blobs to the server and saves commitments + data hashes to file.
**Run with:** v0.9.0 server

```bash
./migration-test -phase write -blobs 10
```

### `read`

Reads blobs using commitments from file and verifies data integrity.
**Run with:** v0.10.0 server (fake Celestia endpoint)

```bash
./migration-test -phase read
```

### `continue`

Adds new blobs using v0.10.0 and appends to the commitments file.
Useful for testing that new writes are also compatible.
**Run with:** v0.10.0 server

```bash
./migration-test -phase continue -blobs 5
```

### `both`

Runs `write` then pauses for you to switch servers, then runs `read`.
Interactive mode - prompts you when to switch.

```bash
./migration-test -phase both -blobs 5
```

---

## Expected Output

### Successful Write Phase (v0.9.0)

```
════════════════════════════════════════════════════════════════
           PHASE 1: WRITE (Submit blobs to v0.9.0)
════════════════════════════════════════════════════════════════
Server:      http://localhost:3100
Blob count:  5
Blob size:   131072 bytes (~128 KB)
Output file: commitments.json
────────────────────────────────────────────────────────────────

[1/5] Submitting blob (131072 bytes)... ✅ OK in 12.5s
[2/5] Submitting blob (131072 bytes)... ✅ OK in 11.8s
[3/5] Submitting blob (131072 bytes)... ✅ OK in 13.2s
[4/5] Submitting blob (131072 bytes)... ✅ OK in 12.1s
[5/5] Submitting blob (131072 bytes)... ✅ OK in 11.9s

────────────────────────────────────────────────────────────────
✅ WRITE PHASE COMPLETE
   Success: 5
   Failed:  0
   Saved:   commitments.json
════════════════════════════════════════════════════════════════

Next steps:
  1. Stop the v0.9.0 server
  2. Start the v0.10.0 server with fake Celestia endpoint
  3. Run: ./migration-test -phase read
```

### Successful Read Phase (v0.10.0)

```
════════════════════════════════════════════════════════════════
           PHASE 2: READ (Verify blobs from S3 fallback)
════════════════════════════════════════════════════════════════
Server:      http://localhost:3100
Input file:  commitments.json
────────────────────────────────────────────────────────────────

Loaded 5 commitments from commitments.json
Written by version: 0.9.0 at 2024-12-10T15:30:00Z
S3 prefix: migration/

[1/5] Reading blob (commitment: 00abc123def456...)... ✅ OK in 245ms (verified)
[2/5] Reading blob (commitment: 01def789abc012...)... ✅ OK in 198ms (verified)
[3/5] Reading blob (commitment: 02abc456def789...)... ✅ OK in 210ms (verified)
[4/5] Reading blob (commitment: 03def012abc345...)... ✅ OK in 185ms (verified)
[5/5] Reading blob (commitment: 04abc789def012...)... ✅ OK in 225ms (verified)

────────────────────────────────────────────────────────────────
📥 READ PHASE RESULTS
   Success (verified): 5
   Failed:             0
   Hash mismatch:      0
────────────────────────────────────────────────────────────────
✅ MIGRATION COMPATIBILITY: PASSED
   All blobs written by v0.9.0 are readable by v0.10.0
════════════════════════════════════════════════════════════════
```

---

## Commitments File Format

The test saves/loads commitments in JSON format:

```json
{
  "version": "0.9.0",
  "created_at": "2024-12-10T15:30:00Z",
  "s3_prefix": "migration/",
  "blobs": [
    {
      "commitment": "00abc123def456...",
      "data_hash": "sha256:e3b0c44298fc1c...",
      "size": 131072
    }
  ]
}
```

---

## S3 Key Format and Migration

v0.9.0 and v0.10.0 use different S3 key formats. v0.10.0 includes automatic backwards compatibility, but you can permanently migrate keys using the `s3migrate` tool.

**See [doc/MIGRATION.md](../../../doc/MIGRATION.md#s3-key-format-change) for full details on:**

- S3 key format differences
- Automatic legacy key lookup
- The `s3migrate` migration tool
- Improved fallback behavior

---

## Troubleshooting

### "Failed to connect" errors in read phase

Make sure:

- v0.10.0 server is running (not v0.9.0!)
- Server is on the same port (default: 3100)
- The fake Celestia endpoint is unreachable (so it falls back to S3)

### "Hash mismatch" errors

This indicates the blob data doesn't match. Check:

- S3 prefix is the same in both configs (`migration/`)
- The blob wasn't corrupted during storage

### "Fallback: blob not found" errors

If v0.10.0 can't find blobs written by v0.9.0:

1. Verify the S3 prefix matches exactly
2. Check that v0.9.0 actually wrote to S3: `aws s3 ls s3://bucket/prefix/`
3. The legacy key lookup should handle the format difference automatically
4. See [doc/MIGRATION.md](../../../doc/MIGRATION.md#s3-key-format-change) for details

### Read phase times out

The fake Celestia endpoint might be responding. Use a definitely-invalid endpoint like `http://localhost:99999`.

### Wrong server version running

Double-check which binary you're running:

```bash
# Check running process
ps aux | grep da-server

# Should show da-server-v0.10.0 for Phase 2
```
