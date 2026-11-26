// SPDX-License-Identifier: Apache-2.0

// Package preble implements a clean-room, Apache-2.0 distributed prompt-locality-aware
// GPU placement scheduler inspired by Preble (Srivatsa et al., ICLR 2025: "Preble:
// Efficient Distributed Prompt Scheduling for LLM Serving").
//
// Core components:
//   - Tenant-isolated Radix/Block Prefix Index (prefix.go): tracks block-aligned KV
//     prefix hashes per GPU replica with exact KV-cache memory budgets, pin-aware LRU
//     eviction, bounded tree pruning, and strict per-tenant / per-ACL namespace
//     isolation (zero cross-tenant KV hit or timing/existence oracle).
//   - Preble E2 Scheduler & Matched Baselines (scheduler.go): implements Exploitation
//     vs. Exploration (E2) placement balancing cached prefix reuse, queued compute load,
//     KV eviction pressure, scale-out cold-start latency, and dynamic hot-prefix
//     replication alongside matched least_loaded, round_robin, and prefix_greedy
//     baselines.
//   - Discrete-Event + Wall-Clock Evaluation Engine (sim.go): evaluates all four
//     policies over identical prompt traces with continuous-batching GPU prefill/decode
//     physics, scale-out cold starts, and wall-clock decision latency accounting.
//   - Typed HTTP Control/Scheduling Surface (http.go): mounts on any github.com/zap-proto/zip
//     host under /v1/preble with server-stamped IAM org isolation and zero host imports.
package preble
