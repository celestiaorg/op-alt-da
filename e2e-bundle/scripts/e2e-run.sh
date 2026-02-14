#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# e2e-run.sh - OP Stack + Celestia E2E Test Orchestrator
# ============================================================
#
# Locally testable: CELESTIA_NETWORK=arabica bash scripts/e2e-run.sh
#
# Environment variables:
#   CELESTIA_NETWORK        - "arabica" or "mocha" (required)
#   CELESTIA_NODE_VERSION   - celestia-node release tag, e.g. "v0.29.0-arabica" (optional, auto-detected)
#   CELESTIA_KEYRING_BASE64 - base64-encoded tar.gz of keys/ directory (optional if keys/ exists)
#   CELESTIA_ADDRESS        - funded Celestia address (for balance check)
#   SYNC_TIMEOUT            - seconds to wait for light node sync (default: 300)
#   SERVICE_TIMEOUT         - seconds to wait for docker services (default: 300)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUNDLE_DIR="$(dirname "$SCRIPT_DIR")"
LOGS_DIR="${BUNDLE_DIR}/logs"

CELESTIA_NETWORK="${CELESTIA_NETWORK:?CELESTIA_NETWORK must be set (arabica or mocha)}"
SERVICE_TIMEOUT="${SERVICE_TIMEOUT:-300}"

# Network-specific settings
case "$CELESTIA_NETWORK" in
    arabica)
        CELESTIA_CORE_IP="validator-1.celestia-arabica-11.com"
        CELESTIA_P2P_NETWORK="arabica"
        CELESTIA_RPC_URL="https://rpc.celestia-arabica-11.com"
        ;;
    mocha)
        CELESTIA_CORE_IP="consensus-full-mocha-4.celestia-mocha.com"
        CELESTIA_P2P_NETWORK="mocha"
        CELESTIA_RPC_URL="https://rpc-mocha.pops.one"
        ;;
    *)
        echo "ERROR: Unknown network: $CELESTIA_NETWORK (expected: arabica or mocha)"
        exit 1
        ;;
esac

echo "============================================================"
echo "  E2E Test: OP Stack + Celestia ${CELESTIA_NETWORK}"
echo "============================================================"
echo ""
echo "Bundle dir:  ${BUNDLE_DIR}"
echo "Network:     ${CELESTIA_NETWORK}"
echo "Node version: ${CELESTIA_NODE_VERSION:-auto-detect}"
echo "CI mode:     ${GITHUB_ACTIONS:-false}"
echo ""

# ============================================================
# Cleanup (always runs on exit)
# ============================================================
cleanup() {
    local exit_code=$?
    echo ""
    echo ">>> Cleaning up..."

    # Collect docker logs before teardown
    mkdir -p "$LOGS_DIR"
    cd "$BUNDLE_DIR"
    for svc in celestia-light anvil op-alt-da op-geth op-node op-batcher op-proposer; do
        docker compose logs "$svc" > "$LOGS_DIR/${svc}.log" 2>&1 || true
    done
    docker compose ps > "$LOGS_DIR/docker-compose-ps.txt" 2>&1 || true

    # Tear down docker (disabled for debugging - uncomment when done)
    # docker compose down -v 2>/dev/null || true
    echo ">>> SKIPPING teardown (debug mode) - run 'docker compose down -v' manually"
    # Also skipping keys/config cleanup since containers are still running

    echo ">>> Cleanup complete (exit code: ${exit_code})"
    exit "$exit_code"
}
trap cleanup EXIT

# ============================================================
# Step 1: Resolve celestia-node version (= Docker image tag)
# ============================================================
echo ">>> [1/6] Resolving celestia-node version..."

if [[ -z "${CELESTIA_NODE_VERSION:-}" ]]; then
    echo "    Auto-detecting latest ${CELESTIA_NETWORK}-tagged release..."

    if command -v gh &>/dev/null; then
        CELESTIA_NODE_VERSION=$(gh api repos/celestiaorg/celestia-node/releases \
            --jq "[.[] | select(.tag_name | test(\"${CELESTIA_NETWORK}\"))][0].tag_name // empty")
    else
        CELESTIA_NODE_VERSION=$(curl -s https://api.github.com/repos/celestiaorg/celestia-node/releases \
            | jq -r "[.[] | select(.tag_name | test(\"${CELESTIA_NETWORK}\"))][0].tag_name // empty")
    fi

    if [[ -z "$CELESTIA_NODE_VERSION" ]]; then
        echo "ERROR: Could not find a ${CELESTIA_NETWORK}-tagged release of celestia-node"
        exit 1
    fi
fi

CELESTIA_NODE_TAG="$CELESTIA_NODE_VERSION"
echo "    Docker image: ghcr.io/celestiaorg/celestia-node:${CELESTIA_NODE_TAG}"
echo "    Core IP:      ${CELESTIA_CORE_IP}"
echo "    P2P network:  ${CELESTIA_P2P_NETWORK}"

