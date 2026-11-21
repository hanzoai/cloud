// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

//go:build linux && arm64

package tetragonenforcement

// Syscall numbers for linux/arm64 (asm-generic syscall table). Go's
// syscall package does not export SYS_SETNS / SYS_FINIT_MODULE, so they
// are pinned here (see payload_linux.go usage).
const (
	sysSetns       = 268
	sysFinitModule = 273
)
