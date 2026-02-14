#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# wait-for-sync.sh - Wait for Celestia light node to fully sync
# ============================================================
#
# Two-phase sync check:
#   Phase 1: header.SyncState - wait for header sync to complete
#   Phase 2: das.SamplingStats - wait for DAS sampling catch-up
#
# Connects to localhost:26658 (mapped from the celestia-light
# container's exposed port in docker-compose).
#
# Environment variables:
#   SYNC_TIMEOUT - seconds to wait (default: 300)

SYNC_TIMEOUT="${SYNC_TIMEOUT:-300}"
POLL_INTERVAL=10
LIGHTNODE_RPC="http://localhost:26658"

echo ">>> Waiting for light node sync (timeout: ${SYNC_TIMEOUT}s)..."

deadline=$((SECONDS + SYNC_TIMEOUT))
START_TIME=$SECONDS

# Phase 1: Wait for header sync to complete
# header.SyncState.end is "0001-01-01T..." while syncing, gets a real timestamp when done.
echo "    Phase 1: Header sync..."

HEADER_DONE=false
while [[ $SECONDS -lt $deadline ]]; do
    RESPONSE=$(curl -s -X POST "$LIGHTNODE_RPC" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"header.SyncState","params":[]}' \
        2>/dev/null || echo '{"error":"connection failed"}')

    if echo "$RESPONSE" | jq -e '.error' > /dev/null 2>&1; then
        ELAPSED=$((SECONDS - START_TIME))
        REMAINING=$((deadline - SECONDS))
        echo "    [${ELAPSED}s] Light node not ready yet (${REMAINING}s remaining)..."
        sleep "$POLL_INTERVAL"
        continue
    fi

    HEIGHT=$(echo "$RESPONSE" | jq -r '.result.height' 2>/dev/null || echo "0")
    TO_HEIGHT=$(echo "$RESPONSE" | jq -r '.result.to_height' 2>/dev/null || echo "0")
    END_TIME=$(echo "$RESPONSE" | jq -r '.result.end' 2>/dev/null || echo "0001")

    ELAPSED=$((SECONDS - START_TIME))

    if [[ "$END_TIME" != "0001"* && "$END_TIME" != "null" && -n "$END_TIME" ]]; then
        echo "    [${ELAPSED}s] Header sync complete (height: ${HEIGHT})"
        HEADER_DONE=true
        break
    fi

    REMAINING=$((deadline - SECONDS))
    echo "    [${ELAPSED}s] Headers: ${HEIGHT} / ${TO_HEIGHT} (${REMAINING}s remaining)"
    sleep "$POLL_INTERVAL"
done

if [[ "$HEADER_DONE" != "true" ]]; then
    echo "ERROR: Header sync did not complete within ${SYNC_TIMEOUT}s"
    curl -s -X POST "$LIGHTNODE_RPC" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"header.SyncState","params":[]}' 2>/dev/null | jq .
    exit 1
fi

# Phase 2: Wait for DAS sampling to catch up
echo "    Phase 2: DAS sampling catch-up..."

while [[ $SECONDS -lt $deadline ]]; do
    RESPONSE=$(curl -s -X POST "$LIGHTNODE_RPC" \
        -H "Content-Type: application/json" \
        -d '{"jsonrpc":"2.0","id":1,"method":"das.SamplingStats","params":[]}' \
        2>/dev/null || echo '{"error":"connection failed"}')

    if echo "$RESPONSE" | jq -e '.error' > /dev/null 2>&1; then
        ELAPSED=$((SECONDS - START_TIME))
        REMAINING=$((deadline - SECONDS))
        echo "    [${ELAPSED}s] DAS not ready yet (${REMAINING}s remaining)..."
        sleep "$POLL_INTERVAL"
        continue
    fi

    CATCH_UP_DONE=$(echo "$RESPONSE" | jq -r '.result.catch_up_done' 2>/dev/null || echo "null")
    NETWORK_HEAD=$(echo "$RESPONSE" | jq -r '.result.network_head_height' 2>/dev/null || echo "?")
    SAMPLED_HEAD=$(echo "$RESPONSE" | jq -r '.result.head_of_sampled_chain' 2>/dev/null || echo "?")
    IS_RUNNING=$(echo "$RESPONSE" | jq -r '.result.is_running' 2>/dev/null || echo "false")

    ELAPSED=$((SECONDS - START_TIME))

    if [[ "$IS_RUNNING" == "true" && "$CATCH_UP_DONE" == "true" ]]; then
        echo ">>> Light node synced! (network_head: ${NETWORK_HEAD}, sampled: ${SAMPLED_HEAD}, took: ${ELAPSED}s)"
        exit 0
    fi

    REMAINING=$((deadline - SECONDS))
    echo "    [${ELAPSED}s] Sampled: ${SAMPLED_HEAD} / ${NETWORK_HEAD} (running: ${IS_RUNNING}, ${REMAINING}s remaining)"
    sleep "$POLL_INTERVAL"
done

echo "ERROR: DAS sampling did not complete within ${SYNC_TIMEOUT}s"
echo "--- Final sync status ---"
curl -s -X POST "$LIGHTNODE_RPC" \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":1,"method":"das.SamplingStats","params":[]}' 2>/dev/null | jq .
exit 1
