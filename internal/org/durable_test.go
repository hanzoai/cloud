// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// durable_test.go is the outage proof for the packaged gate (Durable): two replicas
// that contend for one org's store — the exact "a second replica breaks the store"
// failure — resolve to ONE writer with the other DEFERRING, both openable (never
// "store unavailable"), no split-brain, and no acknowledged write lost across a
// rolling handoff. It exercises the SAME hazards as handoff_test.go, but through the
// reusable Durable a per-org store actually wires, over a plaintext local file (nil
// cipher → the no-sidecar frame path).

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/vfs/replica"
)

// testDSN opens a WAL SQLite file with synchronous(OFF): the durability under test is
// the fenced ship to the object store, not the local fsync, and OFF keeps the suite
// off this box's pathologically slow fsync. Production opens through cek/replica with
// synchronous(NORMAL); the snapshot (checkpoint + file copy) is identical either way.
func testDSN(path string) string {
	return path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(OFF)&_pragma=busy_timeout(5000)"
}

// durablePod models one replica opening an org DB through the Durable gate: its own
// local SQLite file, a Durable bound to the shared object store cs, under a fixed
// membership view. (stubView, newFakeCondStore, electedOwnerID live in fence_test.go.)
type durablePod struct {
	id string
	d  *Durable
	db *sql.DB
}

func newDurablePod(t *testing.T, cs replica.ConditionalStore, id string, set []Member, orgID, dbKey string) *durablePod {
	t.Helper()
	dy := NewDurability(cs, stubView{id: id, set: set}, nil) // nil cipher: plaintext local
	d := dy.For(orgID, dbKey, filepath.Join(t.TempDir(), "research.db"))
	return &durablePod{id: id, d: d}
}

// open opens the local handle AFTER Hydrate (so it sees the hydrated file), pins it
// to one connection (as the real store does), and binds it for Sync's checkpoint.
func (p *durablePod) open(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", testDSN(p.d.dbPath))
	if err != nil {
		t.Fatalf("%s open: %v", p.id, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		t.Fatalf("%s ping: %v", p.id, err)
	}
	p.db = db
	t.Cleanup(func() { _ = db.Close() })
	p.d.Bind(db)
	ensureKV(t, db)
}

func ensureKV(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS kv(k TEXT PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("ensure kv: %v", err)
	}
}

func putKV(t *testing.T, db *sql.DB, k, v string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR REPLACE INTO kv(k,v) VALUES(?,?)`, k, v); err != nil {
		t.Fatalf("put %s: %v", k, err)
	}
}

func hasKV(t *testing.T, db *sql.DB, k string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM kv WHERE k=?`, k).Scan(&n); err != nil {
		t.Fatalf("has %s: %v", k, err)
	}
	return n == 1
}

