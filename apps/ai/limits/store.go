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

package limits

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"

	_ "github.com/hanzoai/sqlite"
)

// limitStore holds what plans include and have used: one row per admitted request,
// each payer's running session, what each covered request used of its plan by the
// class it was in (premium, ours — the `family` column, which once named the Hanzo
// family a cost was spent for and now names the class), what the models under one
// capped rule used of it (share), and each model's daily cap count per payer. It is
// the one database ("limits", a SQLite file in the data dir or a schema on hanzo-sql)
// of the process that serves /v1 models, so every gate admits against the same rows,
// and every transaction of one payer runs in order (Dialect.Serialize: a file's one
// connection, a lock per payer on hanzo-sql): two requests can never both take the
// last of a window or a budget, and a payer waits for nobody else.
//
// Times are unix seconds; amounts are nano-dollars (1e-9 USD). Every statement
// names its payer, the org and the subject within it.
type limitStore struct{ db *sql.DB }

// subsystem names the store in the system namespace; manifest.SQLStores lists it.
const subsystem = "limits"

func openLimits(dir string) (*limitStore, error) {
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*limitStore, error) {
		s := &limitStore{db: db}
		if err := s.migrate(); err != nil {
			return nil, fmt.Errorf("limits schema: %w", err)
		}
		return s, nil
	})
}

