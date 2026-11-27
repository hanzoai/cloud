package tax

// store.go — ONE encrypted SQLite file per org, opened only for an org that uses
// tax, and every fact in exactly one of them.
//
// A payment between two companies is one event with two sides, and each side of
// it that is a DECISION belongs to whoever decides it:
//
//	profile   this org's own W-9 or W-8 facts; each number sealed under this org's key
//	payees    this org as PAYER: who it asked for a W-9, and what it pays them for
//	shares    this org as PAYEE: who asked for its W-9, and what it answered
//	forms     this org as PAYER: the 1099s it prepared, with its copy of the TINs
//	received  this org as PAYEE: the Copy B statements furnished to it
//	filings   this org as PAYER: what it exported for the IRS, and what came back
//
// So a grant lives in the payee's file (the payee decides) and the category lives
// in the payer's (the payer knows what it bought); a furnished statement is a
// COPY in the payee's file, which is what furnishing a statement is. No table
// here holds a row another org's decision could change.
//
// IMMUTABILITY IS THE SCHEMA'S, not the handler's. A form whose statement has left
// the payer — furnished, owed on paper, or void — cannot be updated, and no form
// and no received statement can be deleted: triggers refuse the write, so a bug in
// a handler cannot rewrite a return that was furnished. A correction is a NEW form
// that names the one it supersedes.
//
// EVERY COLUMN THAT HOLDS A PERSON OR A NUMBER IS A SEALED VALUE (internal/org, field.go),
// on both engines, bound to this org's namespace and to the row it sits in:
//
//	profile.data, .tin, .ftin       this org's own W-9 or W-8 facts, and its TINs
//	payees.data, shares.data        who it asked and what it answered
//	forms.content, .payer_tin, .payee_tin, received.content, .payer_tin
//	                                the statements, with their TINs
//	filings.data                    what was exported for the IRS
//
// The store seals what it writes and opens what it reads, so a row in memory is plain and
// a row on disk is not. A TIN no longer travels sealed from one file to another: the payee's
// is opened by the payee's store and sealed by the payer's, each under its own org's key.
// Left plain: ids, years, kinds, statuses, categories, who a share is for, and the times.
//
// A column a trigger guards (a furnished form's content and TINs) is written back as the
// bytes it already holds whenever its plaintext has not changed, because a fresh seal of
// the same value is different bytes and the trigger compares bytes.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
)

var errNotFound = errors.New("tax: not found")

// subsystem names the store: the database and the sealed columns' keys are made from it,
// so a column's key and its schema cannot name two things.
const subsystem = "tax"

// legacyTINSubsystem is what a TIN was concealed under (org.Cipher.Conceal) before the
// field format. Only the migration reads it.
const legacyTINSubsystem = "tax.tin"

// errNoMaster is every open of a store on a process with no data-plane master: a TIN is
// never written in the clear because a key was missing.
var errNoMaster = errors.New("tax: no data-plane master on this process, so a store is neither sealed nor opened")

type store struct {
	db     *sql.DB
	d      dialect.Dialect
	ns     namespace.Namespace
	fields *tenure.Fields
	cipher *tenure.Cipher // opens the TINs a file concealed before the field format; only the migration reads it
}

// openStore is the open func cloud.NewOrgStoreAt calls once per org: master keys the
// sealed columns, and the namespace is the org they are bound to. With no usable master
// the store does not open.
func openStore(master []byte) func(namespace.Namespace, *sql.DB) (*store, error) {
	return func(ns namespace.Namespace, db *sql.DB) (*store, error) {
		c, err := tenure.NewCipher(master)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%w: %v", errNoMaster, err)
		}
		fields, err := c.Fields(ns, subsystem)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("tax: %w", err)
		}
		d := dialect.Of(db)
		s := &store{db: db, d: d, ns: ns, fields: fields, cipher: c}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := d.Migrate(ctx, db, func(tx *sql.Tx) error { return s.migrate(ctx, tx) }); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("tax migrate: %w", err)
		}
		return s, nil
	}
}

