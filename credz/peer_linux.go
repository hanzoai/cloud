//go:build linux

package credz

import (
	"fmt"
	"net"
	"os"
	"syscall"
)

// peerPID names the process on the other end of the socket. SO_PEERCRED is
// recorded by the kernel at connect(2) from the connecting task itself, so the
// PID AND UID here are sound: the peer cannot set them, forge them, or replay
// another process's.
//
// It answers exactly two questions, and no longer pretends to answer a third.
// The uid check is the boundary it buys: only this deployment's own processes
// may ask, so a different uid on the box is not a plugin. The pid is the audit
// trail — the line that says which process was granted what.
//
// WHICH APP the peer is does not come from here and cannot. Everything the
// kernel records ABOUT a process is chosen by whoever called execve: argv most
// obviously (this is bug #51 — a peer exec'd as `billing` was handed billing's
// scope), but /proc/<pid>/exe is only as good as who can write a file, and both
// are self-report with a kernel's stamp on the envelope rather than the contents.
// That identity comes from the launcher now — see credz/launch.
//
// Two further gaps, kept written down because they are what a pid can and cannot
// carry: a peer can execve in place after connect (same pid, new program), and a
// pid can be recycled once the peer exits if it was not a direct un-reaped child
// of whoever is looking. Neither touches the uid check, which is all this is
// asked for.
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
