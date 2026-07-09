// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// handoff_test.go is the money/state-safety proof for the WHOLE composition: HRW
// election (who) + CASFencer monotone lease round (fencing) + replica.FencedStore
// admission (store) + idem exactly-once (dedup), exercised as real pods sharing ONE
// object store, each with its own local SQLite. It answers, directly, the four
// hazards: (a) partition minority, (b) rolling-upgrade handoff, (c) duplicate
// request, (d) a deposed writer that keeps running.
//
// The discipline every pod follows — the "wire the gate" contract:
//
//	takeover : Acquire the lease (fail closed if not the elected owner), then
//	           CarryForward-seal the org DB to the lease round, hydrating the latest
//	           durable snapshot (so we serve on committed state).
//	serve    : apply the request via idem.Once (exactly-once in the local DB), then
//	           SHIP the snapshot fenced at the lease round. A request is ACKED only
//	           if the fenced ship lands; a fenced ship (ErrStaleRound) means we were
//	           deposed — the request is NOT acked and the caller retries elsewhere.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/internal/idem"
	"github.com/hanzoai/ha"
	"github.com/hanzoai/vfs/replica"
)

// pod models one replica: a local per-org SQLite, a fenced ship path and a fencer,
// all bound to the shared object store cs.
type pod struct {
	id     string
	local  *replica.SQLiteDB
	fenced *replica.FencedStore
	fencer *CASFencer
}

func newPod(t *testing.T, cs replica.ConditionalStore, id string, set []Member) *pod {
	t.Helper()
	local, err := replica.OpenSQLite(filepath.Join(t.TempDir(), id+".db"))
	if err != nil {
		t.Fatalf("%s open: %v", id, err)
	}
	t.Cleanup(func() { _ = local.Close() })
	ensureCharges(local)
	return &pod{
		id:     id,
		local:  local,
		fenced: replica.NewFencedStore(cs),
		fencer: NewCASFencer(cs, stubView{id: id, set: set}),
	}
}

// ensureCharges creates the money table: a request charged twice would violate the
// PK, so a double-execute is directly observable as a second row / a PK error.
func ensureCharges(db *replica.SQLiteDB) error {
	_, err := db.DB().ExecContext(context.Background(),
		`CREATE TABLE IF NOT EXISTS charges(req TEXT PRIMARY KEY, amount TEXT)`)
	return err
}

// takeover acquires the lease and seals+hydrates the org DB to the lease round.
func (p *pod) takeover(ctx context.Context, orgID, dbKey string) (ha.Lease, error) {
	lease, err := p.fencer.Acquire(ctx, orgID)
	if err != nil {
		return ha.Lease{}, err
	}
	err = p.fenced.CarryForward(ctx, dbKey, uint64(lease.Round), func(payload []byte) error {
		if len(payload) == 0 {
			return nil // nothing durable yet — keep our freshly-created schema.
		}
		return p.local.Restore(ctx, payload)
	})
	if err != nil {
		return ha.Lease{}, err
	}
	if err := ensureCharges(p.local); err != nil { // an older snapshot may predate the table.
		return ha.Lease{}, err
	}
	return lease, nil
}

// serve applies reqKey exactly-once locally, then ships fenced at round. acked is
// true only if the fenced ship landed. applied reports whether THIS call ran the
// effect (false = idempotent replay of a prior run). A ship fenced as ErrStaleRound
// returns acked=false with a nil error: the pod was deposed and correctly does not
// acknowledge.
func (p *pod) serve(ctx context.Context, dbKey, reqKey, amount string, round uint64) (acked, applied bool, err error) {
	_, applied, err = idem.Once(ctx, p.local.DB(), reqKey, round, func(ctx context.Context, tx *sql.Tx) ([]byte, error) {
		if _, e := tx.ExecContext(ctx, `INSERT INTO charges(req, amount) VALUES(?, ?)`, reqKey, amount); e != nil {
			return nil, e
		}
		return []byte("ok:" + amount), nil
	})
	if err != nil && !errors.Is(err, idem.ErrAlreadyApplied) {
		return false, applied, err
	}
	// Ship-before-ack: re-ship the current snapshot (fresh apply OR dedup replay)
	// and only acknowledge if the fenced store admits our round.
	snap, e := p.local.Snapshot(ctx)
	if e != nil {
		return false, applied, e
	}
	if e := p.fenced.Put(ctx, dbKey, snap, round); e != nil {
		if errors.Is(e, replica.ErrStaleRound) {
			return false, applied, nil // deposed: not acknowledged.
		}
		return false, applied, e
	}
	return true, applied, nil
}

