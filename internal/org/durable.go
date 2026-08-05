// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// durable.go packages the single-writer + durability discipline (proven in
// handoff_test.go with the raw primitives) into ONE reusable value a per-org SQLite
// store wires at its open/write/close seam. It composes the three orthogonal lanes,
// each kept in its own package:
//
//	WHO writes   — ha election via CASLeases: HRW names the owner, the lease binds
//	               it to a monotone Round. (github.com/hanzoai/ha)
//	HOW it ships  — replica.FencedStore over the S3 conditional store: a ship at a
//	               round below the recorded one is rejected (ErrStaleRound), and a
//	               takeover carries the latest durable bytes forward before sealing,
//	               so no acknowledged write is lost. (github.com/hanzoai/vfs/replica)
//	AT REST       — the durable object is sealed with the org's envelope key (Cipher);
//	               the local file keeps whatever cek wrote it as.
//
// # Snapshot is a raw file copy, not VACUUM/logical export
//
// The local file is opened through cek, which on an encryption-capable build stores
// it as ciphertext under a per-database key wrapped in a <db>.dek sidecar. A snapshot
// therefore copies the ACTUAL local bytes (checkpoint the WAL, read the file) and
// ships them together with the sidecar, framed in one payload. A successor writes
// both back and opens through cek exactly as the origin did — no logical export, no
// SQLCipher-specific SQL, so the same code path is correct whether the file is
// encrypted (production) or plaintext (pure-Go dev/tests), and an existing on-disk
// store needs no migration. When cek is not encrypting there is simply no sidecar.
//
// # The wire-the-gate contract a store follows
//
//	open  : Hydrate() BEFORE opening the local handle — acquire the lease and
//	        CarryForward-restore the latest durable snapshot into the local file
//	        (or, for a non-owner, refresh read-only). Then open the handle and Bind()
//	        it so Sync can checkpoint on the same single connection.
//	write : after the write transaction COMMITS, Sync() — snapshot the local file and
//	        ship it fenced at the lease round. A ship rejected as ErrStaleRound means
//	        this replica was deposed: the write is NOT acknowledged and the caller
//	        retries on the new owner. Call Sync outside any open transaction.
//	close : best-effort final Sync, then release (the live *sql.DB is the store's).

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/hanzoai/ha"
	"github.com/hanzoai/vfs/replica"
)

// dekSuffix is cek's key-sidecar suffix (github.com/hanzoai/cloud/cek): the file
// holding a database's wrapped page key. A durable snapshot ships it beside the
// database bytes so a successor can open the encrypted file. Kept as a local
// constant (a stable on-disk convention) so this package stays dep-free of cek.
const dekSuffix = ".dek"

// errCorruptFrame is returned when a durable payload does not decode as a
// (sidecar, database) frame this package wrote — fail closed rather than restore
// arbitrary bytes over a live database.
var errCorruptFrame = errors.New("org: durable payload is not a valid (sidecar,db) frame")

// Durability is the per-deployment, org-agnostic durable-store factory: the shared
// election+fence over ONE object store, plus the optional at-rest envelope. It holds
// no per-org state — For() mints a Durable per org DB. nil ⇒ durability disabled
// (local-only single-node/dev), the caller's default open path unchanged.
type Durability struct {
	leases *CASLeases
	fenced *replica.FencedStore
	cipher *Cipher
}

// NewDurability builds the factory over an atomic-CAS object store (the SeaweedFS S3
// If-Match ConditionalStore), the live membership view (election input), and an
// optional per-org envelope Cipher (nil ⇒ the durable object is stored in the clear;
// pure-Go dev only). The SAME cond backs both the lease and the data ships, so they
// share one linearizable register.
func NewDurability(cond replica.ConditionalStore, view ownerView, cipher *Cipher) *Durability {
	return &Durability{
		leases: NewCASLeases(cond, view),
		fenced: replica.NewFencedStore(cond),
		cipher: cipher,
	}
}

// For mints the Durable binding for one org DB: orgID is the org SLUG (the HRW
// election key AND the cipher AAD — the caller passes the SAME slug the on-disk path
// and the shard router hash use). dbKey is the durable object location
// (replica.DBPath). dbPath is the local SQLite file.
func (dy *Durability) For(orgID, dbKey, dbPath string) *Durable {
	return &Durable{dy: dy, orgID: orgID, dbKey: dbKey, dbPath: dbPath}
}

// Durable binds one org's local SQLite file to its fenced durable object slot,
// gated by the org's single-writer lease. It never opens or closes the local handle
// (the store owns that) — Bind lends it the handle so Sync can checkpoint on the
// same single connection the store writes through.
type Durable struct {
	dy     *Durability
	orgID  string // org slug — HRW key + cipher AAD
	dbKey  string // durable object key (replica.DBPath)
	dbPath string // local SQLite file path

	mu    sync.Mutex
	owned bool     // true iff we hold the lease as the elected writer
	lease ha.Lease // the monotone round every ship is fenced at
	db    *sql.DB  // live handle (set by Bind) — checkpoint runs on its sole conn
}

