// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// pin_test.go holds the two facts a ship rests on and neither of which is obvious:
// while the file is pinned nothing can move it, and the store's connection is not what
// pins it. Both are asserted against a control that must FAIL to hold, so a test that
// stopped being able to observe either would say so instead of passing.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sqlpool"
	"github.com/hanzoai/namespace"
	"github.com/hanzoai/vfs/replica"
)

// fileDigest is the whole-file identity the copy would have read.
func fileDigest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%s:%d", hex.EncodeToString(sum[:8]), len(b))
}

// openPair opens a WAL database the way a store does — one connection — and a second
// handle on the same file, the one a ship pins with.
func openPair(t *testing.T, path string) (db, reader *sql.DB) {
	t.Helper()
	open := func() *sql.DB {
		h, err := sql.Open("sqlite", testDSN(path))
		if err != nil {
			t.Fatalf("open %s: %v", path, err)
		}
		sqlpool.Single(h)
		if err := h.Ping(); err != nil {
			t.Fatalf("ping %s: %v", path, err)
		}
		t.Cleanup(func() { _ = h.Close() })
		return h
	}
	return open(), open()
}

func fillRows(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	blob := make([]byte, 4096)
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`INSERT INTO kv(k,v) VALUES(?,?)`, fmt.Sprintf("%d-%d", time.Now().UnixNano(), i), string(blob)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
}