func chargeCount(t *testing.T, db *replica.SQLiteDB, req string) int {
	t.Helper()
	var n int
	if err := db.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM charges WHERE req = ?`, req).Scan(&n); err != nil {
		t.Fatalf("count charges: %v", err)
	}
	return n
}

// pickDeposedNonOwner returns (owner, other) for a two-pod set under orgID, so a
// test can make the deposed pod the HRW NON-owner deterministically.
func pickDeposedNonOwner(orgID string, a, b string) (owner, other string) {
	set := []Member{{ID: a}, {ID: b}}
	owner = electedOwnerID(orgID, set)
	if owner == a {
		return a, b
	}
	return b, a
}

// (b)+(c): TestHandoffNoDoubleExecuteAcrossUpgrade. The old owner charges req-X and
// ships; ownership flips to a fresh successor that hydrates the shipped snapshot; a
// retry of req-X re-routed to the successor is DEDUPED, not re-charged. This is the
// rolling-upgrade + duplicate-request proof in one.
func TestHandoffNoDoubleExecuteAcrossUpgrade(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	const org, dbKey = "acme", "orgs/acme/app.db"

	// Old owner: sole member in its view, claims round 1, charges req-X, ships.
	old := newPod(t, cs, "pod-old", []Member{{ID: "pod-old"}})
	lo, err := old.takeover(ctx, org, dbKey)
	if err != nil {
		t.Fatalf("old takeover: %v", err)
	}
	acked, applied, err := old.serve(ctx, dbKey, "req-X", "5usd", uint64(lo.Round))
	if err != nil || !acked || !applied {
		t.Fatalf("old serve req-X: acked=%v applied=%v err=%v, want true/true/nil", acked, applied, err)
	}

	// A FRESH successor boots (empty local DB), becomes owner, and takes over: it
	// hydrates the old owner's shipped snapshot (which carries req-X's charge AND
	// its dedup key) and claims a strictly higher round.
	new := newPod(t, cs, "pod-new", []Member{{ID: "pod-new"}})
	ln, err := new.takeover(ctx, org, dbKey)
	if err != nil {
		t.Fatalf("new takeover: %v", err)
	}
	if ln.Round <= lo.Round {
		t.Fatalf("successor round %d not > predecessor %d", ln.Round, lo.Round)
	}

	// The retry of req-X lands on the successor. It must NOT re-charge.
	acked, applied, err = new.serve(ctx, dbKey, "req-X", "5usd", uint64(ln.Round))
	if err != nil {
		t.Fatalf("successor serve req-X retry: %v", err)
	}
	if applied {
		t.Fatal("successor RE-EXECUTED req-X across the handoff (double-charge)")
	}
	if !acked {
		t.Fatal("successor should still acknowledge the (deduped) retry")
	}
	if got := chargeCount(t, new.local, "req-X"); got != 1 {
		t.Fatalf("req-X charged %d times after handoff, want exactly 1", got)
	}
}

// (d) store-fence: TestDeposedWriterShipIsFenced. After a successor takes over, a
// deposed old owner that keeps running and ships at its STALE round is rejected by
// the fenced store, so its work is neither durable nor acknowledged — and the real
// owner never sees that request.
func TestDeposedWriterShipIsFenced(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	const org, dbKey = "acme", "orgs/acme/app.db"

	old := newPod(t, cs, "pod-old", []Member{{ID: "pod-old"}})
	lo, err := old.takeover(ctx, org, dbKey)
	if err != nil {
		t.Fatalf("old takeover: %v", err)
	}
	if acked, _, err := old.serve(ctx, dbKey, "req-X", "5usd", uint64(lo.Round)); err != nil || !acked {
		t.Fatalf("old serve req-X: acked=%v err=%v", acked, err)
	}

	// Successor takes over at a higher round, sealing the DB object.
	new := newPod(t, cs, "pod-new", []Member{{ID: "pod-new"}})
	if _, err := new.takeover(ctx, org, dbKey); err != nil {
		t.Fatalf("new takeover: %v", err)
	}

	// Deposed old owner (unaware — still holding its old round) ships req-Y. Its
	// local apply happens, but the fenced ship is REJECTED, so req-Y is not acked.
	acked, _, err := old.serve(ctx, dbKey, "req-Y", "7usd", uint64(lo.Round))
	if err != nil {
		t.Fatalf("deposed serve unexpected hard error: %v", err)
	}
	if acked {
		t.Fatal("deposed writer ACKED a ship that must have been fenced (split-brain double-write)")
	}

	// The real owner's durable state never received req-Y.
	payload, round, err := new.fenced.Get(ctx, dbKey)
	if err != nil {
		t.Fatalf("read durable object: %v", err)
	}
	if round <= uint64(lo.Round) {
		t.Fatalf("durable round %d did not advance past the deposed owner's %d", round, lo.Round)
	}
	// Restore the durable bytes into a scratch DB and confirm req-Y is absent.
	scratch, err := replica.OpenSQLite(filepath.Join(t.TempDir(), "scratch.db"))
	if err != nil {
		t.Fatalf("scratch open: %v", err)
	}
	defer scratch.Close()
	if err := scratch.Restore(ctx, payload); err != nil {
		t.Fatalf("restore durable: %v", err)
	}
	var n int
	if err := scratch.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM charges WHERE req = 'req-Y'`).Scan(&n); err != nil {
		t.Fatalf("scan durable: %v", err)
	}
	if n != 0 {
		t.Fatal("deposed writer's req-Y leaked into the durable state")
	}
}

