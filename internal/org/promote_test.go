// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// promote_test.go is the M3 proof: a store that opened DEGRADED (read-only, not the
// elected owner) is promoted to the writer IN PLACE when a membership change makes this
// replica the org's owner — re-acquiring the lease, hydrating the predecessor's shipped
// state, and serving writes, with NO process restart and NO split-brain (the promoted
// store takes over at a strictly higher round, so the deposed owner is fenced).

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	"github.com/hanzoai/vfs/replica"
)

// liveView is a MUTABLE membership view — the seam a rolling-upgrade change flows
// through. swap() replaces the live set so PendingPromotion re-evaluates HRW against the
// new pod set, exactly as internal/org.Membership's atomic snapshot does in production.
type liveView struct {
	self string
	mu   sync.Mutex
	set  []Member
}

func (v *liveView) Self() string { return v.self }

func (v *liveView) Members() []Member {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]Member(nil), v.set...)
}

func (v *liveView) swap(ms []Member) {
	v.mu.Lock()
	v.set = ms
	v.mu.Unlock()
}

// openBoundDB opens the local handle for a Durable (as the real store does: one
// connection, bound so Sync checkpoints on it) and ensures the kv table.
func openBoundDB(t *testing.T, d *Durable) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", testDSN(d.dbPath))
	if err != nil {
		t.Fatalf("open %s: %v", d.dbPath, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		t.Fatalf("ping %s: %v", d.dbPath, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	d.Bind(db)
	ensureKV(t, db)
	return db
}

// TestDurablePromotionLiveReacquireNoRestart drives the full M3 transition through the
// Durable primitives the OrgStore orchestrates (PendingPromotion → TryClaim →
// quiesce-close → reopen+Hydrate): a non-owner that opened read-only becomes the elected
// owner after a membership change and takes over live — hydrating the predecessor's k1,
// serving k2, at a higher round — while the deposed owner is fenced.
func TestDurablePromotionLiveReacquireNoRestart(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	const orgID = "acme"
	dbKey := replica.DBPath(orgID, "", "research")
	set := []Member{{ID: "pod-a"}, {ID: "pod-b"}}
	ownerID := electedOwnerID(orgID, set)
	otherID := "pod-a"
	if otherID == ownerID {
		otherID = "pod-b"
	}

	// Owner pod: elected owner under {a,b}. Writes k1, ships.
	ownerDur := NewDurability(cs, &liveView{self: ownerID, set: set}, nil)
	owner := ownerDur.For(orgID, dbKey, filepath.Join(t.TempDir(), "research.db"))
	if err := owner.Hydrate(ctx); err != nil {
		t.Fatalf("owner hydrate: %v", err)
	}
	ownerDB := openBoundDB(t, owner)
	putKV(t, ownerDB, "k1", "v1")
	if acked, err := owner.Sync(ctx); err != nil || !acked {
		t.Fatalf("owner sync k1: acked=%v err=%v", acked, err)
	}

	// Other pod: opens DEGRADED under the SAME live set (not the elected owner). It
	// read-only-hydrates k1 but does not own — writes fail closed.
	otherView := &liveView{self: otherID, set: set}
	otherDur := NewDurability(cs, otherView, nil)
	otherPath := filepath.Join(t.TempDir(), "research.db")
	other := otherDur.For(orgID, dbKey, otherPath)
	if err := other.Hydrate(ctx); err != nil {
		t.Fatalf("other hydrate: %v", err)
	}
	if other.Owned() {
		t.Fatal("non-owner must open degraded (read-only), not owned")
	}
	if other.PendingPromotion() {
		t.Fatal("must NOT be pending promotion while another pod is the elected owner")
	}
	otherDB := openBoundDB(t, other)
	if !hasKV(t, otherDB, "k1") {
		t.Fatal("degraded open should have read-only hydrated the owner's k1")
	}

	// Rolling upgrade: the owner drains out of the live set. The other pod is now the
	// HRW owner — so its degraded store becomes pending-promotion.
	otherView.swap([]Member{{ID: otherID}})
	if !other.PendingPromotion() {
		t.Fatal("must be pending promotion once this replica is the elected owner")
	}

	// M3 promotion, no restart: claim the lease (probe — no file I/O), quiesce the
	// read-only handle, then reopen as owner (fresh Durable, SAME local path — the
	// in-place swap the OrgStore performs). Hydrate renews the claimed lease and
	// CarryForward-restores the latest snapshot under the fresh handle.
	claimed, err := other.TryClaim(ctx)
	if err != nil || !claimed {
		t.Fatalf("TryClaim after election: claimed=%v err=%v, want true/nil", claimed, err)
	}
	_ = otherDB.Close() // quiesce before the reopen's CarryForward swaps the file.

	promoted := otherDur.For(orgID, dbKey, otherPath)
	if err := promoted.Hydrate(ctx); err != nil {
		t.Fatalf("promoted hydrate: %v", err)
	}
	if !promoted.Owned() {
		t.Fatal("promoted store must hold the writer lease")
	}
	if promoted.lease.Round <= owner.lease.Round {
		t.Fatalf("promoted round %d not strictly > deposed owner %d — fencing token not advanced", promoted.lease.Round, owner.lease.Round)
	}
	promotedDB := openBoundDB(t, promoted)
	if !hasKV(t, promotedDB, "k1") {
		t.Fatal("promoted store lost the predecessor's k1 (hydrate-on-promote failed)")
	}
	putKV(t, promotedDB, "k2", "v2")
	if acked, err := promoted.Sync(ctx); err != nil || !acked {
		t.Fatalf("promoted sync k2: acked=%v err=%v", acked, err)
	}

	// The deposed owner that keeps running ships at its STALE round: fenced, never acked.
	putKV(t, ownerDB, "k-stale", "should-be-fenced")
	acked, err := owner.Sync(ctx)
	if err != nil {
		t.Fatalf("deposed owner sync unexpected hard error: %v", err)
	}
	if acked {
		t.Fatal("deposed owner ACKED a fenced ship after promotion — split-brain double-write")
	}

	// Durable state: k1 + k2 present, the deposed writer's k-stale never landed.
	if v, ok := durableValue(t, ctx, cs, dbKey, "k1"); !ok || v != "v1" {
		t.Fatalf("durable k1 = %q,%v, want v1,true", v, ok)
	}
	if v, ok := durableValue(t, ctx, cs, dbKey, "k2"); !ok || v != "v2" {
		t.Fatalf("durable k2 = %q,%v, want v2,true", v, ok)
	}
	if v, ok := durableValue(t, ctx, cs, dbKey, "k-stale"); ok {
		t.Fatalf("deposed writer's k-stale leaked into durable state as %q", v)
	}
}

// TestPendingPromotionFalseWhenNotElected: a degraded store whose replica is NOT the
// elected owner must never report pending-promotion (it stays read-only), and an OWNED
// store never does either (nothing to promote).
func TestPendingPromotionFalseWhenNotElected(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	const orgID = "acme"
	dbKey := replica.DBPath(orgID, "", "research")
	set := []Member{{ID: "pod-a"}, {ID: "pod-b"}}
	ownerID := electedOwnerID(orgID, set)
	otherID := "pod-a"
	if otherID == ownerID {
		otherID = "pod-b"
	}

	// Owned store: not pending (already the writer).
	ownerDur := NewDurability(cs, &liveView{self: ownerID, set: set}, nil)
	owner := ownerDur.For(orgID, dbKey, filepath.Join(t.TempDir(), "research.db"))
	if err := owner.Hydrate(ctx); err != nil {
		t.Fatalf("owner hydrate: %v", err)
	}
	if owner.PendingPromotion() {
		t.Fatal("an owned store is never pending promotion")
	}

	// Degraded store, still not elected: not pending.
	other := NewDurability(cs, &liveView{self: otherID, set: set}, nil).For(orgID, dbKey, filepath.Join(t.TempDir(), "research.db"))
	if err := other.Hydrate(ctx); err != nil {
		t.Fatalf("other hydrate: %v", err)
	}
	if other.PendingPromotion() {
		t.Fatal("a degraded store whose replica is not the elected owner must not be pending promotion")
	}
}
