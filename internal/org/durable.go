// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package org

// durable.go packages the single-writer + durability discipline (proven in
// handoff_test.go with the raw primitives) into ONE reusable value a per-org SQLite
// store wires at its open/write/close client. It composes the three orthogonal lanes,
// each kept in its own package:
//
//	WHO writes   — ha election via CASFencer: HRW names the owner, the lease binds
//	               it to a monotone Round. (github.com/hanzoai/ha)
//	HOW it ships  — replica.FencedStore over the S3 conditional store: a ship at a
//	               round below the recorded one is rejected (ErrStaleRound), and a
//	               takeover carries the latest durable bytes forward before sealing,
//	               so no acknowledged write is lost. (github.com/hanzoai/vfs/replica)
//	AT REST       — the durable object is sealed under the database's own cek key
//	               (Cipher), the SAME key the local file was opened with.
//
// # Snapshot is a raw file copy, not VACUUM/logical export
//
// The local file is opened through cek, which stores it as ciphertext under a key
// DERIVED from this deployment's master and the namespace that owns it. A snapshot
// therefore copies the ACTUAL local bytes (checkpoint the WAL, read the file) and
// ships those, entire. A successor writes them back and opens through cek exactly as
// the origin did — the derivation gives it the same key from the same name, so there
// is no key material in the payload to lose. No logical export, no SQLCipher-specific
// SQL, and an existing on-disk store needs no migration.
//
// # The wire-the-gate contract a store follows
//
//	open  : Hydrate() BEFORE opening the local handle — acquire the lease and
//	        CarryForward-restore the latest durable snapshot into the local file
//	        (or, for a non-owner, refresh read-only). Then open the handle and Bind()
//	        it so Sync can fold the WAL on the same single connection.
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
	"github.com/hanzoai/namespace"
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
	reader opener        // opens a snapshot's own read-only handle; nil ⇒ none available
}

// opener opens a second, READ-ONLY handle on the database a Durable names. A snapshot
// holds a read transaction on it while it copies the file, which is what lets the store
// keep its own connection (see pin). It answers (nil, nil) on a backend that has no
// second handle to give — the on-disk file is then not a database another handle could
// open, and the snapshot copies it unpinned, as it always did.
//
// It is a FUNCTION and not a handle because the snapshot opens one per ship and closes
// it again: nothing else may hold it, nothing has to close it on a promotion or an
// eviction, and a store that never ships never opens one.
type opener func(ns namespace.Namespace, subsystem, path string) (*sql.DB, error)

// NewDurability builds the factory over an atomic-CAS object store (the S3
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
// without touching the fence or round. WithSeal injects the envelope's re-encrypting
// Checkpoint (crypto client); WithReader injects the opener a snapshot pins its file with.
func NewDurability(cond replica.ConditionalStore, view ownerView, cipher *Cipher, opts ...DurabilityOption) *Durability {
	var o durabilityOpts
	for _, fn := range opts {
		fn(&o)
	}
	return &Durability{
		fencer: NewCASFencer(cond, view),
		fenced: replica.NewFencedStore(cond),
		cipher: cipher,
		codec:  wholeFile{seal: o.seal},
		reader: o.reader,
	}
}

// DurabilityOption configures a Durability.
type DurabilityOption func(*durabilityOpts)

type durabilityOpts struct {
	seal   func(*sql.DB) error
	reader opener
}

// WithSeal injects the operation that makes the on-disk file hold every committed write
// on a backend that defers encryption. The pure-Go ENVELOPE keeps its plaintext on tmpfs
// and re-encrypts to the real path only when asked, so without this a ship would read the
// last-sealed ciphertext — a lost acked write on takeover. On the SQLCipher page-level and
// plaintext backends, which encrypt as they write, it is a successful no-op. Folding the
// WAL is NOT its job and never was a backend's choice: the codec does that itself, once,
// on every backend, and the fold is also where the file is pinned for the copy.
func WithSeal(fn func(*sql.DB) error) DurabilityOption {
	return func(o *durabilityOpts) { o.seal = fn }
}

// WithReader injects the opener a snapshot pins its file with — a second, read-only handle
// on the same database. Without it every ship copies the file while writers are free to
// move it; with it the copy reads what the fold left. The composition root supplies it
// where a second handle is safe (see opener).
func WithReader(open opener) DurabilityOption {
	return func(o *durabilityOpts) { o.reader = open }
}

