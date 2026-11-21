// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

//go:build linux && !amd64 && !arm64

package tetragonenforcement

// Syscall numbers are only pinned for amd64 and arm64. On other Linux
// architectures the two affected payloads report "unsupported" instead of
// guessing numbers — an honest skip, not a wrong syscall.
const (
	sysSetns       = -1
	sysFinitModule = -1
)
