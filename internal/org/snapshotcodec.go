// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// snapshotcodec.go is the SWAPPABLE ship mechanism — the seam that lets HOW the durable
// payload is produced/applied change without touching WHEN it ships (the fence, the
// monotone round, CarryForward). Today the default codec checkpoints the WAL and copies
// the whole file (framed with the cek key sidecar); a WAL-frame delta codec
// (github.com/hanzoai/replicate — low-memory streaming) drops in behind this same
// interface later, and the fence ships whatever bytes produce() returns and hands
// whatever bytes it admitted to apply(). The codec works on PLAINTEXT payloads; envelope
// sealing (Cipher) is orthogonal, applied by Sync/restore AROUND the codec — so the two
// concerns (what bytes represent the DB vs. how they are encrypted at rest) never
// complect.

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"github.com/hanzoai/vfs/replica"
)

// dekSuffix is cek's key-sidecar suffix (github.com/hanzoai/cloud/cek): the file holding
// a database's wrapped page key. The wholeFile codec ships it beside the database bytes
// so a successor can open the encrypted file. Kept as a local constant (a stable on-disk
// convention) so this package stays dep-free of cek.
const dekSuffix = ".dek"

// errCorruptFrame is returned when a payload does not decode as a (sidecar, database)
// frame this package wrote — fail closed rather than restore arbitrary bytes over a live
// database.
var errCorruptFrame = errors.New("org: durable payload is not a valid (sidecar,db) frame")

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
// every committed page, copy the file bytes, and frame them with the cek key sidecar so a
// successor can open the encrypted file. It is correct whether the file is encrypted
// (production, sidecar present) or plaintext (pure-Go dev, no sidecar) — the same path, no
// per-build branch.
//
// checkpoint is the crypto-integration seam. The SQLCipher page-level and plaintext
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
// ciphertext); the <db>.dek key sidecar (present only on an encrypting build) is read too
// and framed with the database bytes.
func (wf wholeFile) produce(ctx context.Context, db *sql.DB, dbPath string) ([]byte, error) {
	if wf.checkpoint != nil {
		// Envelope backend: its Checkpoint folds the WAL AND re-encrypts the real path.
		// It takes db's SOLE connection itself, so it must run BEFORE we hold that
		// connection (holding it first would deadlock at MaxOpenConns(1)).
		if err := wf.checkpoint(ctx, db); err != nil {
			return nil, fmt.Errorf("org: snapshot checkpoint %s: %w", dbPath, err)
		}
		// Then hold the SOLE connection across the file read, exactly as the default path
		// does: os.ReadFile is a raw read that takes no SQLite lock, so a concurrent
		// writer's commit — and the WAL auto-checkpoint it can trigger, which writes pages
		// straight into the real path — would tear the image mid-read and ship a CORRUPT
		// snapshot that the fence then acks at the lease round. A write that lands in the
		// gap between the checkpoint and this acquire is simply not in the snapshot, which
		// is safe: it has not been acked, so no acknowledged write is lost.
		conn, err := db.Conn(ctx)
		if err != nil {
			return nil, fmt.Errorf("org: snapshot conn %s: %w", dbPath, err)
		}
		defer conn.Close()
		return readFramed(dbPath)
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
	return readFramed(dbPath)
}

// readFramed reads the real-path database bytes and the cek key sidecar (absent on a
// plaintext build) and frames them into one durable payload.
func readFramed(dbPath string) ([]byte, error) {
	main, err := os.ReadFile(dbPath)
	if err != nil {
		return nil, fmt.Errorf("org: snapshot read %s: %w", dbPath, err)
	}
	sidecar, err := os.ReadFile(dbPath + dekSuffix)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("org: snapshot read sidecar %s: %w", dbPath, err)
	}
	return frame(sidecar, main), nil
}

// apply splits the (sidecar, database) frame, atomically swaps the database bytes into
// place (replica.RestoreFile, which MkdirAll's the parent), then writes the sidecar into
// the now-existing directory. RestoreFile FIRST so a fresh successor whose orgs/<slug>/
// dir does not exist yet gets the directory before the sidecar write. An empty payload is
// a no-op.
func (wholeFile) apply(dbPath string, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	sidecar, main, err := unframe(payload)
	if err != nil {
		return err
	}
	if err := replica.RestoreFile(dbPath, main); err != nil {
		return err
	}
	if len(sidecar) > 0 {
		if err := os.WriteFile(dbPath+dekSuffix, sidecar, 0o600); err != nil {
			return fmt.Errorf("org: write sidecar %s: %w", dbPath, err)
		}
	}
	return nil
}

// frame prepends the sidecar under a uvarint length so a successor can split it from the
// database bytes. len(sidecar)==0 ⇒ a leading 0 byte, i.e. a plaintext store.
func frame(sidecar, main []byte) []byte {
	buf := make([]byte, 0, binary.MaxVarintLen64+len(sidecar)+len(main))
	var n [binary.MaxVarintLen64]byte
	m := binary.PutUvarint(n[:], uint64(len(sidecar)))
	buf = append(buf, n[:m]...)
	buf = append(buf, sidecar...)
	return append(buf, main...)
}

// unframe splits a framed payload back into (sidecar, database). The bound is checked as
// sl > len(b)-m (a SUBTRACTION, never m+sl) so a maliciously large uvarint length cannot
// wrap uint64 addition past the guard and panic the slice — it fails closed.
func unframe(b []byte) (sidecar, main []byte, err error) {
	sl, m := binary.Uvarint(b)
	if m <= 0 || sl > uint64(len(b)-m) {
		return nil, nil, errCorruptFrame
	}
	off := m + int(sl)
	return b[m:off], b[off:], nil
}
