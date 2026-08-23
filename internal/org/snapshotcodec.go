// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// snapshotcodec.go is the SWAPPABLE ship mechanism — the client that lets HOW the durable
// payload is produced/applied change without touching WHEN it ships (the fence, the
// monotone round, CarryForward). Today the default codec checkpoints the WAL and copies
// the whole file; a WAL-frame delta codec
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
	"os"

	"github.com/hanzoai/vfs/replica"
)

// snapshotCodec produces a durable payload from the bound local database and applies a
// restored payload back onto it. produce reads through db's sole connection so the
// payload is consistent against concurrent writes; apply reconstructs the local file
// (and any auxiliary files) atomically. Both operate on plaintext.
type snapshotCodec interface {
	// produce returns the durable payload for the database at dbPath, read through db
	// (the store's single connection). The returned bytes are the fence's ship payload.
	produce(ctx context.Context, db *sql.DB, dbPath string) ([]byte, error)
	// apply writes a produced payload back onto the local database at dbPath. An empty
	// payload (nothing shipped yet) is a no-op that keeps whatever the local file holds.
	apply(dbPath string, payload []byte) error
}

// wholeFile is the default codec: fold the WAL into the real on-disk file so it holds
// every committed page, and ship those bytes.
//
// The bytes are the WHOLE payload. There is no key material to carry beside them: a
// database's key is DERIVED from the deployment's master and the namespace that owns
// it, so a successor holding the same master reopens the restored file by naming it,
// with nothing shipped and nothing to lose. This used to ship a wrapped-key sidecar
// framed ahead of the database, which is the thing that could go missing.
//
// checkpoint is the crypto-integration client. The SQLCipher page-level and plaintext
// backends encrypt on WRITE, so the default nil path (a raw TRUNCATE checkpoint, fail
// closed on busy) already leaves the real path holding fresh bytes. The pure-Go
// encryption ENVELOPE instead defers encryption to Checkpoint/Close — the plaintext lives
// on tmpfs and the real path is stale ciphertext until re-encrypted — so on that backend
// the composition root injects cek's Checkpoint here (WithCheckpoint), and produce reads
// the real path AFTER it, never shipping stale ciphertext (which would be a lost acked
// write on takeover). A nil checkpoint means the write-time-encrypting default.
type wholeFile struct {
	checkpoint func(ctx context.Context, db *sql.DB) error
}

// produce folds the WAL into the real file, then copies it: the checkpoint runs FIRST so
// the real path holds every committed page (and, on the envelope backend, fresh
// ciphertext).
func (wf wholeFile) produce(ctx context.Context, db *sql.DB, dbPath string) ([]byte, error) {
	if wf.checkpoint != nil {
		// Envelope backend: its Checkpoint folds the WAL AND re-encrypts the real path.
		if err := wf.checkpoint(ctx, db); err != nil {
			return nil, fmt.Errorf("org: snapshot checkpoint %s: %w", dbPath, err)
		}
		return readSnapshot(dbPath)
	}
	// Default backend (write-time encryption): hold db's SOLE connection (MaxOpenConns(1))
	// across the TRUNCATE checkpoint AND the file read so no writer folds new frames
	// mid-read. busy!=0 means the checkpoint could not fold all frames (a reader held the
	// WAL) so the file is MISSING committed rows — fail closed (a partial snapshot shipped
	// as complete is a silent lost write). (busy, logFrames, checkpointed) is the row.
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("org: snapshot conn %s: %w", dbPath, err)
	}
	defer conn.Close()
	var busy, logFrames, checkpointed int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return nil, fmt.Errorf("org: snapshot checkpoint %s: %w", dbPath, err)
	}
	if busy != 0 {
		return nil, fmt.Errorf("org: snapshot checkpoint %s did not complete (busy=%d, log=%d, checkpointed=%d) — snapshot would miss committed WAL frames", dbPath, busy, logFrames, checkpointed)
	}
	return readSnapshot(dbPath)
}

// readSnapshot reads the real-path database bytes — the durable payload, entire.
func readSnapshot(dbPath string) ([]byte, error) {
	b, err := os.ReadFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("org: snapshot read %s: %w", dbPath, err)
	}
	return b, nil
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
