//go:build linux

package credz

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

// peerPID names the process on the other end of the socket. SO_PEERCRED is
// recorded by the kernel at connect(2) from the connecting task itself, so the
// PID AND UID here are sound: the peer cannot set them, forge them, or replay
// another process's.
//
// The uid check is what that soundness buys: only this deployment's own
// processes may ask. A different uid on the box is not a plugin.
//
// It buys nothing beyond that. Which APP the peer is comes from peerArgv below,
// and that is not sound — see the caveat in credz.go. Do not read the strength
// of SO_PEERCRED as strength of the app identity built on top of it.
func peerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var ucred *syscall.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		ucred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if serr != nil {
		return 0, serr
	}
	if int(ucred.Uid) != os.Getuid() {
		return 0, fmt.Errorf("credz: peer uid %d is not mine (%d)", ucred.Uid, os.Getuid())
	}
	return int(ucred.Pid), nil
}

// peerArgv reads the peer's command line as the kernel recorded it at execve.
//
// The comment this replaces claimed argv reads "the launcher's own decision back
// out of the kernel rather than trusting the child to repeat it." That is WRONG,
// and the wrongness is the security hole. The kernel stores argv; it does not
// vouch for it. execve takes argv from the CALLER, so a process names itself:
// exec'ing any binary with argv[0]="billing" makes this function return billing.
// The launcher's decision and the child's self-report are indistinguishable here.
//
// Two further gaps, both moot while the above stands: a peer can execve in place
// after connect (same pid, new argv), and a pid can be recycled if the peer exits
// and was not a direct un-reaped child of the launcher — a grandchild of a
// code-execution subsystem is neither.
//
// Keep this honest rather than deleting it: the next person to reach for
// /proc/<pid>/exe should know that too is only as good as who can write a file.
func peerArgv(pid int) ([]string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, err
	}
	b = bytes.TrimRight(b, "\x00")
	if len(b) == 0 {
		return nil, fmt.Errorf("credz: pid %d has an empty cmdline", pid)
	}
	parts := bytes.Split(b, []byte{0})
	argv := make([]string, 0, len(parts))
	for _, p := range parts {
		argv = append(argv, string(p))
	}
	return argv, nil
}
