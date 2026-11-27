// Copyright 2026 Hanzo AI, Inc. All rights reserved.

//go:build !unix

package writerlease

import "os"

// Non-unix build: the lock is a no-op. Production cloud runs on Linux; this
// keeps the package buildable for dev tooling on other platforms without
// pulling in a second platform lock. A single-writer Recreate deployment needs
// no lease anyway, and the surge topology that does is Linux-only.
func tryLockExclusive(*os.File) (bool, error) { return true, nil }
func unlockFile(*os.File) error               { return nil }
