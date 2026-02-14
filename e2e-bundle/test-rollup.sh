#!/bin/bash
# =============================================================================
# test-rollup.sh - Test the OP Stack + Celestia deployment
# Parameterized for multi-network support (Arabica, Mocha)
# =============================================================================
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# CI mode detection
CI_MODE="false"
if [[ "${GITHUB_ACTIONS:-}" == "true" ]] || [[ "${CI:-}" == "true" ]]; then
    CI_MODE="true"
fi

# ============================================================
# Network configuration (parameterized)
# ============================================================
CELESTIA_NETWORK="${CELESTIA_NETWORK:-arabica}"

case "$CELESTIA_NETWORK" in
    arabica)
        NETWORK_DISPLAY="Arabica-11"
        CELESTIA_RPC="${CELESTIA_RPC:-https://rpc.celestia-arabica-11.com}"
        CELESTIA_API="https://api.celestia-arabica-11.com"
        CELESTIA_FAUCET="https://arabica.celenium.io/faucet"
        CELESTIA_EXPLORER="https://arabica.celenium.io/"
        ;;
    mocha)
        NETWORK_DISPLAY="Mocha-4"
        CELESTIA_RPC="${CELESTIA_RPC:-https://rpc-mocha.pops.one:443}"
        CELESTIA_API="https://api-mocha.pops.one"
        CELESTIA_FAUCET="https://faucet.celestia-mocha.com/"
        CELESTIA_EXPLORER="https://mocha.celenium.io/"
        ;;
    *)
        echo "ERROR: Unknown CELESTIA_NETWORK: $CELESTIA_NETWORK (expected: arabica or mocha)"
        exit 1
        ;;
esac

CELESTIA_ADDRESS="${CELESTIA_ADDRESS:-celestia1fg8nqd2fnurnpd06wn75fn9sqqs3hdr06r8r0n}"

# Port configuration (matches docker-compose.yml)
L1_RPC_PORT=9546
L2_RPC_PORT=8545
L2_WS_PORT=8546
OP_NODE_PORT=9545
OP_BATCHER_PORT=8548
OP_PROPOSER_PORT=8560
OP_ALT_DA_PORT=3100
OP_ALT_DA_METRICS_PORT=6060

# URLs
L1_RPC_URL="http://127.0.0.1:${L1_RPC_PORT}"
L2_RPC_URL="http://127.0.0.1:${L2_RPC_PORT}"
OP_ALT_DA_URL="http://127.0.0.1:${OP_ALT_DA_PORT}"

# Test accounts (Anvil defaults)
DEPLOYER_ADDRESS="0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266"
DEPLOYER_KEY="0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
TEST_RECIPIENT="0x70997970C51812dc3A010C7d01b50e0d17dc79C8"

PASSED=0
FAILED=0
SKIPPED=0

# -----------------------------------------------------------------------------
# Helper functions
# -----------------------------------------------------------------------------

print_header() {
    echo ""
    echo -e "${BLUE}============================================================${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}============================================================${NC}"
}

print_subheader() {
    echo ""
    echo -e "${CYAN}--- $1 ---${NC}"
}

log_step() {
    echo -e "${YELLOW}>>> $1${NC}"
}

log_success() {
    echo -e "${GREEN}$1${NC}"
}

log_error() {
    echo -e "${RED}$1${NC}"
}

log_info() {
    echo -e "    $1"
}

test_result() {
    local name="$1"
    local result="$2"
    local details="${3:-}"

    if [[ "$result" == "pass" ]]; then
        log_success "  ✓ $name"
        PASSED=$((PASSED + 1))
    elif [[ "$result" == "skip" ]]; then
        echo -e "  ${YELLOW}⊘ $name (skipped)${NC}"
        SKIPPED=$((SKIPPED + 1))
    else
        log_error "  ✗ $name"
        if [[ -n "$details" ]]; then
            log_info "    $details"
        fi
        FAILED=$((FAILED + 1))
    fi
}

# -----------------------------------------------------------------------------
# Pre-flight checks
# -----------------------------------------------------------------------------

print_header "OP Stack + Celestia ${NETWORK_DISPLAY} Test Suite"

echo ""
echo "Testing deployment at: $(pwd)"
echo ""

