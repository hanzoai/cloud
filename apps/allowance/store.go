// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package allowance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	// sqlpool.Open is the ONE opener: the database is born encrypted under the key
	// cek derives from the process master and the system namespace, and comes back
	// with the single-connection cap already applied.
	"github.com/hanzoai/cloud/sqlpool"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver (registers the
	// "sqlite" database/sql name under both cgo and pure-Go build tags). Blank import
	// registers the driver; importing modernc directly would double-register.
	_ "github.com/hanzoai/sqlite"
)

// Store is one row per subject: how many calls they have taken, and WHICH period
// that count belongs to.
//
// THE PERIOD IS THE ROW, WHICH IS WHY NOTHING RESETS ANYTHING. A count carries the
// day it was made on, so a row from yesterday reads as zero today and is overwritten
// by the first call of the new day. There is no scheduler, no sweep, and no window
// during which a job has not run yet — the turnover is a property of reading, so it
// has already happened for every subject at the same instant, including the ones
// nobody will ever call again.
//
// Isolation is the `subject` PRIMARY KEY and a mandatory `WHERE subject=?` on every
// statement. The value is the billing subject the gateway resolved from a verified
// credential — never a client-supplied header — so one caller can neither read nor
// spend another's allowance.
type Store struct{ db *sql.DB }

func openStore(dir string) (*Store, error) {
	db, err := sqlpool.Open("allowance", dir)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS allowance (
  subject TEXT NOT NULL PRIMARY KEY,
  period  TEXT NOT NULL,
  used    INTEGER NOT NULL
);`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Close releases the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Day is the period a count belongs to: the UTC calendar day. One rule, one
// timezone — a period that moved with the caller would let a traveller take two
// days' worth in one afternoon.
func Day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// Midnight is when the current period ends and counting starts again: the first
// instant of the next UTC day. It is what the product shows as "resets at".
func Midnight(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
}

// Take counts ONE SERVED call against subject's allowance for period and answers the
// count that stands afterwards, plus whether the subject is now out.
//
// The read and the increment are ONE statement inside ONE transaction on the single
// serialized connection, because a read followed by a write is two calls racing for
// the same row — both would see the same count and one increment would vanish. Under
// the transaction the loser sees the winner's count, so a served call is never lost.
//
// AT THE CEILING THE COUNT STOPS. A subject already at the limit is answered
// spent=true and their count is left where it is, so the number a customer reads is
// "10 of 10", never "37 of 10" — the refusals are not usage, and a counter that kept
// climbing would say they were.
//
// limit <= 0 means the plan does not bound this subject. Nothing is written and
// nothing is counted: a plan without a ceiling has no state to keep, so an
// unlimited caller costs this store no rows at all.
func (s *Store) Take(ctx context.Context, subject, period string, limit int64) (used int64, spent bool, err error) {
	if limit <= 0 {
		return 0, false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	used, err = readUsed(ctx, tx, subject, period)
	if err != nil {
		return 0, false, err
	}
	if used >= limit {
		return used, true, nil
	}
	used++
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO allowance (subject, period, used) VALUES (?,?,?)
		 ON CONFLICT(subject) DO UPDATE SET period=excluded.period, used=excluded.used`,
		subject, period, used); err != nil {
		return 0, false, fmt.Errorf("take allowance: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit: %w", err)
	}
	return used, false, nil
}

// Read answers what subject has taken in period, without taking anything. It is the
// product's read — the number behind "3 of 20 left today" — and a subject who has
// never called, or whose only row belongs to an earlier period, has taken nothing.
func (s *Store) Read(ctx context.Context, subject, period string) (int64, error) {
	return readUsed(ctx, s.db, subject, period)
}

// rows is the narrow half of *sql.DB and *sql.Tx that readUsed needs, so the same
// body answers inside a transaction and outside one. Two copies of "a row from
// another period counts as none" could drift, and the drift would be a customer
// billed for yesterday.
type rows interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readUsed(ctx context.Context, r rows, subject, period string) (int64, error) {
	var storedPeriod string
	var used int64
	err := r.QueryRowContext(ctx,
		`SELECT period, used FROM allowance WHERE subject=?`, subject).Scan(&storedPeriod, &used)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("read allowance: %w", err)
	case storedPeriod != period:
		return 0, nil // a count from another period is not this period's
	}
	return used, nil
}
