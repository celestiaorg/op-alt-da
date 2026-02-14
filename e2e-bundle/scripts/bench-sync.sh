#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# bench-sync.sh - Benchmark celestia light node sync time
# ============================================================
#
# Usage:
#   bash scripts/bench-sync.sh                    # defaults: arabica, fresh store
#   bash scripts/bench-sync.sh --keep-store       # reuse existing node store
#   bash scripts/bench-sync.sh --network mocha
#   SYNC_TIMEOUT=600 bash scripts/bench-sync.sh
#
# Env vars:
#   CELESTIA_NODE_TAG   - Docker image tag (default: auto-detect)
#   SYNC_TIMEOUT        - max seconds to wait (default: 600)
#   CONTAINER_NAME      - Docker container name (default: celestia-bench)

NETWORK="arabica"
KEEP_STORE=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --network) NETWORK="$2"; shift 2 ;;
        --keep-store) KEEP_STORE=true; shift ;;
        *) echo "Unknown arg: $1"; exit 1 ;;
    esac
done

case "$NETWORK" in
    arabica)
        CORE_IP="validator-1.celestia-arabica-11.com"
        P2P_NETWORK="arabica"
        RPC_URL="https://rpc.celestia-arabica-11.com"
        ;;
    mocha)
        CORE_IP="consensus-full-mocha-4.celestia-mocha.com"
        P2P_NETWORK="mocha"
        RPC_URL="https://rpc-mocha.pops.one"
        ;;
    *) echo "ERROR: Unknown network: $NETWORK"; exit 1 ;;
esac

CONTAINER_NAME="${CONTAINER_NAME:-celestia-bench}"
SYNC_TIMEOUT="${SYNC_TIMEOUT:-600}"
VOLUME_NAME="${CONTAINER_NAME}-data"
RPC="http://localhost:26658"

# Auto-detect image tag
if [[ -z "${CELESTIA_NODE_TAG:-}" ]]; then
    echo ">>> Auto-detecting latest ${NETWORK}-tagged release..."
    if command -v gh &>/dev/null; then
        CELESTIA_NODE_TAG=$(gh api repos/celestiaorg/celestia-node/releases \
            --jq "[.[] | select(.tag_name | test(\"${NETWORK}\"))][0].tag_name // empty")
    else
        CELESTIA_NODE_TAG=$(curl -s https://api.github.com/repos/celestiaorg/celestia-node/releases \
            | jq -r "[.[] | select(.tag_name | test(\"${NETWORK}\"))][0].tag_name // empty")
    fi
fi

IMAGE="ghcr.io/celestiaorg/celestia-node:${CELESTIA_NODE_TAG}"

echo "============================================================"
echo "  Celestia Light Node Sync Benchmark"
echo "============================================================"
echo "  Network:    ${NETWORK} (${P2P_NETWORK})"
echo "  Image:      ${IMAGE}"
echo "  Core IP:    ${CORE_IP}"
echo "  Keep store: ${KEEP_STORE}"
echo "  Timeout:    ${SYNC_TIMEOUT}s"
echo "============================================================"

# Fetch trusted height+hash for fast sync
echo ">>> Fetching trusted hash for fast sync..."
TRUSTED_DATA=$(curl -s "${RPC_URL}/header" 2>/dev/null \
    | jq -r '.result.header | "\(.height) \(.last_block_id.hash)"' 2>/dev/null || echo "")
if [[ -n "$TRUSTED_DATA" && "$TRUSTED_DATA" != "null null" ]]; then
    SYNC_FROM_HEIGHT=$(echo "$TRUSTED_DATA" | cut -d' ' -f1)
    SYNC_FROM_HASH=$(echo "$TRUSTED_DATA" | cut -d' ' -f2)
    echo "    Fast sync from height ${SYNC_FROM_HEIGHT}"
else
    echo "    WARNING: Could not fetch trusted hash, syncing from genesis"
    SYNC_FROM_HEIGHT=""
    SYNC_FROM_HASH=""
fi

# Cleanup previous run
docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
if [[ "$KEEP_STORE" == "false" ]]; then
    docker volume rm "$VOLUME_NAME" 2>/dev/null || true
    echo ">>> Fresh node store (volume removed)"
else
    echo ">>> Reusing existing node store"
fi

# Start container with fast sync
echo ">>> Starting celestia light node..."
docker run -d \
    --name "$CONTAINER_NAME" \
    -e NODE_TYPE=light \
    -e P2P_NETWORK="$P2P_NETWORK" \
    -e SYNC_FROM_HEIGHT="$SYNC_FROM_HEIGHT" \
    -e SYNC_FROM_HASH="$SYNC_FROM_HASH" \
    -p 26658:26658 \
    -v "${VOLUME_NAME}:/home/celestia" \
    --entrypoint /bin/bash \
    "$IMAGE" \
    -c "celestia light init --p2p.network \$P2P_NETWORK --rpc.addr=0.0.0.0 2>/dev/null || true; \
        if [ -n \"\$SYNC_FROM_HEIGHT\" ] && [ \"\$SYNC_FROM_HEIGHT\" != '0' ]; then \
          echo \"Patching config for fast sync: height=\$SYNC_FROM_HEIGHT\"; \
          sed -i \"s/SyncFromHeight = 0/SyncFromHeight = \$SYNC_FROM_HEIGHT/\" /home/celestia/config.toml; \
          sed -i \"s/SyncFromHash = \\\"\\\"/SyncFromHash = \\\"\$SYNC_FROM_HASH\\\"/\" /home/celestia/config.toml; \
        fi; \
        exec celestia light start \
          --core.ip $CORE_IP \
          --core.port 9090 \
          --p2p.network \$P2P_NETWORK \
          --rpc.skip-auth \
          --rpc.addr 0.0.0.0"

