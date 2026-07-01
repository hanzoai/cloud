package org

import (
	"bytes"
	"context"
	"sync"
	"testing"
)

type errString string

func (e errString) Error() string { return string(e) }

const errNotFound = errString("not found")

// memStore is an in-memory Store (stands in for hanzoai/vfs) — the replication
// logic is proven with no SeaweedFS / network.
type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemStore() *memStore { return &memStore{data: map[string][]byte{}} }

func (m *memStore) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte(nil), data...)
	return nil
}

func (m *memStore) Get(_ context.Context, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.data[key]
	if !ok {
		return nil, "", errNotFound
	}
	return append([]byte(nil), d...), version(d), nil
}

func (m *memStore) Version(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.data[key]
	if !ok {
		return "", errNotFound
	}
	return version(d), nil
}

// memDB is an in-memory DB (stands in for a hanzoai/base handle).
type memDB struct {
	mu sync.Mutex
	b  []byte
}

func (d *memDB) set(b []byte)                              { d.mu.Lock(); d.b = append([]byte(nil), b...); d.mu.Unlock() }
func (d *memDB) get() []byte                               { d.mu.Lock(); defer d.mu.Unlock(); return append([]byte(nil), d.b...) }
func (d *memDB) Snapshot(context.Context) ([]byte, error)  { return d.get(), nil }
func (d *memDB) Restore(_ context.Context, b []byte) error { d.set(b); return nil }

// DBPath layout (HIP-0302): org root, project, user — and determinism.
func TestDBPath(t *testing.T) {
	cases := []struct{ org, scope, svc, want string }{
		{"acme", "", "iam", "orgs/acme/iam.db"},
		{"acme", "projects/site", "base", "orgs/acme/projects/site/base.db"},
		{"acme", "users/dave", "kv", "orgs/acme/users/dave/kv.db"},
	}
	for _, c := range cases {
		if got := DBPath(c.org, c.scope, c.svc); got != c.want {
			t.Errorf("DBPath(%q,%q,%q)=%q, want %q", c.org, c.scope, c.svc, got, c.want)
		}
		if DBPath(c.org, c.scope, c.svc) != DBPath(c.org, c.scope, c.svc) {
			t.Errorf("DBPath not deterministic for %q", c.org)
		}
	}
	if DBPath("a", "", "x") == DBPath("b", "", "x") {
		t.Fatal("different orgs must map to different paths")
	}
}

// Core owner→SeaweedFS→reader flow: owner writes, pushes; a fresh reader pulls
// the exact bytes.
func TestReplicatorPushThenReaderPull(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	key := DBPath("hanzo", "", "iam")

	ownerDB := &memDB{}
	ownerDB.set([]byte("org-state-v1"))
	if err := NewReplicator(key, store, ownerDB).Push(ctx); err != nil {
		t.Fatalf("owner push: %v", err)
	}

	readerDB := &memDB{}
	changed, err := NewReplicator(key, store, readerDB).Pull(ctx)
	if err != nil {
		t.Fatalf("reader pull: %v", err)
	}
	if !changed {
		t.Fatal("first pull should report changed=true")
	}
	if !bytes.Equal(readerDB.get(), []byte("org-state-v1")) {
		t.Fatalf("reader restored %q, want org-state-v1", readerDB.get())
	}
}

// Pull is a no-op (changed=false, no restore) when the remote is unchanged since
// the last pull — the version skip that keeps readers cheap and never clobbers a
// locally-current DB.
func TestReplicatorPullSkipsUnchanged(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	key := DBPath("acme", "", "base")

	ownerDB := &memDB{}
	ownerDB.set([]byte("v1"))
	_ = NewReplicator(key, store, ownerDB).Push(ctx)

	readerDB := &memDB{}
	reader := NewReplicator(key, store, readerDB)
	if c, _ := reader.Pull(ctx); !c {
		t.Fatal("first pull should be changed")
	}
	readerDB.set([]byte("local-untouched")) // if a skipped pull restored, this is lost
	c, err := reader.Pull(ctx)
	if err != nil {
		t.Fatalf("second pull: %v", err)
	}
	if c {
		t.Fatal("unchanged remote → pull should be a no-op (changed=false)")
	}
	if !bytes.Equal(readerDB.get(), []byte("local-untouched")) {
		t.Fatal("no-op pull must not restore/overwrite the local DB")
	}
}

// New owner on handover loads the last committed state, then push+pull
// round-trips a subsequent write to readers.
func TestReplicatorOwnershipHandover(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	key := DBPath("acme", "", "base")

	oldDB := &memDB{}
	oldDB.set([]byte("committed-by-old-owner"))
	_ = NewReplicator(key, store, oldDB).Push(ctx)

	newDB := &memDB{} // new owner boots empty, pulls committed state
	newOwner := NewReplicator(key, store, newDB)
	if _, err := newOwner.Pull(ctx); err != nil {
		t.Fatalf("new owner pull: %v", err)
	}
	if !bytes.Equal(newDB.get(), []byte("committed-by-old-owner")) {
		t.Fatalf("handover lost state: %q", newDB.get())
	}
	newDB.set([]byte("committed-by-new-owner"))
	if err := newOwner.Push(ctx); err != nil {
		t.Fatalf("new owner push: %v", err)
	}
	readerDB := &memDB{}
	if _, err := NewReplicator(key, store, readerDB).Pull(ctx); err != nil {
		t.Fatalf("reader pull: %v", err)
	}
	if !bytes.Equal(readerDB.get(), []byte("committed-by-new-owner")) {
		t.Fatalf("reader missed new owner's write: %q", readerDB.get())
	}
}

// The vfs adapter satisfies Store and round-trips through a fake vfs client.
func TestVFSStoreAdapter(t *testing.T) {
	ctx := context.Background()
	fake := &fakeVFS{m: map[string][]byte{}}
	var s Store = NewVFSStore(fake)
	if err := s.Put(ctx, "orgs/acme/iam.db", []byte("hello")); err != nil {
		t.Fatalf("put: %v", err)
	}
	data, ver, err := s.Get(ctx, "orgs/acme/iam.db")
	if err != nil || string(data) != "hello" {
		t.Fatalf("get: data=%q err=%v", data, err)
	}
	if v2, _ := s.Version(ctx, "orgs/acme/iam.db"); v2 != ver {
		t.Fatalf("version mismatch: %s vs %s", v2, ver)
	}
	if err := NewVFSStore(nil).Put(ctx, "k", nil); err == nil {
		t.Fatal("nil vfs must error, not panic")
	}
}

type fakeVFS struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (f *fakeVFS) Put(_ context.Context, key string, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[key] = append([]byte(nil), payload...)
	return nil
}

func (f *fakeVFS) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.m[key]
	if !ok {
		return nil, errNotFound
	}
	return append([]byte(nil), d...), nil
}
