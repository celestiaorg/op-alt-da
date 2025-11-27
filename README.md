# Celestia Alt-DA Server

A high-performance Data Availability server for Optimism Alt-DA mode using Celestia as the DA layer.

## Quick Start (Production)

```bash
# 1. Build
make da-server

# 2. Configure
cp config.toml.example config.toml
vim config.toml  # Edit with your settings

# 3. Run
./bin/da-server --config config.toml
```

## Architecture

**Async, database-backed design** for high throughput and crash resilience:

- **HTTP API** - Returns immediately after database write (<50ms latency)
- **SQLite Database** - Persistent queue for blobs and batches
- **Submission Worker** - Batches and submits blobs to Celestia
- **Event Listener** - Confirms submissions and updates database
- **S3 Backup** (optional) - Periodic disaster recovery backups

**Request Flow**:
```
PUT /put → Database (pending) → Return commitment (<50ms)
         ↓
    Worker batches 10-50 blobs
         ↓
    Submit to Celestia (CIP-21 signed)
         ↓
    Event listener confirms → Database (confirmed)
         ↓
GET /get → Serve from database (<10ms)
```

**Key Features**:
- ✅ Fast writes: <50ms (database only)
- ✅ Confirmed reads: Only returns 200 OK when confirmed on Celestia
- ✅ Crash resilient: SQLite with WAL mode
- ✅ FIFO ordering: Auto-increment IDs
- ✅ Efficient batching: 10-50 blobs per transaction
- ✅ CIP-21 signed blobs: Secure namespace access

## Configuration

**For Production: Use TOML** (recommended)

```toml
# config.toml
addr = "0.0.0.0"
port = 3100
db_path = "/var/lib/celestia-da/blobs.db"
log_level = "INFO"

[celestia]
namespace = "00000000000000000000000000000000000000a1b2c3d4e5f6789012"
da_rpc_server = "https://your-endpoint.celestia-mocha.quiknode.pro/token"
tls_enabled = true

[celestia.tx_client]
core_grpc_addr = "your-endpoint.celestia-mocha.quiknode.pro:9090"
keyring_path = "/home/celestia/.celestia-light-mocha-4/keys"
default_key_name = "my_celes_key"
p2p_network = "mocha-4"

[batch]
min_blobs = 10
max_blobs = 50
target_blobs = 20

[worker]
submit_period = "6s"
trusted_signers = []  # Required for read-only mode

[metrics]
enabled = true
port = 6060

[backup]
enabled = true
interval = "6h"

[s3]
credential_type = "iam"
bucket = "celestia-da-prod"
path = "backups"
```

**Start with TOML**:
```bash
./bin/da-server --config config.toml
```

**Verify Configuration**:
```
INFO Loading configuration from TOML file         path=config.toml
INFO ✓ TOML configuration loaded and validated successfully
INFO Configuration source: TOML file              path=config.toml
```

**Configuration Hierarchy**:
- **With `--config`**: TOML file is the **source of truth** (everything else ignored)
- **Without `--config`**: Environment variables → CLI flags

**⚠️  Conflict Detection**: Server warns if you mix TOML with environment variables:
```
⚠️  CONFIGURATION CONFLICT DETECTED ⚠️
TOML file takes precedence. The following environment variables will be IGNORED:
  → OP_ALTDA_CELESTIA_NAMESPACE
  → OP_ALTDA_PORT
```

### Configuration Options

**Deployment Modes**:

**Option A: Self-Hosted Node**
```toml
[celestia]
da_rpc_server = "http://localhost:26658"
auth_token = "..."  # From ~/.celestia-*/keys/keyring-test/auth-token
tls_enabled = false
```

**Option B: Service Provider** (RECOMMENDED)
```toml
[celestia]
da_rpc_server = "https://endpoint.celestia-mocha.quiknode.pro/token"
tls_enabled = true

[celestia.tx_client]
core_grpc_addr = "endpoint.celestia-mocha.quiknode.pro:9090"
keyring_path = "~/.celestia-light-mocha-4/keys"
p2p_network = "mocha-4"
```

**Generate Namespace**:
```bash
echo "00000000000000000000000000000000000000$(openssl rand -hex 10)"
```

### For Development: Environment Variables

```bash
cp .env.example .env
vim .env
export $(cat .env | xargs) && ./bin/da-server
```

**Note**: Don't use `.env` in production - use TOML.

## Deployment

### Write Server (op-batcher sidecar)

Accepts PUT requests and submits to Celestia with CIP-21 signatures:

```bash
# 1. Setup keyring
celestia light init --p2p.network mocha-4
celestia-appd keys add my_key --keyring-backend test --home ~/.celestia-light-mocha-4
# Fund with TIA

# 2. Start server
./bin/da-server --config config.toml

# 3. Get signer address (for read-only servers)
# Check logs after first submission:
grep "Celestia signer address configured" /var/log/celestia-da.log
# Output: signer_bech32=celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc
```

### Read-Only Server (op-node sidecar)

Serves GET by indexing Celestia with backfill worker. Rejects PUT.

**⚠️  SECURITY**: MUST configure `trusted_signers` or malicious actors can inject junk data.

