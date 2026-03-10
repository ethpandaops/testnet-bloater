#!/bin/bash
# Config parsing helpers using yq (mikefarah/yq v4+)

get_networks() {
    yq '.networks | keys | .[]' "$1"
}

get_config() {
    yq "$2" "$1"
}

get_config_default() {
    local val
    val=$(yq "$2" "$1")
    if [ "$val" = "null" ] || [ -z "$val" ]; then
        echo "$3"
    else
        echo "$val"
    fi
}

resolve_env_var() {
    local val="$1"
    if [[ "$val" == \$* ]]; then
        local var_name="${val#\$}"
        echo "${!var_name:-}"
    else
        echo "$val"
    fi
}

is_enabled() {
    local enabled
    enabled=$(get_config "$1" ".networks.$2.operations.$3.enabled")
    [ "$enabled" = "true" ]
}
