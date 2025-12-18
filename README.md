# Alt-DA x Celestia

## Overview

This repository implements a Celestia `da-server` for Alt-DA mode using generic commitments. The server provides a simple HTTP API that the OP Stack's `op-batcher` uses to store and retrieve data availability (DA) commitments.

```
┌─────────────────────────────────────────────────────────────────────┐
│  PUT Request → Create blob → Submit via CoreGRPC → Return BlobID    │
│                                    ↑                                │
│                  clientTX (signs with local keyring)                │
│                                                                     │
│  GET Request → Parse BlobID (height + commitment) → blob.Get()      │
│                                    ↑                                │
│                        Bridge node JSON-RPC                         │
├─────────────────────────────────────────────────────────────────────┤
│  Requirements: Local keyring, Bridge node (read), CoreGRPC (write)  │
└─────────────────────────────────────────────────────────────────────┘
```

## Prerequisites

Before running the DA server, you need:

1. **A Celestia keyring with a funded account** - Required for signing transactions
2. **Access to a Celestia bridge/light node** - For reading blobs (JSON-RPC)
3. **Access to a Celestia consensus node** - For submitting blobs (CoreGRPC)
4. **Go 1.21+** - For building from source

### ⚠️ Important: Local Keyring Requirement

The DA server signs transactions locally using a Celestia keyring. This means:

- You must have a keyring directory (e.g., `~/.celestia-light-mocha-4/keys`)
- The keyring must contain a funded key for paying transaction fees
- The key name must match your configuration (`default_key_name`)

**The server will not work without a properly configured local keyring.**

🚀 Prefer not to store keys on disk? Enable the optional AWS KMS backend via `[celestia.kms]` and map each signer to a KMS alias (`alias/op-alt-da/<key_name>`). The server will connect to AWS (or Localstack) and sign via KMS instead of the local keyring.

## Quick Start

### 1. Set Up Celestia Keyring

First, create and fund a Celestia key:

```bash
# Initialize a Celestia light node (creates keyring directory)
celestia light init --p2p.network mocha-4

# Add a new key to the keyring
celestia-appd keys add my_celes_key --keyring-backend test \
  --home ~/.celestia-light-mocha-4

# Display the address to fund with TIA
celestia-appd keys show my_celes_key --keyring-backend test \
  --home ~/.celestia-light-mocha-4 -a

# Fund this address with TIA from:
# - Mocha faucet: https://faucet.celestia-mocha.com/
# - Arabica faucet: https://faucet.celestia-arabica-11.com/
```

### 2. Build the Server

```bash
make da-server
# or
go build -o bin/da-server ./cmd/daserver
```

### 3. Run the Server

```bash
./bin/da-server \
  --celestia.namespace="00000000000000000000000000000000000000000000000000000000acfe" \
  --celestia.server="http://localhost:26658" \
  --celestia.auth-token="your-bridge-auth-token" \
  --celestia.tx-client.core-grpc.addr="consensus-full-mocha-4.celestia-mocha.com:9090" \
  --celestia.tx-client.keyring-path="$HOME/.celestia-light-mocha-4/keys" \
  --celestia.tx-client.key-name="my_celes_key" \
  --celestia.tx-client.p2p-network="mocha-4"
```

Or using a config file:

```bash
./bin/da-server --config=config.toml
```

## Configuration

### CLI Flags

| Flag                        | Environment Variable               | Default                  | Description                       |
| --------------------------- | ---------------------------------- | ------------------------ | --------------------------------- |
| `--addr`                    | `OP_ALTDA_ADDR`                    | `127.0.0.1`              | Server listening address          |
| `--port`                    | `OP_ALTDA_PORT`                    | `3100`                   | Server listening port             |
| `--celestia.server`         | `OP_ALTDA_CELESTIA_SERVER`         | `http://localhost:26658` | Bridge node RPC endpoint          |
| `--celestia.namespace`      | `OP_ALTDA_CELESTIA_NAMESPACE`      | (required)               | Celestia namespace (29 bytes hex) |
| `--celestia.auth-token`     | `OP_ALTDA_CELESTIA_AUTH_TOKEN`     |                          | Bridge node auth token            |
| `--celestia.tls-enabled`    | `OP_ALTDA_CELESTIA_TLS_ENABLED`    | `false`                  | Enable TLS for bridge RPC         |
| `--celestia.compact-blobid` | `OP_ALTDA_CELESTIA_BLOBID_COMPACT` | `true`                   | Use compact blob IDs              |

