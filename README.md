# Celestia Alt-DA Server

A Data Availability server for Optimism Alt-DA mode that uses Celestia as the DA layer.

## Architecture

This server implements an **async, database-backed architecture** designed for high throughput and crash resilience:

### Components

1. **HTTP API** - Returns immediately after storing blobs to database (sub-50ms latency)
2. **Database** - Persistent FIFO queue for blobs and batches (SQLite by default; PostgreSQL/MySQL supported)
3. **Submission Worker** - Background worker that packs and submits blobs to Celestia in batches
4. **Event Listener** - Monitors Celestia for blob confirmations and updates database
5. **S3 Backup** (optional) - Periodic database backups to S3 for disaster recovery

### How It Works

```
┌─────────────┐
│   OP Node   │
└──────┬──────┘
       │ PUT /put (blob data)
       │ GET /get/{commitment}
       ▼
┌─────────────────────────────────────────────────────────┐
│              Celestia DA Server (HTTP API)              │
│  • PUT: Store blob to DB, return commitment (<50ms)    │
│  • GET: Retrieve blob from DB by commitment             │
└────────────────┬─────────────────────────────┬──────────┘
                 │                             │
                 ▼                             ▼
    ┌────────────────────────┐    ┌──────────────────────┐
    │   Database Cache       │    │   Background Jobs    │
    │   (SQLite default)     │    │  ┌────────────────┐  │
    │  ┌──────────────────┐  │    │  │ Submission     │  │
    │  │ Blobs Table      │  │    │  │ Worker         │  │
    │  │  - pending       │◄─┼────┼──│ (batches blobs)│──┼─┐
    │  │  - batched       │  │    │  └────────────────┘  │ │
    │  │  - confirmed     │  │    │                      │ │
    │  └──────────────────┘  │    │  ┌────────────────┐  │ │
    │  ┌──────────────────┐  │    │  │ Event Listener │  │ │
    │  │ Batches Table    │  │    │  │ (confirms      │◄─┼─┘
    │  │  - pending       │  │    │  │  submissions)  │  │
    │  │  - submitted     │  │    │  └────────────────┘  │
    │  │  - confirmed     │  │    └──────────────────────┘
    │  └──────────────────┘  │                │
    └───┬────────────────────┘                │
        │                                     │
        │ Periodic backup                     │ Submit/Get
        │ (optional)                          │
        ▼                                     ▼
   ┌──────────────┐             ┌────────────────────────┐
   │  S3 Bucket   │             │   Celestia Network     │
   │ (backup only)│             │  • Batch submission    │
   └──────────────┘             │  • Blob confirmation   │
                                └────────────────────────┘
```

### Request Flow

**PUT Request (Write Path):**
1. Client sends blob data to `/put`
2. Server computes deterministic commitment
3. Blob stored in database with status `pending_submission`
4. Commitment returned immediately (typically <50ms)
5. Submission worker periodically:
   - Fetches pending blobs from database
   - Packs 10-50 blobs into a batch
   - Submits batch to Celestia
   - Marks blobs as `batched` in database
6. Event listener receives confirmation from Celestia
7. Updates blob status to `confirmed` with Celestia height

**GET Request (Read Path):**
1. Client sends commitment to `/get/{commitment}`
2. Server queries database by commitment
3. Server checks blob confirmation status:
   - If blob not found → Returns 404 "blob not found"
   - If blob exists but status is not "confirmed" → Returns 404 "blob not yet available on DA layer"
   - If blob is confirmed but missing Celestia height → Returns 404 "blob not yet available on DA layer"
   - If blob is confirmed with Celestia height → Returns 200 OK with blob data
4. For confirmed blobs, data is served directly from database cache (typically <10ms)

### Key Features

- **Fast writes**: PUT returns in <50ms (only database write)
- **Confirmed reads only**: GET returns 200 OK only when data is confirmed on Celestia DA (not just cached)
- **Fast confirmed reads**: Once confirmed, GET serves from database cache (<10ms)
- **Crash resilient**: Database with WAL mode (SQLite default) survives server restarts
- **FIFO ordering**: Auto-increment IDs ensure strict ordering
- **Efficient batching**: Packs 10-50 blobs per Celestia transaction
- **Event-driven confirmations**: Real-time updates via Celestia subscriptions
- **Disaster recovery**: Optional S3 backups of entire database

