// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

//go:build linux

package tetragonenforcement

import (
	"fmt"
	"os"
	"syscall"
)

// RunPayload dispatches a payload by name. Linux builds get the full set;
// see payload.go for the shared portable payloads.
func RunPayload(name string) int {
	switch name {
	case "benign":
		return benignPayload()
	case "egress":
		return egressPayload()
	case "sensitive-file":
		return sensitiveFilePayload()
	case "setuid":
		_, _, errno := syscall.Syscall(syscall.SYS_SETUID, 0, 0, 0)
		fmt.Printf("setuid(0) attempted, errno=%v\n", errno)
		return ExitOK
	case "setns":
		// Attempt to re-enter our own pid namespace via setns(2). The
		// syscall must be *attempted*; its errno is irrelevant (the
		// enforcement kprobe fires on entry).
		if sysSetns <= 0 {
			fmt.Println("setns: syscall number not pinned for this architecture")
			return ExitUnsupported
		}
		fd, err := os.Open("/proc/self/ns/pid")
		if err != nil {
			fmt.Println("setns setup:", err)
			return ExitSetup
		}
		defer fd.Close()
		_, _, errno := syscall.Syscall(uintptr(sysSetns), fd.Fd(), 0, 0)
		fmt.Printf("setns attempted, errno=%v\n", errno)
		return ExitOK
	case "kernel-module":
		// finit_module(2) with a fake module file: EINVAL is expected on
		// an unprivileged host, but the load *attempt* is what the
		// security_kernel_read_file kprobe observes.
		if sysFinitModule <= 0 {
			fmt.Println("kernel-module: syscall number not pinned for this architecture")
			return ExitUnsupported
		}
		tmp, err := os.CreateTemp("", "tetragon-fake-*.ko")
		if err != nil {
			fmt.Println("module setup:", err)
			return ExitSetup
		}
		tmp.WriteString("not a kernel module") //nolint:errcheck
		tmp.Close()
		defer os.Remove(tmp.Name())
		fd, err := os.Open(tmp.Name())
		if err != nil {
			fmt.Println("module open:", err)
			return ExitSetup
		}
		defer fd.Close()
		_, _, errno := syscall.Syscall(uintptr(sysFinitModule), fd.Fd(), 0, 0)
		fmt.Printf("finit_module attempted, errno=%v\n", errno)
		return ExitOK
	default:
		return ExitUnsupported
	}
}