// Hydrate acquires the writer lease and restores the latest durable snapshot into
// the local file, BEFORE the store opens its handle. As the elected owner it
// CarryForward-seals the durable object to the lease round while hydrating (safe
// takeover: no acknowledged write lost). A non-owner refreshes read-only. Any other
// condition (store unreachable, empty membership, lost race) degrades to read-only
// on the local file and is returned for the caller to log — it NEVER makes the store
// unopenable, so a store is always available for reads; writes fail closed until a
// later open re-acquires.
//
// RECOVERY from a degraded open (Red M3): a pod that could not acquire at open stays
// read-only for that store's cached lifetime — it does NOT re-acquire in place, because
// a takeover CarryForward restores the durable snapshot OVER the local file, which is
// unsafe under the live handle the store already handed out (stale reads). Recovery is
// therefore a FRESH open: a pod restart (a readiness/liveness probe can gate on the
// degraded log) or the shard router routing the org to a healthy owner. An in-process
// quiesce-close-reopen on the cached entry is the future enhancement; until then the
// safe, simple recovery is re-open.
func (d *Durable) Hydrate(ctx context.Context) error {
	lease, err := d.dy.leases.Acquire(ctx, d.orgID)
	if err != nil {
		// Not the elected writer, or no safe owner / unreachable store: serve reads
		// off the freshest local copy we can get, writes fail closed via Sync.
		d.restoreLatestReadOnly(ctx)
		if errors.Is(err, ErrNotOwner) || errors.Is(err, ErrNoMembership) {
			return nil
		}
		return fmt.Errorf("org: durable acquire %s: %w", d.orgID, err)
	}
	if err := d.dy.fenced.CarryForward(ctx, d.dbKey, uint64(lease.Round), d.restore); err != nil {
		if errors.Is(err, replica.ErrStaleRound) {
			return nil // a newer owner already advanced past us — serve read-only
		}
		return fmt.Errorf("org: durable hydrate %s: %w", d.dbKey, err)
	}
	d.mu.Lock()
	d.lease, d.owned = lease, true
	d.mu.Unlock()
	return nil
}

// Bind lends Durable the store's live handle so Sync checkpoints on the SAME single
// connection the store writes through (serializing snapshot against writes without a
// second handle). Call after the store opens the local file.
func (d *Durable) Bind(db *sql.DB) {
	d.mu.Lock()
	d.db = db
	d.mu.Unlock()
}

// Owned reports whether this replica currently holds the writer lease. A store may
// gate a write on it, but the authoritative gate is Sync's fenced ship.
func (d *Durable) Owned() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.owned
}

// Sync snapshots the local file and ships it to the durable object fenced at the
// lease round — the ship-before-ack step. acked is true only if the fenced store
// admitted our round. A ship rejected as ErrStaleRound returns (false, nil): this
// replica was deposed, so it drops ownership and does NOT acknowledge — the caller
// retries on the new owner. A non-owner returns (false, ErrNotOwner). Call AFTER the
// write transaction commits and OUTSIDE any open transaction (Sync takes the sole
// connection to checkpoint).
func (d *Durable) Sync(ctx context.Context) (acked bool, err error) {
	d.mu.Lock()
	owned, lease := d.owned, d.lease
	d.mu.Unlock()
	if !owned {
		return false, ErrNotOwner
	}
	snap, err := d.snapshot(ctx)
	if err != nil {
		return false, err
	}
	payload := snap
	if d.dy.cipher != nil {
		sealed, err := d.dy.cipher.Seal(d.orgID, snap)
		if err != nil {
			return false, fmt.Errorf("org: durable seal %s: %w", d.dbKey, err)
		}
		payload = sealed
	}
	if err := d.dy.fenced.Put(ctx, d.dbKey, payload, uint64(lease.Round)); err != nil {
		if errors.Is(err, replica.ErrStaleRound) {
			d.mu.Lock()
			d.owned = false
			d.mu.Unlock()
			return false, nil // deposed: not acknowledged
		}
		return false, fmt.Errorf("org: durable ship %s: %w", d.dbKey, err)
	}
	return true, nil
}

// Close best-effort ships any final state under the caller's (time-bounded) ctx —
// ship-before-ack already covers every acknowledged write, so this is
// belt-and-suspenders — and releases. It does NOT close the live *sql.DB; that is the
// store handle the caller owns.
func (d *Durable) Close(ctx context.Context) error {
	d.mu.Lock()
	owned, bound := d.owned, d.db != nil
	d.mu.Unlock()
	if owned && bound {
		_, _ = d.Sync(ctx)
	}
	return nil
}