## Building

### Prerequisites

- Go 1.21 or higher
- Make (optional, for convenience)

### Quick Start

**Build the binary:**

```bash
make da-server
```

This creates `./bin/da-server` with version information embedded.

**Build optimized binary** (smaller size, ~32% reduction):

```bash
make da-server-optimized
```

**Install to GOPATH/bin:**

```bash
make install
```

**Run tests:**

```bash
make test           # Run all unit tests
make test-unit      # Run only unit tests
make test-all       # Run unit + integration tests
```

**Other useful commands:**

```bash
make build          # Build all packages (CI check)
make clean          # Remove binaries
make lint           # Run linter
make fmt            # Auto-fix linting issues
```

### Manual Build

If you prefer not to use Make:

```bash
# Standard build
go build -o bin/da-server ./cmd/daserver

# Optimized build
go build -ldflags="-s -w" -o bin/da-server ./cmd/daserver

# With version info
go build -ldflags="-X main.Version=v1.0.0" -o bin/da-server ./cmd/daserver
```

## Configuration

The server supports two connection modes for Celestia. At startup, the server **automatically detects which mode you're using** based on the flags provided and logs this information prominently.

### 🔍 Runtime Mode Detection

When the server starts, you'll see clear output showing which mode is active:

**Example for OPTION A (Self-hosted Node):**
```
INFO Initializing Async Alt-DA server...
INFO ========================================
INFO Celestia Connection Mode Detected mode="OPTION A: Self-hosted Node (RPC with auth token)"
INFO   mode value="OPTION A"
INFO   description value="Self-hosted Node"
INFO   rpc_endpoint value="http://localhost:26658"
INFO   auth_token value="<redacted>"
INFO ========================================
```

**Example for OPTION B (Service Provider):**
```
INFO Initializing Async Alt-DA server...
INFO ========================================
INFO Celestia Connection Mode Detected mode="OPTION B: Service Provider (client-tx mode with keyring + gRPC)"
INFO   mode value="OPTION B"
INFO   description value="Service Provider (client-tx)"
INFO   rpc_endpoint value="https://rpc.quicknode.pro/..."
INFO   grpc_endpoint value="grpc.quicknode.pro:9090"
INFO   keyring_path value="/root/.celestia-light-mocha-4/keys"
INFO   key_name value="my_celes_key"
INFO   p2p_network value="mocha-4"
INFO ========================================
```

**Configuration Error Detection:**

If you accidentally mix configurations from both modes, or forget required flags, the server will fail with a clear error message:

```
Error: configuration conflict: detected both --celestia.auth-token (OPTION A) and client-tx settings (OPTION B).
Please use ONLY one mode. See .env.example for guidance
```

This makes it immediately obvious which mode is running and helps catch configuration errors early.

---

### Required Flags

```bash
--celestia.server        # Celestia RPC endpoint (default: http://localhost:26658)
--celestia.namespace     # Celestia namespace (29 bytes hex)
--celestia.auth-token    # RPC auth token from celestia-node
--addr                   # Server listening address (default: 127.0.0.1)
--port                   # Server listening port (default: 3100)
```

### Database Configuration

**Default: SQLite** (PostgreSQL/MySQL also supported for HA deployments)

```bash
--db.path               # Database file path (default: ./data/blobs.db)
```

Database and tables are created automatically on first startup. No setup required.

### Batch Configuration Flags

Control how blobs are batched before submission to Celestia:

```bash
--batch.min-blobs       # Minimum blobs before creating batch (default: 10)
--batch.max-blobs       # Maximum blobs per batch (default: 50)
--batch.target-blobs    # Target number of blobs to fetch (default: 20)
--batch.max-size-mb     # Maximum batch size in MB (default: 1)
--batch.min-size-kb     # Minimum size in KB to force submission (default: 500)
```

