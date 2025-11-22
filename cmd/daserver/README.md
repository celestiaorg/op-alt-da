# Alt-DA Server

## Introduction

Async DA server for Optimism Alt-DA mode using Celestia. Stores blobs to database immediately, then batches and submits to Celestia in the background.

## Configuration

See main [README.md](../../README.md) for complete configuration options.

**Quick start:**

```bash
# Generate namespace
export CELESTIA_NAMESPACE=00000000000000000000000000000000000000$(openssl rand -hex 10)

# Run with local Celestia node
./da-server \
  --celestia.server http://localhost:26658 \
  --celestia.auth-token $(cat ~/.celestia-light/keys/keyring-test/auth-token) \
  --celestia.namespace $CELESTIA_NAMESPACE

# Or with service provider (Quicknode, etc)
./da-server \
  --celestia.server https://your-endpoint.celestia-mocha.quiknode.pro/token \
  --celestia.tx-client.core-grpc.addr your-endpoint:9090 \
  --celestia.tx-client.keyring-path ~/.celestia-light-mocha-4/keys \
  --celestia.tx-client.key-name my_key \
  --celestia.tx-client.p2p-network mocha-4 \
  --celestia.namespace $CELESTIA_NAMESPACE
```