// checkpoint asks the engine to copy WAL frames back into the main file — what an
// automatic checkpoint does at a commit, run on demand so the test needs no waiting.
func checkpoint(t *testing.T, db *sql.DB) {
	t.Helper()
	var busy, logFrames, done int
	if err := db.QueryRow(`PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &logFrames, &done); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
}

// A pinned file does not move, and the control proves the same writes move an unpinned
// one. Without the second arm this test would pass just as happily against a database
// nothing was writing to.
func TestPinHoldsTheFileStill(t *testing.T) {
	ctx := context.Background()

	t.Run("pinned", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "org.db")
		db, reader := openPair(t, path)
		ensureKV(t, db)
		fillRows(t, db, 200)

		release, err := pin(ctx, db, reader)
		if err != nil {
			t.Fatalf("pin: %v", err)
		}
		defer release()

		before := fileDigest(t, path)
		fillRows(t, db, 400)
		checkpoint(t, db)
		if after := fileDigest(t, path); after != before {
			t.Fatalf("the main file moved under a pin: %s -> %s", before, after)
		}
	})

	// Why pin holds the store's connection across BOTH the fold and the BEGIN. The lock
	// that stops a checkpoint is the one a reader takes only while the WAL is empty. Let a
	// single frame be appended in between and the reader lands elsewhere, the checkpointer
	// is free, and the file moves under the copy. This arm appends that frame on purpose.
	t.Run("frame appended before the read transaction", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "org.db")
		db, reader := openPair(t, path)
		ensureKV(t, db)
		fillRows(t, db, 200)

		var busy, logFrames, done int
		if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &done); err != nil {
			t.Fatalf("fold: %v", err)
		}
		fillRows(t, db, 1) // the window pin does not leave open

		rconn, err := reader.Conn(ctx)
		if err != nil {
			t.Fatalf("reader conn: %v", err)
		}
		defer rconn.Close()
		tx, err := rconn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		var tables int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&tables); err != nil {
			t.Fatalf("read: %v", err)
		}

		before := fileDigest(t, path)
		fillRows(t, db, 400)
		checkpoint(t, db)
		if after := fileDigest(t, path); after == before {
			t.Fatalf("a read transaction opened after a frame was appended held the file still (%s) — pin need not hold the connection across both statements, and its comment is wrong", before)
		}
	})

	t.Run("unpinned", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "org.db")
		db, reader := openPair(t, path)
		ensureKV(t, db)
		fillRows(t, db, 200)

		release, err := pin(ctx, db, nil) // fold only — nothing holds the file
		if err != nil {
			t.Fatalf("pin: %v", err)
		}
		release()
		_ = reader

		before := fileDigest(t, path)
		fillRows(t, db, 400)
		checkpoint(t, db)
		if after := fileDigest(t, path); after == before {
			t.Fatalf("the control did not move the file (%s), so the arm above proves nothing", before)
		}
	})
}

// pin returns with the store's connection back in the pool, so everything queued behind
// it runs while the file is still held. The deadline is the whole assertion: with the
// connection kept, this statement would wait for release and time out.
func TestPinLeavesTheStoreConnectionFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "org.db")
	db, reader := openPair(t, path)
	ensureKV(t, db)
	fillRows(t, db, 50)

	release, err := pin(context.Background(), db, reader)
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	defer release()

	quick, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int
	if err := db.QueryRowContext(quick, `SELECT COUNT(*) FROM kv`).Scan(&n); err != nil {
		t.Fatalf("a read waited for the pin instead of being served: %v", err)
	}
	if _, err := db.ExecContext(quick, `INSERT INTO kv(k,v) VALUES('under-pin','1')`); err != nil {
		t.Fatalf("a write waited for the pin instead of being served: %v", err)
	}
}

// The whole reason for the pin: a ship that runs while an org is being written must
// restore as an intact database, not a mixture of two. The writers run for the length of
// the ship, and what is asserted is the successor's view — the payload restored and
// checked by the engine itself.
func TestShipUnderWritesRestoresAnIntactDatabase(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	const orgID = "acme"
	dbKey := replica.DBPath(orgID, "", "research")

	dy := NewDurability(cs, &liveView{self: "solo", set: []Member{{ID: "solo"}}}, nil, WithReader(second))
	d := dy.For(testNS(orgID), "research", dbKey, filepath.Join(t.TempDir(), "research.db"))
	if err := d.Hydrate(ctx); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	db := openBoundDB(t, d)
	fillRows(t, db, 400)

	// Writers for the length of the ship. They append to the WAL and, left to itself, the
	// engine folds those frames back into the main file at a commit — which is exactly the
	// file the ship is reading.
	stop := make(chan struct{})
	writing := make(chan struct{})
	go func() {
		defer close(writing)
		blob := make([]byte, 4096)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := db.Exec(`INSERT INTO kv(k,v) VALUES(?,?)`, fmt.Sprintf("w-%d", i), string(blob)); err != nil {
				return
			}
			if i%50 == 0 {
				_, _ = db.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
			}
		}
	}()

	acked, err := d.Sync(ctx)
	close(stop)
	<-writing
	if err != nil || !acked {
		t.Fatalf("sync: acked=%v err=%v", acked, err)
	}

	payload, _, err := replica.NewFencedStore(cs).Get(ctx, dbKey)
	if err != nil {
		t.Fatalf("read durable object: %v", err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch.db")
	if err := replica.RestoreFile(scratch, payload); err != nil {
		t.Fatalf("restore: %v", err)
	}
	sd, err := sql.Open("sqlite", testDSN(scratch))
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer sd.Close()
	var verdict string
	if err := sd.QueryRow(`PRAGMA integrity_check`).Scan(&verdict); err != nil {
		t.Fatalf("the shipped database could not be checked: %v", err)
	}
	if verdict != "ok" {
		t.Fatalf("the shipped database is not intact: %s", verdict)
	}
	var rows int
	if err := sd.QueryRow(`SELECT COUNT(*) FROM kv`).Scan(&rows); err != nil {
		t.Fatalf("count restored: %v", err)
	}
	if rows < 400 {
		t.Fatalf("the shipped database holds %d rows, want at least the 400 committed before the ship", rows)
	}
}

// second is the opener a ship pins with, in the shape the composition root supplies:
// a read-only handle of its own on the same file, one per ship.
func second(_ namespace.Namespace, _, path string) (*sql.DB, error) {
	h, err := sql.Open("sqlite", testDSN(path))
	if err != nil {
		return nil, err
	}
	sqlpool.Single(h)
	return h, h.Ping()
}

// A ship that gets no handle does not acknowledge the write.
//
// The opener is injected, so this asserts the CONTRACT and not one build's engine: an
// opener answering (nil, nil) is exactly the shape cloud's org reader had wherever the
// codec was not linked, and the ship read it as "no reader available", copied the file
// with writers free to move it, and acked. The control is the same ship with a handle,
// which must ack — without it this would pass against a ship that never works.
func TestShipWithoutAHandleDoesNotAck(t *testing.T) {
	ship := func(t *testing.T, open opener) (bool, error) {
		t.Helper()
		ctx := context.Background()
		const orgID = "acme"
		dy := NewDurability(newFakeCondStore(), &liveView{self: "solo", set: []Member{{ID: "solo"}}}, nil, WithReader(open))
		d := dy.For(testNS(orgID), "research", replica.DBPath(orgID, "", "research"), filepath.Join(t.TempDir(), "research.db"))
		if err := d.Hydrate(ctx); err != nil {
			t.Fatalf("hydrate: %v", err)
		}
		fillRows(t, openBoundDB(t, d), 20)
		return d.Sync(ctx)
	}

	t.Run("no handle", func(t *testing.T) {
		acked, err := ship(t, func(namespace.Namespace, string, string) (*sql.DB, error) { return nil, nil })
		if acked || err == nil {
			t.Fatalf("a copy nothing held still was acknowledged: acked=%v err=%v", acked, err)
		}
	})

	t.Run("a handle", func(t *testing.T) {
		acked, err := ship(t, second)
		if !acked || err != nil {
			t.Fatalf("the control did not ship, so the arm above proves nothing: acked=%v err=%v", acked, err)
		}
	})
}

// Two ships on one file take turns, and the store keeps answering while they do.
//
// The collision is not a corner: a plane that ships per write has two in flight
// whenever two writes land together. The first ship holds a read transaction on its
// second handle for the length of the copy, and that is exactly the lock the second
// ship's fold needs. Unserialized, the second takes the store's sole connection and
// then sits inside PRAGMA wal_checkpoint for its whole busy timeout — so the pin's own
// property, that a ship costs the store two short statements and not the copy, is gone:
// everything queued behind that connection waits for the copy after all. Which is why
// the reading below is the assertion and not the acknowledgements: unserialized, the
// second ship's fold eventually wins the lock and both still ack, having stalled every
// statement on that org for seconds on the way.
//
// The seal runs INSIDE the copy, after the fold and with the pin open, so blocking it
// holds the first ship in precisely that state for as long as the test wants.
func TestTwoShipsOnOneFileTakeTurns(t *testing.T) {
	ctx := context.Background()
	cs := newFakeCondStore()
	const orgID = "acme"
	dbKey := replica.DBPath(orgID, "", "research")

	copying := make(chan struct{}) // closed once the first ship is inside the copy
	release := make(chan struct{}) // closed to let it out
	var once sync.Once
	dy := NewDurability(cs, &liveView{self: "solo", set: []Member{{ID: "solo"}}}, nil,
		WithReader(second),
		WithSeal(func(*sql.DB) error {
			first := false
			once.Do(func() { first = true })
			if first {
				close(copying)
				<-release
			}
			return nil
		}))
	d := dy.For(testNS(orgID), "research", dbKey, filepath.Join(t.TempDir(), "research.db"))
	if err := d.Hydrate(ctx); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	db := openBoundDB(t, d)
	fillRows(t, db, 100)

	type outcome struct {
		acked bool
		err   error
	}
	out := make(chan outcome, 2)
	ship := func() {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		acked, err := d.Sync(c)
		out <- outcome{acked, err}
	}

	go ship()
	<-copying
	// Writes between the two ships, which is what makes them two ships: a plane that
	// ships per write is shipping BECAUSE something was written. They land in the WAL,
	// and moving them back into the main file is what the second ship's fold has to do
	// and what the first ship's open read transaction will not let it do.
	fillRows(t, db, 200)
	go ship()
	time.Sleep(200 * time.Millisecond) // long enough for the second ship to get wherever it goes

	// THE ASSERTION. The first ship is inside its copy and the second is behind it, so
	// nothing should be holding the store's connection. A second ship parked inside a
	// fold is holding it, and this waits out the DSN's busy timeout and fails.
	quick, cancel := context.WithTimeout(ctx, 2*time.Second)
	var n int
	err := db.QueryRowContext(quick, `SELECT COUNT(*) FROM kv`).Scan(&n)
	cancel()
	if err != nil {
		t.Fatalf("a read waited on the second ship instead of being served: %v", err)
	}

	close(release)

	for i := 0; i < 2; i++ {
		select {
		case o := <-out:
			if !o.acked || o.err != nil {
				t.Fatalf("ship %d did not acknowledge: acked=%v err=%v", i, o.acked, o.err)
			}
		case <-time.After(time.Minute):
			t.Fatal("a ship never returned")
		}
	}

	payload, _, err := replica.NewFencedStore(cs).Get(ctx, dbKey)
	if err != nil {
		t.Fatalf("read durable object: %v", err)
	}
	scratch := filepath.Join(t.TempDir(), "scratch.db")
	if err := replica.RestoreFile(scratch, payload); err != nil {
		t.Fatalf("restore: %v", err)
	}
	sd, err := sql.Open("sqlite", testDSN(scratch))
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer sd.Close()
	var verdict string
	if err := sd.QueryRow(`PRAGMA integrity_check`).Scan(&verdict); err != nil {
		t.Fatalf("the shipped database could not be checked: %v", err)
	}
	if verdict != "ok" {
		t.Fatalf("the shipped database is not intact: %s", verdict)
	}
}

// A fold that did not finish says which of the two things happened.
//
// Both refuse, so the outcome alone cannot tell them apart, and the message is the whole
// of what a reader gets. `busy=1 log=41 ckpt=41` was reported as a snapshot that would
// miss committed WAL frames when every one of those frames was already in the main file
// and only the reset had lost a race — a reader following that goes looking for lost
// writes that are all present.
func TestAFoldThatDidNotFinishSaysWhich(t *testing.T) {
	if err := folded(0, 41, 41); err != nil {
		t.Fatalf("a finished fold is not a fault: %v", err)
	}
	if err := folded(0, 0, 0); err != nil {
		t.Fatalf("an empty WAL is not a fault: %v", err)
	}

	short := folded(1, 842, 0)
	if short == nil || !strings.Contains(short.Error(), "missing committed rows") {
		t.Fatalf("frames left in the WAL must report the main file incomplete: %v", short)
	}

	standing := folded(1, 41, 41)
	if standing == nil || strings.Contains(standing.Error(), "missing committed rows") {
		t.Fatalf("every frame moved, so nothing is missing; the WAL that would not reset is the fact: %v", standing)
	}
	if !strings.Contains(standing.Error(), "hold the file still") {
		t.Fatalf("the message does not say what a standing WAL costs: %v", standing)
	}
}
