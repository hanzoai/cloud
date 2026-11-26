// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package governance implements HIP-1352 open protocol governance, timelocked
// treasury controls, bounded emergency safeguards, and deterministic
// forkability for Hanzo Cloud without any permanent Hanzo-admin override.
//
// Design principles (aligned with OpenZeppelin Governor/Timelock and Zoo DAO OS):
//
//  1. Zero permanent admin override: there is no privileged bypass path that
//     can alter protocol parameters, drain treasury funds, weaken quorum or
//     timelock floors, or cancel valid governance outcomes.
//  2. Snapshot-locked voting power: each proposal binds to an immutable
//     delegation/power checkpoint captured at proposal creation, preventing
//     flash-loan and post-snapshot transfer inflation.
//  3. Cryptographic authenticity & replay resistance: every proposal, vote,
//     delegation, queue/execute/cancel transition, emergency action, and
//     sovereign export bundle carries a domain-separated Ed25519 signature
//     bound to ProtocolID and a per-member unique nonce.
//  4. Mandatory timelock & strict state machine: proposals move strictly
//     through Pending -> Active -> (Defeated | Succeeded) -> Queued ->
//     (Executed | Canceled | Expired) with non-zero MinTimelockDelay and
//     GracePeriod.
//  5. Bounded emergency safeguards: emergency guardians may only trigger
//     time-bounded subsystem pauses or circuit breakers subject to hard max
//     duration, cooldowns, automatic expiry, and DAO ratification. Guardians
//     cannot pause governance itself, drain treasury, or lower safety floors.
//  6. Deterministic forkability: complete protocol state and hash-chained
//     JSONL audit ledger export into a signed SovereignBundle that any
//     independent community can verify and fork onto a new ProtocolID with
//     zero Hanzo-operated infrastructure.
package governance
