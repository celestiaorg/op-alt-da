# Alt-DA Server

## Introduction

Async DA server for Optimism Alt-DA mode using Celestia. Stores blobs to database immediately, then batches and submits to Celestia in the background.

## Configuration

See main [README.md](../../README.md) for complete configuration options.

**Quick start:**

```bash
# Generate namespace
export CELESTIA_NAMESPACE=00000000000000000000000000000000000000$(openssl rand -hex 10)

# Run with dual-endpoint architecture:
# - Bridge node for reading blobs (JSON-RPC)
# - CoreGRPC for submitting blobs
./da-server \
  --celestia.bridge-addr http://localhost:26658 \
  --celestia.bridge-auth-token $(cat ~/.celestia-light-mocha-4/keys/keyring-test/auth-token) \
  --celestia.core-grpc-addr consensus-full-mocha-4.celestia-mocha.com:9090 \
  --celestia.keyring-path ~/.celestia-light-mocha-4/keys \
  --celestia.key-name my_key \
  --celestia.p2p-network mocha-4 \
  --celestia.namespace $CELESTIA_NAMESPACE
```
