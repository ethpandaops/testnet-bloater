// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title BloaterStorage
/// @notice Generic EIP-7702 compatible key-value storage for testnet bloater.
/// Deployed as delegation target for a wallet EOA. Storage lives at the EOA address.
/// Only the EOA itself (via self-transfer) can update state.
/// Keys are computed off-chain as keccak256(seed, name) to allow fresh counters.
contract BloaterStorage {
    mapping(bytes32 => uint256) public store;

    modifier onlySelf() {
        require(msg.sender == address(this), "only self");
        _;
    }

    receive() external payable {}

    fallback() external payable {}

    function set(bytes32 key, uint256 value) external onlySelf {
        store[key] = value;
    }

    function setMultiple(
        bytes32[] calldata keys,
        uint256[] calldata values
    ) external onlySelf {
        require(keys.length == values.length, "length mismatch");
        for (uint256 i = 0; i < keys.length; i++) {
            store[keys[i]] = values[i];
        }
    }

    function get(bytes32 key) external view returns (uint256) {
        return store[key];
    }

    function getMultiple(
        bytes32[] calldata keys
    ) external view returns (uint256[] memory) {
        uint256[] memory values = new uint256[](keys.length);
        for (uint256 i = 0; i < keys.length; i++) {
            values[i] = store[keys[i]];
        }
        return values;
    }

    /// @notice Execute multiple calls in a single transaction.
    /// Used to batch deposit txs into one self-transfer.
    function multicall(
        address[] calldata targets,
        bytes[] calldata calldatas,
        uint256[] calldata values
    ) external onlySelf {
        require(
            targets.length == calldatas.length &&
            targets.length == values.length,
            "length mismatch"
        );
        for (uint256 i = 0; i < targets.length; i++) {
            (bool success, bytes memory ret) = targets[i].call{value: values[i]}(calldatas[i]);
            require(success, string(abi.encodePacked("call ", i, " failed: ", ret)));
        }
    }

    /// @notice Execute fee-based calls where the fee is read on-chain once.
    /// Used for withdrawal requests where the fee changes between blocks.
    /// @param feeContract The system contract that returns fee via empty staticcall
    /// @param feeCalldatas Calldata for each fee-based call to feeContract
    /// @param feeLimit Maximum acceptable fee per call; reverts if exceeded
    /// @param extraTargets Additional call targets (e.g. storage updates)
    /// @param extraCalldatas Additional call data
    /// @param extraValues Additional call values
    function multicallFee(
        address feeContract,
        bytes[] calldata feeCalldatas,
        uint256 feeLimit,
        address[] calldata extraTargets,
        bytes[] calldata extraCalldatas,
        uint256[] calldata extraValues
    ) external onlySelf {
        // Query fee once - it's constant within a block
        (bool feeOk, bytes memory feeData) = feeContract.staticcall("");
        require(feeOk && feeData.length >= 32, "fee query failed");
        uint256 fee = abi.decode(feeData, (uint256));
        require(fee <= feeLimit, "fee exceeds limit");

        for (uint256 i = 0; i < feeCalldatas.length; i++) {
            (bool success, bytes memory ret) = feeContract.call{value: fee}(feeCalldatas[i]);
            require(success, string(abi.encodePacked("fee call ", i, " failed: ", ret)));
        }

        require(
            extraTargets.length == extraCalldatas.length &&
            extraTargets.length == extraValues.length,
            "extra length mismatch"
        );
        for (uint256 i = 0; i < extraTargets.length; i++) {
            (bool success, bytes memory ret) = extraTargets[i].call{value: extraValues[i]}(extraCalldatas[i]);
            require(success, string(abi.encodePacked("extra call ", i, " failed: ", ret)));
        }
    }
}
