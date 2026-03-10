#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"
CONFIG_FILE="${CONFIG_FILE:-$REPO_DIR/config.yaml}"
BLOATER_TOOL="${BLOATER_TOOL:-$REPO_DIR/bloater-tool}"

source "$SCRIPT_DIR/lib/config.sh"
source "$SCRIPT_DIR/lib/dora.sh"
source "$SCRIPT_DIR/lib/limits.sh"

# Ensure bloater-tool is built
if [ ! -f "$BLOATER_TOOL" ]; then
    echo "Building bloater-tool..."
    cd "$REPO_DIR" && go build -o bloater-tool ./cmd/
    cd "$SCRIPT_DIR"
fi

# Check dependencies
for dep in yq jq curl; do
    if ! command -v "$dep" &>/dev/null; then
        echo "Error: $dep is required but not found"
        exit 1
    fi
done

echo "Using config: $CONFIG_FILE"

# Get list of networks from config
networks=$(get_networks "$CONFIG_FILE")

for network in $networks; do
    echo ""
    echo "=== Processing network: $network ==="

    rpc_url=$(get_config "$CONFIG_FILE" ".networks.$network.rpc_url")
    dora_url=$(get_config "$CONFIG_FILE" ".networks.$network.dora_url")
    privkey=$(resolve_env_var "$(get_config "$CONFIG_FILE" ".networks.$network.wallet_privkey")")
    mnemonic=$(resolve_env_var "$(get_config "$CONFIG_FILE" ".networks.$network.validator_mnemonic")")
    storage_seed=$(get_config_default "$CONFIG_FILE" ".networks.$network.storage_seed" "v1")

    if [ -z "$privkey" ]; then
        echo "  WARNING: No private key configured for $network, skipping"
        continue
    fi
    if [ -z "$mnemonic" ]; then
        echo "  WARNING: No mnemonic configured for $network, skipping"
        continue
    fi

    # Read current on-chain state
    echo "  Reading on-chain state..."
    state=$("$BLOATER_TOOL" read-state \
        --rpc-url "$rpc_url" \
        --private-key "$privkey" \
        --storage-seed "$storage_seed" 2>/dev/null) || {
        echo "  WARNING: Failed to read state (tracker may not be deployed yet)"
        echo "  Run: $BLOATER_TOOL deploy-tracker --rpc-url $rpc_url --private-key <key>"
        continue
    }

    echo "  State: $state"

    # Process deposits if enabled
    if is_enabled "$CONFIG_FILE" "$network" "deposits"; then
        echo "  --- Deposits ---"
        process_deposits "$network" "$state" || {
            echo "  ERROR: Deposit processing failed"
        }
    fi

    # Process withdrawal requests if enabled
    if is_enabled "$CONFIG_FILE" "$network" "withdrawal_requests"; then
        echo "  --- Withdrawal Requests ---"
        process_withdrawals "$network" "$state" || {
            echo "  ERROR: Withdrawal request processing failed"
        }
    fi

    echo "=== Done with $network ==="
done

echo ""
echo "All networks processed."
