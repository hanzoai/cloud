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

// peerPID names the process on the other end of the socket, and it is the entire
// authentication of this protocol. SO_PEERCRED is recorded by the kernel at
// connect(2) from the connecting task itself: the peer cannot set it, cannot
// forge it, and cannot replay another process's. That is why a child sends no
// token — there is nothing a token would add, and a token would be a secret to
// distribute, expire and leak.
//
// The uid check is the same argument one level out: only this deployment's own
// processes may ask. A different uid on the box is not a plugin.
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
// argv is what the manifest itself chose when it spawned the child — a dedicated
// binary's path, or the multi-call binary plus --enable=<app> — so this reads the
// launcher's own decision back out of the kernel rather than trusting the child
// to repeat it.
//
// A PID cannot be recycled out from under this: the launcher holds every child as
// an un-reaped process for as long as it is alive, so the number the kernel just
// handed us still names the task that connected.
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
