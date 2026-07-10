//go:build !unix

package cloud

import "os"

// Non-unix build: the writer lease is a no-op. Production cloud runs on Linux;
// this keeps the package buildable on other platforms (dev tooling) without
// pulling in a platform lock. A single-writer Recreate deployment needs no lease
// anyway, and the surge topology that requires it is Linux-only.
func tryLockExclusive(*os.File) (bool, error) { return true, nil }
func unlockFile(*os.File) error               { return nil }
