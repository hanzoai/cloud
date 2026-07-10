//go:build !unix

package db

import "sync"

// createLockFallback serializes create-lock holders within a single process on
// platforms without flock (e.g. Windows). Commerce ships only on Linux
// containers, so cross-process locking is not a deployment target here; this keeps
// the build green and preserves in-process safety. The lockPath is ignored — the
// process-wide mutex is strictly stronger in-process than a per-path lock and the
// cross-process case does not exist on these platforms.
var createLockFallback sync.Mutex

// withExclusiveFileLock runs fn under a process-wide mutex. See filelock_unix.go
// for the real cross-process (flock) implementation used in production.
func withExclusiveFileLock(_ string, fn func() error) error {
	createLockFallback.Lock()
	defer createLockFallback.Unlock()
	return fn()
}
