// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

//go:build !linux

package tetragonenforcement

// signalOf reports whether a child died from a signal. Non-Linux builds
// never see signal deaths through exec in this harness, so the answer is
// always no; the caller then classifies via the exit code.
func signalOf(err error) (bool, string) {
	_ = err
	return false, ""
}

// kernelVersion is not available off Linux.
func kernelVersion() string { return "" }

// btfAvailable is false off Linux.
func btfAvailable() bool { return false }
