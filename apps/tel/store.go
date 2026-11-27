package tel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
)

// Store holds what this org bought and what it sent.
//
// Every table leads its primary key with `org`, so tenant isolation is a physical
// property of the row rather than a WHERE clause somebody has to remember. A query
// that forgets the org cannot accidentally return another tenant's rows — it
// returns none.
//
// It is one database, a SQLite file or a schema on hanzo-sql (locus.Open), and its
// SQL is the dialect both engines share (sdk/dialect).
//
// The numbers a person called from or texted, and what they said, are SEALED columns
// (internal/org, field.go) on both engines, each bound to its row's (org, id):
//
//	tel_numbers.e164                  a number the org holds
//	tel_calls.from_no, .to_no         who called whom
//	tel_messages.from_no, .to_no      who texted whom
//	tel_messages.body                 what they said
//
// A number is also looked up by value ("is this org allowed to send as this one") and
// held by one org at a time, so tel_numbers keeps e164_blind, the blind index of the
// number, and both the lookup and the unique index read that. Nothing reads a call's
// or a message's numbers or text in a WHERE or an ORDER BY. Left plain: ids, the
// carrier's status vocabulary, country, type, the capability list, prices and times.
type Store struct {
	db     *sql.DB
	fields *tenure.Fields
}

// subsystem names the store in the system namespace; manifest.SQLStores lists it.
const subsystem = "tel"

var (
	numberKey, numberSealed   = []string{"org", "id"}, []string{"e164"}
	callKey, callSealed       = []string{"org", "id"}, []string{"from_no", "to_no"}
	messageKey, messageSealed = []string{"org", "id"}, []string{"from_no", "to_no", "body"}
)

// openStore opens the database and the sealed columns' keys. master is
// cloud.Deps.MasterKey; with none the store does not open, so no number or text is ever
// written in the clear because a key was missing.
func openStore(dir string, master []byte) (*Store, error) {
	c, err := tenure.NewCipher(master)
	if err != nil {
		return nil, fmt.Errorf("tel: %w", err)
	}
	fields, err := c.Fields(namespace.System(), subsystem)
	if err != nil {
		return nil, fmt.Errorf("tel: %w", err)
	}
	return locus.Use(context.Background(), nil, namespace.System(), subsystem, dir, func(db *sql.DB) (*Store, error) {
		s := &Store{db: db, fields: fields}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	})
}

