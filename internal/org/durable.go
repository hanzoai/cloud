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
//	WHO writes   — ha election via CASFencer: HRW names the owner, the lease binds
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
	"errors"
	"fmt"
	"sync"

	"github.com/hanzoai/ha"
	"github.com/hanzoai/vfs/replica"
)

// Durability is the per-deployment, org-agnostic durable-store factory: the shared
// election+fence over ONE object store, plus the optional at-rest envelope. It holds
// no per-org state — For() mints a Durable per org DB. nil ⇒ durability disabled
// (local-only single-node/dev), the caller's default open path unchanged.
type Durability struct {
	fencer *CASFencer
	fenced *replica.FencedStore
	cipher *Cipher
	codec  snapshotCodec // how the durable payload is produced/applied (swappable ship)
}

// NewDurability builds the factory over an atomic-CAS object store (the SeaweedFS S3
// If-Match ConditionalStore), the live membership view (election input), and an
// optional per-org envelope Cipher (nil ⇒ the durable object is stored in the clear;
// pure-Go dev only). The SAME cond backs both the lease and the data ships, so they
// share one linearizable register.
//
// The cond is an INTERFACE by design (replica.ConditionalStore): a read-through/
// write-through cache tier (KV in front of S3) wraps it with a one-line decorator at the
// buildDurability construction site, with no change here — the fence reads and CASes
// through whatever store it is handed. The ship mechanism is likewise swappable: the
// default wholeFile codec can be replaced by a WAL-frame delta codec behind snapshotCodec
// without touching the fence or round. WithCheckpoint injects the envelope's re-encrypting
// Checkpoint (crypto-integration seam).
func NewDurability(cond replica.ConditionalStore, view ownerView, cipher *Cipher, opts ...DurabilityOption) *Durability {
	var o durabilityOpts
	for _, fn := range opts {
		fn(&o)
	}
	return &Durability{
		fencer: NewCASFencer(cond, view),
		fenced: replica.NewFencedStore(cond),
		cipher: cipher,
		codec:  wholeFile{checkpoint: o.checkpoint},
	}
}

// DurabilityOption configures a Durability.
type DurabilityOption func(*durabilityOpts)

type durabilityOpts struct {
	checkpoint func(context.Context, *sql.DB) error
}

// WithCheckpoint injects the operation that folds the WAL into the real on-disk file and,
// on the pure-Go encryption ENVELOPE, re-encrypts that real path — so a fenced ship reads
// FRESH bytes, never stale ciphertext (which would be a lost acked write on takeover). The
// composition root wires cek's Checkpoint here on the envelope backend; the default (no
// option) is a raw TRUNCATE checkpoint, correct for the SQLCipher page-level and plaintext
// backends that encrypt on write. This composes ship-before-ack with encrypt-on-checkpoint.
func WithCheckpoint(fn func(context.Context, *sql.DB) error) DurabilityOption {
	return func(o *durabilityOpts) { o.checkpoint = fn }
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
// RECOVERY from a degraded open (M3): a pod that could not acquire at open stays
// read-only until this replica becomes the org's elected owner (a membership change),
// at which point the OrgStore promotes the store IN PLACE — PendingPromotion gates it,
// TryClaim proves the lease is claimable, then a quiesce-close-reopen swaps in a writer
// handle and CarryForward-restores the latest snapshot under the FRESH handle (never
// under the live one — the swap is why the reopen is required). No process restart.
func (d *Durable) Hydrate(ctx context.Context) error {
	lease, err := d.dy.fencer.Acquire(ctx, d.orgID)
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

// PendingPromotion reports whether this store opened degraded (does not hold the lease)
// yet this replica is NOW the org's elected owner — a membership change made it the
// writer, so the store must be promoted (re-acquire + hydrate + reopen) to serve writes.
// Cheap and I/O-free: a lock plus one HRW over the live member snapshot, evaluated only
// when the store is not already owned. It is the per-request gate the OrgStore checks on
// a cache hit; the actual promotion runs (rarely) only when this returns true.
func (d *Durable) PendingPromotion() bool {
	d.mu.Lock()
	owned := d.owned
	d.mu.Unlock()
	if owned {
		return false
	}
	return d.dy.fencer.ElectsSelf(d.orgID)
}

// TryClaim probes whether this replica can hold the org's writer lease right now and, if
// so, claims it — the promotion gate. It performs ONLY the lease CAS (via the fencer), no
// local-file I/O, so a failed probe (not the elected owner, or the store unreachable)
// costs nothing and leaves any live handle untouched. On success the lease object names
// this replica at a fresh round (fencing any prior owner); the caller then quiesces and
// reopens, whose Hydrate renews THIS same lease and CarryForward-restores the latest
// snapshot under the fresh handle. Returns (false, nil) when not the elected owner,
// (false, err) on a store error, (true, nil) when claimed.
func (d *Durable) TryClaim(ctx context.Context) (bool, error) {
	_, err := d.dy.fencer.Acquire(ctx, d.orgID)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotOwner), errors.Is(err, ErrNoMembership):
		return false, nil
	default:
		return false, err
	}
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

// snapshot produces the durable payload for the bound local database via the swappable
// codec (default wholeFile: checkpoint + framed file copy). It takes the store's SOLE
// connection through the codec so the payload is consistent against concurrent writes.
func (d *Durable) snapshot(ctx context.Context) ([]byte, error) {
	d.mu.Lock()
	db := d.db
	d.mu.Unlock()
	if db == nil {
		return nil, fmt.Errorf("org: durable %s not bound to a db", d.dbKey)
	}
	return d.dy.codec.produce(ctx, db, d.dbPath)
}

// restore is the hydrate callback: it opens the sealed durable payload (envelope
// decryption is orthogonal, done HERE around the codec) and hands the plaintext to the
// codec to apply onto the local file. An empty payload (nothing shipped yet) keeps
// whatever the local file already holds.
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
	return d.dy.codec.apply(d.dbPath, payload)
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
