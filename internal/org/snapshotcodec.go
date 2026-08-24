// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// snapshotcodec.go is the SWAPPABLE ship mechanism — the client that lets HOW the durable
// payload is produced/applied change without touching WHEN it ships (the fence, the
// monotone round, CarryForward). Today the default codec folds the WAL into the main file
// and copies that file whole; a WAL-frame delta codec
// (github.com/hanzoai/replicate — low-memory streaming) drops in behind this same
// interface later, and the fence ships whatever bytes produce() returns and hands
// whatever bytes it admitted to apply(). The codec works on PLAINTEXT payloads; envelope
// sealing (Cipher) is orthogonal, applied by Sync/restore AROUND the codec — so the two
// concerns (what bytes represent the DB vs. how they are encrypted at rest) never
// complect.

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"

	"github.com/hanzoai/vfs/replica"
)

// snapshotCodec produces a durable payload from the bound local database and applies a
// restored payload back onto it. produce reads a database file that is held still for
// the length of the read; apply reconstructs the local file (and any auxiliary files)
// atomically. Both operate on plaintext.
type snapshotCodec interface {
	// produce returns the durable payload for the database at dbPath. db is the store's
	// own handle — the single serialized connection every statement runs through — and
	// reader is a second, read-only handle on the SAME file, or nil where the backend
	// has no second handle to give. The returned bytes are the fence's ship payload.
	produce(ctx context.Context, db, reader *sql.DB, dbPath string) ([]byte, error)
	// apply writes a produced payload back onto the local database at dbPath. An empty
	// payload (nothing shipped yet) is a no-op that keeps whatever the local file holds.
	apply(dbPath string, payload []byte) error
}

// wholeFile is the default codec: fold the WAL into the main file so it holds every
// committed page, hold that file still, and ship its bytes.
//
// The bytes are the WHOLE payload. There is no key material to carry beside them: a
// database's key is DERIVED from the deployment's master and the namespace that owns
// it, so a successor holding the same master reopens the restored file by naming it,
// with nothing shipped and nothing to lose. This used to ship a wrapped-key sidecar
// framed ahead of the database, which is the thing that could go missing.
//
// seal is the crypto client. The SQLCipher page-level and plaintext backends encrypt on
// WRITE, so after the fold their main file already holds fresh ciphertext and seal is a
// successful no-op. The pure-Go encryption ENVELOPE instead defers encryption: its
// plaintext lives on tmpfs and the real path is last-sealed ciphertext until re-encrypted,
// so on that backend seal re-encrypts, and produce reads the real path AFTER it — never
// stale ciphertext, which would be a lost acked write on takeover.
type wholeFile struct {
	seal func(*sql.DB) error
}

// produce folds the WAL into the main file, pins that file, and copies it.
//
// The store's connection is taken for the fold and given back before a single byte is
// copied, so a statement that arrives during a ship waits for two short round trips
// rather than for the copy. The pin is what makes that safe: it is a read transaction on
// a second handle, and while it is open no checkpoint may copy a WAL frame into the main
// file, so the bytes read are the bytes the fold left there.
func (wf wholeFile) produce(ctx context.Context, db, reader *sql.DB, dbPath string) ([]byte, error) {
	release, err := pin(ctx, db, reader)
	if err != nil {
		return nil, fmt.Errorf("org: snapshot %s: %w", dbPath, err)
	}
	defer release()
	if wf.seal != nil {
		if err := wf.seal(db); err != nil {
			return nil, fmt.Errorf("org: snapshot seal %s: %w", dbPath, err)
		}
	}
	return readSnapshot(ctx, dbPath)
}

