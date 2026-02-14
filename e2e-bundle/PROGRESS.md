# E2E Bundle Progress Report

**Date**: 2026-02-14
**Branch**: e2e-bundle work-in-progress

## Architecture

```
e2e-run.sh
  ├── Fetches trusted hash from Celestia RPC (fast sync)
  ├── Injects keyring from CELESTIA_KEYRING_BASE64
  ├── docker compose up -d
  │     celestia-light ──(healthy when DAS catch_up_done)──► op-alt-da ──► op-node ──► op-batcher
  │     anvil ──► op-geth-init ──► op-geth ──────────────────────────────► op-node    op-proposer
  ├── wait-for-sync.sh (progress logging)
  ├── Wait for 6+ services running
  └── test-rollup.sh (16 tests)
```

## What's Working

| Component | Status | Evidence |
|-----------|--------|---------|
| Celestia light node (Docker) | OK | Starts, syncs, DAS catch-up completes |
| Fast sync (trusted hash) | OK | `Healthy 11.2s` via docker-compose |
| DAS healthcheck gating | OK | `depends_on: condition: service_healthy` gates downstream |
| All OP Stack services start | OK | 7 services running |
| L2 block production | OK | +429 blocks in 3s |
| L2 transactions | OK | Send + confirm succeed |
| Contract deployments | OK | DisputeGameFactory, OptimismPortal, L1StandardBridge verified |
| 14/16 tests pass | OK | Service availability, contracts, sync, L2 tx all green |

## What's Broken

### 1. op-alt-da cannot sign blob submissions (BLOCKING)

```
Failed to submit blob to Celestia: mkdir /keys/keyring-test: no such file or directory
```

- Config: `keyring_backend = "test"`, `keyring_path = "/keys"`
- Cosmos SDK keyring-test backend expects `/keys/keyring-test/` subdirectory
- Volume mount: `./keys:/keys` (was `:ro`, removed — still fails)
- **Root cause**: The `keys/` tarball injected from `CELESTIA_KEYRING_BASE64` likely doesn't contain the `keyring-test/` subdirectory structure the Cosmos SDK expects
- **Impact**: op-alt-da runs and responds to health checks, but cannot submit blobs to Celestia

### 2. Two test failures (consequence of #1)

- "OP-Batcher Alt-DA configuration" — no Alt-DA log lines (batcher can't complete DA flow)
- "No Celestia errors" — keyring `mkdir` errors in op-alt-da logs

### 3. Secondary log warning (non-blocking)

```
Celestia failed and no fallback available: requested header (10057302) is below Tail (10132176)
```

- op-node asks for old Celestia height that's been pruned by the light node
- Expected with fast sync (light node only has recent headers)
- Only affects old L1 batches referencing old Celestia heights, not new submissions

## Next Steps

1. **Inspect keyring tarball structure**: Decode `CELESTIA_KEYRING_BASE64` and check if it has `keys/keyring-test/` or flat files under `keys/`
2. **Fix keyring layout**: Ensure the tarball matches what Cosmos SDK keyring-test expects at `/keys/keyring-test/`
3. **Re-run e2e**: Verify blob submissions succeed and all 16 tests pass
4. **Re-enable cleanup**: Uncomment `docker compose down -v` in e2e-run.sh cleanup function
5. **Commit to working branch**

## Files Changed

| File | Change |
|------|--------|
| `docker-compose.yml` | Added `celestia-light` service with DAS healthcheck, fast sync, removed `:ro` from keys mount |
| `scripts/e2e-run.sh` | Fetches trusted hash, exports fast sync vars, debug mode skips teardown + cleanup |
| `scripts/wait-for-sync.sh` | Two-phase sync check (headers then DAS) |
| `scripts/bench-sync.sh` | New: standalone sync benchmark tool |
| `test-rollup.sh` | Reverted to original checks (healthcheck handles timing) |
| `config/config.toml.arabica` | `bridge_addr` → `http://celestia-light:26658` |
| `config/config.toml.mocha` | `bridge_addr` → `http://celestia-light:26658` |
| Deleted: `scripts/setup-lightnode.sh` | Replaced by Docker service |
| Deleted: `docker-compose.ci.yml` | No longer needed (no host.docker.internal) |

## Key Design Decisions

1. **Docker service over binary**: Light node runs as a Docker service in the compose stack instead of a host binary managed with PID files. Eliminates arch detection, binary downloads, and CI vs local differences.

2. **DAS healthcheck as gate**: `celestia-light` is only "healthy" when `das.SamplingStats.catch_up_done == true && is_running == true`. All downstream services wait for this, ensuring the DA layer is fully operational before the rollup starts.

3. **Fast sync with trusted hash**: Fetches current height+hash from public Tendermint RPC at runtime, patches `SyncFromHeight`/`SyncFromHash` in the light node's config.toml. Reduces sync from 300s+ to ~5s.

4. **Container-to-container networking**: op-alt-da connects to `celestia-light:26658` (Docker DNS), not `host.docker.internal:26658`. Eliminates the need for `docker-compose.ci.yml` extra_hosts override.

## How to Run

```bash
# Full e2e (requires CELESTIA_KEYRING_BASE64 or keys/ directory)
cd op-alt-da/e2e-bundle
CELESTIA_NETWORK=arabica bash scripts/e2e-run.sh

# Benchmark light node sync
bash scripts/bench-sync.sh --network arabica

# Manual teardown (when debug mode is enabled)
docker compose down -v
```
