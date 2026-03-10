#!/bin/bash
# Queue size helpers

# Get deposit queue size from Dora API
get_deposit_queue_size() {
    local dora_url="$1"
    local response
    response=$(curl -sf "${dora_url}/api/v1/deposits/queue?limit=1" 2>/dev/null) || {
        echo "0"
        return
    }
    echo "$response" | jq -r '.data.total_count // 0'
}

# Get withdrawal request queue size from the system contract storage.
# Queue size = tail (slot 3) - head (slot 2).
get_withdrawal_queue_size() {
    local rpc_url="$1"
    local contract="$2"

    local head_hex tail_hex
    head_hex=$(curl -sf "$rpc_url" \
        -H "Content-Type: application/json" \
        -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_getStorageAt\",\"params\":[\"$contract\",\"0x2\",\"latest\"]}" \
        2>/dev/null | jq -r '.result // "0x0"') || {
        echo "0"
        return
    }
    tail_hex=$(curl -sf "$rpc_url" \
        -H "Content-Type: application/json" \
        -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"eth_getStorageAt\",\"params\":[\"$contract\",\"0x3\",\"latest\"]}" \
        2>/dev/null | jq -r '.result // "0x0"') || {
        echo "0"
        return
    }

    local head=$((head_hex))
    local tail=$((tail_hex))
    local size=$((tail - head))
    if [ "$size" -lt 0 ]; then
        size=0
    fi
    echo "$size"
}