// migrate makes the tables, in one transaction under the store's migration lock.
// Times are unix seconds and amounts are nano-dollars, so every integer is BIGINT:
// PostgreSQL's INTEGER is 32 bits, and SQLite gives BIGINT the same 64-bit affinity.
func (s *limitStore) migrate() error {
	d := dialect.Of(s.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS request (
  org     TEXT    NOT NULL,
  subject TEXT    NOT NULL,
  at      BIGINT  NOT NULL
);
CREATE INDEX IF NOT EXISTS request_payer_at ON request (org, subject, at);
CREATE TABLE IF NOT EXISTS session (
  org     TEXT    NOT NULL,
  subject TEXT    NOT NULL,
  start   BIGINT  NOT NULL,
  PRIMARY KEY (org, subject)
);
CREATE TABLE IF NOT EXISTS spend (
  org     TEXT    NOT NULL,
  subject TEXT    NOT NULL,
  family  TEXT    NOT NULL,
  at      BIGINT  NOT NULL,
  nanos   BIGINT  NOT NULL
);
CREATE INDEX IF NOT EXISTS spend_payer_at ON spend (org, subject, at);
CREATE TABLE IF NOT EXISTS share (
  org     TEXT    NOT NULL,
  subject TEXT    NOT NULL,
  rule    TEXT    NOT NULL,
  at      BIGINT  NOT NULL,
  nanos   BIGINT  NOT NULL
);
CREATE INDEX IF NOT EXISTS share_payer_rule_at ON share (org, subject, rule, at);
CREATE TABLE IF NOT EXISTS known (
  key   TEXT NOT NULL PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS cap (
  org     TEXT    NOT NULL,
  subject TEXT    NOT NULL,
  model   TEXT    NOT NULL,
  day     TEXT    NOT NULL,
  used    BIGINT  NOT NULL,
  PRIMARY KEY (org, subject, model, day)
);
CREATE TABLE IF NOT EXISTS free_reset (
  org      TEXT   NOT NULL,
  subject  TEXT   NOT NULL,
  reset_at BIGINT NOT NULL,
  PRIMARY KEY (org, subject)
);
DROP TABLE IF EXISTS covered;
DROP TABLE IF EXISTS member;
DROP TABLE IF EXISTS hold;
DROP TABLE IF EXISTS credits;
UPDATE spend SET family = 'ours' WHERE family IN ('enso', 'zen');`+keySchema+rateSchema)
		return err
	})
}

func (s *limitStore) Close() error { return s.db.Close() }

// know keeps the last value the flag plane gave for one usage policy switch.
func (s *limitStore) know(ctx context.Context, key, value string) error {
	if err := dialect.Text(key, value); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO known (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// knowns is every usage policy switch's last value the flag plane gave.
func (s *limitStore) knowns(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM known`)
	if err != nil {
		return nil, fmt.Errorf("limits known: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("limits known: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

// payer begins a transaction that is the only one reading and writing this payer's
// rows until it ends. Every statement of the store names its payer, so a payer's
// transactions are the only ones that can race each other.
func payer(ctx context.Context, db *sql.DB, org, subject string) (*sql.Tx, error) {
	return dialect.Of(db).Serialize(ctx, db, org+"/"+subject)
}

// keep is how long request rows are kept: a day window and a session are both
// shorter.
const keep = 48 * time.Hour

// terms are one payer's limits as its plan states them now.
type terms struct {
	session, day int64                // requests per window; 0 = unbounded
	block        time.Duration        // the session window's length
	allow        map[string]allowance // what each class includes, nano-dollars
	overage      bool                 // past the included usage, prepaid pays
	start, end   time.Time            // the billing period
	// whole is one seat of each seat type, all of it, per class (plan.cap).
	whole map[string]func(id string) Cap
}

// seat is one whole seat of a seat type in class; a class the terms do not
// carry is unbounded.
func (t terms) seat(class string) func(id string) Cap {
	if f := t.whole[class]; f != nil {
		return f
	}
	return func(string) Cap { return Cap{} }
}

// admission is what admit decided: the window that refused, or what the request may
// spend on paid upstream.
type admission struct {
	full    string // "session" or "day"; "" admitted
	resets  time.Time
	spend   int64 // what the class has left, nano-dollars
	used    int64 // what the class has used of its period, nano-dollars
	counted bool  // the request was counted in the windows, at at
	at      int64
}

// q is the one interface a read and a write both run on: the pinned connection
// directly, or a transaction on it.
type q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func scalar(ctx context.Context, db q, query string, args ...any) (int64, error) {
	var n int64
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// sessionStart is the payer's running session's start, 0 when none runs at now.
func sessionStart(ctx context.Context, db q, org, subject string, now time.Time, block time.Duration) (int64, error) {
	var start int64
	err := db.QueryRowContext(ctx, `SELECT start FROM session WHERE org=$1 AND subject=$2`, org, subject).Scan(&start)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read session: %w", err)
	}
	if now.Unix() >= start+int64(block/time.Second) {
		return 0, nil
	}
	return start, nil
}

func requestsSince(ctx context.Context, db q, org, subject string, since int64) (int64, error) {
	n, err := scalar(ctx, db, `SELECT COUNT(*) FROM request WHERE org=$1 AND subject=$2 AND at>=$3`, org, subject, since)
	if err != nil {
		return 0, fmt.Errorf("count requests: %w", err)
	}
	return n, nil
}

// spentSince sums settled use of one class since a moment.
func spentSince(ctx context.Context, db q, org, subject, class string, since int64) (int64, error) {
	n, err := scalar(ctx, db, `SELECT CAST(COALESCE(SUM(nanos),0) AS BIGINT) FROM spend WHERE org=$1 AND subject=$2 AND at>=$3 AND family=$4`,
		org, subject, since, class)
	if err != nil {
		return 0, fmt.Errorf("sum spend: %w", err)
	}
	return n, nil
}

// left is what a class may still use: the least of what its period and, when the
// plan sets one, its short window leave once settled use is counted. It answers the
// period's use beside it.
func left(ctx context.Context, db q, org, subject, class string, now time.Time, t terms) (rest, used int64, err error) {
	a := t.allow[class]
	used, err = spentSince(ctx, db, org, subject, class, t.start.Unix())
	if err != nil {
		return 0, 0, err
	}
	rest = a.period - used
	if a.span > 0 {
		w, err := spentSince(ctx, db, org, subject, class, now.Add(-a.span).Unix())
		if err != nil {
			return 0, 0, err
		}
		rest = min(rest, a.window-w)
	}
	return max(0, rest), used, nil
}

// midnight is the start of the UTC day after now: when the day window turns.
func midnight(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
}

// admit decides one request in one transaction: a full window refuses with when it
// resets; otherwise, when spend is set, what the class may still use is read as the
// request's allowance, and the request is counted (when count is set, and, with
// needLeft, only when the class has something left), its session opened if none
// runs. Nothing is reserved: the answer settles at what it used.
func (s *limitStore) admit(ctx context.Context, org, subject, class string, now time.Time, t terms, spend, count, needLeft bool) (admission, error) {
	if err := dialect.Text(org, subject, class); err != nil {
		return admission{}, err
	}
	tx, err := payer(ctx, s.db, org, subject)
	if err != nil {
		return admission{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var a admission
	start, err := sessionStart(ctx, tx, org, subject, now, t.block)
	if err != nil {
		return a, err
	}
	if count {
		if start != 0 && t.session > 0 {
			used, err := requestsSince(ctx, tx, org, subject, start)
			if err != nil {
				return a, err
			}
			if used >= t.session {
				a.full, a.resets = "session", time.Unix(start, 0).Add(t.block).UTC()
			}
		}
		if t.day > 0 {
			turn := midnight(now)
			used, err := requestsSince(ctx, tx, org, subject, turn.AddDate(0, 0, -1).Unix())
			if err != nil {
				return a, err
			}
			if used >= t.day && turn.After(a.resets) {
				a.full, a.resets = "day", turn
			}
		}
		if a.full != "" {
			return a, nil
		}
	}

	if spend {
		if a.spend, a.used, err = left(ctx, tx, org, subject, class, now, t); err != nil {
			return a, err
		}
	}

	if count && (t.session > 0 || t.day > 0) && (!needLeft || a.spend > 0) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO request (org, subject, at) VALUES ($1,$2,$3)`, org, subject, now.Unix()); err != nil {
			return a, fmt.Errorf("count request: %w", err)
		}
		a.counted, a.at = true, now.Unix()
		if start == 0 && t.session > 0 {
			// A new session opens at this request; rows older than any window are
			// dropped as it does.
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO session (org, subject, start) VALUES ($1,$2,$3)
				 ON CONFLICT(org, subject) DO UPDATE SET start = excluded.start`, org, subject, now.Unix()); err != nil {
				return a, fmt.Errorf("open session: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM request WHERE org=$1 AND subject=$2 AND at<$3`, org, subject, now.Add(-keep).Unix()); err != nil {
				return a, fmt.Errorf("drop old requests: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return admission{}, fmt.Errorf("commit: %w", err)
	}
	return a, nil
}

// settle records what one answer used, never more than its class may still use: use
// never passes what the plan includes. A model under a capped rule also counts what
// it recorded against the rule's share. It reports what it recorded.
func (s *limitStore) settle(ctx context.Context, org, subject, class, rule string, nanos int64, now time.Time, t terms) (int64, error) {
	if err := dialect.Text(org, subject, class, rule); err != nil {
		return 0, err
	}
	tx, err := payer(ctx, s.db, org, subject)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	l, _, err := left(ctx, tx, org, subject, class, now, t)
	if err != nil {
		return 0, err
	}
	recorded := min(nanos, l)
	if recorded > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO spend (org, subject, family, at, nanos) VALUES ($1,$2,$3,$4,$5)`,
			org, subject, class, now.Unix(), recorded); err != nil {
			return 0, fmt.Errorf("record spend: %w", err)
		}
		if rule != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO share (org, subject, rule, at, nanos) VALUES ($1,$2,$3,$4,$5)`,
				org, subject, rule, now.Unix(), recorded); err != nil {
				return 0, fmt.Errorf("record share: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return recorded, nil
}

// take counts one request against a model's daily cap for a payer, in one
// transaction, and answers the count after it — or full, counting nothing, when the
// cap was already reached. The day is the UTC day.
func (s *limitStore) take(ctx context.Context, org, subject, model string, now time.Time, cap int64) (int64, bool, error) {
	if err := dialect.Text(org, subject, model); err != nil {
		return 0, false, err
	}
	day := now.UTC().Format("2006-01-02")
	tx, err := payer(ctx, s.db, org, subject)
	if err != nil {
		return 0, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var used int64
	err = tx.QueryRowContext(ctx, `SELECT used FROM cap WHERE org=$1 AND subject=$2 AND model=$3 AND day=$4`, org, subject, model, day).Scan(&used)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("read cap: %w", err)
	}
	if used >= cap {
		return used, true, nil
	}
	used++
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO cap (org, subject, model, day, used) VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT(org, subject, model, day) DO UPDATE SET used = excluded.used`, org, subject, model, day, used); err != nil {
		return 0, false, fmt.Errorf("count cap: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cap WHERE org=$1 AND subject=$2 AND day<$3`, org, subject, now.UTC().AddDate(0, 0, -2).Format("2006-01-02")); err != nil {
		return 0, false, fmt.Errorf("drop old caps: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit: %w", err)
	}
	return used, false, nil
}

// standing reads a payer's windows and what is left of the budget at now.
type standing struct {
	sessionStart int64
	sessionUsed  int64
	dayUsed      int64
}

func (s *limitStore) read(ctx context.Context, org, subject string, now time.Time, t terms) (standing, error) {
	var st standing
	if err := dialect.Text(org, subject); err != nil {
		return st, err
	}
	var err error
	if st.sessionStart, err = sessionStart(ctx, s.db, org, subject, now, t.block); err != nil {
		return st, err
	}
	if st.sessionStart != 0 {
		if st.sessionUsed, err = requestsSince(ctx, s.db, org, subject, st.sessionStart); err != nil {
			return st, err
		}
	}
	if st.dayUsed, err = requestsSince(ctx, s.db, org, subject, midnight(now).AddDate(0, 0, -1).Unix()); err != nil {
		return st, err
	}
	return st, nil
}

// spent is one class's settled use since a moment.
func (s *limitStore) spent(ctx context.Context, org, subject, class string, since int64) (int64, error) {
	if err := dialect.Text(org, subject, class); err != nil {
		return 0, err
	}
	return spentSince(ctx, s.db, org, subject, class, since)
}

// oldestSpent is the earliest settled spend timestamp (unix seconds) for a class
// at or after since, or 0 when no spend has occurred in the window.
func (s *limitStore) oldestSpent(ctx context.Context, org, subject, class string, since int64) (int64, error) {
	if err := dialect.Text(org, subject, class); err != nil {
		return 0, err
	}
	n, err := scalar(ctx, s.db, `SELECT CAST(COALESCE(MIN(at),0) AS BIGINT) FROM spend WHERE org=$1 AND subject=$2 AND at>=$3 AND family=$4 AND nanos>0`,
		org, subject, since, class)
	if err != nil {
		return 0, fmt.Errorf("oldest spend: %w", err)
	}
	return n, nil
}

// share is what the models under one capped rule used of the plan since a moment.
func (s *limitStore) share(ctx context.Context, org, subject, rule string, since int64) (int64, error) {
	if err := dialect.Text(org, subject, rule); err != nil {
		return 0, err
	}
	n, err := scalar(ctx, s.db, `SELECT CAST(COALESCE(SUM(nanos),0) AS BIGINT) FROM share WHERE org=$1 AND subject=$2 AND rule=$3 AND at>=$4`,
		org, subject, rule, since)
	if err != nil {
		return 0, fmt.Errorf("sum share: %w", err)
	}
	return n, nil
}

// peek reads a daily count without taking one.
func (s *limitStore) peek(ctx context.Context, org, subject, model string, now time.Time) (int64, error) {
	if err := dialect.Text(org, subject, model); err != nil {
		return 0, err
	}
	var used int64
	err := s.db.QueryRowContext(ctx, `SELECT used FROM cap WHERE org=$1 AND subject=$2 AND model=$3 AND day=$4`,
		org, subject, model, now.UTC().Format("2006-01-02")).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read cap: %w", err)
	}
	return used, nil
}

// release takes back what one admitted request was counted against, in one
// transaction: its row in the request windows (at at, when counted), one from each
// daily count it took (takes, at day), and the session it opened, when it was that
// session's only request.
func (s *limitStore) release(ctx context.Context, org, subject string, counted bool, at int64, day string, takes []string) error {
	if err := dialect.Text(append([]string{org, subject, day}, takes...)...); err != nil {
		return err
	}
	tx, err := payer(ctx, s.db, org, subject)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if counted {
		// Take back ONE of the rows this payer was counted in at that second. They are
		// the same row (a request has no identity of its own), so the rows at that
		// second are counted, removed, and all but one put back: no rowid, which
		// PostgreSQL has no counterpart of.
		same, err := scalar(ctx, tx, `SELECT COUNT(*) FROM request WHERE org=$1 AND subject=$2 AND at=$3`, org, subject, at)
		if err != nil {
			return fmt.Errorf("release request: %w", err)
		}
		if same > 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM request WHERE org=$1 AND subject=$2 AND at=$3`, org, subject, at); err != nil {
				return fmt.Errorf("release request: %w", err)
			}
			for range same - 1 {
				if _, err := tx.ExecContext(ctx, `INSERT INTO request (org, subject, at) VALUES ($1,$2,$3)`, org, subject, at); err != nil {
					return fmt.Errorf("release request: %w", err)
				}
			}
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM session WHERE org=$1 AND subject=$2 AND start=$3 AND NOT EXISTS (SELECT 1 FROM request WHERE org=$4 AND subject=$5 AND at>=$6)`,
			org, subject, at, org, subject, at); err != nil {
			return fmt.Errorf("release session: %w", err)
		}
	}
	for _, model := range takes {
		if _, err := tx.ExecContext(ctx, `UPDATE cap SET used = used - 1 WHERE org=$1 AND subject=$2 AND model=$3 AND day=$4 AND used > 0`,
			org, subject, model, day); err != nil {
			return fmt.Errorf("release cap: %w", err)
		}
	}
	return tx.Commit()
}

// lastFreeReset returns the unix timestamp of the payer's most recent self-service
// free reset, or 0 if they have never used one.
func (s *limitStore) lastFreeReset(ctx context.Context, org, subject string) (int64, error) {
	if err := dialect.Text(org, subject); err != nil {
		return 0, err
	}
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT reset_at FROM free_reset WHERE org=$1 AND subject=$2`, org, subject).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read free reset: %w", err)
	}
	return at, nil
}

// freeReset clears the payer's active usage counters (requests, session, spend,
// share, daily caps) in one serialized payer transaction if at least cooldown has
// elapsed since their last free reset. Returns (lastAt, nextAt, ok, err): when ok
// is false the payer is still on cooldown until nextAt and nothing was cleared.
func (s *limitStore) freeReset(ctx context.Context, org, subject string, now time.Time, cooldown time.Duration) (lastAt, nextAt time.Time, ok bool, err error) {
	if err := dialect.Text(org, subject); err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	tx, err := payer(ctx, s.db, org, subject)
	if err != nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var prev int64
	qerr := tx.QueryRowContext(ctx, `SELECT reset_at FROM free_reset WHERE org=$1 AND subject=$2`, org, subject).Scan(&prev)
	if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
		return time.Time{}, time.Time{}, false, fmt.Errorf("read free reset: %w", qerr)
	}
	if prev > 0 {
		prevTime := time.Unix(prev, 0).UTC()
		unlock := prevTime.Add(cooldown)
		if now.Before(unlock) {
			return prevTime, unlock, false, nil
		}
	}

	for _, stmt := range []string{
		`DELETE FROM request WHERE org=$1 AND subject=$2`,
		`DELETE FROM session WHERE org=$1 AND subject=$2`,
		`DELETE FROM spend WHERE org=$1 AND subject=$2`,
		`DELETE FROM share WHERE org=$1 AND subject=$2`,
		`DELETE FROM cap WHERE org=$1 AND subject=$2`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, org, subject); err != nil {
			return time.Time{}, time.Time{}, false, fmt.Errorf("clear usage: %w", err)
		}
	}
	ts := now.UTC().Truncate(time.Second)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO free_reset (org, subject, reset_at) VALUES ($1,$2,$3)
		 ON CONFLICT (org, subject) DO UPDATE SET reset_at = excluded.reset_at`,
		org, subject, ts.Unix()); err != nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("record free reset: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return time.Time{}, time.Time{}, false, fmt.Errorf("commit: %w", err)
	}
	return ts, ts.Add(cooldown), true, nil
}
