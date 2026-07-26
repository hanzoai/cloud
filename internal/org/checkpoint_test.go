// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// checkpoint_test.go pins the crypto-integration contract: a fenced ship must fold the
// WAL into the real on-disk file (and, on the pure-Go encryption envelope, re-encrypt it)
// BEFORE reading the bytes it ships — otherwise it ships STALE state and a takeover reads
// a lost acked write. It proves the WithCheckpoint seam runs on every Sync before the
// read, that the shipped snapshot carries the just-committed write, and that a failing
// checkpoint fails the ship CLOSED (never a stale ship acked).

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/vfs/replica"
)

// TestProduceHoldsSoleConnAcrossReadWithCheckpoint pins the torn-snapshot guard on the
// PRODUCTION ship path. os.ReadFile takes no SQLite lock, so if produce reads the file
// without holding the store's sole connection, a concurrent commit — and the WAL
// auto-checkpoint it can trigger, which writes pages straight into the real path — tears
// the image mid-read; the fence then ACKS that corrupt snapshot at the lease round and a
// successor restores it. The default (nil-checkpoint) path holds the connection across
// the read; the injected-checkpoint path, which is the one buildDurability wires for
// production, must do the same.
//
// Deterministic: the sole connection (MaxOpenConns(1)) is held by the test, so a produce
// that acquires a connection for its read CANNOT get one and fails on the ctx deadline.
// Without the guard produce would happily read the file and return a payload.
func TestProduceHoldsSoleConnAcrossReadWithCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.db")
	db, err := sql.Open("sqlite", testDSN(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ensureKV(t, db)
	putKV(t, db, "k1", "v1")

	// Occupy the SOLE connection, standing in for a concurrent in-flight writer.
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("take sole conn: %v", err)
	}
	defer held.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	// A no-op checkpoint: this asserts the guard around the READ, independently of what
	// the injected checkpoint itself does (production's also takes, and releases, a conn).
	codec := wholeFile{checkpoint: func(context.Context, *sql.DB) error { return nil }}
	if _, err := codec.produce(ctx, db, path); err == nil {
		t.Fatal("produce read the database file WITHOUT holding the sole connection — a concurrent writer can tear the snapshot mid-read and the fence would ack the corrupt image")
	}

	// Control: with the connection free, the same produce succeeds and carries the write.
	held.Close()
	payload, err := codec.produce(context.Background(), db, path)
	if err != nil {
		t.Fatalf("produce with a free connection must succeed: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("produce returned an empty payload")
	}
}

// TestSyncCheckpointsBeforeShip: with an injected checkpoint (standing in for the
// envelope's re-encrypting Checkpoint), Sync must invoke it before reading the file, and
// the durable object must then carry the committed write — i.e. the ship is FRESH.
func TestSyncCheckpointsBeforeShip(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	const orgID = "acme"
	dbKey := replica.DBPath(orgID, "", "research")

	var checkpoints atomic.Int64
	dy := NewDurability(cs, &liveView{self: "solo", set: []Member{{ID: "solo"}}}, nil,
		WithCheckpoint(func(ctx context.Context, db *sql.DB) error {
			// Do the real WAL fold (the envelope would ALSO re-encrypt here) so the file
			// read that follows sees the latest committed page, then record the call.
			if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				return err
			}
			checkpoints.Add(1)
			return nil
		}))
	d := dy.For(orgID, dbKey, filepath.Join(t.TempDir(), "research.db"))
	if err := d.Hydrate(ctx); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	db := openBoundDB(t, d)

	putKV(t, db, "k1", "v1")
	if acked, err := d.Sync(ctx); err != nil || !acked {
		t.Fatalf("sync: acked=%v err=%v", acked, err)
	}
	if checkpoints.Load() == 0 {
		t.Fatal("Sync must run the injected checkpoint before shipping (envelope re-encrypt point)")
	}
	// The shipped durable object carries the committed write — the ship was fresh, not
	// stale. (On the envelope backend this is exactly what guards against shipping stale
	// ciphertext that predates k1.)
	if v, ok := durableValue(t, ctx, cs, dbKey, "k1"); !ok || v != "v1" {
		t.Fatalf("durable k1 = %q,%v after checkpointed ship, want v1,true", v, ok)
	}
}

// TestSyncFailsClosedOnCheckpointError: if the checkpoint (envelope re-encrypt) fails, the
// ship must NOT proceed — a stale snapshot shipped as complete is a silent lost write. The
// write is not acknowledged and the durable object is untouched.
func TestSyncFailsClosedOnCheckpointError(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	const orgID = "acme"
	dbKey := replica.DBPath(orgID, "", "research")

	dy := NewDurability(cs, &liveView{self: "solo", set: []Member{{ID: "solo"}}}, nil,
		WithCheckpoint(func(context.Context, *sql.DB) error {
			return fmt.Errorf("envelope re-encrypt failed")
		}))
	d := dy.For(orgID, dbKey, filepath.Join(t.TempDir(), "research.db"))
	if err := d.Hydrate(ctx); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	db := openBoundDB(t, d)

	putKV(t, db, "k1", "v1")
	acked, err := d.Sync(ctx)
	if acked || err == nil {
		t.Fatalf("a failed checkpoint must fail the ship closed: acked=%v err=%v", acked, err)
	}
	// Fail-closed guarantees no stale ship: Sync errors out of produce() BEFORE any
	// fenced Put, so the write is neither acknowledged nor shipped — the successor will
	// never read a snapshot that predates the checkpoint that could not run.
}
