// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

//go:build linux

package tetragonenforcement

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// signalOf reports whether a child died from a signal, and its name.
// Enforcement (Tetragon Sigkill action) manifests as SIGKILL.
func signalOf(err error) (bool, string) {
	ee, ok := err.(*exec.ExitError)
	if !ok {
		return false, ""
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return false, ""
	}
	sig := ws.Signal()
	if int(sig) == 9 {
		return true, "SIGKILL"
	}
	return true, fmt.Sprintf("signal-%d", int(sig))
}

// kernelVersion reads /proc/version (best-effort, empty on failure).
func kernelVersion() string {
	b, err := os.ReadFile("/proc/version")
	if err != nil {
		return ""
	}
	return string(b)
}

// btfAvailable reports whether kernel BTF is exposed (required by modern
// Tetragon).
func btfAvailable() bool {
	_, err := os.Stat("/sys/kernel/btf/vmlinux")
	return err == nil
}