# Check if docker compose is running
if ! docker compose ps 2>/dev/null | grep -q "anvil"; then
    echo "Docker compose services don't appear to be running."
    echo "Start with:"
    echo "  docker compose up -d"
    exit 1
fi

echo "Mode: OP Stack + Celestia ${NETWORK_DISPLAY} DA"
echo ""

# -----------------------------------------------------------------------------
# Test 1: Check all services are running
# -----------------------------------------------------------------------------

print_subheader "Service Availability"

log_step "Checking service availability..."

# L1 (Anvil)
if curl -s --max-time 5 -o /dev/null "${L1_RPC_URL}"; then
    L1_BLOCK=$(cast block-number --rpc-url "${L1_RPC_URL}" 2>/dev/null || echo "N/A")
    L1_CHAIN_ID=$(cast chain-id --rpc-url "${L1_RPC_URL}" 2>/dev/null || echo "N/A")
    test_result "L1 (Anvil) - Chain: ${L1_CHAIN_ID}, Block: ${L1_BLOCK}" "pass"
else
    test_result "L1 (Anvil)" "fail" "Not accessible at ${L1_RPC_URL}"
fi

# L2 (OP-Geth)
if curl -s --max-time 5 -o /dev/null "${L2_RPC_URL}"; then
    L2_BLOCK=$(cast block-number --rpc-url "${L2_RPC_URL}" 2>/dev/null || echo "N/A")
    L2_CHAIN_ID=$(cast chain-id --rpc-url "${L2_RPC_URL}" 2>/dev/null || echo "N/A")
    test_result "L2 (OP-Geth) - Chain: ${L2_CHAIN_ID}, Block: ${L2_BLOCK}" "pass"
else
    test_result "L2 (OP-Geth)" "fail" "Not accessible at ${L2_RPC_URL}"
fi

# OP-Node
if curl -s --max-time 5 -o /dev/null "http://127.0.0.1:${OP_NODE_PORT}"; then
    test_result "OP-Node" "pass"
else
    test_result "OP-Node" "fail" "Not accessible at port ${OP_NODE_PORT}"
fi

# OP-Batcher
if curl -s --max-time 5 -o /dev/null "http://127.0.0.1:${OP_BATCHER_PORT}/healthz"; then
    test_result "OP-Batcher" "pass"
else
    test_result "OP-Batcher" "fail" "Not accessible at port ${OP_BATCHER_PORT}"
fi

# OP-Proposer
if curl -s --max-time 5 -o /dev/null "http://127.0.0.1:${OP_PROPOSER_PORT}/healthz"; then
    test_result "OP-Proposer" "pass"
else
    test_result "OP-Proposer" "fail" "Not accessible at port ${OP_PROPOSER_PORT}"
fi

# OP-ALT-DA
if curl -s --max-time 5 -o /dev/null "${OP_ALT_DA_URL}/health"; then
    test_result "OP-ALT-DA (Celestia DA Server)" "pass"
else
    test_result "OP-ALT-DA" "fail" "Not accessible at ${OP_ALT_DA_URL}"
fi

# -----------------------------------------------------------------------------
# Test 2: Check Celestia connectivity and balance
# -----------------------------------------------------------------------------

print_subheader "Celestia ${NETWORK_DISPLAY} Integration"

log_step "Checking Celestia testnet connection..."

# Check Celestia balance (try celestia-appd first, fallback to REST API)
if command -v celestia-appd &> /dev/null; then
    BALANCE=$(celestia-appd query bank balances "${CELESTIA_ADDRESS}" \
        --node "${CELESTIA_RPC}" \
        --output json 2>/dev/null | jq -r '.balances[] | select(.denom=="utia") | .amount' || echo "0")

    BALANCE_TIA=$(echo "scale=6; $BALANCE / 1000000" | bc 2>/dev/null || echo "0")

    if [[ "$BALANCE" -gt 0 ]]; then
        test_result "Celestia balance: ${BALANCE_TIA} TIA" "pass"
    else
        test_result "Celestia balance" "fail" "No balance found. Visit ${CELESTIA_FAUCET}"
    fi