#### Transaction Client (Required for Writes)

| Flag                                         | Environment Variable                                | Default        | Description                            |
| -------------------------------------------- | --------------------------------------------------- | -------------- | -------------------------------------- |
| `--celestia.tx-client.key-name`              | `OP_ALTDA_CELESTIA_TX_CLIENT_KEY_NAME`              | `my_celes_key` | Key name in keyring                    |
| `--celestia.tx-client.keyring-path`          | `OP_ALTDA_CELESTIA_TX_CLIENT_KEYRING_PATH`          | **(required)** | Path to keyring directory (omit when using KMS) |
| `--celestia.tx-client.core-grpc.addr`        | `OP_ALTDA_CELESTIA_TX_CLIENT_CORE_GRPC_ADDR`        | **(required)** | CoreGRPC endpoint                      |
| `--celestia.tx-client.core-grpc.tls-enabled` | `OP_ALTDA_CELESTIA_TX_CLIENT_CORE_GRPC_TLS_ENABLED` | `true`         | Enable TLS for CoreGRPC                |
| `--celestia.tx-client.core-grpc.auth-token`  | `OP_ALTDA_CELESTIA_TX_CLIENT_CORE_GRPC_AUTH_TOKEN`  |                | CoreGRPC auth token                    |
| `--celestia.tx-client.p2p-network`           | `OP_ALTDA_CELESTIA_TX_CLIENT_P2P_NETWORK`           | `mocha-4`      | Network: mocha-4, arabica-11, celestia |

#### Fallback Storage (Optional)

| Flag                              | Environment Variable                     | Default     | Description                              |
| --------------------------------- | ---------------------------------------- | ----------- | ---------------------------------------- |
| `--fallback.enabled`              | `OP_ALTDA_FALLBACK_ENABLED`              | `false`     | Enable fallback storage                  |
| `--fallback.provider`             | `OP_ALTDA_FALLBACK_PROVIDER`             | `s3`        | Fallback provider type                   |
| `--fallback.s3.bucket`            | `OP_ALTDA_FALLBACK_S3_BUCKET`            |             | S3 bucket name                           |
| `--fallback.s3.prefix`            | `OP_ALTDA_FALLBACK_S3_PREFIX`            |             | S3 key prefix                            |
| `--fallback.s3.endpoint`          | `OP_ALTDA_FALLBACK_S3_ENDPOINT`          |             | S3 endpoint (for MinIO, etc.)            |
| `--fallback.s3.region`            | `OP_ALTDA_FALLBACK_S3_REGION`            | `us-east-1` | S3 region                                |
| `--fallback.s3.credential-type`   | `OP_ALTDA_FALLBACK_S3_CREDENTIAL_TYPE`   |             | Credential type: static, environment, iam |
| `--fallback.s3.access-key-id`     | `OP_ALTDA_FALLBACK_S3_ACCESS_KEY_ID`     |             | S3 access key                            |
| `--fallback.s3.access-key-secret` | `OP_ALTDA_FALLBACK_S3_ACCESS_KEY_SECRET` |             | S3 secret key                            |
| `--fallback.s3.timeout`           | `OP_ALTDA_FALLBACK_S3_TIMEOUT`           | `30s`       | S3 operation timeout                     |

#### Metrics

| Flag                | Environment Variable       | Default | Description               |
| ------------------- | -------------------------- | ------- | ------------------------- |
| `--metrics.enabled` | `OP_ALTDA_METRICS_ENABLED` | `false` | Enable Prometheus metrics |
| `--metrics.port`    | `OP_ALTDA_METRICS_PORT`    | `6060`  | Prometheus metrics port   |