// seal is the text to store for column of the row (table, id) whose plaintext is plain. A
// column the caller did not change keeps the bytes it holds (raw, as read): a fresh seal of
// one value is different bytes, and the triggers that keep a final record final compare
// bytes. An empty value is stored empty.
func (s *store) seal(table, id, column, raw, plain string) (string, error) {
	if raw != "" {
		probe := s.fields.Row(table, id)
		if probe.Open(column, raw) == plain && probe.Err() == nil {
			return raw, nil
		}
	}
	r := s.fields.Row(table, id)
	out := r.Seal(column, plain)
	return out, r.Err()
}

// open is the plaintext of column of the row (table, id) as it was stored.
func (s *store) open(table, id, column, stored string) (string, error) {
	r := s.fields.Row(table, id)
	out := r.Open(column, stored)
	return out, r.Err()
}

// sealTIN and openTIN are seal and open for a U.S. TIN held in a BLOB column.
func (s *store) sealTIN(table, id, column string, raw []byte, t TIN) ([]byte, error) {
	out, err := s.seal(table, id, column, string(raw), t.pack())
	if err != nil || out == "" {
		return nil, err
	}
	return []byte(out), nil
}

func (s *store) openTIN(table, id, column string, stored []byte) (TIN, error) {
	plain, err := s.open(table, id, column, string(stored))
	if err != nil {
		return TIN{}, err
	}
	return unpackTIN(plain)
}

// sealFTIN and openFTIN are the same for a W-8's foreign TIN.
func (s *store) sealFTIN(table, id, column string, raw []byte, f FTIN) ([]byte, error) {
	out, err := s.seal(table, id, column, string(raw), f.pack())
	if err != nil || out == "" {
		return nil, err
	}
	return []byte(out), nil
}

func (s *store) openFTIN(table, id, column string, stored []byte) (FTIN, error) {
	plain, err := s.open(table, id, column, string(stored))
	if err != nil {
		return FTIN{}, err
	}
	return unpackFTIN(plain)
}

