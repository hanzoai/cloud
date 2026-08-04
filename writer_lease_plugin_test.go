// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The lease is a SINGLE-HOLDER lock on one inode under DataDir. Every process
// that takes it excludes every other, which is exactly right between two pod
// generations rolling over the same PVC — and exactly wrong between the sibling
// processes of ONE pod, which share that DataDir by design.
//
// cloud is a plugin host: kms, pubsub and kafka are separate processes in one
// container. With CLOUD_WRITER_LEASE set and no host/child distinction, the first
// child to start took the lock and every sibling blocked until the 90s
// fail-closed deadline. Nothing bound :8000, the liveness probe killed the pod,
// and the replacement deadlocked identically — api.hanzo.ai served 503 in a
// restart loop caused entirely by its own safety mechanism.
//
// So: the host takes the lease, a child never does.
func TestWriterLease_OnlyTheHostTakesIt(t *testing.T) {
	dir := t.TempDir()
	log := luxlog.New("test").New("subsystem", "writer-lease")

	// The host holds it, as it must — this is the interlock working.
	release, err := acquireWriterLease(dir, time.Second, log)
	if err != nil {
		t.Fatalf("the host must be able to take the lease: %v", err)
	}
	defer func() { _ = release() }()

	// A SECOND host-shaped process is refused. Two pod generations, one PVC:
	// the whole point.
	if _, err := acquireWriterLease(dir, 200*time.Millisecond, nil); err == nil {
		t.Fatal("a second holder was admitted — the lease is not exclusive, and a surge roll would double-open the stores")
	}

	// A plugin child does not ask at all. underRouter is the guard in Serve, so
	// pin the predicate the guard reads: with ZIP_ADDR set this process is a
	// child of a host that already holds the lease.
	t.Setenv(zip.AddrEnv, dir+"/kms.sock")
	if !underRouter() {
		t.Fatal("a process spawned with ZIP_ADDR must report as a plugin child, or it will contend for its own host's lease")
	}

	t.Setenv(zip.AddrEnv, "")
	if underRouter() {
		t.Fatal("a directly-started process must report as the host, or nothing ever takes the lease")
	}
}