// snapshot copies the local file consistently: it takes the store's SOLE connection
// (MaxOpenConns(1)), TRUNCATE-checkpoints the WAL so the main file holds every
// committed page, then reads the file bytes while still holding the connection so no
// writer can fold new frames in mid-read. The <db>.dek key sidecar (present only on
// an encrypting build) is read too and framed with the database bytes.
func (d *Durable) snapshot(ctx context.Context) ([]byte, error) {
	d.mu.Lock()
	db := d.db
	d.mu.Unlock()
	if db == nil {
		return nil, fmt.Errorf("org: durable %s not bound to a db", d.dbKey)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("org: durable snapshot conn %s: %w", d.dbKey, err)
	}
	defer conn.Close()
	// TRUNCATE-checkpoint the WAL so the main file holds every committed page, and
	// CHECK the result: busy!=0 means the checkpoint could not fold all frames (a
	// reader held the WAL), so the file on disk is MISSING committed rows. Fail closed
	// — a partial snapshot shipped as complete is a silent lost write. (busy, logFrames,
	// checkpointed) is the PRAGMA's row.
	var busy, logFrames, checkpointed int
	if err := conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return nil, fmt.Errorf("org: durable checkpoint %s: %w", d.dbKey, err)
	}
	if busy != 0 {
		return nil, fmt.Errorf("org: durable checkpoint %s did not complete (busy=%d, log=%d, checkpointed=%d) — snapshot would miss committed WAL frames", d.dbKey, busy, logFrames, checkpointed)
	}
	main, err := os.ReadFile(d.dbPath)
	if err != nil {
		return nil, fmt.Errorf("org: durable read %s: %w", d.dbPath, err)
	}
	sidecar, err := os.ReadFile(d.dbPath + dekSuffix)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("org: durable read sidecar %s: %w", d.dbPath, err)
	}
	return frame(sidecar, main), nil
}

// restore is the hydrate callback: it opens the sealed durable payload, splits the
// (sidecar, database) frame, writes the sidecar (so cek can open the encrypted file)
// and atomically swaps the database bytes into place via replica.RestoreFile. An
// empty payload (nothing shipped yet) keeps whatever the local file already holds.
func (d *Durable) restore(sealed []byte) error {
	if len(sealed) == 0 {
		return nil
	}
	payload := sealed
	if d.dy.cipher != nil {
		pt, err := d.dy.cipher.Open(d.orgID, sealed)
		if err != nil {
			return fmt.Errorf("org: durable open %s: %w", d.dbKey, err)
		}
		payload = pt
	}
	sidecar, main, err := unframe(payload)
	if err != nil {
		return err
	}
	// RestoreFile FIRST: it MkdirAll's the parent and atomically swaps the database
	// bytes into place. The sidecar is written AFTER, into the now-existing directory,
	// so both the encrypted file and its key are present before cek opens them. (Writing
	// the sidecar first would fail on a fresh successor whose orgs/<slug>/ dir does not
	// exist yet.)
	if err := replica.RestoreFile(d.dbPath, main); err != nil {
		return err
	}
	if len(sidecar) > 0 {
		if err := os.WriteFile(d.dbPath+dekSuffix, sidecar, 0o600); err != nil {
			return fmt.Errorf("org: durable write sidecar %s: %w", d.dbPath, err)
		}
	}
	return nil
}

// restoreLatestReadOnly refreshes the local file from the durable object without a
// lease — the non-owner / degraded path. Best-effort: an absent or unreachable
// object leaves the local file as-is (serve whatever is on disk), never an error.
func (d *Durable) restoreLatestReadOnly(ctx context.Context) {
	payload, _, err := d.dy.fenced.Get(ctx, d.dbKey)
	if err != nil {
		return
	}
	_ = d.restore(payload)
}

// frame prepends the sidecar under a uvarint length so a successor can split it from
// the database bytes. len(sidecar)==0 ⇒ a leading 0 byte, i.e. a plaintext store.
func frame(sidecar, main []byte) []byte {
	buf := make([]byte, 0, binary.MaxVarintLen64+len(sidecar)+len(main))
	var n [binary.MaxVarintLen64]byte
	m := binary.PutUvarint(n[:], uint64(len(sidecar)))
	buf = append(buf, n[:m]...)
	buf = append(buf, sidecar...)
	return append(buf, main...)
}

// unframe splits a framed payload back into (sidecar, database). The bound is checked
// as sl > len(b)-m (a SUBTRACTION, never m+sl) so a maliciously large uvarint length
// cannot wrap uint64 addition past the guard and panic the slice — it fails closed.
func unframe(b []byte) (sidecar, main []byte, err error) {
	sl, m := binary.Uvarint(b)
	if m <= 0 || sl > uint64(len(b)-m) {
		return nil, nil, errCorruptFrame
	}
	off := m + int(sl)
	return b[m:off], b[off:], nil
}
