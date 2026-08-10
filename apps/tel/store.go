package tel

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/hanzoai/cloud/sqlpool"
)

// Store holds what this org bought and what it sent.
//
// Every table leads its primary key with `org`, so tenant isolation is a physical
// property of the row rather than a WHERE clause somebody has to remember. A query
// that forgets the org cannot accidentally return another tenant's rows — it
// returns none.
type Store struct{ db *sql.DB }

func openStore(dir string) (*Store, error) {
	db, err := sqlpool.Open("tel", dir)
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

// migrate creates the three tables. Idempotent (IF NOT EXISTS).
//
// Calls and messages are RECORDS, not queues: they are written once when the
// carrier accepts the request and updated when it reports an outcome. Nothing
// reads them to decide what to do next, which is why there is no status index and
// no claim column.
func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS tel_numbers (
  org      TEXT NOT NULL,
  id       TEXT NOT NULL,
  e164     TEXT NOT NULL,
  country  TEXT NOT NULL DEFAULT '',
  type     TEXT NOT NULL DEFAULT '',
  capable  TEXT NOT NULL DEFAULT '',
  monthly  INTEGER NOT NULL DEFAULT 0,
  currency TEXT NOT NULL DEFAULT '',
  bought   INTEGER NOT NULL DEFAULT (unixepoch()),
  PRIMARY KEY (org, id)
);
-- A number is held by one org at a time; the unique index is what makes a second
-- org's attempt to record the same number fail rather than shadow the first.
CREATE UNIQUE INDEX IF NOT EXISTS tel_numbers_e164 ON tel_numbers (e164);

CREATE TABLE IF NOT EXISTS tel_calls (
  org     TEXT NOT NULL,
  id      TEXT NOT NULL,
  from_no TEXT NOT NULL,
  to_no   TEXT NOT NULL,
  status  TEXT NOT NULL DEFAULT '',
  agent   TEXT NOT NULL DEFAULT '',
  started INTEGER NOT NULL DEFAULT (unixepoch()),
  PRIMARY KEY (org, id)
);

CREATE TABLE IF NOT EXISTS tel_messages (
  org     TEXT NOT NULL,
  id      TEXT NOT NULL,
  from_no TEXT NOT NULL,
  to_no   TEXT NOT NULL,
  body    TEXT NOT NULL DEFAULT '',
  status  TEXT NOT NULL DEFAULT '',
  sent    INTEGER NOT NULL DEFAULT (unixepoch()),
  PRIMARY KEY (org, id)
);`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) PutNumber(ctx context.Context, n Number) error {
	capable, _ := json.Marshal(n.Capable)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tel_numbers (org,id,e164,country,type,capable,monthly,currency)
		 VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(org,id) DO UPDATE SET e164=excluded.e164, country=excluded.country,
		   type=excluded.type, capable=excluded.capable, monthly=excluded.monthly,
		   currency=excluded.currency`,
		n.Org, n.ID, n.E164, n.Country, n.Type, string(capable), n.Monthly, n.Currency)
	return err
}

func scanNumber(rows *sql.Rows) (Number, error) {
	var n Number
	var capable string
	if err := rows.Scan(&n.Org, &n.ID, &n.E164, &n.Country, &n.Type, &capable, &n.Monthly, &n.Currency); err != nil {
		return Number{}, err
	}
	if capable != "" {
		_ = json.Unmarshal([]byte(capable), &n.Capable)
	}
	return n, nil
}

const numberCols = `org,id,e164,country,type,capable,monthly,currency`

func (s *Store) Numbers(ctx context.Context, org string) ([]Number, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+numberCols+` FROM tel_numbers WHERE org=? ORDER BY bought DESC`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Number{}
	for rows.Next() {
		n, err := scanNumber(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) Number(ctx context.Context, org, id string) (Number, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+numberCols+` FROM tel_numbers WHERE org=? AND id=?`, org, id)
	if err != nil {
		return Number{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Number{}, rows.Err()
	}
	return scanNumber(rows)
}

// NumberByE164 answers "is this org allowed to send as this number". It is the
// check that keeps one tenant from putting another's caller ID on a call.
func (s *Store) NumberByE164(ctx context.Context, org, e164 string) (Number, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+numberCols+` FROM tel_numbers WHERE org=? AND e164=?`, org, strings.TrimSpace(e164))
	if err != nil {
		return Number{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Number{}, rows.Err()
	}
	return scanNumber(rows)
}

func (s *Store) DeleteNumber(ctx context.Context, org, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tel_numbers WHERE org=? AND id=?`, org, id)
	return err
}

func (s *Store) PutCall(ctx context.Context, c Call) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tel_calls (org,id,from_no,to_no,status,agent) VALUES (?,?,?,?,?,?)
		 ON CONFLICT(org,id) DO UPDATE SET status=excluded.status`,
		c.Org, c.ID, c.From, c.To, c.Status, c.Agent)
	return err
}

func (s *Store) Calls(ctx context.Context, org string) ([]Call, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org,id,from_no,to_no,status,agent FROM tel_calls WHERE org=? ORDER BY started DESC LIMIT 500`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Call{}
	for rows.Next() {
		var c Call
		if err := rows.Scan(&c.Org, &c.ID, &c.From, &c.To, &c.Status, &c.Agent); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Call(ctx context.Context, org, id string) (Call, error) {
	var c Call
	err := s.db.QueryRowContext(ctx,
		`SELECT org,id,from_no,to_no,status,agent FROM tel_calls WHERE org=? AND id=?`, org, id).
		Scan(&c.Org, &c.ID, &c.From, &c.To, &c.Status, &c.Agent)
	if err == sql.ErrNoRows {
		return Call{}, nil
	}
	return c, err
}

func (s *Store) PutMessage(ctx context.Context, m Message) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tel_messages (org,id,from_no,to_no,body,status) VALUES (?,?,?,?,?,?)
		 ON CONFLICT(org,id) DO UPDATE SET status=excluded.status`,
		m.Org, m.ID, m.From, m.To, m.Text, m.Status)
	return err
}

func (s *Store) Messages(ctx context.Context, org string) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org,id,from_no,to_no,body,status FROM tel_messages WHERE org=? ORDER BY sent DESC LIMIT 500`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Org, &m.ID, &m.From, &m.To, &m.Text, &m.Status); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Counts(ctx context.Context, org string) (numbers, calls, messages int, err error) {
	q := func(table string) (int, error) {
		var n int
		e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE org=?`, org).Scan(&n)
		return n, e
	}
	if numbers, err = q("tel_numbers"); err != nil {
		return
	}
	if calls, err = q("tel_calls"); err != nil {
		return
	}
	messages, err = q("tel_messages")
	return
}