#### Logging

| Flag           | Environment Variable  | Default | Description                               |
| -------------- | --------------------- | ------- | ----------------------------------------- |
| `--log.level`  | `OP_ALTDA_LOG_LEVEL`  | `INFO`  | Log level (DEBUG, INFO, WARN, ERROR)      |
| `--log.format` | `OP_ALTDA_LOG_FORMAT` | `text`  | Log format (text, terminal, logfmt, json) |

### TOML Configuration

See `config.toml.example` for a complete example:

```toml
# Server settings
addr = "127.0.0.1"
port = 3100
log_level = "info"
log_format = "text"

[celestia]
namespace = "00000000000000000000000000000000000000000000000000000000acfe"
blobid_compact = true

# Bridge node (for reading blobs via JSON-RPC)
bridge_addr = "http://localhost:26658"
bridge_auth_token = "your-bridge-auth-token"

# Core gRPC (for submitting blobs to consensus)
core_grpc_addr = "consensus-full-mocha-4.celestia-mocha.com:9090"
core_grpc_tls_enabled = true

# Keyring (REQUIRED - for signing transactions)
keyring_path = "~/.celestia-light-mocha-4/keys"
default_key_name = "my_celes_key"
p2p_network = "mocha-4"

# Optional AWS KMS keyring (alias/op-alt-da/<key_name>)
[celestia.kms]
enabled = false
region = "us-east-1"
endpoint = ""
alias_prefix = "alias/op-alt-da/"

[submission]
timeout = "60s"
tx_priority = 2  # 1=low, 2=medium, 3=high

[read]
timeout = "30s"

[metrics]
enabled = true
port = 6060
```

### AWS KMS Keyring (Optional)

You can offload signing to AWS KMS (or Localstack) instead of storing private keys locally.

1. Create (or import) a secp256k1 key per signer in KMS and attach an alias `alias/op-alt-da/<key_name>` e.g.: `aws kms create-alias --alias-name alias/op-alt-da/my_celes_key --target-key-id <key-id>`
2. Populate `[celestia.kms]` in your config with the AWS region, optional endpoint (e.g., `http://localhost:4566` for Localstack), and alias prefix.
3. Optionally import a hex encoded private key exported from node keystore e.g.: `cel-key export --unarmored-hex --unsafe my_celes_key`

When enabled, the DA server enumerates matching aliases, fetches public keys via `GetPublicKey`, and signs transactions via the KMS `Sign` API.

### Generating a Namespace

A valid namespace is 29 bytes (58 hex characters). For version 0 namespaces, the first 18 bytes must be zeros:

```bash
export NAMESPACE=00000000000000000000000000000000000000$(openssl rand -hex 10)
echo $NAMESPACE
```

## API Reference

See [API.md](./API.md) for detailed API documentation.

### Quick Reference

| Endpoint           | Method | Description                 |
| ------------------ | ------ | --------------------------- |
| `/put`             | PUT    | Submit blob to Celestia     |
| `/get/:commitment` | GET    | Retrieve blob by commitment |
| `/health`          | GET    | Health check endpoint       |

### Example Usage

```bash
# Submit a blob
COMMITMENT=$(curl -s -X PUT http://localhost:3100/put \
  -H "Content-Type: application/octet-stream" \
  --data-binary "hello world")
echo "Commitment: $COMMITMENT"

# Retrieve the blob
curl http://localhost:3100/get/$COMMITMENT

# Health check
curl http://localhost:3100/health
```

## Fallback Storage

The server supports optional fallback storage for resilience. When enabled, fallback always performs both:

- **Write-through**: After successful Celestia submission, blob is also written to fallback asynchronously
- **Read-fallback**: If blob not found in Celestia, attempt to read from fallback
- **Read-through**: When blob is found in Celestia, it's also written to fallback for future requests