# Fetch trusted height+hash for fast sync (skip sampling the entire chain)
echo "    Fetching trusted hash for fast sync..."
TRUSTED_DATA=$(curl -s "${CELESTIA_RPC_URL}/header" 2>/dev/null \
    | jq -r '.result.header | "\(.height) \(.last_block_id.hash)"' 2>/dev/null || echo "")
if [[ -n "$TRUSTED_DATA" && "$TRUSTED_DATA" != "null null" ]]; then
    SYNC_FROM_HEIGHT=$(echo "$TRUSTED_DATA" | cut -d' ' -f1)
    SYNC_FROM_HASH=$(echo "$TRUSTED_DATA" | cut -d' ' -f2)
    echo "    Fast sync from height ${SYNC_FROM_HEIGHT}"
else
    echo "    WARNING: Could not fetch trusted hash, will sync from genesis (slower)"
    SYNC_FROM_HEIGHT=""
    SYNC_FROM_HASH=""
fi

# ============================================================
# Step 2: Prepare keyring
# ============================================================
echo ">>> [2/6] Preparing keyring..."
if [[ -n "${CELESTIA_KEYRING_BASE64:-}" ]]; then
    echo "    Decoding keyring from CELESTIA_KEYRING_BASE64..."
    echo "$CELESTIA_KEYRING_BASE64" | base64 -d | tar xz -C "$BUNDLE_DIR"
    find "$BUNDLE_DIR/keys" -type d -exec chmod 700 {} \;
    find "$BUNDLE_DIR/keys" -type f -exec chmod 600 {} \;
    echo "    Keyring injected to ${BUNDLE_DIR}/keys/"
elif [[ -d "$BUNDLE_DIR/keys" ]]; then
    echo "    Using existing keys/ directory"
else
    echo "ERROR: No keyring available."
    echo "  Set CELESTIA_KEYRING_BASE64 env var, or provide a keys/ directory in ${BUNDLE_DIR}/"
    exit 1
fi

# ============================================================
# Step 3: Generate config.toml
# ============================================================
echo ">>> [3/6] Generating config.toml for ${CELESTIA_NETWORK}..."
CONFIG_SRC="${BUNDLE_DIR}/config/config.toml.${CELESTIA_NETWORK}"

if [[ ! -f "$CONFIG_SRC" ]]; then
    echo "ERROR: Config template not found: ${CONFIG_SRC}"
    exit 1
fi

cp "$CONFIG_SRC" "$BUNDLE_DIR/config.toml"

# ============================================================
# Step 4: Balance check (non-blocking)
# ============================================================
echo ">>> [4/6] Checking wallet balance..."
if [[ -n "${CELESTIA_ADDRESS:-}" ]]; then
    bash "$SCRIPT_DIR/check-balance.sh" || echo "    Balance check failed (non-blocking, continuing...)"
else
    echo "    Skipping balance check (CELESTIA_ADDRESS not set)"
fi

# ============================================================
# Step 5: Start docker compose (includes celestia light node)
# ============================================================
echo ">>> [5/6] Starting docker compose services..."
cd "$BUNDLE_DIR"

# Export vars for docker-compose.yml substitution
export CELESTIA_NODE_TAG
export CELESTIA_CORE_IP
export CELESTIA_P2P_NETWORK
export SYNC_FROM_HEIGHT
export SYNC_FROM_HASH

docker compose up -d

# Wait for light node DAS sync (progress logging).
# The celestia-light healthcheck gates on DAS catch-up, so dependent services
# (op-alt-da → op-node → op-batcher) won't start until this completes.
echo "    Waiting for light node DAS sync..."
bash "$SCRIPT_DIR/wait-for-sync.sh"

# Wait for remaining services to become healthy (they start after light node is synced)
echo "    Waiting for OP Stack services to be healthy (timeout: ${SERVICE_TIMEOUT}s)..."
deadline=$((SECONDS + SERVICE_TIMEOUT))
healthy_count=0

while [[ $SECONDS -lt $deadline ]]; do
    # Count running containers (excluding init containers that exited)
    running=$(docker compose ps --format json 2>/dev/null | jq -s 'map(select(.State == "running")) | length' 2>/dev/null || echo "0")

    if [[ "$running" -ge 6 ]]; then
        echo "    ${running} services running"
        healthy_count=$running
        # Give services a few extra seconds to stabilize
        sleep 10
        break
    fi

    elapsed=$((SECONDS))
    remaining=$((deadline - SECONDS))
    echo "    [${elapsed}s] ${running}/6+ services running... (${remaining}s remaining)"
    sleep 10
done

if [[ "$healthy_count" -lt 6 ]]; then
    echo "ERROR: Services did not start within ${SERVICE_TIMEOUT}s"
    docker compose ps
    exit 1
fi

echo "    All core services are running"

# ============================================================
# Step 6: Run tests
# ============================================================
echo ">>> [6/6] Running test-rollup.sh..."
echo ""

# Export vars for test-rollup.sh
export CELESTIA_NETWORK
export CELESTIA_ADDRESS="${CELESTIA_ADDRESS:-}"

cd "$BUNDLE_DIR"
bash "$BUNDLE_DIR/test-rollup.sh"
