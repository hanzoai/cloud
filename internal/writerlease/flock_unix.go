// Copyright 2026 Hanzo AI, Inc. All rights reserved.

//go:build unix

package writerlease

import (
	"os"

	"golang.org/x/sys/unix"
)

// tryLockExclusive attempts a non-blocking exclusive flock. It returns
// (true, nil) when the lease is acquired, (false, nil) when another live opener
// holds it (EWOULDBLOCK), and (false, err) on a real error.
//
// flock(2) and NOT fcntl(2) record locks, deliberately: a POSIX record lock is
// owned by the PROCESS, so a second lock taken inside the same process succeeds
// silently. In a plugin host with children that is the exact case, and it would
// report safety while providing none.
func tryLockExclusive(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if err == unix.EWOULDBLOCK {
		return false, nil
	}
	return false, err
}

// unlockFile releases the flock. The kernel also releases it on fd close and on
// process death, so this is the graceful path, not the only one — a holder that
// is OOM-killed cannot strand the lease.
func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
