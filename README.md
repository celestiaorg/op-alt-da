# Celestia Alt-DA Server

A Data Availability server for Optimism Alt-DA mode that uses Celestia as the DA layer.

## Architecture

This server implements an **async, database-backed architecture** designed for high throughput and crash resilience:

### Components

1. **HTTP API** - Returns immediately after storing blobs to database (sub-50ms latency)
2. **SQLite Database** - Persistent FIFO queue for blobs and batches (WAL mode for crash safety)
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
    │   SQLite Database      │    │   Background Jobs    │
    │  ┌──────────────────┐  │    │  ┌────────────────┐  │
    │  │ Blobs Table      │  │    │  │ Submission     │  │
    │  │  - pending       │◄─┼────┼──│ Worker         │  │
    │  │  - batched       │  │    │  │ (batches blobs)│  │
    │  │  - confirmed     │  │    │  └────────┬───────┘  │
    │  └──────────────────┘  │    │           │          │
    │  ┌──────────────────┐  │    │  ┌────────▼───────┐  │
    │  │ Batches Table    │  │    │  │ Event Listener │  │
    │  │  - pending       │  │    │  │ (confirms      │  │
    │  │  - submitted     │  │    │  │  submissions)  │  │
    │  │  - confirmed     │  │    │  └────────────────┘  │
    │  └──────────────────┘  │    └──────────────────────┘
    └────────────┬───────────┘
                 │
                 │ Periodic backup
                 ▼
        ┌────────────────┐
        │  S3 Bucket     │
        │  (backup only) │
        └────────────────┘
                 ▲
                 │
                 ▼
    ┌────────────────────────┐
    │   Celestia Network     │
    │  • Batch submission    │
    │  • Event subscription  │
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
3. Returns blob data immediately (database read)

### Key Features

- **Fast writes**: PUT returns in <50ms (only database write)
- **Crash resilient**: SQLite with WAL mode survives server restarts
- **FIFO ordering**: Auto-increment IDs ensure strict ordering
- **Efficient batching**: Packs 10-50 blobs per Celestia transaction
- **Event-driven confirmations**: Real-time updates via Celestia subscriptions
- **Disaster recovery**: Optional S3 backups of entire database

## Configuration

### Required Flags

```bash
--celestia.server        # Celestia RPC endpoint (default: http://localhost:26658)
--celestia.namespace     # Celestia namespace (29 bytes hex)
--celestia.auth-token    # RPC auth token from celestia-node
--addr                   # Server listening address (default: 127.0.0.1)
--port                   # Server listening port (default: 3100)
```

### Database Flags

```bash
--db.path               # SQLite database file path (default: ./blobs.db)
```

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
- RPC endpoint for reads (blob.Get, blob.GetAll, blob.Subscribe)
- gRPC endpoint for writes (submitting transactions)

---

### 1. Generate a Celestia Namespace

```bash
export NAMESPACE=00000000000000000000000000000000000000$(openssl rand -hex 10)
echo $NAMESPACE
```

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
  --celestia.namespace $NAMESPACE \
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
  --celestia.namespace $NAMESPACE \
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
  --celestia.namespace $NAMESPACE \
  --celestia.auth-token $(cat ~/.celestia-light/keys/keyring-test/auth-token) \
  --addr 127.0.0.1 \
  --port 3100 \
  --db.path ./data/blobs.db
```

**Production setup (local node + S3 backups):**

```bash
./celestia-da-server \
  --celestia.server http://localhost:26658 \
  --celestia.namespace $NAMESPACE \
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

When `--metrics.enabled` is set, the server exposes Prometheus metrics on the configured port (default: 6060):

```bash
curl http://localhost:6060/metrics
```

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

You can inspect the database directly with sqlite3:

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
- **GET latency**: <10ms (database read only)
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