These flags allow you to tune batching behavior as Celestia's throughput grows. The worker will create a batch when **either**:
- Blob count reaches `min-blobs` or `max-blobs`
- Total size reaches `min-size-kb`

This maximizes efficiency by packing as much data as possible into each Celestia transaction.

### Worker Configuration Flags

Control worker timeouts and retry behavior for maximum operational control:

```bash
--worker.submit-period          # How often to check for pending blobs (default: 2s)
--worker.submit-timeout         # Timeout for Celestia submit operations (default: 60s)
--worker.max-retries            # Maximum retries for failed submissions (default: 10)
--worker.reconcile-period       # How often to reconcile unconfirmed batches (default: 30s)
--worker.reconcile-age          # Age threshold for reconciliation (default: 2m)
--worker.get-timeout            # Timeout for Celestia Get during reconciliation (default: 30s)
```

**Submission Worker**: Processes pending blobs every `submit-period`, submitting batches to Celestia with `submit-timeout`. This allows operators to tune submission frequency based on expected load.

**Event Listener**: Reconciles unconfirmed batches every `reconcile-period`, checking for batches older than `reconcile-age` that haven't been confirmed. The `get-timeout` controls how long to wait for Celestia Get operations during reconciliation.

### Optional S3 Backup Flags

When enabled, the server periodically backs up the SQLite database to S3:

```bash
--backup.enabled                # Enable S3 database backups (default: false)
--backup.interval               # Backup interval (default: 1h)
--s3.bucket                     # S3 bucket name for database backups
--s3.path                       # Path prefix in S3 bucket
--s3.endpoint                   # S3 endpoint URL
--s3.credential-type            # Authentication: [iam, static]
--s3.access-key-id              # Access key (if credential-type=static)
--s3.access-key-secret          # Secret key (if credential-type=static)
```

### Logging Flags

```bash
--log.level             # Log level: DEBUG, INFO, WARN, ERROR (default: INFO)
--log.format            # Format: text, terminal, logfmt, json (default: text)
--log.color             # Enable colored output (default: false)
--log.pid               # Show process ID in logs (default: false)
```

### Metrics Flags

```bash
--metrics.enabled       # Enable Prometheus metrics (default: false)
--metrics.port          # Metrics server port (default: 6060)
```

### Advanced Celestia Client Flags

