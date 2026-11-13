//go:build windows

package cek

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// flock takes an exclusive advisory lock on <path>.cek.lock, serializing
// first-touch and migration for the SAME database across processes/goroutines
// (different databases never contend). Returns an unlock func.
func flock(path string) (func(), error) {
	lockPath := path + lockSuffix
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("cek: create lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("cek: open lock %q: %w", lockPath, err)
	}
	var ol windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ol); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cek: acquire lock %q: %w", lockPath, err)
	}
	return func() {
		var uol windows.Overlapped
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &uol)
		_ = f.Close()
	}, nil
}