Fallback failures are non-fatal and logged as warnings.

### S3 Fallback Example

```bash
./bin/da-server \
  --celestia.namespace="$NAMESPACE" \
  --celestia.tx-client.keyring-path="$HOME/.celestia-light-mocha-4/keys" \
  --celestia.tx-client.core-grpc.addr="consensus-full-mocha-4.celestia-mocha.com:9090" \
  --fallback.enabled=true \
  --fallback.s3.bucket="my-da-fallback" \
  --fallback.s3.region="us-east-1"
```

## Metrics

When enabled (`--metrics.enabled=true`), Prometheus metrics are available at `http://localhost:6060/metrics`.

### Available Metrics

| Metric                                 | Type      | Description                         |
| -------------------------------------- | --------- | ----------------------------------- |
| `op_altda_request_duration_seconds`    | Histogram | HTTP request duration by method     |
| `op_altda_blob_size_bytes`             | Histogram | Size of submitted/retrieved blobs   |
| `op_altda_inclusion_height`            | Gauge     | Latest Celestia inclusion height    |
| `celestia_submission_duration_seconds` | Histogram | Time to submit blob to Celestia     |
| `celestia_submissions_total`           | Counter   | Total blob submissions              |
| `celestia_submission_errors_total`     | Counter   | Failed blob submissions             |
| `celestia_retrieval_duration_seconds`  | Histogram | Time to retrieve blob from Celestia |
| `celestia_retrievals_total`            | Counter   | Total blob retrievals               |
| `celestia_retrieval_errors_total`      | Counter   | Failed blob retrievals              |

## Testing

### Run Tests

```bash
# All unit tests
make test

# Unit tests only
make test-unit

# Integration tests (requires Celestia devnet access)
CELESTIA_BRIDGE="http://localhost:26658" \
CELESTIA_AUTH_TOKEN="your-token" \
CELESTIA_NAMESPACE="your-namespace" \
make test-integration

# All tests
make test-all
```

### Manual Testing

```bash
# Start the server (ensure keyring is configured)
./bin/da-server --config=config.toml

# Test PUT
curl -X PUT http://localhost:3100/put \
  -H "Content-Type: application/octet-stream" \
  --data-binary "hello world"

# Test GET with the returned commitment
curl http://localhost:3100/get/0x010c...

# Check metrics
curl -s http://localhost:6060/metrics | grep -E "^(op_altda|celestia)_"
```

## Integration with OP Stack

To use this DA server with the OP Stack:

1. Configure `da_params` in your kurtosis config:

```yaml
da_params:
  image: ghcr.io/celestiaorg/op-alt-da:latest
```

2. Add `altda_deploy_config`:

```yaml
altda_deploy_config:
  use_altda: true
  da_commitment_type: GenericCommitment
  da_challenge_window: 100
  da_resolve_window: 100
  da_bond_size: 0
  da_resolver_refund_percentage: 0
```

For more details, see the [Optimism Alt-DA documentation](https://docs.optimism.io/operators/chain-operators/features/alt-da-mode).

## Troubleshooting

### "keyring not found" or "key not found"

Ensure your keyring path is correct and contains the specified key:

```bash
# List keys in keyring
celestia-appd keys list --keyring-backend test \
  --home ~/.celestia-light-mocha-4

# Verify keyring path exists
ls -la ~/.celestia-light-mocha-4/keys/
```

### "insufficient funds"

Your Celestia account needs TIA to pay for transaction fees:

```bash
# Check balance
celestia-appd query bank balances $(celestia-appd keys show my_celes_key -a \
  --keyring-backend test --home ~/.celestia-light-mocha-4) \
  --node https://rpc-mocha.pops.one:443
```

### "connection refused" on CoreGRPC

Verify the CoreGRPC endpoint is accessible:

```bash
# Test connection
grpcurl -plaintext consensus-full-mocha-4.celestia-mocha.com:9090 list
```

## License

MIT License - see [LICENSE](./LICENSE)
