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
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/org"
	"github.com/hanzoai/vfs/replica"
	luxlog "github.com/luxfi/log"
)

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

// TestRecordShipsSoTakeoverKeepsIt: Record() on the owner ingests AND ships; a fresh
// successor that takes over hydrates the shipped snapshot and SEES the row. Before the
// fix (ingest with no Sync) the row was never shipped, so the successor saw zero rows
// — the lost acked write H1 names.
func TestRecordShipsSoTakeoverKeepsIt(t *testing.T) {
	ctx := context.Background()
	cas := newMemCAS()
	const orgID = "acme"

	ownerDur := org.NewDurability(cas, soleMembership(t, "pod-owner"), nil)
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
	succDur := org.NewDurability(cas, soleMembership(t, "pod-successor"), nil)
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
