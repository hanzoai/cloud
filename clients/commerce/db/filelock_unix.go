//go:build unix

package db

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// withExclusiveFileLock runs fn while holding an exclusive advisory lock (flock
// LOCK_EX) on lockPath, creating the lockfile if needed. It is a CROSS-PROCESS
// mutex: two processes sharing the data directory (an accidental >1 replica, or
// the migration tool running beside the daemon) are serialized through it, so the
// "mint a fresh tenant DEK + create its db" critical section can never interleave
// and produce a mismatched .db/.dek pair.
//
// flock is per-open-file-description and released when the fd is closed, so a
// crashed holder's lock is reclaimed by the kernel — no stale lock survives a
// process death. The lock is advisory, which is sufficient: every writer of these
// files routes through this same codebase. In-process callers are already
// serialized by Manager.mu; this guards the orthogonal cross-process case.
func withExclusiveFileLock(lockPath string, fn func() error) (err error) {
	f, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if openErr != nil {
		return fmt.Errorf("open create-lock %q: %w", lockPath, openErr)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close create-lock %q: %w", lockPath, cerr)
		}
	}()

	if lerr := unix.Flock(int(f.Fd()), unix.LOCK_EX); lerr != nil {
		return fmt.Errorf("acquire create-lock %q: %w", lockPath, lerr)
	}
	defer func() {
		if uerr := unix.Flock(int(f.Fd()), unix.LOCK_UN); uerr != nil && err == nil {
			err = fmt.Errorf("release create-lock %q: %w", lockPath, uerr)
		}
	}()

	return fn()
}
