# Migration Guide: v0.9.x → v0.10.0

This guide helps operators upgrade from v0.9.x to v0.10.0.

## Summary

| Change                | Impact              | Action Required                    |
| --------------------- | ------------------- | ---------------------------------- |
| TxClient now required | **Breaking**        | Configure keyring and CoreGRPC     |
| S3 flags restructured | Backward compatible | Update flag names                  |
| config.toml support   | New feature         | Optional: migrate from CLI to TOML |

---

## New: config.toml Support

v0.10.0 introduces config.toml as the recommended way to configure the server. CLI flags still work and take precedence over config.toml values.

See `config.toml.example` for a complete reference.

---

## Breaking Change: TxClient Required

### What Changed

v0.10.0 requires the TxClient for blob submissions. Auth tokens alone are no longer sufficient.

### Why

Direct signing via keyring provides:

- More reliable blob submissions
- Better transaction management
- Support for parallel submissions

### Migration

**v0.9.x** (CLI flags, no longer works for writes):

```bash
./da-server \
  --celestia.auth-token="$AUTH_TOKEN" \
  --celestia.server="http://localhost:26658" \
  ...
```

**v0.10.0** (config.toml, recommended):

```toml
[celestia]
core_grpc_addr = "consensus-full-mocha-4.celestia-mocha.com:9090"
core_grpc_tls_enabled = true
keyring_path = "~/.celestia-light-mocha-4/keys"
default_key_name = "my_celes_key"
p2p_network = "mocha-4"  # or "celestia" for mainnet
```

<details>
<summary>v0.10.0 (CLI flags) - click to expand</summary>

```bash
./da-server \
  --celestia.tx-client.keyring-path="$HOME/.celestia-light-mocha-4/keys" \
  --celestia.tx-client.core-grpc.addr="consensus-full-mocha-4.celestia-mocha.com:9090" \
  --celestia.tx-client.p2p-network="mocha-4" \
  ...
```

</details>

### Setup Steps

1. **Create a Celestia key** (if you don't have one):

   ```bash
   celestia light init --p2p.network mocha-4
   celestia-appd keys add my_celes_key --keyring-backend test \
     --home ~/.celestia-light-mocha-4
   ```

2. **Fund the key** with TIA for gas fees

3. **Update your startup command** or create a config.toml

---

## S3 Configuration Changes

### Flag Mapping

**v0.9.x** (CLI flags):

```bash
./da-server \
  --s3.bucket="my-bucket" \
  --s3.path="blobs" \
  --s3.credential-type="iam" \
  --routing.fallback=true \
  --routing.cache=true
```

**v0.10.0** (config.toml):

```toml
[fallback]
enabled = true
provider = "s3"

[fallback.s3]
bucket = "my-bucket"
prefix = "blobs"        # renamed from "path"
region = "us-east-1"    # new field
credential_type = "iam"
```

<details>
<summary>v0.10.0 CLI flags mapping - click to expand</summary>

| v0.9.x Flag              | v0.10.0 Flag                      |
| ------------------------ | --------------------------------- |
| `--s3.bucket`            | `--fallback.s3.bucket`            |
| `--s3.path`              | `--fallback.s3.prefix`            |
| `--s3.endpoint`          | `--fallback.s3.endpoint`          |
| `--s3.credential-type`   | `--fallback.s3.credential-type`   |
| `--s3.access-key-id`     | `--fallback.s3.access-key-id`     |
| `--s3.access-key-secret` | `--fallback.s3.access-key-secret` |
| `--s3.timeout`           | `--fallback.s3.timeout`           |
| `--routing.fallback`     | `--fallback.enabled`              |
| `--routing.cache`        | (removed, always enabled)         |
| (none)                   | `--fallback.s3.region`            |

</details>

### Behavior

Fallback behavior is unchanged:

- **Write-through**: Blobs written to S3 after Celestia submission
- **Read-fallback**: S3 used if Celestia retrieval fails

### Data Compatibility

**Your existing S3 data is fully compatible.** The key format remains:

```
{prefix}/{hex-encoded-commitment}
```

No data migration required.

---

## New Configuration Options

v0.10.0 adds security and performance settings (config.toml):

```toml
# HTTP server timeouts (DoS protection)
read_timeout = "30s"
write_timeout = "120s"
idle_timeout = "60s"

[submission]
# Request body size limit
max_blob_size = "2MB"
```

---

## Rollback

If you need to rollback to v0.9.x:

1. Your S3 data is compatible in both directions
2. Revert to old CLI flag names
3. Auth token authentication will work again

## Questions?

Open an issue at https://github.com/celestiaorg/op-alt-da/issues
