// SPDX-License-Identifier: MIT
pragma solidity ^0.8.24;

/// @title TreasuryAnchor
/// @notice Minimal, immutable on-chain witness for the Hanzo treasury's off-chain
/// double-entry ledger. The cloud treasury periodically commits a deterministic root
/// (SHA-256 hash-chain of the whole journal + the reserve balance — ledger.Root) here.
/// A change to ANY historical posting changes the root, so the sequence of anchored
/// roots makes the off-chain books tamper-evident and EVM-auditable. Deployed on the
/// Hanzo L1 (chain 36963).
///
/// Design: as small as it can be and still be correct. One owner (the KMS-held
/// treasury signer) may anchor; anyone may read. No upgradeability, no admin knobs,
/// no token — the contract's only job is to store an append-only, timestamped
/// sequence of roots and emit an event per anchor. Ownership is transferable once
/// (e.g. EOA → a threshold signer) but nothing else mutates.
contract TreasuryAnchor {
    /// @notice The account allowed to anchor (the treasury's KMS signer).
    address public owner;

    /// @notice Total number of roots anchored.
    uint256 public count;

    /// @notice The most recently anchored root and the block/time it landed.
    bytes32 public latestRoot;
    uint256 public latestBlock;
    uint256 public latestTime;

    /// @notice Emitted on every anchor. `seq` is the 1-based anchor index.
    event Anchored(uint256 indexed seq, bytes32 indexed root, uint256 blockNumber, uint256 timestamp);
    event OwnerChanged(address indexed previousOwner, address indexed newOwner);

    error NotOwner();
    error ZeroRoot();
    error ZeroAddress();

    modifier onlyOwner() {
        if (msg.sender != owner) revert NotOwner();
        _;
    }

    constructor() {
        owner = msg.sender;
        emit OwnerChanged(address(0), msg.sender);
    }

    /// @notice Commit a ledger root. Reverts on the zero root (a misconfigured caller).
    /// @param root the SHA-256 ledger-commitment (ledger.Root) to anchor.
    function anchor(bytes32 root) external onlyOwner {
        if (root == bytes32(0)) revert ZeroRoot();
        count += 1;
        latestRoot = root;
        latestBlock = block.number;
        latestTime = block.timestamp;
        emit Anchored(count, root, block.number, block.timestamp);
    }

    /// @notice Transfer the anchoring right (e.g. EOA → threshold signer). One-way; the
    /// new owner is the only account that can anchor or transfer thereafter.
    function transferOwnership(address newOwner) external onlyOwner {
        if (newOwner == address(0)) revert ZeroAddress();
        emit OwnerChanged(owner, newOwner);
        owner = newOwner;
    }

    /// @notice The latest anchor, for cheap off-chain verification.
    function latest() external view returns (bytes32 root, uint256 blockNumber, uint256 timestamp, uint256 total) {
        return (latestRoot, latestBlock, latestTime, count);
    }
}