// migrate creates the tables and carries a file an earlier build wrote forward, on the
// transaction Dialect.Migrate holds.
//
// The ordered tables are listed in the order they were written. A file ordered them by
// rowid; an order the store keeps itself is a seq column, numbered as a row is written
// (dialect.NextSeq), and a file written before it has its rows numbered once. The
// triggers that keep a furnished form and a received statement from being rewritten are
// lifted for that one numbering and put back before the transaction ends.
func (s *store) migrate(ctx context.Context, tx *sql.Tx) error {
	d := s.d
	if _, err := tx.ExecContext(ctx, schema(d)); err != nil {
		return err
	}
	// A file written before a W-8's foreign TIN was kept has no column for it; a fresh one
	// has it from the schema and this does nothing.
	have, err := d.HasColumn(ctx, tx, "profile", "ftin")
	if err != nil {
		return err
	}
	if !have {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE profile ADD COLUMN ftin `+d.Blob()); err != nil {
			return err
		}
	}
	if !d.Postgres() {
		for _, trigger := range []string{"forms_final", "received_final"} {
			if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+trigger); err != nil {
				return err
			}
		}
	}
	for _, table := range []string{"shares", "forms", "received", "filings"} {
		if err := d.Sequence(ctx, tx, table); err != nil {
			return err
		}
	}
	// A SQLite file from before the columns were fields holds them in plaintext, and its
	// TINs concealed under the org's key. Seal them in place, before any read, while the
	// triggers are down, so the store never reads a value it did not write and
	// cmd/sqlmove copies ciphertext. hanzo-sql tables are born sealed.
	if !d.Postgres() {
		if err := s.sealLegacy(ctx, tx); err != nil {
			return err
		}
	}
	// A statement that has left the payer is final: the one change it may take is an
	// owed paper statement being recorded as mailed, which moves its status and
	// touches nothing it says.
	if err := d.Refuse(ctx, tx, "forms_final", "forms", "UPDATE",
		`OLD.status IN ('furnished', 'void')
  OR (OLD.status = 'owed' AND (NEW.status <> 'furnished'
      OR NEW.content IS DISTINCT FROM OLD.content OR NEW.payer_tin IS DISTINCT FROM OLD.payer_tin
      OR NEW.payee_tin IS DISTINCT FROM OLD.payee_tin OR NEW.supersedes IS DISTINCT FROM OLD.supersedes))`,
		"tax: a furnished form is immutable; correct it instead"); err != nil {
		return err
	}
	if err := d.Refuse(ctx, tx, "forms_kept", "forms", "DELETE", "", "tax: a form is never deleted; void or correct it"); err != nil {
		return err
	}
	if err := d.Refuse(ctx, tx, "received_final", "received", "UPDATE", "", "tax: a furnished statement is immutable"); err != nil {
		return err
	}
	return d.Refuse(ctx, tx, "received_kept", "received", "DELETE", "", "tax: a furnished statement is never deleted")
}

func (s *store) Close() error { return s.db.Close() }

func schema(d dialect.Dialect) string {
	return `
CREATE TABLE IF NOT EXISTS profile (
  id         BIGINT  PRIMARY KEY CHECK (id = 1),
  version    BIGINT  NOT NULL,
  data       TEXT NOT NULL,
  tin        ` + d.Blob() + `,
  ftin       ` + d.Blob() + `,
  updated_at BIGINT  NOT NULL
);

CREATE TABLE IF NOT EXISTS payees (
  payee      TEXT PRIMARY KEY,
  share      TEXT NOT NULL,
  category   TEXT NOT NULL,
  data       TEXT NOT NULL DEFAULT '{}',
  created_at BIGINT  NOT NULL,
  updated_at BIGINT  NOT NULL
);

CREATE TABLE IF NOT EXISTS shares (
  id         TEXT PRIMARY KEY,
  payer      TEXT NOT NULL UNIQUE,
  status     TEXT NOT NULL,
  data       TEXT NOT NULL DEFAULT '{}',
  created_at BIGINT  NOT NULL,
  updated_at BIGINT  NOT NULL,
  seq        BIGINT  NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS forms (
  id         TEXT PRIMARY KEY,
  payee      TEXT NOT NULL,
  year       BIGINT  NOT NULL,
  kind       TEXT NOT NULL,
  status     TEXT NOT NULL,
  supersedes TEXT NOT NULL DEFAULT '',
  content    TEXT NOT NULL,
  payer_tin  ` + d.Blob() + `,
  payee_tin  ` + d.Blob() + `,
  created_at BIGINT  NOT NULL,
  updated_at BIGINT  NOT NULL,
  seq        BIGINT  NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_forms_year ON forms(year, kind, payee);
CREATE INDEX IF NOT EXISTS ix_forms_supersedes ON forms(supersedes);

-- One live return per payee, kind and year, and one live correction per form.
-- Preparing reads the forms and then writes, and so does correcting; two of either
-- racing each other would each see none and each write one — two 1099s for one
-- payee's year, both reviewable, furnishable and exportable. The index refuses the
-- second; a voided form leaves it, so voiding is how a draft is replaced.
CREATE UNIQUE INDEX IF NOT EXISTS ux_forms_original ON forms(payee, year, kind)
  WHERE supersedes = '' AND status <> 'void';
CREATE UNIQUE INDEX IF NOT EXISTS ux_forms_correction ON forms(supersedes)
  WHERE supersedes <> '' AND status <> 'void';

CREATE TABLE IF NOT EXISTS received (
  id           TEXT PRIMARY KEY,
  payer        TEXT NOT NULL,
  year         BIGINT  NOT NULL,
  kind         TEXT NOT NULL,
  supersedes   TEXT NOT NULL DEFAULT '',
  content      TEXT NOT NULL,
  payer_tin    ` + d.Blob() + `,
  furnished_at BIGINT  NOT NULL,
  seq          BIGINT  NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ix_received_year ON received(year, payer);

CREATE TABLE IF NOT EXISTS filings (
  id         TEXT PRIMARY KEY,
  year       BIGINT  NOT NULL,
  kind       TEXT NOT NULL,
  status     TEXT NOT NULL,
  data       TEXT NOT NULL,
  created_at BIGINT  NOT NULL,
  updated_at BIGINT  NOT NULL,
  seq        BIGINT  NOT NULL DEFAULT 0
);
`
}

// sealLegacy seals what an older SQLite file holds in plaintext (UpgradeTx, while the
// triggers are down) and moves its TINs, concealed under the org's key with no row in the
// binding, onto the field format.
func (s *store) sealLegacy(ctx context.Context, tx *sql.Tx) error {
	for _, t := range []struct {
		table       string
		key, sealed []string
	}{
		{"profile", []string{"id"}, []string{"data"}},
		{"payees", []string{"payee"}, []string{"data"}},
		{"shares", []string{"id"}, []string{"data"}},
		{"forms", []string{"id"}, []string{"content"}},
		{"received", []string{"id"}, []string{"content"}},
		{"filings", []string{"id"}, []string{"data"}},
	} {
		if _, err := s.fields.UpgradeTx(ctx, tx, t.table, t.key, t.sealed); err != nil {
			return err
		}
	}
	for _, t := range []struct {
		table, key string
		columns    []string
	}{
		{"profile", "id", []string{"tin", "ftin"}},
		{"forms", "id", []string{"payer_tin", "payee_tin"}},
		{"received", "id", []string{"payer_tin"}},
	} {
		if err := s.sealTINs(ctx, tx, t.table, t.key, t.columns); err != nil {
			return err
		}
	}
	return nil
}

// sealTINs moves the TINs of one table off org.Cipher.Conceal: each is opened under the
// namespace and "tax.tin", then sealed as a field bound to its row and column. A value that
// is a field already is left, so a second open writes nothing.
func (s *store) sealTINs(ctx context.Context, tx *sql.Tx, table, key string, columns []string) error {
	rows, err := tx.QueryContext(ctx, `SELECT CAST(`+key+` AS TEXT), `+strings.Join(columns, ", ")+` FROM `+table)
	if err != nil {
		return fmt.Errorf("tax: read %s: %w", table, err)
	}
	type held struct {
		id   string
		blob [][]byte
	}
	var found []held
	for rows.Next() {
		h := held{blob: make([][]byte, len(columns))}
		dest := []any{&h.id}
		for i := range h.blob {
			dest = append(dest, &h.blob[i])
		}
		if err := rows.Scan(dest...); err != nil {
			_ = rows.Close()
			return fmt.Errorf("tax: read %s: %w", table, err)
		}
		found = append(found, h)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return fmt.Errorf("tax: read %s: %w", table, err)
	}
	for _, h := range found {
		r := s.fields.Row(table, h.id)
		var sets []string
		var args []any
		for i, c := range columns {
			blob := h.blob[i]
			if len(blob) == 0 || strings.HasPrefix(string(blob), "seal:") {
				continue
			}
			plain, err := s.cipher.Open(s.ns, legacyTINSubsystem, blob)
			if err != nil {
				return fmt.Errorf("tax: %s.%s of %s did not open under its owner's key: %w", table, c, h.id, err)
			}
			args = append(args, []byte(r.Seal(c, string(plain))))
			sets = append(sets, fmt.Sprintf("%s = $%d", c, len(args)))
		}
		if err := r.Err(); err != nil {
			return err
		}
		if len(sets) == 0 {
			continue
		}
		args = append(args, h.id)
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET %s WHERE %s = $%d", table, strings.Join(sets, ", "), key, len(args)), args...); err != nil {
			return fmt.Errorf("tax: write %s: %w", table, err)
		}
	}
	return nil
}

// ---- profile ---------------------------------------------------------------

type profileRow struct {
	version int
	data    string
	tin     TIN  // the U.S. TIN
	ftin    FTIN // a W-8's foreign TIN
	updated int64

	// raw, rawTIN and rawFTIN are the columns as they were read: a write that does not
	// change a value stores the bytes it already holds, and rewriteProfile compares the
	// stored record against what it read.
	raw             string
	rawTIN, rawFTIN []byte
}

// profileID is the one profile row's key; its id column is the constant 1.
const profileID = "1"

func (s *store) profile(ctx context.Context) (*profileRow, error) {
	var r profileRow
	err := s.db.QueryRowContext(ctx, `SELECT version, data, tin, ftin, updated_at FROM profile WHERE id = 1`).
		Scan(&r.version, &r.raw, &r.rawTIN, &r.rawFTIN, &r.updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tax: read profile: %w", err)
	}
	if r.data, err = s.open("profile", profileID, "data", r.raw); err != nil {
		return nil, fmt.Errorf("tax: read profile: %w", err)
	}
	if r.tin, err = s.openTIN("profile", profileID, "tin", r.rawTIN); err != nil {
		return nil, fmt.Errorf("tax: read profile: %w", err)
	}
	if r.ftin, err = s.openFTIN("profile", profileID, "ftin", r.rawFTIN); err != nil {
		return nil, fmt.Errorf("tax: read profile: %w", err)
	}
	return &r, nil
}

func (s *store) putProfile(ctx context.Context, r profileRow) error {
	if err := dialect.Text(r.data); err != nil {
		return fmt.Errorf("tax: write profile: %w", err)
	}
	data, err := s.seal("profile", profileID, "data", r.raw, r.data)
	if err != nil {
		return fmt.Errorf("tax: write profile: %w", err)
	}
	tin, err := s.sealTIN("profile", profileID, "tin", r.rawTIN, r.tin)
	if err != nil {
		return fmt.Errorf("tax: write profile: %w", err)
	}
	ftin, err := s.sealFTIN("profile", profileID, "ftin", r.rawFTIN, r.ftin)
	if err != nil {
		return fmt.Errorf("tax: write profile: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO profile (id, version, data, tin, ftin, updated_at) VALUES (1, $1, $2, $3, $4, $5)
		 ON CONFLICT(id) DO UPDATE SET version = excluded.version, data = excluded.data,
		   tin = excluded.tin, ftin = excluded.ftin, updated_at = excluded.updated_at`,
		r.version, data, tin, ftin, r.updated)
	if err != nil {
		return fmt.Errorf("tax: write profile: %w", err)
	}
	return nil
}

// rewriteProfile replaces the stored record at its version and TIN, only if the
// record is still the one the caller read (r.raw); errMoved when it is not.
func (s *store) rewriteProfile(ctx context.Context, r profileRow) error {
	if err := dialect.Text(r.data); err != nil {
		return fmt.Errorf("tax: write profile: %w", err)
	}
	data, err := s.seal("profile", profileID, "data", "", r.data)
	if err != nil {
		return fmt.Errorf("tax: write profile: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE profile SET data = $1, updated_at = $2 WHERE id = 1 AND version = $3 AND data = $4`,
		data, r.updated, r.version, r.raw)
	if err != nil {
		return fmt.Errorf("tax: write profile: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errMoved
	}
	return nil
}

// ---- payees (this org as payer) --------------------------------------------

type payeeRow struct {
	payee    string
	share    string
	category Category
	data     string
	created  int64
	updated  int64
}

const payeeCols = `payee, share, category, data, created_at, updated_at`

func (s *store) scanPayee(sc interface{ Scan(...any) error }) (*payeeRow, error) {
	var r payeeRow
	if err := sc.Scan(&r.payee, &r.share, &r.category, &r.data, &r.created, &r.updated); err != nil {
		return nil, err
	}
	var err error
	if r.data, err = s.open("payees", r.payee, "data", r.data); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *store) payee(ctx context.Context, payee string) (*payeeRow, error) {
	r, err := s.scanPayee(s.db.QueryRowContext(ctx, `SELECT `+payeeCols+` FROM payees WHERE payee = $1`, payee))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tax: read payee: %w", err)
	}
	return r, nil
}

func (s *store) payeeByShare(ctx context.Context, share string) (*payeeRow, error) {
	r, err := s.scanPayee(s.db.QueryRowContext(ctx, `SELECT `+payeeCols+` FROM payees WHERE share = $1`, share))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tax: read payee: %w", err)
	}
	return r, nil
}

func (s *store) payees(ctx context.Context) ([]payeeRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+payeeCols+` FROM payees ORDER BY payee`)
	if err != nil {
		return nil, fmt.Errorf("tax: list payees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []payeeRow
	for rows.Next() {
		r, err := s.scanPayee(rows)
		if err != nil {
			return nil, fmt.Errorf("tax: scan payee: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *store) putPayee(ctx context.Context, r payeeRow) error {
	if err := dialect.Text(r.payee, r.share, string(r.category), r.data); err != nil {
		return fmt.Errorf("tax: write payee: %w", err)
	}
	data, err := s.seal("payees", r.payee, "data", "", r.data)
	if err != nil {
		return fmt.Errorf("tax: write payee: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO payees (`+payeeCols+`) VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT(payee) DO UPDATE SET share = excluded.share, category = excluded.category,
		   data = excluded.data, updated_at = excluded.updated_at`,
		r.payee, r.share, r.category, data, r.created, r.updated)
	if err != nil {
		return fmt.Errorf("tax: write payee: %w", err)
	}
	return nil
}

// ---- shares (this org as payee) --------------------------------------------

type shareRow struct {
	id      string
	payer   string
	status  string
	data    string
	created int64
	updated int64
}

const shareCols = `id, payer, status, data, created_at, updated_at`

func (s *store) scanShare(sc interface{ Scan(...any) error }) (*shareRow, error) {
	var r shareRow
	if err := sc.Scan(&r.id, &r.payer, &r.status, &r.data, &r.created, &r.updated); err != nil {
		return nil, err
	}
	var err error
	if r.data, err = s.open("shares", r.id, "data", r.data); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *store) share(ctx context.Context, id string) (*shareRow, error) {
	return s.shareWhere(ctx, `id = $1`, id)
}

func (s *store) shareFor(ctx context.Context, payer string) (*shareRow, error) {
	return s.shareWhere(ctx, `payer = $1`, payer)
}

func (s *store) shareWhere(ctx context.Context, where string, arg string) (*shareRow, error) {
	r, err := s.scanShare(s.db.QueryRowContext(ctx, `SELECT `+shareCols+` FROM shares WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tax: read share: %w", err)
	}
	return r, nil
}

func (s *store) shares(ctx context.Context) ([]shareRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+shareCols+` FROM shares ORDER BY created_at, seq, id`)
	if err != nil {
		return nil, fmt.Errorf("tax: list shares: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []shareRow
	for rows.Next() {
		r, err := s.scanShare(rows)
		if err != nil {
			return nil, fmt.Errorf("tax: scan share: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *store) putShare(ctx context.Context, r shareRow) error {
	if err := dialect.Text(r.id, r.payer, r.status, r.data); err != nil {
		return fmt.Errorf("tax: write share: %w", err)
	}
	data, err := s.seal("shares", r.id, "data", "", r.data)
	if err != nil {
		return fmt.Errorf("tax: write share: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO shares (`+shareCols+`, seq) VALUES ($1, $2, $3, $4, $5, $6, `+dialect.NextSeq("shares")+`)
		 ON CONFLICT(id) DO UPDATE SET status = excluded.status, data = excluded.data,
		   updated_at = excluded.updated_at`,
		r.id, r.payer, r.status, data, r.created, r.updated)
	if err != nil {
		return fmt.Errorf("tax: write share: %w", err)
	}
	return nil
}

// ---- forms (this org as payer) ---------------------------------------------

type formRow struct {
	id         string
	payee      string
	year       int
	kind       Kind
	status     Status
	supersedes string
	content    string
	payerTIN   TIN
	payeeTIN   TIN
	created    int64
	updated    int64

	// The columns as they were read: a status move that changes none of them stores the
	// bytes it already holds, which is what the trigger on a final form compares.
	rawContent               string
	rawPayerTIN, rawPayeeTIN []byte
}

const formCols = `id, payee, year, kind, status, supersedes, content, payer_tin, payee_tin, created_at, updated_at`

func (s *store) scanForm(sc interface{ Scan(...any) error }) (*formRow, error) {
	var r formRow
	err := sc.Scan(&r.id, &r.payee, &r.year, &r.kind, &r.status, &r.supersedes, &r.rawContent,
		&r.rawPayerTIN, &r.rawPayeeTIN, &r.created, &r.updated)
	if err != nil {
		return nil, err
	}
	if r.content, err = s.open("forms", r.id, "content", r.rawContent); err != nil {
		return nil, err
	}
	if r.payerTIN, err = s.openTIN("forms", r.id, "payer_tin", r.rawPayerTIN); err != nil {
		return nil, err
	}
	if r.payeeTIN, err = s.openTIN("forms", r.id, "payee_tin", r.rawPayeeTIN); err != nil {
		return nil, err
	}
	return &r, nil
}

// sealForm is the three sealed columns of a form, ready to store.
func (s *store) sealForm(r formRow) (content string, payer, payee []byte, err error) {
	if content, err = s.seal("forms", r.id, "content", r.rawContent, r.content); err != nil {
		return
	}
	if payer, err = s.sealTIN("forms", r.id, "payer_tin", r.rawPayerTIN, r.payerTIN); err != nil {
		return
	}
	payee, err = s.sealTIN("forms", r.id, "payee_tin", r.rawPayeeTIN, r.payeeTIN)
	return
}

func (s *store) form(ctx context.Context, id string) (*formRow, error) {
	r, err := s.scanForm(s.db.QueryRowContext(ctx, `SELECT `+formCols+` FROM forms WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tax: read form: %w", err)
	}
	return r, nil
}

// forms lists the forms for a year, oldest first; year 0 is every year.
func (s *store) forms(ctx context.Context, year int) ([]formRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+formCols+` FROM forms WHERE ($1 = 0 OR year = $1) ORDER BY year, created_at, seq, id`, year)
	if err != nil {
		return nil, fmt.Errorf("tax: list forms: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []formRow
	for rows.Next() {
		r, err := s.scanForm(rows)
		if err != nil {
			return nil, fmt.Errorf("tax: scan form: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// successor is the form that supersedes id, when one does.
func (s *store) successor(ctx context.Context, id string) (string, error) {
	var next string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM forms WHERE supersedes = $1 AND status <> 'void' ORDER BY created_at DESC, seq DESC, id DESC LIMIT 1`, id).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return next, err
}

// insertForm writes a new form; errMoved when another live form already holds its
// place (ux_forms_original, ux_forms_correction).
func (s *store) insertForm(ctx context.Context, r formRow) error {
	if err := dialect.Text(r.id, r.payee, string(r.kind), string(r.status), r.supersedes, r.content); err != nil {
		return fmt.Errorf("tax: write form: %w", err)
	}
	content, payer, payee, err := s.sealForm(r)
	if err != nil {
		return fmt.Errorf("tax: write form: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO forms (`+formCols+`, seq) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, `+dialect.NextSeq("forms")+`)`,
		r.id, r.payee, r.year, r.kind, r.status, r.supersedes, content, payer, payee, r.created, r.updated)
	if dialect.Unique(err) {
		return errMoved
	}
	if err != nil {
		return fmt.Errorf("tax: write form: %w", err)
	}
	return nil
}

// updateForm moves a form from one status to the next. The WHERE names the status
// the caller read, so two writers racing on one form cannot both move it; the
// triggers refuse any write to a final form whatever the caller read.
func (s *store) updateForm(ctx context.Context, r formRow, from Status) error {
	content, payer, payee, err := s.sealForm(r)
	if err != nil {
		return fmt.Errorf("tax: update form: %w", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE forms SET status = $1, content = $2, payer_tin = $3, payee_tin = $4, updated_at = $5
		 WHERE id = $6 AND status = $7`,
		r.status, content, payer, payee, r.updated, r.id, from)
	if err != nil {
		return fmt.Errorf("tax: update form: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errMoved
	}
	return nil
}

// errMoved is a row that changed between the read and the write.
var errMoved = errors.New("tax: the row changed since it was read")

// ---- received (this org as payee) ------------------------------------------

type receivedRow struct {
	id         string
	payer      string
	year       int
	kind       Kind
	supersedes string
	content    string
	payerTIN   TIN
	furnished  int64
}

const receivedCols = `id, payer, year, kind, supersedes, content, payer_tin, furnished_at`

func (s *store) scanReceived(sc interface{ Scan(...any) error }) (*receivedRow, error) {
	var r receivedRow
	var tin []byte
	if err := sc.Scan(&r.id, &r.payer, &r.year, &r.kind, &r.supersedes, &r.content, &tin, &r.furnished); err != nil {
		return nil, err
	}
	var err error
	if r.content, err = s.open("received", r.id, "content", r.content); err != nil {
		return nil, err
	}
	if r.payerTIN, err = s.openTIN("received", r.id, "payer_tin", tin); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *store) receive(ctx context.Context, r receivedRow) error {
	if err := dialect.Text(r.id, r.payer, string(r.kind), r.supersedes, r.content); err != nil {
		return fmt.Errorf("tax: deliver statement: %w", err)
	}
	content, err := s.seal("received", r.id, "content", "", r.content)
	if err != nil {
		return fmt.Errorf("tax: deliver statement: %w", err)
	}
	tin, err := s.sealTIN("received", r.id, "payer_tin", nil, r.payerTIN)
	if err != nil {
		return fmt.Errorf("tax: deliver statement: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO received (`+receivedCols+`, seq) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, `+dialect.NextSeq("received")+`)`,
		r.id, r.payer, r.year, r.kind, r.supersedes, content, tin, r.furnished)
	if err != nil {
		return fmt.Errorf("tax: deliver statement: %w", err)
	}
	return nil
}

func (s *store) statement(ctx context.Context, id string) (*receivedRow, error) {
	r, err := s.scanReceived(s.db.QueryRowContext(ctx, `SELECT `+receivedCols+` FROM received WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tax: read statement: %w", err)
	}
	return r, nil
}

func (s *store) statements(ctx context.Context, year int) ([]receivedRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+receivedCols+` FROM received WHERE ($1 = 0 OR year = $1) ORDER BY year, furnished_at, seq, id`, year)
	if err != nil {
		return nil, fmt.Errorf("tax: list statements: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []receivedRow
	for rows.Next() {
		r, err := s.scanReceived(rows)
		if err != nil {
			return nil, fmt.Errorf("tax: scan statement: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ---- filings (this org as payer) -------------------------------------------

type filingRow struct {
	id      string
	year    int
	kind    Kind
	status  string
	data    string
	created int64
	updated int64
}

const filingCols = `id, year, kind, status, data, created_at, updated_at`

func (s *store) scanFiling(sc interface{ Scan(...any) error }) (*filingRow, error) {
	var r filingRow
	if err := sc.Scan(&r.id, &r.year, &r.kind, &r.status, &r.data, &r.created, &r.updated); err != nil {
		return nil, err
	}
	var err error
	if r.data, err = s.open("filings", r.id, "data", r.data); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *store) filing(ctx context.Context, id string) (*filingRow, error) {
	r, err := s.scanFiling(s.db.QueryRowContext(ctx, `SELECT `+filingCols+` FROM filings WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("tax: read filing: %w", err)
	}
	return r, nil
}

func (s *store) filings(ctx context.Context) ([]filingRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+filingCols+` FROM filings ORDER BY created_at, seq, id`)
	if err != nil {
		return nil, fmt.Errorf("tax: list filings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []filingRow
	for rows.Next() {
		r, err := s.scanFiling(rows)
		if err != nil {
			return nil, fmt.Errorf("tax: scan filing: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *store) putFiling(ctx context.Context, r filingRow) error {
	if err := dialect.Text(r.id, string(r.kind), r.status, r.data); err != nil {
		return fmt.Errorf("tax: write filing: %w", err)
	}
	data, err := s.seal("filings", r.id, "data", "", r.data)
	if err != nil {
		return fmt.Errorf("tax: write filing: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO filings (`+filingCols+`, seq) VALUES ($1, $2, $3, $4, $5, $6, $7, `+dialect.NextSeq("filings")+`)
		 ON CONFLICT(id) DO UPDATE SET status = excluded.status, data = excluded.data, updated_at = excluded.updated_at`,
		r.id, r.year, r.kind, r.status, data, r.created, r.updated)
	if err != nil {
		return fmt.Errorf("tax: write filing: %w", err)
	}
	return nil
}
