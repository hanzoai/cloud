package kms

// durable_test.go — a secret is in the object store before its write returns.
//
// Nothing here ever shipped. A secret reached durable storage only when its org store
// was evicted or the process shut down, so a pod lost between the two took every secret
// written since with it — and cloud deploys strategy Recreate at one replica, so the
// successor hydrates the durable snapshot OVER the local file. An unshipped secret is
// not merely at risk of being lost; it is replaced by an older copy of the same tenant's
// store, on the ordinary deploy path, after the write said it was kept.
//
// The owner below is never closed, which is what an ungraceful teardown is. The
// successor is a different store over its own empty data directory, sharing only the
// object store — the same double apps/label and apps/research use for the same proof.

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/org"
	sqlitedrv "github.com/hanzoai/sqlite"
	"github.com/hanzoai/vfs/replica"
	kmsstore "github.com/luxfi/kms/pkg/store"
)

// cas is an in-process replica.ConditionalStore: one atomic (data, generation) slot per
// key, a single mutex making PutIfVersion indivisible — the server-side CAS a real S3
// gateway provides. Two "pods" share ONE.
type cas struct {
	mu   sync.Mutex
	objs map[string]casObj
}

type casObj struct {
	data []byte
	ver  int
}

func newCAS() *cas { return &cas{objs: map[string]casObj{}} }

func (c *cas) Get(_ context.Context, key string) ([]byte, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.objs[key]
	if !ok {
		return nil, "", replica.ErrNotFound
	}
	return append([]byte(nil), o.data...), strconv.Itoa(o.ver), nil
}

func (c *cas) PutIfVersion(_ context.Context, key string, data []byte, expect string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.objs[key]
	cur := ""
	if ok {
		cur = strconv.Itoa(o.ver)
	}
	if cur != expect {
		return "", fmt.Errorf("%w: have %q want %q", replica.ErrConflict, cur, expect)
	}
	c.objs[key] = casObj{data: append([]byte(nil), data...), ver: o.ver + 1}
	return strconv.Itoa(o.ver + 1), nil
}

func (c *cas) holds(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.objs[key]
	return ok
}

// pod builds a secret store over the shared object store, as the deployment does: the
// crypto client the ship ends with, and a membership naming this pod the sole writer of
// every org.
func pod(t *testing.T, store *cas, id string) *secretStore {
	t.Helper()
	m := org.NewMembership(id, org.StaticSource(org.Member{ID: id, Addr: id}), time.Second)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("membership: %v", err)
	}
	t.Cleanup(m.Stop)
	return newSecretStore(cloud.Base{
		DataDir: t.TempDir(),
		Durable: org.NewDurability(store, m, nil, org.WithSeal(sqlitedrv.Checkpoint)),
	}, false)
}

// takeover is a successor pod that has never seen this org: its own empty data
// directory, sharing only the object store with the pod that wrote. Opening the org's
// store is what hydrates the durable snapshot onto it — the same first touch its first
// write would do.
//
// A read alone does not reach that far, and that is a separate hole reported rather than
// changed here: dbFor answers a miss for a namespace with no file on THIS pod's disk and
// never asks the object store, so a secret that exists durably and not locally reads as
// absent until something opens the store.
func takeover(t *testing.T, store *cas, id, path string) *secretStore {
	t.Helper()
	s := pod(t, store, id)
	ns, err := namespaceFor(path)
	if err != nil {
		t.Fatalf("namespace for %q: %v", path, err)
	}
	if _, err := s.stores.For(ns); err != nil {
		t.Fatalf("%s could not open the org it is taking over: %v", id, err)
	}
	return s
}

func TestASecretIsDurableBeforeItsWriteReturns(t *testing.T) {
	const path, name, env, plaintext = "/orgs/acme/ci", "TOKEN", "main", "s3cr3t-value"
	key := testKey()
	sealed, err := kmsstore.Seal(key, path, name, env, []byte(plaintext))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	store := newCAS()
	owner := pod(t, store, "pod-owner")
	if err := owner.put(sealed); err != nil {
		t.Fatalf("put: %v", err)
	}

	// The object exists at the moment the write returned, and the owner is still
	// running. Nothing has been closed, evicted or drained.
	if slot := replica.DBPath("acme", "", "kms"); !store.holds(slot) {
		t.Fatalf("the write returned and %s holds nothing — the secret exists only on the pod that wrote it", slot)
	}

	// And it is a whole store, not merely bytes: a successor with an EMPTY data
	// directory hydrates it and reads the secret back, under the key cek derives from
	// the same master and the same namespace.
	succ := takeover(t, store, "pod-successor", path)
	got, err := succ.get(path, name, env)
	if err != nil {
		t.Fatalf("the successor cannot read a secret the owner acknowledged: %v", err)
	}
	pt, err := kmsstore.Open(key, got)
	if err != nil {
		t.Fatalf("open on the successor: %v", err)
	}
	if string(pt) != plaintext {
		t.Fatalf("the successor holds %q, want %q", pt, plaintext)
	}

	// A DELETE is a write too, and the same thing is owed to it: a secret revoked on
	// one pod and still readable on its successor is the failure that matters most.
	// The successor does the revoking, because it is the writer now — its takeover
	// claimed the lease at a higher round, which is what deposes the pod above.
	if err := succ.del(path, name, env); err != nil {
		t.Fatalf("del: %v", err)
	}
	after := takeover(t, store, "pod-after", path)
	if _, err := after.get(path, name, env); err == nil {
		t.Fatal("the successor still serves a secret the owner deleted and acknowledged")
	}
}

// A read ships nothing. The bound this keeps is the one that matters on a store opened
// per request: a get that shipped would put a whole-file upload on every secret read.
func TestReadingASecretShipsNothing(t *testing.T) {
	const path, name, env = "/orgs/acme/ci", "TOKEN", "main"
	sealed, err := kmsstore.Seal(testKey(), path, name, env, []byte("v"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	store := newCAS()
	s := pod(t, store, "pod-owner")
	if err := s.put(sealed); err != nil {
		t.Fatalf("put: %v", err)
	}

	store.mu.Lock()
	before := store.objs[replica.DBPath("acme", "", "kms")].ver
	store.mu.Unlock()

	for range 5 {
		if _, err := s.get(path, name, env); err != nil {
			t.Fatalf("get: %v", err)
		}
		if _, err := s.list(path, env); err != nil {
			t.Fatalf("list: %v", err)
		}
		if _, err := s.find(findQuery{Path: path}); err != nil {
			t.Fatalf("find: %v", err)
		}
	}

	store.mu.Lock()
	after := store.objs[replica.DBPath("acme", "", "kms")].ver
	store.mu.Unlock()
	if after != before {
		t.Fatalf("fifteen reads moved the durable object %d times", after-before)
	}
}