// For mints the Durable binding for one org DB. It takes the NAME — the same
// (ns, subsystem) the local file was opened under — rather than the parts a name
// is made of, so the snapshot cannot be keyed for one database and shipped as
// another: the election key is ns.ID() (the ENTITY, so every one of an org's
// project-scoped files has one elected writer) and the snapshot key is cek's over
// (ns, subsystem). dbKey is the durable object location (replica.DBPath). dbPath
// is the local SQLite file.
func (dy *Durability) For(ns namespace.Namespace, subsystem, dbKey, dbPath string) *Durable {
	return &Durable{dy: dy, ns: ns, subsystem: subsystem, dbKey: dbKey, dbPath: dbPath}
}

// Durable binds one org's local SQLite file to its fenced durable object slot,
// gated by the org's single-writer lease. It never opens or closes the local handle
// (the store owns that) — Bind lends it the handle so Sync can checkpoint on the
// same single connection the store writes through.
type Durable struct {
	dy        *Durability
	ns        namespace.Namespace // the database's name — HRW key (ns.ID()) + snapshot key
	subsystem string              // which database of that entity — bound into the snapshot key
	dbKey     string              // durable object key (replica.DBPath)
	dbPath    string              // local SQLite file path

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
	lease, err := d.dy.fencer.Acquire(ctx, d.ns.ID())
	if err != nil {
		// Not the elected writer, or no safe owner / unreachable store: serve reads
		// off the freshest local copy we can get, writes fail closed via Sync.
		d.restoreLatestReadOnly(ctx)
		if errors.Is(err, ErrNotOwner) || errors.Is(err, ErrNoMembership) {
			return nil
		}
		return fmt.Errorf("org: durable acquire %s: %w", d.ns, err)
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

// Bind lends Durable the store's live handle so Sync folds the WAL on the SAME single
// connection the store writes through. Call after the store opens the local file.
func (d *Durable) Bind(db *sql.DB) {
	d.mu.Lock()
	d.db = db
	d.mu.Unlock()
}

// Owned reports whether this replica holds the writer lease AND is still the org's
// elected owner. A store may gate a write on it, but the authoritative gate is
// Sync's fenced ship.
//
// BOTH HALVES, because the lease alone is a memory. `owned` is written once, at
// Hydrate, and cleared in exactly one place: a ship refused as a stale round. So a
// replica the fleet has re-elected away goes on answering true until it happens to
// attempt a ship — and the acts that most need this gate are the ones that never
// ship. Ending a lease stops a KUBERNETES POD before any ship is attempted, and
// ending a row that is not `holding` charges nothing and so ships nothing at all;
// neither would ever have learned the belief was stale. A deposed pod went on
// deleting the new owner's pods with no mechanism anywhere to stop it.
//
// The election half is the same cheap question PendingPromotion already asks in the
// other direction — one lock and an HRW over the live member snapshot, no I/O — so
// this closes within one membership refresh rather than never.
//
// It is deliberately not a claim about the OBJECT STORE. A store that cannot be
// reached does not move ownership: this replica still holds the lease and is still
// elected, so it must go on ending expired leases through an outage. What it must
// not do is call the resulting debit durable, and that is Sync's answer, not this
// one.
func (d *Durable) Owned() bool {
	d.mu.Lock()
	owned := d.owned
	d.mu.Unlock()
	return owned && d.dy.fencer.ElectsSelf(d.ns.ID())
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
	return d.dy.fencer.ElectsSelf(d.ns.ID())
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
	_, err := d.dy.fencer.Acquire(ctx, d.ns.ID())
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
// connection for the two statements that fold the WAL and pin the file).
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
		sealed, err := d.dy.cipher.Seal(d.ns, d.subsystem, snap)
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
// codec (default wholeFile: fold the WAL, pin the file, copy it).
//
// It opens the snapshot's own read-only handle for the length of this call and closes it
// again. One handle per ship costs an open on a file that is about to be read whole and
// sent to an object store, and it buys a lifetime nothing else has to manage: no handle
// survives a promotion, an eviction or a shutdown, because none outlives the ship that
// made it.
func (d *Durable) snapshot(ctx context.Context) (payload []byte, err error) {
	d.mu.Lock()
	db := d.db
	d.mu.Unlock()
	if db == nil {
		return nil, fmt.Errorf("org: durable %s not bound to a db", d.dbKey)
	}
	var reader *sql.DB
	if d.dy.reader != nil {
		// A reader that cannot be opened fails the ship rather than falling back to an
		// unpinned copy: the write is not acknowledged and the caller retries, where a
		// fallback would ship a file writers were free to move under it.
		if reader, err = d.dy.reader(d.ns, d.subsystem, d.dbPath); err != nil {
			return nil, fmt.Errorf("org: durable reader %s: %w", d.dbKey, err)
		}
		if reader != nil {
			defer reader.Close()
		}
	}
	return d.dy.codec.produce(ctx, db, reader, d.dbPath)
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
		pt, err := d.dy.cipher.Open(d.ns, d.subsystem, sealed)
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
