// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package research

// durable_record_test.go is the H1 regression proof: the in-process Record() seam
// (experiments A/B evidence) must ship its write fenced before returning, exactly
// like the HTTP ingest path, so a takeover keeps it and a non-owner fails closed.
// It runs a durable research OrgStore over an in-process CAS (no live SeaweedFS) and
// opens through cek, so it also exercises the key-sidecar cross-pod restore with the
// process master key. Requires CLOUD_KMS_MASTER_KEY_REF, as the whole research suite
// does (cek opens fail closed without it).

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/internal/org"
	sqlitedrv "github.com/hanzoai/sqlite"
	"github.com/hanzoai/vfs/replica"
	luxlog "github.com/luxfi/log"
)

// shipCheckpoint mirrors what the composition root wires (build.go: NewDurability
// with WithCheckpoint(durableCheckpoint)). Without it the ship reads the real path
// on a backend that has not written it yet: the pure-Go envelope keeps the
// plaintext on tmpfs and only re-encrypts to the real file on Checkpoint or Close,
// so a snapshot taken without this ships stale ciphertext — or, on a store that has
// never been closed, no file at all. sqlitedrv.Checkpoint is a no-op on the
// write-time-encrypting backends, so this is correct on every build.
func shipCheckpoint() org.DurabilityOption {
	return org.WithCheckpoint(func(_ context.Context, db *sql.DB) error {
		return sqlitedrv.Checkpoint(db)
	})
}

// memCAS is an in-process replica.ConditionalStore: one atomic (data, generation)
// slot per key, a single mutex making PutIfVersion's compare-and-set indivisible —
// the server-side CAS a real SeaweedFS S3 gateway provides. Two "pods" share ONE.
type memCAS struct {
	mu   sync.Mutex
	objs map[string]memObj
}

type memObj struct {
	data []byte
	ver  int
}

func newMemCAS() *memCAS { return &memCAS{objs: map[string]memObj{}} }

func (m *memCAS) Get(_ context.Context, key string) ([]byte, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	if !ok {
		return nil, "", replica.ErrNotFound
	}
	return append([]byte(nil), o.data...), strconv.Itoa(o.ver), nil
}

func (m *memCAS) PutIfVersion(_ context.Context, key string, data []byte, expect string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	cur := ""
	if ok {
		cur = strconv.Itoa(o.ver)
	}
	if cur != expect {
		return "", fmt.Errorf("%w: have %q want %q", replica.ErrConflict, cur, expect)
	}
	m.objs[key] = memObj{data: append([]byte(nil), data...), ver: o.ver + 1}
	return strconv.Itoa(o.ver + 1), nil
}

// soleMembership names id as the only writer-eligible member, so id is the HRW owner
// of every org — a pod that believes it is the sole replica (the rolling-handoff model
// handoff_test.go uses).
func soleMembership(t *testing.T, id string) *org.Membership {
	t.Helper()
	m := org.NewMembership(id, org.StaticSource(org.Member{ID: id, Addr: id}), time.Second)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("membership start: %v", err)
	}
	t.Cleanup(m.Stop)
	return m
}

// cekCanReopen reports whether cek can create a store and reopen it on THIS build —
// the exact capability the successor's cross-pod restore needs. It holds on a pure-Go
// (plaintext) build and on a real CGO+libsqlcipher build; it fails only on a CGO build
// whose SQLite lacks SQLCipher, where cek writes a plaintext file it then cannot
// migrate on reopen (sqlcipher_export is absent). A throwaway temp store, no global
// state touched.
func cekCanReopen(t *testing.T) bool {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cek-probe.db")
	db, err := cek.Open(p)
	if err != nil {
		return false
	}
	if _, err := db.Exec(`CREATE TABLE probe(x)`); err != nil {
		_ = db.Close()
		return false
	}
	_ = db.Close()
	db2, err := cek.Open(p) // the reopen a broken-SQLCipher build fails (migrate → sqlcipher_export)
	if err != nil {
		return false
	}
	_ = db2.Close()
	return true
}