// (d) election-gate: TestDeposedWriterRefusedByElection. Once membership converges
// so HRW names the successor, the deposed pod's Acquire is refused (ErrNotOwner)
// before it does any work — the cheap, common path.
func TestDeposedWriterRefusedByElection(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	const org, dbKey = "acme", "orgs/acme/app.db"
	ownerID, deposedID := pickDeposedNonOwner(org, "pod-1", "pod-2")
	both := []Member{{ID: "pod-1"}, {ID: "pod-2"}}

	// The elected owner takes over and serves.
	owner := newPod(t, cs, ownerID, both)
	lo, err := owner.takeover(ctx, org, dbKey)
	if err != nil {
		t.Fatalf("owner takeover: %v", err)
	}
	if acked, _, err := owner.serve(ctx, dbKey, "req-X", "5usd", uint64(lo.Round)); err != nil || !acked {
		t.Fatalf("owner serve: acked=%v err=%v", acked, err)
	}

	// The deposed pod (also sees the converged set) is refused by the election gate.
	deposed := newPod(t, cs, deposedID, both)
	if _, err := deposed.takeover(ctx, org, dbKey); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("deposed takeover: err=%v, want ErrNotOwner", err)
	}
}

// (a) partition: TestPartitionMinorityCannotAcquire. A pod that cannot reach the
// linearizable store (its object store is unreachable) cannot advance the round, so
// Acquire fails and it never writes — the minority is fenced by its own inability to
// claim, and the majority (which can reach the store) advances unobstructed.
func TestPartitionMinorityCannotAcquire(t *testing.T) {
	ctx := context.Background()
	partitioned := newFakeCondStore()
	partitioned.failAll = fmt.Errorf("network partition: object store unreachable")

	minority := NewCASFencer(partitioned, stubView{id: "pod-minority", set: []Member{{ID: "pod-minority"}}})
	if _, err := minority.Acquire(ctx, "acme"); err == nil {
		t.Fatal("a store-partitioned minority must NOT be able to acquire the lease")
	}

	// Meanwhile the majority, with a reachable store, acquires and advances normally.
	reachable := newFakeCondStore()
	majority := NewCASFencer(reachable, stubView{id: "pod-majority", set: []Member{{ID: "pod-majority"}}})
	if _, err := majority.Acquire(ctx, "acme"); err != nil {
		t.Fatalf("majority with a reachable store must acquire: %v", err)
	}
}
