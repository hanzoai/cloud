// Package twopod builds a fleet of replicas that share one object store and
// nothing else — the arrangement production runs in and the one a test is most
// likely to leave out.
//
// WHY IT EXISTS. Concurrency inside a process is easy to model and is not the
// hazard. Twenty-five goroutines on one *sql.DB exercise the SQLite compare-and-set
// beautifully and prove nothing about two pods, because the two pods are CASing
// two DIFFERENT FILES: each has its own volume, each reads its own copy, and each
// wins its own CAS. Every conclusion drawn from the single-file harness — the
// watermark serializes the meter, the ledger's idempotency key stops a double
// post, the reaper cannot end a lease twice — is true within one process and says
// nothing about the fleet. A test has to hold two files or it is testing the
// wrong object.
//
// WHAT A POD IS HERE. A DataDir of its own (the RWO volume), a Durability of its
// own over the SHARED object store (the S3 bucket every replica ships to), and a
// membership view onto one converged set (what the K8s API tells every pod). That
// is the whole of what makes a replica a replica, so `cloud.Base` built from it
// behaves exactly as it does in the cluster: OrgStore hydrates before it opens,
// the fencer elects one writer per org, and a ship at a stale round is refused.
//
// Two things are DELIBERATELY not modelled, and both would make the instrument
// lie. There is no network, so a ship is instantaneous — which removes latency and
// leaves ORDER, and order is what the fence is about. And the object store is a
// map rather than S3, so it is linearizable by construction; that is the property
// `org.ProbeCAS` proves of the real one at boot, so assuming it here is assuming
// what production checks rather than what production hopes.
package twopod

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/org"
	sqlitedrv "github.com/hanzoai/sqlite"
	"github.com/hanzoai/vfs/replica"
)

// Fleet is a set of replicas over one object store.
type Fleet struct {
	t     *testing.T
	Store *Objects // the shared bucket, exposed so a test can break it on purpose

	mu    sync.Mutex
	pods  map[string]*Pod
	alive []org.Member
}

// Pod is one replica: its own volume, and its own durability over the shared
// object store.
//
// It stops there rather than handing back a `cloud.Base`, and the reason is a
// layer rather than a preference: this package would then import cloud, and cloud
// is what its callers' tests are IN — an in-package test cannot import a package
// that imports it. So a pod is the two facts that make a replica a replica, and
// each caller assembles the value its own layer takes (Base for a subsystem, Deps
// for a composition root) out of them.
type Pod struct {
	ID      string
	Dir     string
	Durable *org.Durability
}

// New starts a fleet of ids, all live, all sharing one object store.
func New(t *testing.T, ids ...string) *Fleet {
	t.Helper()
	f := &Fleet{t: t, Store: NewObjects(), pods: map[string]*Pod{}}
	for _, id := range ids {
		f.Start(id)
	}
	return f
}

// Start brings a replica up — or back up on a FRESH volume, which is what a
// rescheduled pod gets. A restart that kept its directory would prove nothing
// about hydration, and hydration is what makes a replica disposable.
func (f *Fleet) Start(id string) *Pod {
	f.t.Helper()
	dir := f.t.TempDir()
	p := &Pod{ID: id, Dir: dir}
	{
		// WithSeal is not optional and is not a detail: on the pure-Go codec the
		// database lives in an envelope and the REAL path holds nothing until it is
		// checkpointed, so a ship without it reads a file that is not there and
		// NOTHING is ever durable. Production passes it unconditionally
		// (buildDurability) because on the page-level backends it is a successful
		// no-op, and a harness that left it out would model a fleet in which every
		// ship fails — which looks like the gate working and is the gate never
		// being reached.
		//
		// WithReader is deliberately absent: it opens the second read-only handle a
		// ship pins its file with, and it answers nil on a build with no linked
		// codec, which is the build these tests run under. Nothing here writes to an
		// org while its own ship is in flight, so there is no writer for the pin to
		// hold off.
		p.Durable = org.NewDurability(f.Store, view{self: id, fleet: f}, nil,
			org.WithSeal(sqlitedrv.Checkpoint))
	}
	f.mu.Lock()
	f.pods[id] = p
	f.alive = append(f.alive, org.Member{ID: id, Addr: id})
	f.mu.Unlock()
	return p
}