// TestRecordShipsSoTakeoverKeepsIt: Record() on the owner ingests AND ships; a fresh
// successor that takes over hydrates the shipped snapshot and SEES the row. Before the
// fix (ingest with no Sync) the row was never shipped, so the successor saw zero rows
// — the lost acked write H1 names.
func TestRecordShipsSoTakeoverKeepsIt(t *testing.T) {
	// Gate on the cek capability THIS build actually has, not on what it advertises.
	// The successor reopens the shipped store through cek; on a CGO build whose SQLite
	// lacks SQLCipher, PRAGMA key is silently ignored so cek writes a PLAINTEXT file it
	// then cannot migrate ("sqlcipher_export: no such function"), and the whole-suite
	// TestMain still injects a dev key because sqlitedrv.EncryptionAvailable() reports a
	// (false-positive) yes. cekCanReopen probes the real round-trip: it holds under
	// pure-Go (plaintext) AND real libsqlcipher (encrypted, exercising the .dek sidecar
	// cross-pod restore), and skips only on that broken-capability build so
	// `go test ./...` stays green everywhere. Ship-before-return is proven regardless by
	// TestRecordOnNonOwnerFailsClosed and by this test under CGO_ENABLED=0.
	if !cekCanReopen(t) {
		t.Skip("cek cannot reopen a store on this build (SQLite lacks SQLCipher) — the encrypted cross-pod sidecar round-trip runs in the CGO+libsqlcipher CI lane")
	}
	ctx := context.Background()
	cas := newMemCAS()
	const orgID = "acme"

	ownerDur := org.NewDurability(cas, soleMembership(t, "pod-owner"), nil, shipCheckpoint())
	ownerStore := cloud.NewOrgStore(t.TempDir(), "research", openStore, cloud.WithDurable(ownerDur))
	t.Cleanup(func() { _ = ownerStore.CloseAll() })
	mountedStores = ownerStore
	t.Cleanup(func() { mountedStores = nil })

	rows := []Experiment{{ID: "exp-1:A", Kind: "ab", Subject: "variant-a", Value: 0.42, N: 100}}
	if err := Record(ctx, orgID, "p1", rows); err != nil {
		t.Fatalf("Record on the owner must ship and succeed: %v", err)
	}

	// Successor pod (fresh data dir) takes over: For() hydrates the shipped snapshot
	// (its cek key sidecar restored under the SAME master, into a nested orgs/<slug>/
	// dir that did not exist), then reads the evidence back. A logger surfaces a
	// degraded hydrate as a test failure rather than a silent empty store.
	succDur := org.NewDurability(cas, soleMembership(t, "pod-successor"), nil, shipCheckpoint())
	succStore := cloud.NewOrgStore(t.TempDir(), "research", openStore, cloud.WithDurable(succDur), cloud.WithStoreLogger(luxlog.New("succ")))
	t.Cleanup(func() { _ = succStore.CloseAll() })
	st, err := succStore.For(orgID, "")
	if err != nil {
		t.Fatalf("successor For: %v", err)
	}
	got, err := st.listExperiments(ctx, "p1", "ab")
	if err != nil {
		t.Fatalf("successor list: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("takeover LOST the Record write — Record must ship (Sync) before returning")
	}
}

// TestDurableForDedupsConcurrentOpens (M1): concurrent For() for the SAME org resolve
// to ONE opened store — the in-flight dedup runs the object-store hydrate + cek open
// exactly once and with the store-wide lock released, never double-opening a file cek
// cannot open twice.
func TestDurableForDedupsConcurrentOpens(t *testing.T) {
	cas := newMemCAS()
	dur := org.NewDurability(cas, soleMembership(t, "pod-a"), nil, shipCheckpoint())
	stores := cloud.NewOrgStore(t.TempDir(), "research", openStore, cloud.WithDurable(dur))
	t.Cleanup(func() { _ = stores.CloseAll() })

	const n = 8
	got := make([]*store, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) { defer wg.Done(); got[i], errs[i] = stores.For("acme", "") }(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("concurrent For[%d]: %v", i, errs[i])
		}
		if got[i] != got[0] {
			t.Fatal("concurrent For opened the org store more than once — in-flight dedup failed")
		}
	}
}

// TestRecordOnNonOwnerFailsClosed: on a pod that is NOT the org's elected writer,
// Record must ERROR (not-acked), never silently persist a stale divergent local copy.
func TestRecordOnNonOwnerFailsClosed(t *testing.T) {
	ctx := context.Background()
	cas := newMemCAS()
	const orgID = "acme"

	set := []org.Member{{ID: "pod-a"}, {ID: "pod-b"}}
	owner, _ := org.Owner(orgID, set)
	self := "pod-a"
	if self == owner.ID {
		self = "pod-b"
	}
	m := org.NewMembership(self, org.StaticSource(set...), time.Second)
	if err := m.Start(ctx); err != nil {
		t.Fatalf("membership start: %v", err)
	}
	t.Cleanup(m.Stop)

	store := cloud.NewOrgStore(t.TempDir(), "research", openStore, cloud.WithDurable(org.NewDurability(cas, m, nil)))
	t.Cleanup(func() { _ = store.CloseAll() })
	mountedStores = store
	t.Cleanup(func() { mountedStores = nil })

	rows := []Experiment{{ID: "exp-x:A", Kind: "ab", Subject: "v"}}
	if err := Record(ctx, orgID, "p1", rows); err == nil {
		t.Fatal("Record on a non-owner must fail closed (not-acked), not silently persist a stale local write")
	}
}