// durableValue restores the durable object into a scratch DB and reads kv[k]; ok is
// false when the row is absent. It reads through a fresh FencedStore over the same
// object store, unframes the (sidecar,db) payload, and opens the database bytes — the
// exact path a successor takes, so it proves what a successor would actually see.
func durableValue(t *testing.T, ctx context.Context, cs replica.ConditionalStore, dbKey, k string) (string, bool) {
	t.Helper()
	payload, _, err := replica.NewFencedStore(cs).Get(ctx, dbKey)
	if err != nil {
		t.Fatalf("read durable object: %v", err)
	}
	_, main, err := unframe(payload)
	if err != nil {
		t.Fatalf("unframe durable payload: %v", err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch.db")
	if err := replica.RestoreFile(scratch, main); err != nil {
		t.Fatalf("restore scratch: %v", err)
	}
	sd, err := replica.OpenSQLite(scratch)
	if err != nil {
		t.Fatalf("open scratch: %v", err)
	}
	defer sd.Close()
	var v string
	switch err := sd.DB().QueryRowContext(ctx, `SELECT v FROM kv WHERE k=?`, k).Scan(&v); {
	case err == sql.ErrNoRows:
		return "", false
	case err != nil:
		t.Fatalf("scratch query %s: %v", k, err)
	}
	return v, true
}

// TestDurableContentionOneWriterOtherDefers is the direct outage proof: two replicas
// with the SAME converged membership open the SAME org store. Exactly one is the
// HRW-elected writer; the other DEFERS (its write fails closed). BOTH open without a
// "store unavailable" error, and the durable object only ever holds the owner's write
// — no split-brain.
func TestDurableContentionOneWriterOtherDefers(t *testing.T) {
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

	owner := newDurablePod(t, cs, ownerID, set, orgID, dbKey)
	other := newDurablePod(t, cs, otherID, set, orgID, dbKey)

	// Both hydrate WITHOUT error — the store is available on both replicas (the exact
	// regression: a second replica must not break the store).
	if err := owner.d.Hydrate(ctx); err != nil {
		t.Fatalf("owner hydrate: %v", err)
	}
	if err := other.d.Hydrate(ctx); err != nil {
		t.Fatalf("second replica hydrate returned an error (store must stay available): %v", err)
	}
	if !owner.d.Owned() {
		t.Fatal("the HRW-elected owner did not acquire the writer lease")
	}
	if other.d.Owned() {
		t.Fatal("the non-owner acquired the lease too — split-brain (two writers)")
	}

	owner.open(t)
	other.open(t)

	// The owner writes and ships; the ship is acknowledged.
	putKV(t, owner.db, "k1", "owner-wrote")
	if acked, err := owner.d.Sync(ctx); err != nil || !acked {
		t.Fatalf("owner sync: acked=%v err=%v, want true/nil", acked, err)
	}

	// The non-owner's write is refused at the gate — never a second writer to the
	// same durable object.
	putKV(t, other.db, "k1", "non-owner-wrote")
	acked, err := other.d.Sync(ctx)
	if acked || !errors.Is(err, ErrNotOwner) {
		t.Fatalf("non-owner sync: acked=%v err=%v, want false/ErrNotOwner", acked, err)
	}

	// The durable object holds ONLY the owner's write.
	if v, ok := durableValue(t, ctx, cs, dbKey, "k1"); !ok || v != "owner-wrote" {
		t.Fatalf("durable k1 = %q,%v, want \"owner-wrote\",true (non-owner must not have written)", v, ok)
	}
}

// TestDurableTakeoverHydratesNoLostWriteFencesDeposed is the rolling-handoff proof:
// a fresh successor becomes owner, HYDRATES the predecessor's shipped snapshot (no
// lost write), serves on it, and advances the round; the deposed predecessor that
// keeps running is FENCED (its stale-round ship is rejected, never acknowledged,
// never reaches the durable state).
func TestDurableTakeoverHydratesNoLostWriteFencesDeposed(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	const orgID = "acme"
	dbKey := replica.DBPath(orgID, "", "research")

	// Old owner: sole-member view, claims round 1, writes k1, ships.
	old := newDurablePod(t, cs, "pod-old", []Member{{ID: "pod-old"}}, orgID, dbKey)
	if err := old.d.Hydrate(ctx); err != nil {
		t.Fatalf("old hydrate: %v", err)
	}
	old.open(t)
	putKV(t, old.db, "k1", "v1")
	if acked, err := old.d.Sync(ctx); err != nil || !acked {
		t.Fatalf("old sync k1: acked=%v err=%v", acked, err)
	}
	oldRound := old.d.lease.Round

	// Fresh successor boots (empty local dir), becomes owner, hydrates the shipped
	// snapshot and claims a strictly higher round.
	next := newDurablePod(t, cs, "pod-new", []Member{{ID: "pod-new"}}, orgID, dbKey)
	if err := next.d.Hydrate(ctx); err != nil {
		t.Fatalf("successor hydrate: %v", err)
	}
	if !next.d.Owned() {
		t.Fatal("successor did not acquire the lease")
	}
	if next.d.lease.Round <= oldRound {
		t.Fatalf("successor round %d not strictly > predecessor %d", next.d.lease.Round, oldRound)
	}
	next.open(t)

	// The successor SERVES on the hydrated state: k1 survived the handoff.
	if !hasKV(t, next.db, "k1") {
		t.Fatal("successor lost the pre-handoff write k1 (hydrate-on-open failed)")
	}

	// Successor writes k2 and ships — durable now carries k1 AND k2.
	putKV(t, next.db, "k2", "v2")
	if acked, err := next.d.Sync(ctx); err != nil || !acked {
		t.Fatalf("successor sync k2: acked=%v err=%v", acked, err)
	}

	// The DEPOSED old owner keeps running and ships at its STALE round: fenced.
	putKV(t, old.db, "k-stale", "should-be-fenced")
	acked, err := old.d.Sync(ctx)
	if err != nil {
		t.Fatalf("deposed old sync unexpected hard error: %v", err)
	}
	if acked {
		t.Fatal("deposed writer ACKED a fenced ship (split-brain double-write)")
	}
	if old.d.Owned() {
		t.Fatal("deposed writer still believes it holds the lease")
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

// TestDurableShipsAndRestoresSidecar covers the cek-encrypting build's path: the
// database's <db>.dek key sidecar must travel WITH the database bytes so a successor
// can open the encrypted file. A fake sidecar stands in for cek's real one (no cek /
// no fsync needed) — what is under test is that Durable frames it on ship and writes
// it back on hydrate.
func TestDurableShipsAndRestoresSidecar(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	const orgID = "acme"
	dbKey := replica.DBPath(orgID, "", "research")

	old := newDurablePod(t, cs, "pod-old", []Member{{ID: "pod-old"}}, orgID, dbKey)
	if err := old.d.Hydrate(ctx); err != nil {
		t.Fatalf("old hydrate: %v", err)
	}
	old.open(t)
	putKV(t, old.db, "k1", "v1")
	sidecar := []byte("fake-dek: fileID(16) || wrapped-DEK")
	if err := os.WriteFile(old.d.dbPath+dekSuffix, sidecar, 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	if acked, err := old.d.Sync(ctx); err != nil || !acked {
		t.Fatalf("old sync: acked=%v err=%v", acked, err)
	}

	// A fresh successor hydrates: it must receive BOTH the database (k1) AND the
	// sidecar written beside its own file.
	next := newDurablePod(t, cs, "pod-new", []Member{{ID: "pod-new"}}, orgID, dbKey)
	if err := next.d.Hydrate(ctx); err != nil {
		t.Fatalf("successor hydrate: %v", err)
	}
	got, err := os.ReadFile(next.d.dbPath + dekSuffix)
	if err != nil {
		t.Fatalf("successor sidecar not restored: %v", err)
	}
	if !bytes.Equal(got, sidecar) {
		t.Fatalf("successor sidecar = %q, want %q", got, sidecar)
	}
	next.open(t)
	if !hasKV(t, next.db, "k1") {
		t.Fatal("successor lost k1 across the sidecar ship")
	}
}

// TestFrameRoundTrip pins the (sidecar,db) framing: empty and non-empty sidecars both
// split back exactly, so the encrypting (sidecar) and plaintext (no-sidecar) builds
// share one wire format.
func TestFrameRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sidecar []byte
		db      []byte
	}{
		{"no-sidecar", nil, []byte("plaintext-db-bytes")},
		{"with-sidecar", []byte("wrapped-dek"), []byte("encrypted-db-bytes")},
		{"empty-both", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc, db, err := unframe(frame(tc.sidecar, tc.db))
			if err != nil {
				t.Fatalf("unframe: %v", err)
			}
			if len(sc) != len(tc.sidecar) || (len(sc) > 0 && !bytes.Equal(sc, tc.sidecar)) {
				t.Fatalf("sidecar = %q, want %q", sc, tc.sidecar)
			}
			if len(db) != len(tc.db) || (len(db) > 0 && !bytes.Equal(db, tc.db)) {
				t.Fatalf("db = %q, want %q", db, tc.db)
			}
		})
	}
	if _, _, err := unframe([]byte{0xff}); err == nil {
		t.Fatal("unframe of a truncated frame must error, not restore arbitrary bytes")
	}
}

// TestDurableStoreUnreachableDegradesReadOnly proves the availability posture: when
// the object store is unreachable the store still OPENS (never "unavailable"), reads
// serve off local, and writes fail CLOSED (no unfenced write).
func TestDurableStoreUnreachableDegradesReadOnly(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	cs.failAll = fmt.Errorf("object store unreachable")
	const orgID = "acme"
	dbKey := replica.DBPath(orgID, "", "research")

	p := newDurablePod(t, cs, "pod-a", []Member{{ID: "pod-a"}}, orgID, dbKey)

	// Hydrate surfaces the store error (for the caller to log) but does NOT prevent
	// opening the local handle.
	if err := p.d.Hydrate(ctx); err == nil {
		t.Fatal("expected the unreachable-store error to surface for logging")
	}
	p.open(t) // must succeed regardless
	if p.d.Owned() {
		t.Fatal("must not hold the lease when the store is unreachable")
	}

	// A write fails closed — never an unfenced ship.
	putKV(t, p.db, "k1", "v1")
	if acked, err := p.d.Sync(ctx); acked || err == nil {
		t.Fatalf("unreachable-store write must fail closed: acked=%v err=%v", acked, err)
	}
}