// Stop removes a replica from the live set, as K8s does the moment a pod stops
// being Ready. Its volume goes with it: whatever it held and did not ship is gone,
// which is the definition of "not durable" and the thing a failover test is for.
func (f *Fleet) Stop(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pods, id)
	kept := f.alive[:0]
	for _, m := range f.alive {
		if m.ID != id {
			kept = append(kept, m)
		}
	}
	f.alive = kept
}

// Pod answers the replica by id, failing the test if it is not live.
func (f *Fleet) Pod(id string) *Pod {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.pods[id]
	if !ok {
		f.t.Fatalf("twopod: %s is not a live replica", id)
	}
	return p
}

// Owner is the replica the election names for org — the one a request routes to
// and the only one whose writes can be acknowledged.
func (f *Fleet) Owner(tenant string) *Pod {
	f.t.Helper()
	f.mu.Lock()
	members := append([]org.Member(nil), f.alive...)
	f.mu.Unlock()
	m, ok := org.Owner(tenant, members)
	if !ok {
		f.t.Fatalf("twopod: no owner for %s — the live set is empty", tenant)
	}
	return f.Pod(m.ID)
}

// Other is the replica that does NOT own org. It is the interesting one: a
// non-owner holds the same files and must decline to act on them.
func (f *Fleet) Other(tenant string) *Pod {
	f.t.Helper()
	owner := f.Owner(tenant)
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, p := range f.pods {
		if id != owner.ID {
			return p
		}
	}
	f.t.Fatalf("twopod: %s has no non-owner — start a second replica", tenant)
	return nil
}

// view is one pod's membership: its own identity over the fleet's converged set.
type view struct {
	self  string
	fleet *Fleet
}

func (v view) Self() string { return v.self }
func (v view) Members() []org.Member {
	v.fleet.mu.Lock()
	defer v.fleet.mu.Unlock()
	return append([]org.Member(nil), v.fleet.alive...)
}

// Objects is the shared bucket: a linearizable conditional store, plus a switch a
// test can use to make it unreachable.
//
// The version token is a COUNTER rather than a content hash, because two writes of
// identical bytes are two versions and a conditional put has to be able to tell
// them apart — a hash would let a replica's stale put succeed against a value that
// had been overwritten and restored.
type Objects struct {
	mu   sync.Mutex
	data map[string][]byte
	ver  map[string]string
	seq  int

	// down makes every call fail, modelling an object store that is unreachable
	// rather than one that refuses. The difference decides what a caller may
	// conclude: a refusal says somebody else owns this, an outage says nothing at
	// all, and the two must not lead to the same action.
	down bool
}

// NewObjects builds an empty bucket.
func NewObjects() *Objects {
	return &Objects{data: map[string][]byte{}, ver: map[string]string{}}
}

// ErrDown is what an unreachable bucket answers. It is not ErrConflict and it is
// not ErrNotFound: it is the third answer, and a caller that folds it into either
// of the others is the defect this distinction exists to catch.
var ErrDown = errors.New("twopod: the object store is unreachable")

// Down takes the bucket offline until Up. Every Get and every conditional put
// fails, so a ship cannot be acknowledged and a write must not be reported done.
func (o *Objects) Down() { o.mu.Lock(); o.down = true; o.mu.Unlock() }

// Up brings it back.
func (o *Objects) Up() { o.mu.Lock(); o.down = false; o.mu.Unlock() }

func (o *Objects) Get(_ context.Context, key string) ([]byte, string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.down {
		return nil, "", ErrDown
	}
	b, ok := o.data[key]
	if !ok {
		return nil, "", fmt.Errorf("%s: %w", key, replica.ErrNotFound)
	}
	return append([]byte(nil), b...), o.ver[key], nil
}

func (o *Objects) PutIfVersion(_ context.Context, key string, data []byte, expect string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.down {
		return "", ErrDown
	}
	// "" means the object must not exist yet — the create-only put. Anything else
	// must match the version the caller last read, or somebody wrote in between and
	// the caller's belief about the object is stale.
	if o.ver[key] != expect {
		return "", fmt.Errorf("%s: %w", key, replica.ErrConflict)
	}
	o.seq++
	v := fmt.Sprintf("v%d", o.seq)
	o.data[key] = append([]byte(nil), data...)
	o.ver[key] = v
	return v, nil
}

// compile-time proof the bucket is what the fence takes.
var _ replica.ConditionalStore = (*Objects)(nil)
