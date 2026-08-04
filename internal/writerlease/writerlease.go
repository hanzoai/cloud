// Copyright 2026 Hanzo AI, Inc. All rights reserved.

// Package writerlease answers ONE question for ONE process: must I take the
// pod's writer lease before anything on this volume is opened for write?
//
// The lease states a POD-level fact — "this pod owns this volume" — using a
// PROCESS-level primitive, flock(2) on {DataDir}/.writer.lock. Those are only
// the same sentence when exactly one process per pod ever reaches for the lock,
// and cloud is a plugin host: the router in cmd/cloud spawns every subsystem
// (kms, pubsub, kafka, …) as its own child process, all sharing one DataDir.
//
// On 2026-08-04 they were not the same sentence. The acquire lived in
// cloud.Listen — the body every PLUGIN binary runs, and the one thing the router
// never calls. Turning CLOUD_WRITER_LEASE on therefore aimed the interlock at
// the siblings instead of at the other pod: kms won the flock, pubsub and kafka
// blocked until the 90s fail-closed deadline, nothing bound the listener, the
// liveness probe killed the pod, and its replacement deadlocked identically.
// api.hanzo.ai served 503 for four minutes at the hands of its own safety
// mechanism.
//
// The repair that followed excluded the children but never handed the lock to
// the router, so no process in the pod took it at all: an interlock that reads
// as armed and holds nothing. That is the more dangerous of the two states. The
// deadlock announced itself; a lock held by nobody is quiet right up until
// someone believes the log line, switches the Deployment to RollingUpdate, and
// two pods open the same files.
//
// So the duty is decided here, once, from the process's own position in the
// tree, and every caller does as it is told:
//
//	Take    — this process is the root of its pod. Take the lock BEFORE it
//	          spawns anything; release it AFTER the last child is gone.
//	Inherit — a parent already holds it for this whole pod. Do not touch it.
//	Off     — this deployment asked for no lease. The default, and correct
//	          under strategy: Recreate, where Kubernetes retires the old pod
//	          before starting the new one, so no two writers ever coexist.
//
// This package imports only the stdlib and role, so the light router binary can
// link it without dragging in the request tier it deliberately does not build.
package writerlease

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/role"
)

const (
	// Enable is the operator switch. Unset ⇒ Off ⇒ byte-identical to a
	// deployment that never heard of a lease.
	Enable = "CLOUD_WRITER_LEASE"

	// Held is how the answer travels DOWN. The pod root writes its own PID here
	// once it holds the lock, and zip spawns every child with
	// append(os.Environ(), …) — so each child is born already knowing the volume
	// is spoken for, rather than discovering it by losing a race.
	//
	// The value is the holder's PID rather than a bare "1" so a child can CHECK
	// the claim instead of believing it: the stamp counts only when it names the
	// child's own parent. That closes the single way this design could fail open,
	// which is a stray CLOUD_WRITER_LEASE_HELD in a manifest talking a pod root
	// out of taking the lock.
	Held = "CLOUD_WRITER_LEASE_HELD"

	// LockName is the flock anchor under DataDir: zero length, never a store, so
	// it is safe to create and to leave on the volume.
	LockName = ".writer.lock"

	// DefaultWait is how long a pod root waits for the PREVIOUS pod to let go of
	// the volume before refusing to boot. A handoff budget, not a retry budget:
	// the predecessor is closing stores, and if it has not finished within this
	// the fault is in its shutdown, which starting anyway would only compound.
	// One definition, because the router and the standalone binary must wait the
	// same amount or the pair has two different ideas of when a roll has failed.
	DefaultWait = 90 * time.Second

	// zipAddr is the private socket zip hands each child it spawns. It is
	// corroboration, not the primary signal: it proves a parent owns a plugin
	// table, which is a different fact that happens to imply this one. A child
	// whose parent somehow left no stamp still must not contend, and this is what
	// keeps that case out of the deadlock.
	zipAddr = "ZIP_ADDR"
)

// Duty is what this process must do about the pod's writer lease.
type Duty int

const (
	Off Duty = iota
	Take
	Inherit
)

func (d Duty) String() string {
	switch d {
	case Take:
		return "take"
	case Inherit:
		return "inherit"
	default:
		return "off"
	}
}

// Assess reports this process's duty and the sentence explaining it. Boot logs
// the sentence, because "who holds the lease" being unanswerable is precisely
// how both of the failures above happened.
func Assess(getenv func(string) string) (Duty, string) { return assess(getenv, os.Getppid) }

// assess is the whole decision, pure: environment and parent PID in, duty out.
func assess(getenv func(string) string, ppid func() int) (Duty, string) {
	if !truthy(getenv(Enable)) {
		return Off, "no writer lease: " + Enable + " unset — correct under strategy: Recreate, where Kubernetes never runs two pods on this volume"
	}
	// A reader opens no store; taking the lock would only lock the volume against
	// the writer it exists to protect.
	if r, err := role.Parse(getenv(role.EnvVar)); err == nil && r.IsReader() {
		return Off, "no writer lease: " + role.EnvVar + "=reader — a reader opens no store and must not hold the volume against the writer"
	}
	if pid, ok := stampedBy(getenv); ok {
		if pid == ppid() {
			return Inherit, fmt.Sprintf("writer lease inherited: this process's parent (pid %d) holds it for the whole pod", pid)
		}
		// A stamp that does not name our parent is not evidence about this pod.
		// Fall through and judge by position instead of trusting a stale or
		// hand-placed value — believing it is the only way to reach "nobody holds
		// the lock" again.
		if strings.TrimSpace(getenv(zipAddr)) == "" {
			return Take, fmt.Sprintf("writer lease is this process's to take: %s names pid %d, which is not its parent (%d), so it says nothing about this pod", Held, pid, ppid())
		}
	}
	if strings.TrimSpace(getenv(zipAddr)) != "" {
		return Inherit, "writer lease inherited: " + zipAddr + " is set, so a router spawned this process — a plugin child shares its parent's volume and must never contend for it"
	}
	return Take, "writer lease is this process's to take: nothing spawned it and no parent holds one, so it is the root of its pod"
}