else
    # Fallback: REST API (works without celestia-appd)
    BALANCE=$(curl -s "${CELESTIA_API}/cosmos/bank/v1beta1/balances/${CELESTIA_ADDRESS}" 2>/dev/null \
        | jq -r '.balances[]? | select(.denom=="utia") | .amount' 2>/dev/null || echo "0")

    if [[ -n "$BALANCE" ]] && [[ "$BALANCE" != "null" ]] && [[ "$BALANCE" -gt 0 ]] 2>/dev/null; then
        BALANCE_TIA=$(echo "scale=6; $BALANCE / 1000000" | bc 2>/dev/null || echo "0")
        test_result "Celestia balance: ${BALANCE_TIA} TIA (via REST)" "pass"
    else
        test_result "Celestia balance" "fail" "No balance found. Visit ${CELESTIA_FAUCET}"
    fi
fi

# Check OP-ALT-DA logs for Celestia connectivity
DA_LOGS=$(docker compose logs op-alt-da --tail=50 2>/dev/null || echo "")
if echo "${DA_LOGS}" | grep -qi "${CELESTIA_NETWORK}\|celestia"; then
    test_result "OP-ALT-DA connected to ${NETWORK_DISPLAY}" "pass"
else
    test_result "OP-ALT-DA ${NETWORK_DISPLAY} connection" "fail" "No ${NETWORK_DISPLAY} references in logs"
fi

# -----------------------------------------------------------------------------
# Test 3: Verify contract deployments on L1
# -----------------------------------------------------------------------------

print_subheader "Contract Deployments on L1"

log_step "Checking deployed contracts..."