```toml
# config-readonly.toml
read_only = true

[celestia]
namespace = "..."
da_rpc_server = "..."

[worker]
trusted_signers = [
  "celestia15m7s9d0ldd9ur9mgh9m6r4kc396dp68szwqmyc"  # Bech32 address from writer logs
]

[backfill]
enabled = true
start_height = 0
end_height = 0  # 0 = current chain tip
```

**Start**:
```bash
./bin/da-server --config config-readonly.toml
```

**Monitor Progress**:
```bash
grep "Backfill" /var/log/celestia-da-readonly.log
sqlite3 /var/lib/celestia-da/blobs.db \
  "SELECT COUNT(*) FROM blobs WHERE status='confirmed'"
```

### High Availability Setup

```
┌──────────────────────────────────────────────────┐
│  op-batcher ──PUT──► Write Server                │
│                           │                      │
│                           │ Submit (CIP-21)      │
│                           ▼                      │
│                   ┌─────────────────┐            │
│                   │ Celestia Network│            │
│                   └─────────────────┘            │
│                           │                      │
│                           │ Backfill (verify)    │
│                           ▼                      │
│  op-node 1 ──GET──► Read-Only Server 1          │
│  op-node 2 ──GET──► Read-Only Server 2          │
└──────────────────────────────────────────────────┘
```

**Key Points**:
- Write servers submit with CIP-21 signatures
- Read-only servers verify signatures via `trusted_signers`
- Without trusted signers, attackers can spam your namespace (Woods attack)

## Monitoring

**Prometheus Metrics** (`--metrics.enabled`):
```bash
curl http://localhost:6060/metrics
```

**Available Metrics**:
- `celestia_submissions_total` - Total batch submissions
- `celestia_submission_errors_total` - Failed submissions
- `celestia_submission_duration_seconds` - Submission latency
- `celestia_submission_size_bytes` - Batch sizes
- `celestia_retrievals_total` - Total retrievals
- `celestia_retrieval_errors_total` - Failed retrievals

**Database Inspection**:
```bash
sqlite3 blobs.db "SELECT status, COUNT(*) FROM blobs GROUP BY status"
```

## Troubleshooting

### Configuration conflict warning
```
⚠️  CONFIGURATION CONFLICT DETECTED ⚠️
TOML file takes precedence. Environment variables will be IGNORED
```
**Fix**: Remove environment variables or don't use `--config`.

### Read-only server rejecting blobs
```bash
grep "untrusted signer" /var/log/celestia-da-readonly.log
```
**Fix**: Add writer's signer address to `worker.trusted_signers` in TOML.

### Writer not submitting
```bash
grep "submission_worker" /var/log/celestia-da-write.log
# Check keyring: ls -la ~/.celestia-light-mocha-4/keys
# Check balance: celestia-appd query bank balances <address>
```

### Invalid namespace length
```
Error: celestia.namespace must be 58 hex characters (29 bytes)
```
**Fix**:
```bash
echo "00000000000000000000000000000000000000$(openssl rand -hex 10)"
```

## Building

```bash
make da-server          # Standard build
make da-server-optimized # Optimized (smaller binary)
make install            # Install to $GOPATH/bin
make test               # Run tests
make lint               # Run linter
```

**Manual Build**:
```bash
go build -o bin/da-server ./cmd/daserver
```

## API

**PUT (Store blob)**:
```bash
curl -X POST http://localhost:3100/put \
  -H "Content-Type: application/octet-stream" \
  --data-binary @data.bin
```

**GET (Retrieve blob)**:
```bash
curl http://localhost:3100/get/0x0c<commitment>
```

See [API.md](API.md) for complete documentation.

## Performance

- **PUT latency**: <50ms (database write)
- **GET latency**: <10ms (confirmed blobs from cache)
- **Time to availability**: 15-90 seconds (until confirmed on Celestia)
- **Batch size**: 10-50 blobs per transaction
- **Throughput**: 1000+ blobs/sec

## Testing

```bash
make test               # All tests
go test ./db -v        # Database tests
go test ./worker -v    # Worker tests
go test ./batch -v     # Batch tests
```

**Integration Tests** (Kurtosis devnet):
```bash
cd kurtosis-devnet
just simple-devnet
```

## Security

**Production Checklist**:
- [ ] Use TOML configuration (`--config config.toml`)
- [ ] Configure `trusted_signers` for read-only servers
- [ ] Enable metrics for monitoring
- [ ] Enable S3 backups
- [ ] Use IAM roles (not static credentials)
- [ ] Use absolute paths for `db_path` and `keyring_path`
- [ ] Run as non-root user
- [ ] Restrict config file permissions: `chmod 600 config.toml`

**Don't**:
- ❌ Mix TOML config with environment variables
- ❌ Use `.env` files in production
- ❌ Run read-only servers without `trusted_signers`
- ❌ Commit secrets to version control

## Resources

- [API Documentation](API.md)
- [Optimism Alt-DA Mode](https://docs.optimism.io/operators/chain-operators/features/alt-da-mode)
- [Celestia Documentation](https://docs.celestia.org)
- [CIP-21: Signed Blobs](https://forum.celestia.org/t/cip-blobs-with-verified-author/1714)
- [Quicknode Celestia](https://www.quicknode.com/docs/celestia)
