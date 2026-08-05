// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package idem is exactly-once request execution over a per-org SQLite database:
// a request named by a caller-supplied idempotency key runs AT MOST ONCE, and any
// retry — including one re-routed to a different replica after a rolling upgrade —
// returns the first run's recorded result instead of executing the effect again.
//
// It is the request-layer generalization of visor's insert-once MeterLease
// (object/meter_lease.go): the key is the PRIMARY KEY of a row written IN THE SAME
// TRANSACTION as the effect, so the dedup record and the effect commit atomically —
// either both land or neither. There is no window in which the effect is applied
// but the key is unrecorded (which would allow a re-run), nor one in which the key
// is recorded but the effect is missing (which would wrongly suppress a run).
//
// # Exactly-once across a writer handoff
//
// The row lives in the per-org SQLite the HA substrate snapshots to the object
// store, so the dedup record SHIPS WITH THE DATA. A successor that hydrates the
// latest fenced snapshot before it serves (hydrate-before-write) therefore sees
// every acknowledged request's key and refuses to re-run it. Exactly-once across a
// handoff is two composed properties, neither sufficient alone:
//
//	fenced ship-before-ack : a request is 'done' only once its effect is durably
//	                         in the fenced object store; a FENCED ship means the
//	                         request FAILED and the caller retries (never acks).
//	dedup key in the WAL    : the successor hydrates the shipped snapshot, so a
//	                         retry of an acknowledged request finds its key present.
//
// This package owns only the first-writer-wins dedup + result recall. The fenced
// ship (github.com/hanzoai/vfs/replica.FencedStore) and the single-writer gate +
// hydrate (internal/org) are its composition partners, each in its own lane.
package idem

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrAlreadyApplied reports that key was executed by a prior call. It is the
// 'fail if already done' signal: Once returns it (with the stored result) instead
// of running the effect a second time. Callers that want idempotent success return
// the result and swallow this; callers that want a hard 'duplicate' error surface
// it. errors.Is(err, ErrAlreadyApplied) distinguishes it from a real failure.
var ErrAlreadyApplied = errors.New("idem: request already applied")

// maxBusyRetries bounds retries when a concurrent writer holds the SQLite write
// lock longer than busy_timeout. The per-org DB opens with busy_timeout(10000), so
// this is a rarely-reached backstop, not the primary concurrency mechanism.
const maxBusyRetries = 5

// Once runs apply EXACTLY ONCE for key against db and records the outcome.
//
// On the FIRST call for key: it opens a transaction, inserts the key row, runs
// apply WITHIN that same transaction, stores apply's result bytes, and commits —
// so the dedup row and every effect apply performed (which apply MUST perform
// through the provided *sql.Tx, or atomicity is lost) land together. It returns
// (result, true, nil).
//
// On any LATER call for the same key: apply is NOT run; Once returns the stored
// result with applied=false and ErrAlreadyApplied wrapped in err. round is
// recorded alongside the row (the fencing round under which the request was first
// applied) for audit; it does not affect dedup.
func Once(ctx context.Context, db *sql.DB, key string, round uint64, apply func(context.Context, *sql.Tx) ([]byte, error)) (result []byte, applied bool, err error) {
	if db == nil {
		return nil, false, fmt.Errorf("idem: nil db")
	}
	if key == "" {
		return nil, false, fmt.Errorf("idem: empty idempotency key")
	}
	if err := EnsureSchema(ctx, db); err != nil {
		return nil, false, err
	}
	for attempt := 0; attempt < maxBusyRetries; attempt++ {
		result, applied, err = once(ctx, db, key, round, apply)
		if isBusy(err) {
			continue // busy_timeout was exceeded under contention; retry the whole tx.
		}
		return result, applied, err
	}
	return nil, false, fmt.Errorf("idem: %q contended beyond %d retries", key, maxBusyRetries)
}

func once(ctx context.Context, db *sql.DB, key string, round uint64, apply func(context.Context, *sql.Tx) ([]byte, error)) ([]byte, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	// The INSERT is the FIRST statement, so the transaction takes the write lock
	// here; a concurrent Once for the same key either blocks (busy_timeout) then
	// fails the unique key, or fails it outright — both routes converge on 'load
	// the winner's result'.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO idempotency(key, round, result, created_at) VALUES(?, ?, NULL, ?)`,
		key, round, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		_ = tx.Rollback()
		if isBusy(err) {
			return nil, false, err // surfaced to the retry loop.
		}
		// Any non-busy insert failure most likely means the key already exists
		// (the winner committed). Confirm by loading it; a present row is a
		// definitive duplicate, so we never misreport a genuine error as one.
		if prior, ok, lerr := load(ctx, db, key); lerr != nil {
			return nil, false, lerr
		} else if ok {
			return prior, false, ErrAlreadyApplied
		}
		return nil, false, fmt.Errorf("idem: insert %q: %w", key, err)
	}

	res, err := apply(ctx, tx)
	if err != nil {
		_ = tx.Rollback() // undoes the key row AND every effect apply wrote — retryable.
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE idempotency SET result = ? WHERE key = ?`, res, key); err != nil {
		_ = tx.Rollback()
		return nil, false, fmt.Errorf("idem: record result %q: %w", key, err)
	}
	if err := tx.Commit(); err != nil {
		if isBusy(err) {
			return nil, false, err
		}
		// A commit that lost to a peer leaves the peer's row committed; treat a
		// now-present row as the duplicate it is.
		if prior, ok, lerr := load(ctx, db, key); lerr == nil && ok {
			return prior, false, ErrAlreadyApplied
		}
		return nil, false, fmt.Errorf("idem: commit %q: %w", key, err)
	}
	return res, true, nil
}

// load reads a recorded result. ok=false means the key has never been applied.
func load(ctx context.Context, db *sql.DB, key string) (result []byte, ok bool, err error) {
	var res []byte
	err = db.QueryRowContext(ctx, `SELECT result FROM idempotency WHERE key = ?`, key).Scan(&res)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("idem: load %q: %w", key, err)
	}
	return res, true, nil
}

// Applied reports whether key has already been executed, returning the stored
// result — the read-only 'fail if already done' check for a caller that wants to
// short-circuit before doing any work.
func Applied(ctx context.Context, db *sql.DB, key string) (result []byte, done bool, err error) {
	if err := EnsureSchema(ctx, db); err != nil {
		return nil, false, err
	}
	return load(ctx, db, key)
}

// EnsureSchema creates the idempotency table if absent. Safe to call repeatedly;
// Once calls it, so callers rarely need to.
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS idempotency(
		key        TEXT PRIMARY KEY,
		round      INTEGER NOT NULL,
		result     BLOB,
		created_at TEXT NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("idem: ensure schema: %w", err)
	}
	return nil
}

// Prune deletes dedup rows first applied before cutoff. Best-effort retention
// control: prune only keys older than the longest window in which a retry can
// still arrive, or a legitimately-retried old request would re-execute. Returns
// the number of rows removed.
func Prune(ctx context.Context, db *sql.DB, cutoff time.Time) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM idempotency WHERE created_at < ?`, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("idem: prune: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// isBusy reports whether err is a SQLite lock-contention error (busy_timeout
// exceeded). Matched by message so this package stays driver-agnostic (no
// modernc/mattn import): both drivers surface these exact substrings.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "database is locked") ||
		strings.Contains(s, "database table is locked") ||
		strings.Contains(s, "SQLITE_BUSY")
}
