// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

//go:build linux && amd64

package tetragonenforcement

// Syscall numbers for linux/amd64 (x86-64 syscall table). Go's syscall
// package does not export SYS_SETNS / SYS_FINIT_MODULE, so they are
// pinned here (see payload_linux.go usage).
const (
	sysSetns       = 308
	sysFinitModule = 313
)