// stampedBy reads the parent's claim. Returns false for absent or unparseable.
func stampedBy(getenv func(string) string) (int, bool) {
	raw := strings.TrimSpace(getenv(Held))
	if raw == "" {
		return 0, false
	}
	pid, err := strconv.Atoi(raw)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// Hold is the ONE call every entrypoint makes. It assesses, takes the lock when
// this process is the pod's root, and stamps the environment so that every child
// spawned afterwards inherits the answer instead of racing for it.
//
// It returns a release func that is ALWAYS non-nil, so no caller needs a branch
// to decide whether it has something to release. Release the lock only once the
// stores are closed and the children are gone: the lock is the statement "the
// volume is mine", and a successor is entitled to open everything the moment it
// is withdrawn.
//
// logf is the caller's logger as a plain function, so this package stays free of
// any particular logging dependency and the light router links no more than it
// already does.
func Hold(dataDir string, timeout time.Duration, logf func(string, ...any)) (func() error, error) {
	duty, why := Assess(os.Getenv)
	if logf != nil {
		logf("writer lease", "duty", duty.String(), "why", why, "data_dir", dataDir)
	}
	if duty != Take {
		return func() error { return nil }, nil
	}
	release, err := Acquire(dataDir, timeout, logf)
	if err != nil {
		return func() error { return nil }, err
	}
	// The answer now travels down with every process this one starts. Deliberately
	// host-wide (os.Setenv, which zip copies into each child) rather than per
	// child: unlike a credential, this fact is the SAME for every child, because
	// it is a fact about the pod and not about any one of them.
	if serr := os.Setenv(Held, strconv.Itoa(os.Getpid())); serr != nil {
		_ = release()
		return func() error { return nil }, fmt.Errorf("writer lease: stamp %s: %w", Held, serr)
	}
	var once sync.Once
	return func() error {
		var err error
		once.Do(func() {
			os.Unsetenv(Held)
			err = release()
		})
		return err
	}, nil
}

// Acquire takes the exclusive lock, polling a non-blocking flock until it is
// free or timeout elapses.
//
// On timeout it fails CLOSED: a writer that cannot prove it is the only opener
// refuses to open the stores rather than risk a double open. That is the right
// trade for two POD GENERATIONS — it costs a slow rollout and saves the
// database. It was the wrong trade only because it used to be applied between
// siblings, where the wait could never end.
func Acquire(dataDir string, timeout time.Duration, logf func(string, ...any)) (func() error, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("writer lease: data dir %q: %w", dataDir, err)
	}
	path := filepath.Join(dataDir, LockName)
	deadline := time.Now().Add(timeout)
	backoff := 50 * time.Millisecond
	waited := false

	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("writer lease: open %q: %w", path, err)
		}
		ok, lerr := tryLockExclusive(f)
		if lerr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("writer lease: lock %q: %w", path, lerr)
		}
		if ok {
			// flock locks an INODE, not a path. If the file we locked is no longer
			// the file at that path, another process unlinked and recreated it and
			// is free to lock the new inode with no error on either side — two
			// holders, both certain. Recheck and start over rather than hold a lock
			// on a file nobody else will consult.
			if same, serr := sameFile(f, path); serr != nil || !same {
				_ = unlockFile(f)
				_ = f.Close()
				if serr != nil {
					return nil, fmt.Errorf("writer lease: verify %q: %w", path, serr)
				}
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("writer lease: %q keeps being replaced under the lock after %s — refusing to open the stores while the anchor is unstable", path, timeout)
				}
				continue
			}
			if logf != nil {
				logf("writer lease acquired", "path", path, "waited", waited, "pid", os.Getpid())
			}
			var once sync.Once
			return func() error {
				var err error
				once.Do(func() {
					uerr := unlockFile(f)
					cerr := f.Close()
					if uerr != nil {
						err = uerr
						return
					}
					err = cerr
				})
				return err
			}, nil
		}
		_ = f.Close()
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("writer lease: %q still held by another writer after %s — refusing to open the exclusive stores (fail-closed; the previous writer has not released it — inspect its shutdown)", path, timeout)
		}
		waited = true
		if logf != nil {
			logf("writer lease held by peer; waiting for handoff", "path", path, "backoff", backoff.String())
		}
		time.Sleep(backoff)
		if backoff < time.Second {
			backoff *= 2
		}
	}
}

// sameFile reports whether the open descriptor still refers to the file at path.
func sameFile(f *os.File, path string) (bool, error) {
	locked, err := f.Stat()
	if err != nil {
		return false, err
	}
	onDisk, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return os.SameFile(locked, onDisk), nil
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
