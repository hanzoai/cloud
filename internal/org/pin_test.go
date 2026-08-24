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

	dy := NewDurability(cs, &liveView{self: "solo", set: []Member{{ID: "solo"}}}, nil,
		WithReader(func(_ namespace.Namespace, _, p string) (*sql.DB, error) {
			h, err := sql.Open("sqlite", testDSN(p))
			if err != nil {
				return nil, err
			}
			sqlpool.Single(h)
			return h, h.Ping()
		}))
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
