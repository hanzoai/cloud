//go:build unix

package cloud

import (
	"os"

	"golang.org/x/sys/unix"
)

// tryLockExclusive attempts a non-blocking exclusive flock. It returns
// (true, nil) when the lease is acquired, (false, nil) when another live opener
// holds it (EWOULDBLOCK), and (false, err) on a real error.
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

// unlockFile releases the flock. The kernel also releases it on fd close /
// process death, so this is the graceful path, not the only one.
func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