For production deployments, you can configure the experimental transaction client to use service providers like [Quicknode](https://www.quicknode.com/docs/celestia):

```bash
--celestia.tx-client.key-name                    # Keyring key name
--celestia.tx-client.keyring-path                # Keyring directory path
--celestia.tx-client.core-grpc.addr              # Core gRPC endpoint
--celestia.tx-client.core-grpc.tls-enabled       # Enable TLS (default: true)
--celestia.tx-client.core-grpc.auth-token        # Core gRPC auth token
--celestia.tx-client.p2p-network                 # Network: mocha-4, arabica-11, mainnet
--celestia.compact-blobid                        # Use compact blob IDs (default: true)
```

## Deployment Guide

This section provides production-ready deployment instructions for DevOps teams.

### Architecture Overview

There are **two deployment modes**:

1. **Write Server** - Accepts PUT requests and submits blobs to Celestia (deployed with op-batcher)
   - Can also serve GET requests from its own database
   - Used when you need to write AND read (e.g., single op-batcher + op-node setup)

2. **Read-Only Server** - Only serves GET requests by indexing blobs from Celestia (deployed with op-node)
   - Rejects PUT requests
   - Used for dedicated read replicas or when you only need to read (e.g., separate op-node instances)

### Deployment Topology

**Option 1: Single server (simplest)**

```
┌───────────────────────────────────────────┐
│  op-batcher ──PUT──┐                      │
│                    ▼                      │
│              ┌──────────┐                 │
│              │  Write   │                 │
│              │  Server  │                 │
│              └──────────┘                 │
│                    ▲                      │
│  op-node ────GET───┘                      │
└───────────────────────────────────────────┘
                     │
                     │ Submit blobs (CIP-21 signed)
                     ▼
            ┌─────────────────┐
            │ Celestia Network│
            └─────────────────┘
```

Write server handles both PUT (from op-batcher) and GET (from op-node).

---

**Option 2: Separate read replicas (high availability)**

```
┌──────────────────────────────────────────────────────────┐
│  op-batcher ──PUT──► Write Server                        │
│                           │                              │
│                           │ Submit blobs (CIP-21 signed) │
│                           ▼                              │
│                   ┌─────────────────┐                    │
│                   │ Celestia Network│                    │
│                   └─────────────────┘                    │
│                           │                              │
│                           │ Backfill (verify CIP-21)     │
│                           ▼                              │
│  op-node 1 ──GET──► Read-Only Server 1                  │
│  op-node 2 ──GET──► Read-Only Server 2                  │
└──────────────────────────────────────────────────────────┘
```

Write server submits blobs, read-only servers index them via backfill.

**Key points:**
- Write servers can serve both PUT and GET requests
- Read-only servers reject PUT, only serve GET via backfill worker
- Read-only servers **MUST** configure `--trusted-signer` with write server's signer address for security
- Without trusted signers, malicious actors can inject junk data into your namespace

---

## Running the Server

### Important: Choose Your Deployment Mode

You have **TWO options** for connecting to Celestia. Choose ONE:

**OPTION B: Service Provider** (RECOMMENDED)
- Use a service provider like Quicknode
- No local node infrastructure required
- Uses RPC endpoint for reads (Get, GetAll, Subscribe)
- Uses gRPC endpoint for writes (Submit via client-tx)
- Requires keyring for transaction signing
- Easiest setup for production deployments

**OPTION A: Local Celestia Node**
- Run your own celestia-node (light or full node) locally
- Uses RPC endpoint for both reads and writes
- Requires running local infrastructure

**⚠️ Important**: According to [celestia-node client-tx documentation](https://github.com/celestiaorg/celestia-node/blob/52cd8b52ec031bb9e3b4e476e0b159db2053384c/api/client/readme.md), when using client-tx (Option B) you need **BOTH** API endpoints:
- RPC endpoint for reads (blob.Get, blob.GetAll, Subscribe)
- gRPC endpoint for writes (submitting transactions)

---

### 1. Generate a Celestia Namespace

The namespace must be a **29-byte hex string** (58 characters). For Celestia version 0, it requires 18 leading zeros (36 hex chars) + 10 random bytes (20 hex chars).

**Generate and set as environment variable:**

```bash
# Generate the namespace (36 zeros + 20 random hex chars)
export OP_ALTDA_CELESTIA_NAMESPACE=00000000000000000000000000000000000000$(openssl rand -hex 10)

# Verify it looks correct (should be 58 characters)
echo $OP_ALTDA_CELESTIA_NAMESPACE
```

**Example output:**
```
00000000000000000000000000000000000000a1b2c3d4e5f6a7b8c9d0
```

**Important notes:**
- ✅ Use `OP_ALTDA_CELESTIA_NAMESPACE` (not just `NAMESPACE`)
- ✅ No `0x` prefix - just the raw hex string
- ✅ Must be exactly 58 characters (29 bytes × 2)
- ✅ Validated automatically on server startup

---

### 2. Setup for Your Chosen Mode

#### OPTION B: Service Provider Setup (RECOMMENDED)

**Step 1**: Get RPC and gRPC endpoints from your provider (e.g., Quicknode)

You'll receive:
- RPC endpoint (for reads): `https://your-endpoint.celestia-mocha.quiknode.pro/your-token`
- gRPC endpoint (for writes): `your-endpoint.celestia-mocha.quiknode.pro:9090`

**Step 2**: Initialize keyring and create a key

```bash
# Initialize keyring
celestia light init --p2p.network mocha-4

# Create or import a key (this key will sign transactions)
celestia-appd keys add my_key --keyring-backend test --home ~/.celestia-light-mocha-4

# Or import existing key
celestia-appd keys import my_key <key-file> --keyring-backend test --home ~/.celestia-light-mocha-4
```

**Step 3**: Fund your key with TIA for gas fees

#### OPTION A: Local Celestia Node Setup

**Step 1**: Start your celestia-node (light or full)

```bash
# Example: Start light node on Mocha testnet
celestia light start --core.ip consensus.lunaroasis.net --p2p.network mocha-4
```

**Step 2**: Get the auth token (auto-generated)

```bash
# Light node
cat ~/.celestia-light/keys/keyring-test/auth-token

# Full node
cat ~/.celestia-full/keys/keyring-test/auth-token
```

**Step 3**: Ensure your node has sufficient balance (TIA) for gas fees

---

### 3. Start the Server

#### OPTION B: Start with Service Provider (Quicknode) - RECOMMENDED

**Basic setup:**

```bash
./celestia-da-server \
  --celestia.server https://your-endpoint.celestia-mocha.quiknode.pro/your-token \
  --celestia.namespace $OP_ALTDA_CELESTIA_NAMESPACE \
  --celestia.tx-client.core-grpc.addr your-endpoint.celestia-mocha.quiknode.pro:9090 \
  --celestia.tx-client.p2p-network mocha-4 \
  --celestia.tx-client.keyring-path ~/.celestia-light-mocha-4/keys \
  --celestia.tx-client.key-name my_key \
  --db.path ./blobs.db \
  --port 3100
```

**Note**: The `--celestia.server` (RPC) is used for reads and `--celestia.tx-client.core-grpc.addr` (gRPC) is used for writes.

**Production setup (Quicknode + S3 backups):**

```bash
./celestia-da-server \
  --celestia.server https://your-endpoint.celestia-mocha.quiknode.pro/your-token \
  --celestia.namespace $OP_ALTDA_CELESTIA_NAMESPACE \
  --celestia.tx-client.core-grpc.addr your-endpoint.celestia-mocha.quiknode.pro:9090 \
  --celestia.tx-client.p2p-network mocha-4 \
  --celestia.tx-client.keyring-path ~/.celestia-light-mocha-4/keys \
  --celestia.tx-client.key-name my_key \
  --addr 0.0.0.0 \
  --port 3100 \
  --db.path /var/lib/celestia-da/blobs.db \
  --backup.enabled \
  --backup.interval 6h \
  --s3.bucket my-da-backups \
  --metrics.enabled \
  --log.level INFO \
  --log.format json
```

#### OPTION A: Start with Local Celestia Node

**Basic setup:**

```bash
./celestia-da-server \
  --celestia.server http://localhost:26658 \
  --celestia.namespace $OP_ALTDA_CELESTIA_NAMESPACE \
  --celestia.auth-token $(cat ~/.celestia-light/keys/keyring-test/auth-token) \
  --addr 127.0.0.1 \
  --port 3100 \
  --db.path ./data/blobs.db
```

**Production setup (local node + S3 backups):**

```bash
./celestia-da-server \
  --celestia.server http://localhost:26658 \
  --celestia.namespace $OP_ALTDA_CELESTIA_NAMESPACE \
  --celestia.auth-token $(cat ~/.celestia-light/keys/keyring-test/auth-token) \
  --addr 0.0.0.0 \
  --port 3100 \
  --db.path /var/lib/celestia-da/blobs.db \
  --backup.enabled \
  --backup.interval 6h \
  --s3.bucket my-da-backups \
  --s3.path celestia-da/backups \
  --s3.credential-type iam \
  --metrics.enabled \
  --metrics.port 6060 \
  --log.level INFO \
  --log.format json
```

---

## Production Deployment Guide

### Write Server (op-batcher sidecar)

Write servers accept PUT requests, submit blobs to Celestia with CIP-21 signatures, and serve GET requests from their database.

**Use case**: Single server for both op-batcher (writes) and op-node (reads), or dedicated write server in HA setup.

**Setup:**

```bash
# 1. Create keyring and fund account
celestia light init --p2p.network mocha-4
celestia-appd keys add my_write_key --keyring-backend test --home ~/.celestia-light-mocha-4
# Fund the address with TIA

# 2. Start write server (OPTION B: Service Provider)
./bin/da-server \
  --celestia.server https://your-endpoint.celestia-mocha.quiknode.pro/your-token \
  --celestia.namespace $OP_ALTDA_CELESTIA_NAMESPACE \
  --celestia.tx-client.core-grpc.addr your-endpoint.celestia-mocha.quiknode.pro:9090 \
  --celestia.tx-client.p2p-network mocha-4 \
  --celestia.tx-client.keyring-path ~/.celestia-light-mocha-4/keys \
  --celestia.tx-client.key-name my_write_key \
  --addr 0.0.0.0 \
  --port 3100 \
  --db.path /var/lib/celestia-da/blobs.db \
  --metrics.enabled \
  --log.level INFO

# 3. Test
curl -X POST http://localhost:3100/put \
  -H "Content-Type: application/octet-stream" \
  --data-binary "test data"
```

**Get signer address for read-only servers:**

```bash
# Method 1: From keyring (bech32 address, needs conversion to hex)
celestia-appd keys show my_write_key --keyring-backend test --home ~/.celestia-light-mocha-4

# Method 2: From logs after first submission (already in hex format)
# Start read-only server temporarily without --trusted-signer, then check logs:
grep "blob_signer" /var/log/celestia-da-readonly.log
# Output: blob_signer="0123456789abcdef0123456789abcdef01234567"
```

---

### Read-Only Server (op-node sidecar)

Read-only servers serve GET requests by scanning Celestia and indexing blobs. They reject PUT requests.

**Use case**: Dedicated read replicas for op-node instances, or when you only need to read (no writes).

**⚠️ SECURITY**: MUST configure `--trusted-signer` with write server's signer address(es) or malicious actors can inject junk data.

**Setup:**

```bash
# 1. Get signer address from write server (see above)
export TRUSTED_SIGNER=0123456789abcdef0123456789abcdef01234567  # hex format, 40 chars

# 2. Start read-only server
./bin/da-server \
  --celestia.server https://your-endpoint.celestia-mocha.quiknode.pro/your-token \
  --celestia.namespace $OP_ALTDA_CELESTIA_NAMESPACE \
  --read-only \
  --backfill.enabled=true \
  --backfill.start-height=0 \
  --backfill.period=15s \
  --trusted-signer=$TRUSTED_SIGNER \
  --addr 0.0.0.0 \
  --port 3100 \
  --db.path /var/lib/celestia-da-readonly/blobs.db \
  --metrics.enabled \
  --log.level INFO

# 3. Monitor backfill progress
grep "Backfill" /var/log/celestia-da-readonly.log
# Expected: "Successfully indexed batch batch_id=X blob_count=Y height=Z"

# 4. Test GET (after blobs are indexed)
curl http://localhost:3100/get/0x0c<commitment_from_write_server>
```

**Key flags:**
- `--read-only` - Disables PUT endpoint and submission worker
- `--backfill.enabled=true` - Enables scanning Celestia blocks
- `--backfill.start-height=0` - Start from genesis (or set to current height to skip history)
- `--trusted-signer` - **REQUIRED** - Comma-separated hex addresses (40 chars each)

**Multiple write servers (HA):**

```bash
--trusted-signer=0123456789abcdef0123456789abcdef01234567,789abcdef0123456789abcdef0123456789abcde
```

---

### Monitoring

**Check backfill sync progress:**

```bash
sqlite3 /var/lib/celestia-da-readonly/blobs.db \
  "SELECT last_height FROM sync_state WHERE worker_name='backfill_worker'"
```

**Check indexed blob count:**

```bash
sqlite3 /var/lib/celestia-da-readonly/blobs.db \
  "SELECT COUNT(*) FROM blobs WHERE status='confirmed'"
```

**Prometheus metrics** (if `--metrics.enabled`):
- Write: `celestia_submissions_total`, `celestia_submission_errors_total`
- Read: `celestia_retrievals_total`, indexed blob count

---

### Troubleshooting

**Read-only rejecting blobs:**

```bash
grep "untrusted signer" /var/log/celestia-da-readonly.log
grep "blob_signer" /var/log/celestia-da-readonly.log
# Fix: Update --trusted-signer with correct hex address
```

**Write server not submitting:**

```bash
grep "submission_worker" /var/log/celestia-da-write.log
# Check keyring: ls -la ~/.celestia-light-mocha-4/keys
# Check balance: celestia-appd query bank balances <address>
```

---

### Graceful Shutdown

Server handles SIGTERM gracefully:

```bash
kill -TERM <pid>  # or: systemctl stop celestia-da-server
# Workers finish current operations, backfill progress is persisted
```

## API

The server exposes HTTP endpoints for storing and retrieving blobs. See [API.md](API.md) for complete API documentation.

## Testing

### Run All Tests

```bash
go test ./... -v
```

### Run Specific Test Suites

```bash
# Database tests
go test ./db -v

# Worker tests (submission + event listener)
go test ./worker -v

# Batch packing tests
go test ./batch -v

# Commitment tests
go test ./commitment -v

# HTTP handler tests
go test . -v -run TestCelestiaServer

# Concurrency stress tests
go test . -v -run TestConcurrent
```

### Test Coverage

```bash
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

### Integration Testing with Kurtosis Devnet

To test against a simulated Celestia network:

1. Configure `kurtosis-devnet/simple.yaml`:

```yaml
da_params:
  image: opcelestia/localestia-da-server:latest

optimism_package:
  altda_deploy_config:
    use_altda: false
    da_commitment_type: GenericCommitment
    da_challenge_window: 100
    da_resolve_window: 100
    da_bond_size: 0
    da_resolver_refund_percentage: 0
```

2. Start the devnet:

```bash
cd kurtosis-devnet
just simple-devnet
```

The devnet uses [localestia](https://github.com/celestiaorg/localestia) to simulate a Celestia network.

## Monitoring

### Prometheus Metrics

#### Enabling Metrics

The server provides Prometheus metrics for monitoring Celestia DA operations. Metrics are **disabled by default**.

**To enable metrics:**

```bash
# Option 1: Command-line flags (CORRECT syntax - no equals sign!)
./bin/da-server \
  --metrics.enabled \
  --metrics.port 6060 \
  ... other flags ...

# ❌ WRONG - Don't use equals sign for boolean flags
# --metrics.enabled=true  (This WON'T work!)

# ✅ CORRECT - Just the flag name
# --metrics.enabled

# Option 2: Environment variables
export OP_ALTDA_METRICS_ENABLED=true
export OP_ALTDA_METRICS_PORT=6060
./bin/da-server ...

# Option 3: In .env file
OP_ALTDA_METRICS_ENABLED=true
OP_ALTDA_METRICS_PORT=6060
```

**Verify metrics are enabled:**

When you start the server with metrics enabled, you should see:

```
INFO ========================================
INFO Prometheus Metrics: ENABLED port=6060
INFO ========================================
...
INFO ========================================
INFO Metrics server starting endpoint="http://127.0.0.1:6060/metrics"
INFO Access metrics at: url="http://127.0.0.1:6060/metrics"
INFO ========================================
```

**If metrics are disabled, you'll see:**
```
INFO ========================================
INFO Prometheus Metrics: DISABLED note="Set --metrics.enabled or OP_ALTDA_METRICS_ENABLED=true to enable"
INFO ========================================
```

**Test metrics endpoint:**

```bash
curl http://localhost:6060/metrics
```

**Troubleshooting:**

If you don't see metrics logs or can't access the endpoint:
1. ✅ **Check flag syntax**: Use `--metrics.enabled` NOT `--metrics.enabled=true` (no equals sign!)
2. ✅ Check you're using `--metrics.enabled` (not `--metrics.enable`)
3. ✅ Check environment variable is `OP_ALTDA_METRICS_ENABLED=true` (not `"true"` in quotes for shell)
4. ✅ Verify port 6060 isn't already in use: `lsof -i :6060`
5. ✅ Check logs for "Prometheus Metrics: ENABLED" message
6. ✅ Ensure you're connecting to the right host (metrics binds to same host as main server)

#### Celestia DA Layer Metrics

These metrics track performance of Celestia DA operations from the worker's perspective:

**Submission Metrics** (worker → Celestia):
- `celestia_submission_duration_seconds` - Histogram of time taken to submit batches to Celestia
- `celestia_submission_size_bytes` - Histogram of batch sizes submitted (in bytes)
- `celestia_submissions_total` - Counter of total successful batch submissions
- `celestia_submission_errors_total` - Counter of failed batch submissions

**Retrieval Metrics** (worker ← Celestia):
- `celestia_retrieval_duration_seconds` - Histogram of time taken to retrieve blobs from Celestia
- `celestia_retrieval_size_bytes` - Histogram of blob sizes retrieved (in bytes)
- `celestia_retrievals_total` - Counter of total successful blob retrievals
- `celestia_retrieval_errors_total` - Counter of failed blob retrievals

**Example Queries**:

```promql
# Average submission time over last 5 minutes
rate(celestia_submission_duration_seconds_sum[5m]) / rate(celestia_submission_duration_seconds_count[5m])

# Total data submitted to Celestia (MB)
sum(celestia_submission_size_bytes) / 1024 / 1024

# Submission error rate
rate(celestia_submission_errors_total[5m]) / rate(celestia_submissions_total[5m])

# 95th percentile retrieval latency
histogram_quantile(0.95, rate(celestia_retrieval_duration_seconds_bucket[5m]))
```

### Database Inspection

For SQLite (default), you can inspect the database directly with sqlite3:

```bash
sqlite3 blobs.db

# Check blob counts by status
SELECT status, COUNT(*) FROM blobs GROUP BY status;

# Check recent batches
SELECT batch_id, status, submitted_at, confirmed_at
FROM batches
ORDER BY batch_id DESC
LIMIT 10;

# Check specific blob
SELECT blob_id, status, celestia_height, created_at
FROM blobs
WHERE commitment = x'<commitment_hex>';
```

## Troubleshooting

### Server won't start

**Error**: `failed to connect to celestia-node`
- Verify celestia-node is running: `curl http://localhost:26658`
- Check auth token is correct
- Ensure namespace is valid (29 bytes hex, version 0 requires 18 leading zeros)

**Error**: `failed to open database`
- Check `--db.path` directory exists and is writable
- Verify no other process is using the database file

### Blobs not confirming

**Check submission worker status:**
```bash
# Look for submission worker logs
grep "submission_worker" /var/log/celestia-da.log

# Check if batches are being created
sqlite3 blobs.db "SELECT COUNT(*) FROM batches WHERE status='submitted';"
```

**Check event listener status:**
```bash
# Look for event listener logs
grep "event_listener" /var/log/celestia-da.log

# Check if confirmations are being received
sqlite3 blobs.db "SELECT COUNT(*) FROM blobs WHERE status='confirmed';"
```

### High latency

- Check if database file is on fast storage (SSD recommended)
- Monitor batch sizes (should be 10-50 blobs per batch)
- Verify celestia-node has sufficient balance for gas fees
- Check network connectivity to Celestia network

## Performance Characteristics

- **PUT latency**: <50ms (database write only)
- **GET latency (confirmed blobs)**: <10ms (database read from cache)
- **GET latency (unconfirmed blobs)**: Returns 404 until confirmed on DA layer
- **Time to availability**: 15-90 seconds (time from PUT until GET returns 200 OK)
- **Batch size**: 10-50 blobs per Celestia transaction
- **Confirmation time**: 15-60 seconds (depends on Celestia block time)
- **Throughput**: 1000+ blobs/sec (limited by database, not Celestia)
- **Storage**: ~1KB overhead per blob (database metadata)

## License

See [LICENSE](LICENSE) file for details.

## Resources

- [API Documentation](API.md) - HTTP API reference
- [Optimism Alt-DA Mode Documentation](https://docs.optimism.io/operators/chain-operators/features/alt-da-mode)
- [Celestia Node Documentation](https://docs.celestia.org)
- [Celestia Go Client](https://docs.celestia.org/how-to-guides/client/go)
- [Quicknode Celestia Documentation](https://www.quicknode.com/docs/celestia)
