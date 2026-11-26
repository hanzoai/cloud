// SPDX-License-Identifier: Apache-2.0

// Package sandboxpolicy implements a clean-room, Tetragon-inspired runtime
// enforcement and hash-chained audit engine for Visor agent sandboxes.
//
// Modelled on Cilium Tetragon TracingPolicy semantics (HIP-1349, issue #33),
// sandboxpolicy provides deterministic policy compilation and enforcement across
// three runtime domains:
//
//  1. Syscall domain: allowlist/denylist by Linux syscall number or canonical
//     name plus 64-bit argument filters, compiling directly into kernel-ready
//     classic BPF / seccomp-BPF filter bytecode (struct sock_filter) alongside
//     an equivalent deterministic userspace evaluator and cBPF VM for
//     cross-platform CI verification.
//  2. Process execution domain: canonical path normalization prior to prefix
//     matching, symlink target verification, argument pattern restrictions,
//     parent-child lineage constraints, and privilege-escalation / capability
//     change refusal.
//  3. Network egress/bind domain: IPv4/IPv6 CIDR allowlist and denylist with
//     canonical IPv4-mapped IPv6 unmapping (preventing ::ffff:169.254.169.254
//     bypasses), cloud metadata endpoint blocking, loopback/private-range
//     restrictions, and port rules.
//
// Enforcement supports AuditOnly (telemetry-only), EnforceDeny (synchronous
// EPERM / Override denial), and EnforceSigkill (synchronous termination),
// failing closed whenever compilation fails, rules conflict, or a sandbox has
// no valid compiled policy bound.
//
// This package imports only the Go standard library, github.com/zap-proto/zip,
// and github.com/luxfi/log — never its host — so it mounts cleanly in any zip
// host and satisfies apps.TestAppsImportNothingOfTheirHost.
package sandboxpolicy