# Read addresses from addresses.json
if [[ -f "addresses.json" ]]; then
    DISPUTE_GAME_FACTORY=$(jq -r '.l1.DisputeGameFactoryProxy // empty' addresses.json 2>/dev/null)
    OPTIMISM_PORTAL=$(jq -r '.l1.OptimismPortalProxy // empty' addresses.json 2>/dev/null)
    L1_STANDARD_BRIDGE=$(jq -r '.l1.L1StandardBridgeProxy // empty' addresses.json 2>/dev/null)

    # Check DisputeGameFactory
    if [[ -n "$DISPUTE_GAME_FACTORY" ]] && [[ "$DISPUTE_GAME_FACTORY" != "null" ]]; then
        CODE=$(cast code "$DISPUTE_GAME_FACTORY" --rpc-url "$L1_RPC_URL" 2>/dev/null || echo "0x")
        if [[ "$CODE" != "0x" ]] && [[ ${#CODE} -gt 4 ]]; then
            test_result "DisputeGameFactory (${DISPUTE_GAME_FACTORY:0:10}...)" "pass"
        else
            test_result "DisputeGameFactory" "fail" "No code at $DISPUTE_GAME_FACTORY"
        fi
    fi

    # Check OptimismPortal
    if [[ -n "$OPTIMISM_PORTAL" ]] && [[ "$OPTIMISM_PORTAL" != "null" ]]; then
        CODE=$(cast code "$OPTIMISM_PORTAL" --rpc-url "$L1_RPC_URL" 2>/dev/null || echo "0x")
        if [[ "$CODE" != "0x" ]] && [[ ${#CODE} -gt 4 ]]; then
            test_result "OptimismPortal (${OPTIMISM_PORTAL:0:10}...)" "pass"
        else
            test_result "OptimismPortal" "fail" "No code at $OPTIMISM_PORTAL"
        fi
    fi

    # Check L1StandardBridge
    if [[ -n "$L1_STANDARD_BRIDGE" ]] && [[ "$L1_STANDARD_BRIDGE" != "null" ]]; then
        CODE=$(cast code "$L1_STANDARD_BRIDGE" --rpc-url "$L1_RPC_URL" 2>/dev/null || echo "0x")
        if [[ "$CODE" != "0x" ]] && [[ ${#CODE} -gt 4 ]]; then
            test_result "L1StandardBridge (${L1_STANDARD_BRIDGE:0:10}...)" "pass"
        else
            test_result "L1StandardBridge" "fail" "No code at $L1_STANDARD_BRIDGE"
        fi
    fi
else
    test_result "addresses.json" "fail" "File not found"
fi

# -----------------------------------------------------------------------------
# Test 4: Check L2 sync and block production
# -----------------------------------------------------------------------------

print_subheader "L2 Sync Status"

log_step "Checking L2 block production..."

# Get current block
L2_BLOCK_START=$(cast block-number --rpc-url "${L2_RPC_URL}" 2>/dev/null || echo "0")
echo "    Current L2 block: ${L2_BLOCK_START}"

# Wait and check for new blocks
sleep 3
L2_BLOCK_END=$(cast block-number --rpc-url "${L2_RPC_URL}" 2>/dev/null || echo "0")

if [[ "$L2_BLOCK_END" -gt "$L2_BLOCK_START" ]]; then
    BLOCKS_PRODUCED=$((L2_BLOCK_END - L2_BLOCK_START))
    test_result "L2 producing blocks (+${BLOCKS_PRODUCED} in 3s)" "pass"
elif [[ "$L2_BLOCK_END" -gt 0 ]]; then
    test_result "L2 has blocks (${L2_BLOCK_END})" "pass"
    log_info "Block production may be paused (no new txs)"
else
    test_result "L2 block production" "fail" "No blocks produced"
fi

# Check gas price
GAS_PRICE=$(cast gas-price --rpc-url "${L2_RPC_URL}" 2>/dev/null || echo "N/A")
if [[ "$GAS_PRICE" != "N/A" ]]; then
    GAS_GWEI=$(echo "scale=4; $GAS_PRICE / 1000000000" | bc 2>/dev/null || echo "$GAS_PRICE wei")
    log_info "L2 gas price: ${GAS_GWEI} gwei"
fi

# -----------------------------------------------------------------------------
# Test 5: Check OP-Batcher activity (posting to Celestia)
# -----------------------------------------------------------------------------

print_subheader "OP-Batcher Activity (Celestia DA)"

log_step "Checking OP-Batcher status..."

# Get recent batcher logs
BATCHER_LOGS=$(docker compose logs op-batcher --tail=200 2>/dev/null || echo "")

# Check for Alt-DA references
if echo "${BATCHER_LOGS}" | grep -qi "altda\|da-server\|alt-da"; then
    test_result "OP-Batcher using Alt-DA" "pass"
else
    test_result "OP-Batcher Alt-DA configuration" "fail" "No Alt-DA references in logs"
fi

# Check for batch submissions
if echo "${BATCHER_LOGS}" | grep -qi "published\|batch sent\|l1 transaction"; then
    test_result "OP-Batcher posting batches" "pass"
else
    log_info "  No batch submissions yet (may need L2 transactions)"
fi

# Check for DA server calls
if echo "${BATCHER_LOGS}" | grep -qi "da.*server\|commitment"; then
    test_result "OP-Batcher communicating with DA server" "pass"
fi

# Check for errors
if echo "${BATCHER_LOGS}" | grep -qi "error.*da\|failed.*da"; then
    DA_ERROR=$(echo "${BATCHER_LOGS}" | grep -i "error.*da\|failed.*da" | tail -1)
    test_result "No DA errors" "fail" "${DA_ERROR:0:100}..."
else
    test_result "No critical DA errors" "pass"
fi

# -----------------------------------------------------------------------------
# Test 6: Check OP-ALT-DA activity (Celestia submissions)
# -----------------------------------------------------------------------------

print_subheader "OP-ALT-DA Server Activity"

log_step "Checking Celestia blob submissions..."

DA_LOGS=$(docker compose logs op-alt-da --tail=200 2>/dev/null || echo "")

# Check for Celestia submissions
if echo "${DA_LOGS}" | grep -qi "submitted.*celestia\|posted.*blob\|celestia.*success\|blob successfully submitted"; then
    SUBMIT_COUNT=$(echo "${DA_LOGS}" | grep -ci "submitted.*celestia\|posted.*blob\|blob successfully submitted" 2>/dev/null || echo "0")
    test_result "Blobs submitted to Celestia (${SUBMIT_COUNT} found)" "pass"
else
    log_info "  No Celestia submissions yet (waiting for batch data)"
fi

# Check for successful reads
if echo "${DA_LOGS}" | grep -qi "retrieved.*blob\|read.*celestia"; then
    test_result "OP-ALT-DA reading from Celestia" "pass"
fi

# Check for Celestia errors
if echo "${DA_LOGS}" | grep -qi "error.*celestia\|failed.*submit"; then
    CELESTIA_ERROR=$(echo "${DA_LOGS}" | grep -i "error.*celestia\|failed.*submit" | tail -1)
    test_result "No Celestia errors" "fail" "${CELESTIA_ERROR:0:100}..."
else
    test_result "No Celestia submission errors" "pass"
fi

# -----------------------------------------------------------------------------
# Test 7: Test L2 transaction
# -----------------------------------------------------------------------------

print_subheader "L2 Transaction Test"

log_step "Testing L2 transaction capability..."

if command -v cast &> /dev/null; then
    # Check balance first
    BALANCE=$(cast balance "$DEPLOYER_ADDRESS" --rpc-url "${L2_RPC_URL}" 2>/dev/null || echo "0")
    BALANCE_ETH=$(echo "scale=6; $BALANCE / 1000000000000000000" | bc 2>/dev/null || echo "0")
    log_info "Deployer L2 balance: ${BALANCE_ETH} ETH"

    # Test transaction if we have balance
    MIN_BALANCE=1000000000000000
    if [[ "$BALANCE" -lt "$MIN_BALANCE" ]]; then
        # Auto-bridge ETH from L1 to L2 via OptimismPortal
        log_step "Bridging 1 ETH from L1 to L2..."

        if [[ -f "addresses.json" ]]; then
            OPTIMISM_PORTAL=$(jq -r '.chain_state.OptimismPortalProxy // empty' addresses.json 2>/dev/null)

            if [[ -n "$OPTIMISM_PORTAL" ]] && [[ "$OPTIMISM_PORTAL" != "null" ]]; then
                BRIDGE_RESULT=$(cast send "$OPTIMISM_PORTAL" \
                    "depositTransaction(address,uint256,uint64,bool,bytes)" \
                    "$DEPLOYER_ADDRESS" \
                    0 \
                    100000 \
                    false \
                    "0x" \
                    --rpc-url "${L1_RPC_URL}" \
                    --private-key "${DEPLOYER_KEY}" \
                    --value 1ether \
                    2>&1) || true

                if echo "${BRIDGE_RESULT}" | grep -q "transactionHash\|0x[a-fA-F0-9]\{64\}"; then
                    BRIDGE_TX=$(echo "${BRIDGE_RESULT}" | grep -oE "0x[a-fA-F0-9]{64}" | head -1)
                    test_result "Bridge TX sent to OptimismPortal: ${BRIDGE_TX:0:18}..." "pass"

                    # Wait for L2 to process the deposit
                    log_info "Waiting for L2 to process deposit (up to 60s)..."
                    for i in {1..30}; do
                        sleep 2
                        NEW_BALANCE=$(cast balance "$DEPLOYER_ADDRESS" --rpc-url "${L2_RPC_URL}" 2>/dev/null || echo "0")
                        if [[ "$NEW_BALANCE" -gt "$MIN_BALANCE" ]]; then
                            NEW_BALANCE_ETH=$(echo "scale=6; $NEW_BALANCE / 1000000000000000000" | bc 2>/dev/null || echo "0")
                            test_result "Deposit confirmed on L2 (balance: ${NEW_BALANCE_ETH} ETH)" "pass"
                            BALANCE="$NEW_BALANCE"
                            break
                        fi
                        echo -n "."
                    done
                    echo ""

                    if [[ "$BALANCE" -lt "$MIN_BALANCE" ]]; then
                        test_result "Deposit processing" "fail" "Timeout waiting for L2 balance"
                    fi
                else
                    test_result "Bridge transaction" "fail" "Could not send to OptimismPortal"
                    log_info "$BRIDGE_RESULT"
                fi
            else
                test_result "Bridge ETH" "skip"
                log_info "OptimismPortal address not found in addresses.json"
            fi
        else
            test_result "Bridge ETH" "skip"
            log_info "addresses.json not found"
        fi
    fi

    # Now test L2 transaction if we have balance
    if [[ "$BALANCE" -gt "$MIN_BALANCE" ]]; then
        # Estimate gas first
        GAS_ESTIMATE=$(cast estimate \
            --rpc-url "${L2_RPC_URL}" \
            --from "$DEPLOYER_ADDRESS" \
            "$TEST_RECIPIENT" \
            --value 1000 2>/dev/null || echo "0")

        if [[ "$GAS_ESTIMATE" -gt 0 ]]; then
            test_result "Gas estimation works (${GAS_ESTIMATE} gas)" "pass"

            # Send actual transaction
            TX_RESULT=$(cast send \
                --rpc-url "${L2_RPC_URL}" \
                --private-key "${DEPLOYER_KEY}" \
                --value 0.001ether \
                "${TEST_RECIPIENT}" \
                2>&1) || true

            if echo "${TX_RESULT}" | grep -q "transactionHash"; then
                TX_HASH=$(echo "${TX_RESULT}" | grep "transactionHash" | awk '{print $2}')
                test_result "L2 transaction sent: ${TX_HASH:0:18}..." "pass"

                # Wait for receipt
                sleep 2
                RECEIPT=$(cast receipt "$TX_HASH" --rpc-url "${L2_RPC_URL}" 2>/dev/null || echo "")
                if echo "$RECEIPT" | grep -q "status.*1\|status.*true\|blockNumber"; then
                    test_result "L2 transaction confirmed" "pass"
                fi
            else
                test_result "L2 transaction" "fail" "${TX_RESULT:0:100}"
            fi
        else
            test_result "Gas estimation" "fail" "Could not estimate gas"
        fi
    else
        test_result "L2 transaction" "skip"
        log_info "Insufficient L2 balance (need to bridge from L1 or fund genesis)"
    fi
else
    test_result "L2 transaction" "skip"
    log_info "cast not installed"
fi

# -----------------------------------------------------------------------------
# Summary
# -----------------------------------------------------------------------------

print_header "Test Summary"

TOTAL=$((PASSED + FAILED + SKIPPED))

echo ""
echo "  Passed:  ${PASSED}"
echo "  Failed:  ${FAILED}"
echo "  Skipped: ${SKIPPED}"
echo "  Total:   ${TOTAL}"
echo ""

# CI annotations
if [[ "$CI_MODE" == "true" ]]; then
    if [[ ${FAILED} -gt 0 ]]; then
        echo "::error::E2E ${NETWORK_DISPLAY}: ${FAILED}/${TOTAL} tests failed"
    else
        echo "::notice::E2E ${NETWORK_DISPLAY}: ${PASSED}/${TOTAL} tests passed"
    fi
fi

if [[ ${FAILED} -eq 0 ]]; then
    log_success "All tests passed! OP Stack + ${NETWORK_DISPLAY} bundle is working!"

    # Only show interactive info when not in CI
    if [[ "$CI_MODE" != "true" ]]; then
        echo ""
        echo -e "${CYAN}Endpoints:${NC}"
        echo "  L1 RPC (Anvil):         ${L1_RPC_URL}"
        echo "  L2 RPC (OP-Geth):       ${L2_RPC_URL}"
        echo "  L2 WebSocket:           ws://127.0.0.1:${L2_WS_PORT}"
        echo "  OP-Node:                http://127.0.0.1:${OP_NODE_PORT}"
        echo "  OP-Batcher:             http://127.0.0.1:${OP_BATCHER_PORT}"
        echo "  OP-Proposer:            http://127.0.0.1:${OP_PROPOSER_PORT}"
        echo "  OP-ALT-DA:              ${OP_ALT_DA_URL}"
        echo "  Celestia ${NETWORK_DISPLAY}:    ${CELESTIA_RPC}"
        echo ""
        echo -e "${CYAN}Celestia Info:${NC}"
        echo "  Address:                ${CELESTIA_ADDRESS}"
        echo "  Faucet:                 ${CELESTIA_FAUCET}"
        echo "  Explorer:               ${CELESTIA_EXPLORER}"
        echo ""
        echo -e "${CYAN}Useful commands:${NC}"
        echo "  docker compose logs -f op-batcher      # Watch batch submissions"
        echo "  docker compose logs -f op-alt-da       # Watch Celestia DA activity"
        echo "  docker compose logs -f op-node         # Watch L2 derivation"
        echo "  cast block-number --rpc-url ${L2_RPC_URL}  # Get L2 block"
        echo ""
    fi
    exit 0
else
    log_error "Some tests failed."
    if [[ "$CI_MODE" != "true" ]]; then
        echo ""
        echo "  docker compose logs op-batcher"
        echo "  docker compose logs op-alt-da"
        echo "  docker compose logs op-node"
        echo "  docker compose logs op-geth"
        echo "  docker compose ps  # Check service status"
        echo ""
    fi
    exit 1
fi