// pin folds the WAL into the main database file and holds that file still, returning the
// release its caller defers. It is the whole reason a ship no longer costs the store its
// connection.
//
// WHAT IT HOLDS, AND FOR HOW LONG. The store's connection is taken for two statements —
// the fold, and the BEGIN that opens the read transaction — and released before produce
// reads anything. Everything queued behind that connection waits for those two, not for
// the copy, the seal or the object store.
//
// WHY THE READ TRANSACTION HOLDS THE FILE STILL. In WAL mode a writer appends frames to
// the -wal file and never touches the main database file; the only thing that writes the
// main file is a checkpoint. A reader that begins while the WAL is empty takes the same
// lock a checkpointer must hold exclusively to copy frames back, so for as long as that
// transaction is open no checkpoint — the automatic one a commit triggers included —
// can move a byte of the main file. The copy therefore reads exactly what the fold left.
//
// WHY THE ORDER IS FOLD, PIN, RELEASE. That lock is the one a reader takes only when the
// WAL is empty. A frame appended between the fold and the BEGIN puts the reader on a
// different lock, leaves the checkpointer free, and the file moves under the copy. Every
// write to this database runs through the connection held across both statements, so
// there is no moment in which one can be appended.
//
// A nil reader is the backend that has no second handle to give: the fold still runs,
// and produce copies a file nothing here holds still.
func pin(ctx context.Context, db, reader *sql.DB) (release func(), err error) {
	nothing := func() {}

	var rconn *sql.Conn
	if reader != nil {
		if rconn, err = reader.Conn(ctx); err != nil {
			return nothing, fmt.Errorf("reader: %w", err)
		}
		// Every way out of here that is not a pin gives the reader back.
		defer func() {
			if err != nil {
				rconn.Close()
			}
		}()
		// The handle is the snapshot's alone and it only ever reads. Saying so to the
		// engine is what keeps that true of every statement, not only the two below.
		if _, err = rconn.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
			return nothing, fmt.Errorf("reader read-only: %w", err)
		}
	}

	var wconn *sql.Conn
	if wconn, err = db.Conn(ctx); err != nil {
		return nothing, fmt.Errorf("conn: %w", err)
	}
	defer wconn.Close()

	var busy, logFrames, checkpointed int
	if err = wconn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return nothing, fmt.Errorf("fold: %w", err)
	}
	if err = folded(busy, logFrames, checkpointed); err != nil {
		return nothing, err
	}
	if rconn == nil {
		return nothing, nil
	}

	var tx *sql.Tx
	if tx, err = rconn.BeginTx(ctx, nil); err != nil {
		return nothing, fmt.Errorf("pin: %w", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()
	// BEGIN alone is deferred and takes no lock. A statement is what opens the read
	// transaction, and reading the schema is the cheapest one every database answers.
	var tables int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&tables); err != nil {
		return nothing, fmt.Errorf("pin read: %w", err)
	}
	return func() {
		tx.Rollback()
		rconn.Close()
	}, nil
}

// folded reads the row PRAGMA wal_checkpoint(TRUNCATE) answers with — (busy, log,
// checkpointed) — and says what a fold that did not finish left behind.
//
// There are two ways not to finish and they are different facts. Frames left in the WAL
// means the main file is MISSING committed rows, so a copy of it would be a partial
// snapshot shipped as a whole one. Every frame moved and a WAL still standing means the
// file is complete and only the reset lost the race — but the read transaction the copy
// is about to open takes the lock a checkpointer needs ONLY while the WAL is empty, so
// with one standing there is nothing holding the file still.
//
// Both fail closed. Reporting the first for the second is what makes it worth separating:
// it sends a reader hunting for lost writes that are all present.
func folded(busy, logFrames, checkpointed int) error {
	if busy == 0 {
		return nil
	}
	if checkpointed != logFrames {
		return fmt.Errorf("fold moved %d of %d WAL frames — the main file is missing committed rows", checkpointed, logFrames)
	}
	return fmt.Errorf("fold left a WAL of %d frames standing, every one of them already in the main file — something else is reading this database, so a read transaction here would not hold the file still", logFrames)
}

// readSnapshot reads the main-path database bytes — the durable payload, entire — and
// stops at the caller's deadline.
//
// The deadline is the point. The read runs with the file pinned, and a pin lets writes
// accumulate in the WAL instead of blocking; a read with no ceiling would let that grow
// for as long as the disk took. Ending at the ship's own deadline bounds it: the write
// is not acknowledged, the caller retries, and the WAL is folded on the next attempt.
func readSnapshot(ctx context.Context, dbPath string) ([]byte, error) {
	f, err := os.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("org: snapshot read %s: %w", dbPath, err)
	}
	defer f.Close()
	size := int64(0)
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	out := make([]byte, 0, size)
	buf := make([]byte, 4<<20)
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("org: snapshot read %s: %w", dbPath, err)
		}
		n, err := f.Read(buf)
		out = append(out, buf[:n]...)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("org: snapshot read %s: %w", dbPath, err)
		}
	}
}

// apply atomically swaps the database bytes into place (replica.RestoreFile, which
// MkdirAll's the parent so a fresh successor whose orgs/<slug>/ dir does not exist yet
// gets one). An empty payload — nothing shipped yet — is a no-op that keeps whatever the
// local file holds.
func (wholeFile) apply(dbPath string, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	return replica.RestoreFile(dbPath, payload)
}
