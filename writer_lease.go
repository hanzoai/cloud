package cloud

// Writer lease — the cross-process interlock that makes a zero-gap (surge)
// writer roll SAFE despite the exclusive-lock ZapDB KMS store.
//
// The writer embeds stores that permit exactly one live opener: the ZapDB KMS
// store (an OS-locked Badger fork whose live files are NOT RO-shareable — see
// clients/kms.TestConcurrentOpen_...) and the audit chain whose head is recovered
// at open. Under strategy: Recreate the old pod fully terminates before the new
// one starts, so there is never an overlap and no lease is needed (default OFF).
//
// To shrink the roll gap we can instead run the writer as RollingUpdate
// (maxUnavailable:1, maxSurge:1) with same-node podAffinity so the surge pod is
// already scheduled and running when the old pod is told to terminate. THEN the
// lease is what keeps it safe: the new writer blocks in acquireWriterLease before
// it opens any store, and the old writer releases the lease LAST at shutdown —
// after every store is closed. So the exclusive stores are handed off, never
// double-opened, and the gap shrinks from "schedule + pull + full startup"
// (Recreate) to just "old flush/close + new open".
//
// The lock is an flock(2) advisory exclusive lock on {DataDir}/.writer.lock. Two
// pods sharing the one RWO PVC on the same node interlock through the same inode;
// a crashed holder's lock is reclaimed by the kernel on process death, so a lease
// can never be stranded by an OOM/crash.

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	luxlog "github.com/luxfi/log"
)

// writerLockName is the lock file under DataDir. It is NOT a store — only a
// zero-length advisory-lock anchor — so it is safe to create/keep on the PVC.
const writerLockName = ".writer.lock"

// acquireWriterLease takes the exclusive writer lease, polling (non-blocking
// flock) until it is free or timeout elapses. On success it returns a release
// func the caller MUST invoke AFTER every store is closed (the lease's whole
// purpose is to gate store opens). On timeout it fails CLOSED: a writer that
// cannot prove it is the sole store-opener refuses to open the stores rather than
// risk a double-open.
func acquireWriterLease(dataDir string, timeout time.Duration, log luxlog.Logger) (func() error, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("writer lease: data dir %q: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, writerLockName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("writer lease: open %q: %w", path, err)
	}

	deadline := time.Now().Add(timeout)
	backoff := 50 * time.Millisecond
	waited := false
	for {
		ok, lerr := tryLockExclusive(f)
		if lerr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("writer lease: lock %q: %w", path, lerr)
		}
		if ok {
			if log != nil {
				log.Info("writer lease acquired", "path", path, "waited", waited)
			}
			return func() error {
				uerr := unlockFile(f)
				cerr := f.Close()
				if uerr != nil {
					return uerr
				}
				return cerr
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("writer lease: %q still held by another writer after %s — refusing to open the exclusive stores (fail-closed; the previous writer has not released it — inspect its shutdown)", path, timeout)
		}
		waited = true
		if log != nil {
			log.Info("writer lease held by peer; waiting for handoff", "path", path, "backoff", backoff.String())
		}
		time.Sleep(backoff)
		if backoff < time.Second {
			backoff *= 2
		}
	}
}
