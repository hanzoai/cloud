// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package idem

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/hanzoai/sqlite" // ONE Hanzo driver (registers "sqlite"); its !cgo backend is modernc — never import modernc directly
)

// openDB opens a per-org SQLite at path with the SAME durability profile the HA
// substrate uses (WAL + busy_timeout), so the concurrency behaviour under test
// matches production. A fresh `effects` table stands in for whatever state a
// request mutates.
func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS effects(id TEXT PRIMARY KEY, val TEXT)`); err != nil {
		t.Fatalf("create effects: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// applyDebit is a representative money-critical effect: it inserts one row into
// `effects`. Run twice for the same id it would violate the PK — so a double-run
// is observable as an error, and exactly-once is observable as one row.
func applyDebit(id, val string, calls *int64) func(context.Context, *sql.Tx) ([]byte, error) {
	return func(ctx context.Context, tx *sql.Tx) ([]byte, error) {
		atomic.AddInt64(calls, 1)
		if _, err := tx.ExecContext(ctx, `INSERT INTO effects(id, val) VALUES(?, ?)`, id, val); err != nil {
			return nil, err
		}
		return []byte("receipt:" + val), nil
	}
}

func rowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestOnceRunsEffectOnce: the first call applies the effect, stores the result,
// and reports applied=true.
func TestOnceRunsEffectOnce(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "org.db"))
	var calls int64
	res, applied, err := Once(context.Background(), db, "req-1", 7, applyDebit("charge-1", "5usd", &calls))
	if err != nil || !applied {
		t.Fatalf("first Once: applied=%v err=%v, want applied=true nil", applied, err)
	}
	if string(res) != "receipt:5usd" {
		t.Fatalf("result = %q, want receipt:5usd", res)
	}
	if calls != 1 || rowCount(t, db, "effects") != 1 {
		t.Fatalf("effect ran %d times / %d rows, want 1/1", calls, rowCount(t, db, "effects"))
	}
}

// TestDuplicateReturnsPriorResultNotReexecuted is the core exactly-once proof: a
// retried request with the same key does NOT run the effect again and returns the
// first run's result with ErrAlreadyApplied ('fail if already done').
func TestDuplicateReturnsPriorResultNotReexecuted(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "org.db"))
	var calls int64
	ctx := context.Background()

	first, _, err := Once(ctx, db, "req-1", 7, applyDebit("charge-1", "5usd", &calls))
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	res, applied, err := Once(ctx, db, "req-1", 9, applyDebit("charge-1", "5usd", &calls))
	if !errors.Is(err, ErrAlreadyApplied) {
		t.Fatalf("retry: err=%v, want ErrAlreadyApplied", err)
	}
	if applied {
		t.Fatal("retry must NOT re-apply the effect")
	}
	if string(res) != string(first) {
		t.Fatalf("retry result = %q, want prior %q", res, first)
	}
	if calls != 1 || rowCount(t, db, "effects") != 1 {
		t.Fatalf("effect ran %d times / %d rows across a retry, want 1/1 (double-execute!)", calls, rowCount(t, db, "effects"))
	}
}

// TestConcurrentOnceExactlyOnce races many goroutines on the same key: the effect
// must run EXACTLY once, exactly one caller sees applied=true, and all callers
// return the same result. This is the same shape as visor's
// TestConcurrentClaimMeterHourExactlyOnce, generalized to arbitrary requests.
func TestConcurrentOnceExactlyOnce(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "org.db"))
	var calls int64
	const racers = 16
	var wins int64
	results := make([][]byte, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, applied, err := Once(context.Background(), db, "req-hot", 3, applyDebit("charge-hot", "1usd", &calls))
			if err != nil && !errors.Is(err, ErrAlreadyApplied) {
				t.Errorf("racer %d: unexpected error %v", i, err)
				return
			}
			if applied {
				atomic.AddInt64(&wins, 1)
			}
			results[i] = res
		}()
	}
	close(start)
	wg.Wait()

	if calls != 1 {
		t.Fatalf("effect ran %d times under concurrency, want exactly 1", calls)
	}
	if wins != 1 {
		t.Fatalf("%d racers saw applied=true, want exactly 1", wins)
	}
	if rowCount(t, db, "effects") != 1 {
		t.Fatalf("%d effect rows, want 1", rowCount(t, db, "effects"))
	}
	for i, r := range results {
		if string(r) != "receipt:1usd" {
			t.Fatalf("racer %d got result %q, want receipt:1usd (all must see the one result)", i, r)
		}
	}
}

// TestSurvivesWriterHandoff is the handoff proof: a request applied on the OLD
// writer's DB is not re-executed by a SUCCESSOR that inherits the same durable
// bytes. We model 'the successor hydrated the shipped snapshot' by reopening the
// very file the old writer committed to — the dedup row is in it, so the retry on
// the successor short-circuits and the effect is not doubled.
func TestSurvivesWriterHandoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "org.db")
	ctx := context.Background()

	// Old writer applies req-1 and (implicitly) 'ships' by committing to the file.
	old := openDB(t, path)
	var oldCalls int64
	if _, _, err := Once(ctx, old, "req-1", 4, applyDebit("charge-1", "5usd", &oldCalls)); err != nil {
		t.Fatalf("old writer apply: %v", err)
	}
	_ = old.Close()

	// Successor boots on the inherited bytes and retries the same request.
	succ := openDB(t, path)
	var succCalls int64
	res, applied, err := Once(ctx, succ, "req-1", 5, applyDebit("charge-1", "5usd", &succCalls))
	if !errors.Is(err, ErrAlreadyApplied) || applied {
		t.Fatalf("successor: applied=%v err=%v, want dedup (ErrAlreadyApplied, applied=false)", applied, err)
	}
	if string(res) != "receipt:5usd" {
		t.Fatalf("successor recalled result %q, want receipt:5usd", res)
	}
	if succCalls != 0 {
		t.Fatalf("successor ran the effect %d times, want 0 (handoff double-execute!)", succCalls)
	}
	if rowCount(t, succ, "effects") != 1 {
		t.Fatalf("%d effect rows after handoff, want 1", rowCount(t, succ, "effects"))
	}
}

// TestFailedApplyRollsBackAndRetries proves the atomicity of the dedup row with
// the effect: if apply fails, neither the key nor the effect persists, so the
// request is cleanly retryable and then runs exactly once.
func TestFailedApplyRollsBackAndRetries(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "org.db"))
	ctx := context.Background()

	boom := errors.New("downstream unavailable")
	_, applied, err := Once(ctx, db, "req-1", 1, func(ctx context.Context, tx *sql.Tx) ([]byte, error) {
		if _, e := tx.ExecContext(ctx, `INSERT INTO effects(id, val) VALUES('charge-1','5usd')`); e != nil {
			return nil, e
		}
		return nil, boom // effect wrote, but the request ultimately fails.
	})
	if applied || !errors.Is(err, boom) {
		t.Fatalf("failed apply: applied=%v err=%v, want applied=false err=boom", applied, err)
	}
	// Nothing persisted: no dedup key, no effect row.
	if _, done, _ := Applied(ctx, db, "req-1"); done {
		t.Fatal("a failed request must NOT leave a dedup key (would wrongly suppress the retry)")
	}
	if rowCount(t, db, "effects") != 0 {
		t.Fatalf("failed apply left %d effect rows, want 0 (rollback broken)", rowCount(t, db, "effects"))
	}

	// The retry now succeeds exactly once.
	var calls int64
	if _, ap, err := Once(ctx, db, "req-1", 1, applyDebit("charge-1", "5usd", &calls)); err != nil || !ap {
		t.Fatalf("retry after failure: applied=%v err=%v, want applied=true nil", ap, err)
	}
	if calls != 1 || rowCount(t, db, "effects") != 1 {
		t.Fatalf("retry ran %d times / %d rows, want 1/1", calls, rowCount(t, db, "effects"))
	}
}

// TestPruneRemovesOldKeys covers retention: keys older than the cutoff are removed
// so the table stays bounded (a pruned key is only safe once no retry can arrive).
func TestPruneRemovesOldKeys(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "org.db"))
	ctx := context.Background()
	var calls int64
	if _, _, err := Once(ctx, db, "old", 1, applyDebit("c-old", "1usd", &calls)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	n, err := Prune(ctx, db, time.Now().Add(time.Hour)) // cutoff in the future → removes the row.
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}
	if _, done, _ := Applied(ctx, db, "old"); done {
		t.Fatal("pruned key must no longer be recorded")
	}
}
