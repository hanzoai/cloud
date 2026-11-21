// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package tetragonenforcement

import (
	"fmt"
	"net"
	"os"
	"time"
)

// Portable payloads: run on every OS the harness builds on. Linux-only
// payloads live in payload_linux.go; the fallback dispatcher for unknown
// payload names on non-Linux hosts lives in payload_other.go.
//
// Payload protocol: the child process prints one diagnostic line and
// returns an exit code from {ExitOK, ExitUnsupported, ExitSetup}. The
// enforcement agent (Tetragon) may kill the child mid-flight, which the
// parent observes as a signal (SIGKILL), not an exit code.

// EgressTarget is a documentation-only address (RFC 5737 TEST-NET-3) that
// is safe to attempt: nothing routable answers on it in any sane network.
const EgressTarget = "203.0.113.1:9"

func egressPayload() int {
	conn, err := net.DialTimeout("tcp", EgressTarget, 2*time.Second)
	if err != nil {
		fmt.Printf("connect %s attempted: %v\n", EgressTarget, err)
		return ExitOK
	}
	conn.Close()
	fmt.Printf("connect %s completed\n", EgressTarget)
	return ExitOK
}

func benignPayload() int {
	f, err := os.CreateTemp("", "tetragon-benign-*.txt")
	if err != nil {
		fmt.Println("benign setup:", err)
		return ExitSetup
	}
	name := f.Name()
	f.WriteString("benign control") //nolint:errcheck
	f.Close()
	defer os.Remove(name)
	if _, err := os.ReadFile(name); err != nil {
		fmt.Println("benign read:", err)
		return ExitSetup
	}
	// Loopback dial to a closed port: instant refusal, no external effect.
	conn, err := net.DialTimeout("tcp", "127.0.0.1:1", 500*time.Millisecond)
	if err == nil {
		conn.Close()
	}
	fmt.Println("benign control completed")
	return ExitOK
}

func sensitiveFilePayload() int {
	// The kprobe on security_file_permission fires on the permission
	// check itself; whether or not the read succeeds does not matter.
	if _, err := os.ReadFile("/etc/shadow"); err != nil {
		fmt.Println("shadow read:", err)
	}
	return ExitOK
}