// migrate creates the three tables, in one transaction under the store's migration
// lock. Idempotent (IF NOT EXISTS).
//
// Calls and messages are RECORDS, not queues: they are written once when the
// carrier accepts the request and updated when it reports an outcome. Nothing
// reads them to decide what to do next, which is why there is no status index and
// no claim column.
//
// The time a row was made is set by the insert, from Go, and not by a column default
// (unixepoch() is SQLite's); a file that has the default keeps it, and no insert
// reaches it.
func (s *Store) migrate() error {
	d := dialect.Of(s.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS tel_numbers (
  org       TEXT NOT NULL,
  id        TEXT NOT NULL,
  e164      TEXT NOT NULL,
  e164_blind TEXT NOT NULL DEFAULT '',
  country  TEXT NOT NULL DEFAULT '',
  type     TEXT NOT NULL DEFAULT '',
  capable  TEXT NOT NULL DEFAULT '',
  monthly  BIGINT NOT NULL DEFAULT 0,
  currency TEXT NOT NULL DEFAULT '',
  bought   BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (org, id)
);

CREATE TABLE IF NOT EXISTS tel_calls (
  org     TEXT NOT NULL,
  id      TEXT NOT NULL,
  from_no TEXT NOT NULL,
  to_no   TEXT NOT NULL,
  status  TEXT NOT NULL DEFAULT '',
  agent   TEXT NOT NULL DEFAULT '',
  started BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (org, id)
);

CREATE TABLE IF NOT EXISTS tel_messages (
  org     TEXT NOT NULL,
  id      TEXT NOT NULL,
  from_no TEXT NOT NULL,
  to_no   TEXT NOT NULL,
  body    TEXT NOT NULL DEFAULT '',
  status  TEXT NOT NULL DEFAULT '',
  sent    BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (org, id)
);`)
		if err != nil {
			return err
		}
		// A file from before the number was blinded has the table without the column.
		if have, err := d.HasColumn(ctx, tx, "tel_numbers", "e164_blind"); err != nil {
			return err
		} else if !have {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE tel_numbers ADD COLUMN e164_blind TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
		// And holds the numbers in plaintext, which are blinded here, before the seal below
		// destroys the plaintext they are made from. hanzo-sql tables are born sealed.
		if !d.Postgres() {
			if err := s.blindNumbers(ctx, tx); err != nil {
				return err
			}
		}
		// A number is held by one org at a time; the unique index is what makes a second
		// org's attempt to record the same number fail rather than shadow the first. It
		// is over the blind index: the sealed number is different every time it is sealed.
		for _, q := range []string{
			`DROP INDEX IF EXISTS tel_numbers_e164`,
			`CREATE UNIQUE INDEX IF NOT EXISTS tel_numbers_blind ON tel_numbers (e164_blind)`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	// A file from before the columns were sealed holds the numbers, calls and texts in
	// plaintext. Seal them in place, before any read, so the store never reads a value it
	// did not write and cmd/sqlmove copies ciphertext. hanzo-sql tables are born sealed,
	// and Upgrade leaves them alone. It is a walk in small transactions of its own, after
	// the schema has committed.
	for _, t := range []struct {
		table       string
		key, sealed []string
	}{
		{"tel_numbers", numberKey, numberSealed},
		{"tel_calls", callKey, callSealed},
		{"tel_messages", messageKey, messageSealed},
	} {
		if _, err := s.fields.Upgrade(context.Background(), s.db, t.table, t.key, t.sealed); err != nil {
			return err
		}
	}
	return nil
}

// blindNumbers gives every number a file holds in plaintext its blind index, before
// Upgrade seals the number: afterwards the plaintext is gone.
func (s *Store) blindNumbers(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT org, id, e164 FROM tel_numbers WHERE e164_blind = '' AND e164 <> '' AND substr(e164, 1, 5) <> 'seal:'`)
	if err != nil {
		return err
	}
	type held struct{ org, id, e164 string }
	var found []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.org, &h.id, &h.e164); err != nil {
			_ = rows.Close()
			return err
		}
		found = append(found, h)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, h := range found {
		if _, err := tx.ExecContext(ctx, `UPDATE tel_numbers SET e164_blind = $1 WHERE org = $2 AND id = $3`,
			s.fields.Blind("tel_numbers", "e164", h.e164), h.org, h.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) PutNumber(ctx context.Context, n Number) error {
	capable, _ := json.Marshal(n.Capable)
	if err := dialect.Text(n.Org, n.ID, n.E164, n.Country, n.Type, string(capable), n.Currency); err != nil {
		return fmt.Errorf("put number: %w", err)
	}
	r := s.fields.Row("tel_numbers", n.Org, n.ID)
	e164 := r.Seal("e164", n.E164)
	if err := r.Err(); err != nil {
		return fmt.Errorf("put number: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tel_numbers (org,id,e164,e164_blind,country,type,capable,monthly,currency,bought)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		 ON CONFLICT(org,id) DO UPDATE SET e164=excluded.e164, e164_blind=excluded.e164_blind, country=excluded.country,
		   type=excluded.type, capable=excluded.capable, monthly=excluded.monthly,
		   currency=excluded.currency`,
		n.Org, n.ID, e164, s.fields.Blind("tel_numbers", "e164", n.E164), n.Country, n.Type, string(capable), n.Monthly, n.Currency, time.Now().Unix())
	return err
}

func (s *Store) scanNumber(rows *sql.Rows) (Number, error) {
	var n Number
	var capable string
	if err := rows.Scan(&n.Org, &n.ID, &n.E164, &n.Country, &n.Type, &capable, &n.Monthly, &n.Currency); err != nil {
		return Number{}, err
	}
	r := s.fields.Row("tel_numbers", n.Org, n.ID)
	n.E164 = r.Open("e164", n.E164)
	if err := r.Err(); err != nil {
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
		`SELECT `+numberCols+` FROM tel_numbers WHERE org=$1 ORDER BY bought DESC`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Number{}
	for rows.Next() {
		n, err := s.scanNumber(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) Number(ctx context.Context, org, id string) (Number, error) {
	if dialect.Text(org, id) != nil {
		return Number{}, nil // nothing is kept under text the store refuses
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+numberCols+` FROM tel_numbers WHERE org=$1 AND id=$2`, org, id)
	if err != nil {
		return Number{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Number{}, rows.Err()
	}
	return s.scanNumber(rows)
}

// NumberByE164 answers "is this org allowed to send as this number". It is the
// check that keeps one tenant from putting another's caller ID on a call.
func (s *Store) NumberByE164(ctx context.Context, org, e164 string) (Number, error) {
	if dialect.Text(org, e164) != nil {
		return Number{}, nil // nothing is kept under text the store refuses
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+numberCols+` FROM tel_numbers WHERE org=$1 AND e164_blind=$2`, org, s.fields.Blind("tel_numbers", "e164", strings.TrimSpace(e164)))
	if err != nil {
		return Number{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Number{}, rows.Err()
	}
	return s.scanNumber(rows)
}

func (s *Store) DeleteNumber(ctx context.Context, org, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tel_numbers WHERE org=$1 AND id=$2`, org, id)
	return err
}

func (s *Store) PutCall(ctx context.Context, c Call) error {
	if err := dialect.Text(c.Org, c.ID, c.From, c.To, c.Status, c.Agent); err != nil {
		return fmt.Errorf("put call: %w", err)
	}
	r := s.fields.Row("tel_calls", c.Org, c.ID)
	from, to := r.Seal("from_no", c.From), r.Seal("to_no", c.To)
	if err := r.Err(); err != nil {
		return fmt.Errorf("put call: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tel_calls (org,id,from_no,to_no,status,agent,started) VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT(org,id) DO UPDATE SET status=excluded.status`,
		c.Org, c.ID, from, to, c.Status, c.Agent, time.Now().Unix())
	return err
}

func (s *Store) Calls(ctx context.Context, org string) ([]Call, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org,id,from_no,to_no,status,agent FROM tel_calls WHERE org=$1 ORDER BY started DESC LIMIT 500`, org)
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
		if err := s.openCall(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Call(ctx context.Context, org, id string) (Call, error) {
	if dialect.Text(org, id) != nil {
		return Call{}, nil // nothing is kept under text the store refuses
	}
	var c Call
	err := s.db.QueryRowContext(ctx,
		`SELECT org,id,from_no,to_no,status,agent FROM tel_calls WHERE org=$1 AND id=$2`, org, id).
		Scan(&c.Org, &c.ID, &c.From, &c.To, &c.Status, &c.Agent)
	if err == sql.ErrNoRows {
		return Call{}, nil
	}
	if err != nil {
		return c, err
	}
	return c, s.openCall(&c)
}

// openCall opens a call's sealed numbers. The row key is read off the row itself, so a
// ciphertext carried to another call or tenant fails here.
func (s *Store) openCall(c *Call) error {
	r := s.fields.Row("tel_calls", c.Org, c.ID)
	c.From, c.To = r.Open("from_no", c.From), r.Open("to_no", c.To)
	return r.Err()
}

func (s *Store) PutMessage(ctx context.Context, m SMS) error {
	if err := dialect.Text(m.Org, m.ID, m.From, m.To, m.Text, m.Status); err != nil {
		return fmt.Errorf("put message: %w", err)
	}
	r := s.fields.Row("tel_messages", m.Org, m.ID)
	from, to, body := r.Seal("from_no", m.From), r.Seal("to_no", m.To), r.Seal("body", m.Text)
	if err := r.Err(); err != nil {
		return fmt.Errorf("put message: %w", err)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tel_messages (org,id,from_no,to_no,body,status,sent) VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT(org,id) DO UPDATE SET status=excluded.status`,
		m.Org, m.ID, from, to, body, m.Status, time.Now().Unix())
	return err
}

func (s *Store) Messages(ctx context.Context, org string) ([]SMS, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT org,id,from_no,to_no,body,status FROM tel_messages WHERE org=$1 ORDER BY sent DESC LIMIT 500`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SMS{}
	for rows.Next() {
		var m SMS
		if err := rows.Scan(&m.Org, &m.ID, &m.From, &m.To, &m.Text, &m.Status); err != nil {
			return nil, err
		}
		r := s.fields.Row("tel_messages", m.Org, m.ID)
		m.From, m.To, m.Text = r.Open("from_no", m.From), r.Open("to_no", m.To), r.Open("body", m.Text)
		if err := r.Err(); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Counts(ctx context.Context, org string) (numbers, calls, messages int, err error) {
	q := func(table string) (int, error) {
		var n int
		e := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE org=$1`, org).Scan(&n)
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
