package twopod

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/sdk/tenure"
	sqlitedrv "github.com/hanzoai/sqlite"
	"github.com/hanzoai/vfs/replica"
)

// Fleet is a set of replicas over one object store.
type Fleet struct {
	t     *testing.T
	Store *Objects // the shared bucket, exposed so a test can break it on purpose

	mu    sync.Mutex
	pods  map[string]*Pod
	alive []tenure.Member
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
	Durable *tenure.Durability
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
		p.Durable = tenure.NewDurability(f.Store, view{self: id, fleet: f}, nil,
			tenure.WithSeal(sqlitedrv.Checkpoint))
	}
	f.mu.Lock()
	f.pods[id] = p
	f.alive = append(f.alive, tenure.Member{ID: id, Addr: id})
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
	members := append([]tenure.Member(nil), f.alive...)
	f.mu.Unlock()
	m, ok := tenure.Owner(tenant, members)
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
func (v view) Members() []tenure.Member {
	v.fleet.mu.Lock()
	defer v.fleet.mu.Unlock()
	return append([]tenure.Member(nil), v.fleet.alive...)
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