cleanup() {
    echo ""
    echo ">>> Stopping container..."
    docker stop "$CONTAINER_NAME" 2>/dev/null || true
    docker rm "$CONTAINER_NAME" 2>/dev/null || true
    if [[ "$KEEP_STORE" == "false" ]]; then
        docker volume rm "$VOLUME_NAME" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# Wait for RPC to come up
echo ">>> Waiting for RPC..."
for i in $(seq 1 60); do
    if curl -s -X POST "$RPC" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"header.SyncState","params":[]}' \
        2>/dev/null | jq -e '.result' > /dev/null 2>&1; then
        echo ">>> RPC is up (${i}s)"
        break
    fi
    if [[ $i -eq 60 ]]; then
        echo "ERROR: RPC never came up"
        docker logs "$CONTAINER_NAME" --tail 30
        exit 1
    fi
    sleep 1
done

# Phase 1: Wait for header sync to complete
# header.SyncState.end is zero ("0001-01-01T...") while syncing, gets a real timestamp when done.
echo ""
echo ">>> Phase 1: Header sync..."
START_TIME=$SECONDS
POLL_INTERVAL=5
deadline=$((SECONDS + SYNC_TIMEOUT))

while [[ $SECONDS -lt $deadline ]]; do
    RESPONSE=$(curl -s -X POST "$RPC" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"header.SyncState","params":[]}' \
        2>/dev/null || echo '{"error":"connection failed"}')

    if echo "$RESPONSE" | jq -e '.error' > /dev/null 2>&1; then
        elapsed=$((SECONDS - START_TIME))
        echo "    [${elapsed}s] Header sync not ready yet..."
        sleep "$POLL_INTERVAL"
        continue
    fi

    HEIGHT=$(echo "$RESPONSE" | jq -r '.result.height' 2>/dev/null || echo "0")
    TO_HEIGHT=$(echo "$RESPONSE" | jq -r '.result.to_height' 2>/dev/null || echo "0")
    END_TIME=$(echo "$RESPONSE" | jq -r '.result.end' 2>/dev/null || echo "0001")

    elapsed=$((SECONDS - START_TIME))

    # Sync is done when end is not the zero-value
    if [[ "$END_TIME" != "0001"* && "$END_TIME" != "null" && -n "$END_TIME" ]]; then
        echo "    [${elapsed}s] Header sync complete! (height: ${HEIGHT}, target: ${TO_HEIGHT})"
        break
    fi

    remaining=$((deadline - SECONDS))
    echo "    [${elapsed}s] headers: ${HEIGHT} / ${TO_HEIGHT} (${remaining}s remaining)"
    sleep "$POLL_INTERVAL"
done

HEADER_ELAPSED=$((SECONDS - START_TIME))

if [[ "$END_TIME" == "0001"* || "$END_TIME" == "null" || -z "$END_TIME" ]]; then
    echo ""
    echo "============================================================"
    echo "  TIMEOUT during header sync after ${HEADER_ELAPSED}s"
    echo "============================================================"
    curl -s -X POST "$RPC" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"header.SyncState","params":[]}' 2>/dev/null | jq .
    exit 1
fi

# Phase 2: Wait for DAS sampling to catch up
echo ""
echo ">>> Phase 2: DAS sampling catch-up..."
DAS_START=$SECONDS

while [[ $SECONDS -lt $deadline ]]; do
    RESPONSE=$(curl -s -X POST "$RPC" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"das.SamplingStats","params":[]}' \
        2>/dev/null || echo '{"error":"connection failed"}')

    if echo "$RESPONSE" | jq -e '.error' > /dev/null 2>&1; then
        elapsed=$((SECONDS - START_TIME))
        echo "    [${elapsed}s] DAS not ready yet..."
        sleep "$POLL_INTERVAL"
        continue
    fi

    CATCH_UP_DONE=$(echo "$RESPONSE" | jq -r '.result.catch_up_done' 2>/dev/null || echo "null")
    IS_RUNNING=$(echo "$RESPONSE" | jq -r '.result.is_running' 2>/dev/null || echo "false")
    NETWORK_HEAD=$(echo "$RESPONSE" | jq -r '.result.network_head_height' 2>/dev/null || echo "?")
    SAMPLED_HEAD=$(echo "$RESPONSE" | jq -r '.result.head_of_sampled_chain' 2>/dev/null || echo "?")

    elapsed=$((SECONDS - START_TIME))
    das_elapsed=$((SECONDS - DAS_START))

    if [[ "$IS_RUNNING" == "true" && "$CATCH_UP_DONE" == "true" ]]; then
        echo ""
        echo "============================================================"
        echo "  SYNCED - Total: ${elapsed}s (headers: ${HEADER_ELAPSED}s, DAS: ${das_elapsed}s)"
        echo "  Network head: ${NETWORK_HEAD}"
        echo "  Sampled head: ${SAMPLED_HEAD}"
        echo "============================================================"
        exit 0
    fi

    remaining=$((deadline - SECONDS))
    echo "    [${elapsed}s] sampled: ${SAMPLED_HEAD} / network: ${NETWORK_HEAD} (running: ${IS_RUNNING}, ${remaining}s remaining)"
    sleep "$POLL_INTERVAL"
done

elapsed=$((SECONDS - START_TIME))
echo ""
echo "============================================================"
echo "  TIMEOUT after ${elapsed}s (limit: ${SYNC_TIMEOUT}s)"
echo "============================================================"
echo "--- Final DAS status ---"
curl -s -X POST "$RPC" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"das.SamplingStats","params":[]}' 2>/dev/null | jq .
exit 1
