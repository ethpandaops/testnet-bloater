#!/bin/bash
# Rate limit and queue check logic

# Calculate how many operations we can submit considering:
# per_day_limit, total_count, max_queue_size
calculate_allowed_count() {
    local config_file="$1"
    local network="$2"
    local op_type="$3"
    local current_total="$4"
    local current_daily="$5"
    local daily_ts="$6"
    local queue_size="$7"

    local per_day
    per_day=$(get_config "$config_file" ".networks.$network.operations.$op_type.per_day_limit")
    local total_limit
    total_limit=$(get_config "$config_file" ".networks.$network.operations.$op_type.total_count")
    local max_queue
    max_queue=$(get_config "$config_file" ".networks.$network.operations.$op_type.max_queue_size")

    # Check total lifetime limit
    local remaining_total=$((total_limit - current_total))
    if [ "$remaining_total" -le 0 ]; then
        echo 0
        return
    fi

    # Check daily limit (reset if new day)
    local today_start
    today_start=$(date -d "today 00:00:00" +%s 2>/dev/null || date -j -f "%Y%m%d" "$(date +%Y%m%d)" +%s 2>/dev/null || echo 0)
    local daily_remaining
    if [ "$daily_ts" -lt "$today_start" ]; then
        daily_remaining=$per_day
    else
        daily_remaining=$((per_day - current_daily))
    fi
    if [ "$daily_remaining" -le 0 ]; then
        echo 0
        return
    fi

    # Check queue limit
    local queue_remaining=$((max_queue - queue_size))
    if [ "$queue_remaining" -le 0 ]; then
        echo 0
        return
    fi

    # Return minimum of all limits
    local allowed=$remaining_total
    [ "$daily_remaining" -lt "$allowed" ] && allowed=$daily_remaining
    [ "$queue_remaining" -lt "$allowed" ] && allowed=$queue_remaining
    echo "$allowed"
}

# Process deposits for a network
process_deposits() {
    local network="$1"
    local state="$2"

    local total
    total=$(echo "$state" | jq -r '.totalDeposits')
    local last_index
    last_index=$(echo "$state" | jq -r '.lastDepositIndex')
    local daily
    daily=$(echo "$state" | jq -r '.dailyDepositCount')
    local daily_ts
    daily_ts=$(echo "$state" | jq -r '.dailyDepositTimestamp')

    local queue_size
    queue_size=$(get_deposit_queue_size "$dora_url")
    echo "  Deposit queue size: $queue_size"

    local allowed
    allowed=$(calculate_allowed_count \
        "$CONFIG_FILE" "$network" "deposits" \
        "$total" "$daily" "$daily_ts" "$queue_size")

    echo "  Allowed deposits: $allowed (total so far: $total, daily: $daily)"
    if [ "$allowed" -le 0 ]; then
        echo "  No deposits allowed at this time, skipping"
        return
    fi

    local deposit_amount
    deposit_amount=$(get_config "$CONFIG_FILE" ".networks.$network.operations.deposits.deposit_amount")
    local batch_size
    batch_size=$(get_config_default "$CONFIG_FILE" ".networks.$network.operations.deposits.batch_size" "16")
    local storage_interval
    storage_interval=$(get_config_default "$CONFIG_FILE" ".networks.$network.operations.deposits.storage_update_interval" "50")
    local deposit_contract
    deposit_contract=$(get_config "$CONFIG_FILE" ".networks.$network.deposit_contract")
    local fork_version
    fork_version=$(get_config_default "$CONFIG_FILE" ".networks.$network.genesis_fork_version" "0x00000000")

    echo "  Submitting $allowed deposits starting at index $last_index..."
    "$BLOATER_TOOL" run-deposits \
        --rpc-url "$rpc_url" \
        --private-key "$privkey" \
        --storage-seed "$storage_seed" \
        --mnemonic "$mnemonic" \
        --deposit-contract "$deposit_contract" \
        --deposit-amount "$deposit_amount" \
        --start-index "$last_index" \
        --count "$allowed" \
        --batch-size "$batch_size" \
        --storage-interval "$storage_interval" \
        --genesis-fork-version "$fork_version"
}

# Process withdrawal requests for a network
process_withdrawals() {
    local network="$1"
    local state="$2"

    local total
    total=$(echo "$state" | jq -r '.totalWithdrawalRequests')
    local cycle_idx
    cycle_idx=$(echo "$state" | jq -r '.lastWithdrawalCycleIndex')
    local daily
    daily=$(echo "$state" | jq -r '.dailyWithdrawalCount')
    local daily_ts
    daily_ts=$(echo "$state" | jq -r '.dailyWithdrawalTimestamp')
    local max_deposit_idx
    max_deposit_idx=$(echo "$state" | jq -r '.lastDepositIndex')

    if [ "$max_deposit_idx" -le 0 ]; then
        echo "  No deposits yet, skipping withdrawal requests"
        return
    fi

    local withdrawal_contract
    withdrawal_contract=$(get_config "$CONFIG_FILE" ".networks.$network.withdrawal_request_contract")

    local queue_size
    queue_size=$(get_withdrawal_queue_size "$rpc_url" "$withdrawal_contract")
    echo "  Withdrawal request queue size: $queue_size"

    local allowed
    allowed=$(calculate_allowed_count \
        "$CONFIG_FILE" "$network" "withdrawal_requests" \
        "$total" "$daily" "$daily_ts" "$queue_size")

    echo "  Allowed withdrawal requests: $allowed (total so far: $total, daily: $daily)"
    if [ "$allowed" -le 0 ]; then
        echo "  No withdrawal requests allowed at this time, skipping"
        return
    fi

    local withdraw_amount
    withdraw_amount=$(get_config_default "$CONFIG_FILE" ".networks.$network.operations.withdrawal_requests.withdraw_amount" "0")
    local fee_limit
    fee_limit=$(get_config_default "$CONFIG_FILE" ".networks.$network.operations.withdrawal_requests.fee_limit" "1000000000000")
    local batch_size
    batch_size=$(get_config_default "$CONFIG_FILE" ".networks.$network.operations.withdrawal_requests.batch_size" "16")
    local storage_interval
    storage_interval=$(get_config_default "$CONFIG_FILE" ".networks.$network.operations.withdrawal_requests.storage_update_interval" "50")

    echo "  Submitting $allowed withdrawal requests (cycle at $cycle_idx, range [0,$max_deposit_idx))..."
    "$BLOATER_TOOL" run-withdrawals \
        --rpc-url "$rpc_url" \
        --private-key "$privkey" \
        --storage-seed "$storage_seed" \
        --mnemonic "$mnemonic" \
        --withdrawal-request-contract "$withdrawal_contract" \
        --withdraw-amount "$withdraw_amount" \
        --fee-limit "$fee_limit" \
        --start-index 0 \
        --max-index "$max_deposit_idx" \
        --cycle-start-index "$cycle_idx" \
        --count "$allowed" \
        --batch-size "$batch_size" \
        --storage-interval "$storage_interval"
}
