#!/usr/bin/env bash
set -euo pipefail

# ============================================================
# check-balance.sh - Verify Celestia wallet has sufficient funds
# ============================================================
#
# Environment variables:
#   CELESTIA_NETWORK - "arabica" or "mocha" (required)
#   CELESTIA_ADDRESS - Celestia address to check (required)
#   MIN_BALANCE_UTIA - Minimum balance in utia (default: 50000 = 0.05 TIA)

CELESTIA_NETWORK="${CELESTIA_NETWORK:?CELESTIA_NETWORK must be set}"
CELESTIA_ADDRESS="${CELESTIA_ADDRESS:?CELESTIA_ADDRESS must be set}"
MIN_BALANCE_UTIA="${MIN_BALANCE_UTIA:-50000}"

# Network-specific REST API endpoints
case "$CELESTIA_NETWORK" in
    arabica)
        API_URL="https://api.celestia-arabica-11.com"
        ;;
    mocha)
        API_URL="https://api-mocha.pops.one"
        ;;
    *)
        echo "WARNING: Unknown network $CELESTIA_NETWORK, skipping balance check"
        exit 0
        ;;
esac

echo ">>> Checking balance for ${CELESTIA_ADDRESS} on ${CELESTIA_NETWORK}..."

RESPONSE=$(curl -s "${API_URL}/cosmos/bank/v1beta1/balances/${CELESTIA_ADDRESS}" 2>/dev/null || echo '{}')
BALANCE=$(echo "$RESPONSE" | jq -r '.balances[]? | select(.denom=="utia") | .amount' 2>/dev/null || echo "0")

if [[ -z "$BALANCE" || "$BALANCE" == "null" ]]; then
    BALANCE="0"
fi

# Convert to TIA for display
TIA_BALANCE=$(awk "BEGIN {printf \"%.6f\", $BALANCE / 1000000}")

if [[ "$BALANCE" -lt "$MIN_BALANCE_UTIA" ]]; then
    echo "WARNING: Low balance! ${TIA_BALANCE} TIA (${BALANCE} utia) < minimum ${MIN_BALANCE_UTIA} utia"
    echo "  Fund address: ${CELESTIA_ADDRESS}"
    if [[ "${GITHUB_ACTIONS:-}" == "true" ]]; then
        echo "::warning::Celestia wallet balance low: ${TIA_BALANCE} TIA. Blob submissions may fail."
    fi
    exit 1
else
    echo ">>> Balance OK: ${TIA_BALANCE} TIA (${BALANCE} utia)"
fi
