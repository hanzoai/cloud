//go:build !windows

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// acquireGPULock acquires an exclusive advisory flock across every path in paths in
// order, polling non-blocking so ctx cancellation or timeout aborts cleanly instead of
// hanging the worker indefinitely. The returned unlock function releases all held locks.
func acquireGPULock(ctx context.Context, paths ...string) (func(), error) {
	var held []*os.File
	releaseAll := func() {
		for i := len(held) - 1; i >= 0; i-- {
			_ = syscall.Flock(int(held[i].Fd()), syscall.LOCK_UN)
			_ = held[i].Close()
		}
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			releaseAll()
			return nil, fmt.Errorf("open gpu lock %s: %w", p, err)
		}
		for {
			err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				held = append(held, f)
				break
			}
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
				_ = f.Close()
				releaseAll()
				return nil, fmt.Errorf("flock %s: %w", p, err)
			}
			select {
			case <-ctx.Done():
				_ = f.Close()
				releaseAll()
				return nil, ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	return releaseAll, nil
}

// isAnyGPULockHeld probes each path with a non-blocking exclusive flock without
// creating missing files or directories. Returns true when any existing lock file
// is currently held by another active process/descriptor.
func isAnyGPULockHeld(paths ...string) bool {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue
		}
		f, err := os.OpenFile(p, os.O_RDWR, 0)
		if err != nil {
			f, err = os.Open(p)
			if err != nil {
				continue
			}
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
			continue
		}
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true
		}
	}
	return false
}
