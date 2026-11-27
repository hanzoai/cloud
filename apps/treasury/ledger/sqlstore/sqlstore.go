package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	// The ONE Hanzo SQLite driver (registers "sqlite" under both build tags) — the
	// Base storage substrate, identical to every other clients/* store.
	_ "github.com/hanzoai/sqlite"

	"github.com/hanzoai/cloud/apps/treasury/ledger"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
)

// Store is the ledger persistence. ONE database holds the whole chart of accounts,
// journal and policy for a deployment (the platform's own books — not per-org, unlike
// a tenant product store). Write transactions are serialized, so the engine's
// balance-guard read-then-write is atomic under load.
type Store struct {
	db *sql.DB

	// fields seals the entry memo: the operator's note, and the reason a clawback or a
	// restore was made, which is a person's words about a person's money.
	fields *tenure.Fields

	// usageKind is the entry kind whose refs are scoped BY PROGRAM (the wallet the
	// money left), and so the kind whose pre-scope rows [Store.backfillUsageProgram]
	// re-keys at open. It is a PARAMETER because the value is the writer's, not this
	// layer's: this is a generic journal, and which of its kinds are wallet-scoped is
	// a fact only the app that writes them knows. Empty means no kind is — the house
	// book's own kinds are all book-scoped — and the repair does not run.
	usageKind string

	// anchoring serializes the anchor's chain transactions on a file, where the one
	// process is the whole deployment ([Store.Anchoring]).
	anchoring sync.Mutex
}

// Open opens (creating + migrating) the ledger database subsystem names for ns,
// under dir.
//
// The namespace is a PARAMETER because these stores do not share one: a customer's
// ledger belongs to that org, the house book belongs to the deployment. It is also
// what keys the file, so an opener cannot name one entity's ledger and unlock it
// with another's key — that pairing is made once, inside cek, from this one value.
// The memo column's key is made from the same pair and master, for the same reason.
//
// usageKind names the one kind whose refs are unique per WALLET rather than per book,
// which is what [Store.backfillUsageProgram] repairs on rows written before that scope
// existed. The opener states it (finance passes its own KindUsage) so this package
// holds no constant belonging to the app whose money it stores; "" opens a store with
// no such kind and no repair.
//
// master is cloud.Deps.MasterKey; with none the ledger does not open, so no memo is
// ever written in the clear because a key was missing.
func Open(ns namespace.Namespace, subsystem, dir, usageKind string, master []byte) (*Store, error) {
	// locus.Use made the handle, so it closes it when the opener fails. The opener
	// must not: it is handed a database somebody else owns, and on the durable plane
	// that owner closes it too — a double close on a failed migration, which is the
	// kind of tidiness that reads as harmless right up until the second closer is
	// doing something else with the handle.
	open := Opener(master, subsystem, usageKind)
	st, err := locus.Use(context.Background(), nil, ns, subsystem, dir, func(db *sql.DB) (*Store, error) {
		return open(ns, db)
	})
	if err != nil {
		return nil, fmt.Errorf("open %s ledger for %s: %w", subsystem, ns, err)
	}
	return st, nil
}

// Opener is [Open] over a database somebody else opened — the migration and the
// repair, with no opinion about where the file is or how it got there. It is the
// func a cloud.OrgStore takes (cloud.NewOrgStoreAt), which hands it the org's
// namespace along with the database.
//
// It exists because the ledger has TWO openers and only one of them may choose a
// path. Open resolves (ns, subsystem, dir) itself, which is right for a lone
// process. A ledger on the durable plane is opened by that plane instead — which
// hydrates the entity's snapshot from the object store, claims the writer lease,
// and only then hands over a handle — so the path, the pool and the key are all
// settled before this package sees anything, and choosing them a second time here
// would open an empty database beside the real one.
//
// The split is the whole of the change: everything a ledger IS lives below this
// line, and everything about WHERE it lives is above it.
// The handle stays the CALLER's on every exit, success or not: this function did
// not open it and does not know who else holds it.
func Opener(master []byte, subsystem, usageKind string) func(namespace.Namespace, *sql.DB) (*Store, error) {
	return func(ns namespace.Namespace, db *sql.DB) (*Store, error) {
		c, err := tenure.NewCipher(master)
		if err != nil {
			return nil, fmt.Errorf("treasury: %w", err)
		}
		fields, err := c.Fields(ns, subsystem)
		if err != nil {
			return nil, fmt.Errorf("treasury: %w", err)
		}
		s := &Store{db: db, usageKind: usageKind, fields: fields}
		if err := s.migrate(); err != nil {
			return nil, err
		}
		return s, nil
	}
}

// entryKey and entrySealed name the sealed column of the journal: the entry's id is its
// primary key, and the ledger is one org's file, so the namespace the fields are keyed
// by is the tenant.
var entryKey, entrySealed = []string{"id"}, []string{"memo"}

// sealMemo is the text to store in an entry's memo column.
func sealMemo(f *tenure.Fields, id, memo string) (string, error) {
	r := f.Row("treasury_entries", id)
	out := r.Seal("memo", memo)
	return out, r.Err()
}

// openMemo is the memo an entry's stored column holds.
func openMemo(f *tenure.Fields, id, stored string) (string, error) {
	r := f.Row("treasury_entries", id)
	out := r.Open("memo", stored)
	return out, r.Err()
}

// migrate brings the database to the current schema, in ONE transaction that holds
// the store's migration lock (two replicas opening one book must not run its DDL at
// once). Everything below goes through tx and nothing through s.db: the transaction
// is the connection that holds the lock, and on a file it is the only connection
// there is.
//
// A SQLite file from before the memo was sealed holds it in plaintext. Once the schema
// has committed, the memos are sealed in place, before any read, so the store never reads
// a value it did not write and cmd/sqlmove copies ciphertext. (hanzo-sql tables are born
// sealed, and Upgrade leaves them alone.) The sealing is a walk in small transactions of its own (org.Fields.Upgrade),
// which is why it is not part of the migration: a book of half a million entries is
// sealed in a few thousand of them, each durable, and an open that is interrupted or
// fails finishes the rest at the next, where one transaction under a minute's limit would
// roll back all it had done and fail again at the same place. Idempotent: a second open
// seals nothing and rewrites nothing.
func (s *Store) migrate() error {
	d := dialect.Of(s.db)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.Migrate(ctx, s.db, func(tx *sql.Tx) error { return s.migrateIn(ctx, d, tx) }); err != nil {
		return err
	}
	if _, err := s.fields.Upgrade(context.Background(), s.db, "treasury_entries", entryKey, entrySealed); err != nil {
		return fmt.Errorf("seal entry memos: %w", err)
	}
	return nil
}

func (s *Store) migrateIn(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	// Target 18-decimal schema — a fresh database gets this directly. Every integer is
	// BIGINT: PostgreSQL's INTEGER is 32 bits, and SQLite gives BIGINT the same
	// 64-bit INTEGER affinity it gives INTEGER, so one word serves both.
	ddl := `
CREATE TABLE IF NOT EXISTS treasury_accounts (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,
  balance    TEXT NOT NULL DEFAULT '0',
  created_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS treasury_entries (
  id         TEXT PRIMARY KEY,
  seq        BIGINT NOT NULL DEFAULT 0,
  kind       TEXT NOT NULL,
  program    TEXT NOT NULL DEFAULT '',
  ref        TEXT NOT NULL,
  memo       TEXT NOT NULL DEFAULT '',
  project    TEXT NOT NULL DEFAULT '',
  service    TEXT NOT NULL DEFAULT '',
  amount     TEXT NOT NULL DEFAULT '0',
  created_at BIGINT NOT NULL,
  UNIQUE(kind, program, ref)
);
CREATE INDEX IF NOT EXISTS ix_treasury_entries_created ON treasury_entries(created_at);

CREATE TABLE IF NOT EXISTS treasury_postings (
  id         ` + d.Identity() + `,
  entry_id   TEXT NOT NULL REFERENCES treasury_entries(id),
  account    TEXT NOT NULL,
  amount     TEXT NOT NULL DEFAULT '0',
  created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_treasury_postings_account ON treasury_postings(account);
CREATE INDEX IF NOT EXISTS ix_treasury_postings_entry   ON treasury_postings(entry_id);

CREATE TABLE IF NOT EXISTS treasury_policy (
  id                BIGINT PRIMARY KEY CHECK (id = 1),
  revenue_share_bps BIGINT NOT NULL DEFAULT 0,
  updated_at        BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS treasury_limit (
  id         BIGINT PRIMARY KEY CHECK (id = 1),
  amount     TEXT NOT NULL DEFAULT '0',
  updated_at BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS treasury_owing (
  id    BIGINT PRIMARY KEY CHECK (id = 1),
  since BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS treasury_anchor (
  id          BIGINT PRIMARY KEY CHECK (id = 1),
  root        TEXT NOT NULL DEFAULT '',
  tx_hash     TEXT NOT NULL DEFAULT '',
  block       BIGINT NOT NULL DEFAULT 0,
  anchored_at BIGINT NOT NULL DEFAULT 0,
  entries     BIGINT NOT NULL DEFAULT 0,
  signer_org  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS treasury_usage (
  id         TEXT PRIMARY KEY,
  program    TEXT NOT NULL,
  ref        TEXT NOT NULL,
  entry_id   TEXT NOT NULL DEFAULT '',
  model      TEXT NOT NULL DEFAULT '',
  provider   TEXT NOT NULL DEFAULT '',
  class      TEXT NOT NULL DEFAULT '',
  units      BIGINT NOT NULL DEFAULT 0,
  amount     TEXT NOT NULL DEFAULT '0',
  cost       TEXT NOT NULL DEFAULT '0',
  paid_by    TEXT NOT NULL DEFAULT '',
  project    TEXT NOT NULL DEFAULT '',
  service    TEXT NOT NULL DEFAULT '',
  actor      TEXT NOT NULL DEFAULT '',
  api_key    TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  UNIQUE(program, ref)
);
CREATE INDEX IF NOT EXISTS ix_treasury_usage_created ON treasury_usage(created_at);
CREATE INDEX IF NOT EXISTS ix_treasury_usage_entry   ON treasury_usage(entry_id);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("treasury migrate: %w", err)
	}
	if err := s.migrateCentsToUnits(ctx, d, tx); err != nil {
		return err
	}
	// Re-apply the DDL so any table REBUILT by the legacy migration regains its indexes
	// (every statement is IF NOT EXISTS, so this is a no-op for already-present objects).
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("treasury reindex: %w", err)
	}
	if err := s.addEntryScope(ctx, d, tx); err != nil {
		return err
	}
	if err := s.addEntrySeq(ctx, d, tx); err != nil {
		return err
	}
	if err := s.addUseAttribution(ctx, d, tx); err != nil {
		return err
	}
	// AFTER the indexes are back, because the repair below reads the postings of each
	// candidate entry TWICE as correlated subqueries. The legacy migration rebuilds
	// treasury_postings without its indexes, so running the repair before this line
	// scans the whole postings table once per usage row — the one open where the file
	// is largest and the customer is waiting on it.
	return s.backfillUsageProgram(ctx, tx)
}

// addEntryScope gives a file made before entries carried a scope its project and
// service columns. CREATE TABLE IF NOT EXISTS leaves an existing table alone, and the
// cents rebuild above makes the table without them, so both reach here without the
// columns; a fresh file already has them and this does nothing.
//
// An entry written before the columns existed reads "" on both axes: its scope was
// never recorded, so it counts toward the org-wide sum and toward no scoped one.
func (s *Store) addEntryScope(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	for _, col := range []string{"project", "service"} {
		has, err := d.HasColumn(ctx, tx, "treasury_entries", col)
		if err != nil {
			return fmt.Errorf("detect entry %s: %w", col, err)
		}
		if has {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE treasury_entries ADD COLUMN `+col+` TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add entry %s: %w", col, err)
		}
	}
	return nil
}

// addUseAttribution gives a file made before the usage log named who acted and on which
// key its actor and api_key columns. A use counted before them reads "" on both: who
// made the call was never recorded, and is not guessed. A fresh store already has them
// and this does nothing.
func (s *Store) addUseAttribution(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	for _, col := range []string{"actor", "api_key"} {
		has, err := d.HasColumn(ctx, tx, "treasury_usage", col)
		if err != nil {
			return fmt.Errorf("detect use %s: %w", col, err)
		}
		if has {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE treasury_usage ADD COLUMN `+col+` TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add use %s: %w", col, err)
		}
	}
	return nil
}

// addEntrySeq gives a file made before entries carried an explicit sequence its seq
// column, numbered in the order the rows were written, and indexes it.
//
// An entry's id is random, so the order two entries of one second were written in is
// a fact the journal has to record, and a SQLite file recorded it only as the rowid
// the engine keeps out of sight. PostgreSQL has no rowid, so the order is a column,
// and the same column serves both engines. A new entry takes MAX(seq)+1 inside the
// transaction that writes it, which is safe because write transactions are serialized
// (see [Store.Tx]); the unique index is what would turn a breach of that into an
// error instead of a tie.
//
// THE UPGRADE OF A SQLITE FILE IS ONE TRANSACTION, and now the whole of [Store.migrate]
// is. The column, the numbering, the index and the trigger below commit together or
// not at all (SQLite's DDL is transactional), because the halves are not safe apart: a column added and not yet
// numbered leaves every row at 0, and the unique index that follows then refuses to
// build, on every open after, which is a book that cannot be opened. The numbering
// is WHERE seq = 0, so running it again on a file that has the column finishes
// whatever a previous run did not, and does nothing to one that is done.
//
// THE TRIGGER IS THE ROLLBACK. The version that predates this column inserts entries
// without a seq; the default 0 would collide on the second one. The trigger gives
// such a row its rowid, which is also what MAX(seq)+1 yields on a table that is
// numbered by rowid, so an old binary started against a migrated book keeps writing,
// and a rollback needs no data step.
//
// The numbering and the trigger read rowid, and only a SQLite file reaches them: a
// PostgreSQL database is made with the column and has no older version to serve.
func (s *Store) addEntrySeq(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	const index = `CREATE UNIQUE INDEX IF NOT EXISTS ux_treasury_entries_seq ON treasury_entries(seq)`
	if d.Postgres() {
		if _, err := tx.ExecContext(ctx, index); err != nil {
			return fmt.Errorf("index entry seq: %w", err)
		}
		return nil
	}
	has, err := d.HasColumn(ctx, tx, "treasury_entries", "seq")
	if err != nil {
		return fmt.Errorf("detect entry seq: %w", err)
	}
	steps := []string{
		`UPDATE treasury_entries SET seq = rowid WHERE seq = 0`,
		index,
		`CREATE TRIGGER IF NOT EXISTS treasury_entries_seq AFTER INSERT ON treasury_entries WHEN NEW.seq = 0
		 BEGIN UPDATE treasury_entries SET seq = rowid WHERE rowid = NEW.rowid; END`,
	}
	if !has {
		steps = append([]string{`ALTER TABLE treasury_entries ADD COLUMN seq BIGINT NOT NULL DEFAULT 0`}, steps...)
	}
	for _, step := range steps {
		if _, err := tx.ExecContext(ctx, step); err != nil {
			return fmt.Errorf("entry seq: %w", err)
		}
	}
	return nil
}

// backfillUsageProgram gives every pre-existing usage entry the program it would be
// written with today: the wallet the money left.
//
// A usage ref is unique WITHIN THE WALLET it debits — the entry's program is that wallet
// — so one subject's key can never swallow another's. Entries written before that scope
// existed carry an EMPTY program, and the probe that asks whether an act is already paid for
// now asks under the wallet: it does not see them. On any surface whose ref is STABLE
// across the deploy, that miss is a SECOND debit for one act, and the customer pays twice
// for work they already paid for.
//
// The wallet is not guessed. A usage entry is exactly two legs — wallet out,
// revenue:platform in — so the wallet is the NEGATIVE one, read off the posting itself.
// Key and money therefore name the same account by construction, which is the identity
// the writer states in code and this restates for the rows written before it.
//
// IDEMPOTENT by its own WHERE: a second run finds no usage row left with an empty program.
//
// SCOPED TO USAGE. A deposit's empty program is DELIBERATE — a settlement ref is unique
// across the whole org's books, not per wallet — so widening this would re-key every
// payment ever taken.
//
// An entry with NO negative leg is not a debit this ledger wrote, and it is SKIPPED
// rather than given a NULL program: the column is NOT NULL, so writing one would abort
// the migration and leave the org's wallet unopenable. One malformed row must not be able
// to close a customer's books.
//
// NEITHER MUST A ROW THAT IS ALREADY NAMED. The same act can hold BOTH keys at once —
// one row under the empty program and one under the wallet — whenever its ref was
// nameable by the client across the change: an org that renewed the same domain once
// before and once after writes `domain:renew:foo.com` into both namespaces. Re-keying
// the legacy row then collides with the wallet row on UNIQUE(kind, program, ref), and a
// plain UPDATE makes that ABORT: migrate fails, Open fails, the org's prepaid gate has
// no ledger to read and fail-closes, and EVERY paid request for that tenant is refused
// until someone edits the file by hand. NOT EXISTS skips the colliding row instead — the
// honest outcome, because the wallet row IS that act's entry, so the probe already finds
// it and the legacy row's empty program debits nobody a second time. It also makes this
// repair CURATIVE: a store that already aborted opens on the next try.
func (s *Store) backfillUsageProgram(ctx context.Context, tx *sql.Tx) error {
	if s.usageKind == "" {
		return nil // no wallet-scoped kind in this book; nothing is mis-keyed.
	}
	// The debited leg is the FIRST negative posting of the entry, in the order it was
	// written: a scalar subquery must name one row on PostgreSQL, where SQLite quietly
	// takes whichever it meets first.
	const wallet = `(SELECT p.account FROM treasury_postings p
	                   WHERE p.entry_id = treasury_entries.id AND p.amount LIKE '-%'
	                   ORDER BY p.id LIMIT 1)`
	// NOT EXISTS is what UPDATE OR IGNORE was: a row whose re-keyed name is already
	// taken is left as it is. PostgreSQL has no OR IGNORE, and a unique violation there
	// aborts the whole statement.
	const backfill = `
UPDATE treasury_entries
   SET program = ` + wallet + `
 WHERE kind = $1
   AND program = ''
   AND ` + wallet + ` IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM treasury_entries taken
                    WHERE taken.kind = treasury_entries.kind
                      AND taken.ref = treasury_entries.ref
                      AND taken.program = ` + wallet + `)`
	if _, err := tx.ExecContext(ctx, backfill, s.usageKind); err != nil {
		return fmt.Errorf("treasury backfill usage program: %w", err)
	}
	return nil
}

// migrateCentsToUnits rebuilds a pre-migration file (the legacy INTEGER-cents schema:
// treasury_entries.amount_cents + INTEGER posting amounts, no accounts.balance) into the
// 18-decimal schema, converting every stored cents value ×1e16 to the exact 18-decimal integer and materializing
// each account's running balance. Idempotent: on an already-migrated file it detects the
// absent amount_cents column and returns immediately. It runs inside the migration's
// transaction, so a failure part-way leaves the legacy tables as they were.
func (s *Store) migrateCentsToUnits(ctx context.Context, d dialect.Dialect, tx *sql.Tx) error {
	legacy, err := d.HasColumn(ctx, tx, "treasury_entries", "amount_cents")
	if err != nil {
		return fmt.Errorf("detect legacy schema: %w", err)
	}
	if !legacy {
		return nil // already migrated (or fresh)
	}

	// The 18-decimal tables were created above with different names? No — CREATE TABLE IF NOT
	// EXISTS was a no-op because the legacy tables already exist. Rename them aside, make
	// the 18-decimal tables, copy-convert, drop the legacy copies.
	for _, stmt := range []string{
		`ALTER TABLE treasury_entries  RENAME TO treasury_entries_legacy`,
		`ALTER TABLE treasury_postings RENAME TO treasury_postings_legacy`,
		`ALTER TABLE treasury_accounts RENAME TO treasury_accounts_legacy`,
		`CREATE TABLE treasury_accounts (
		   id TEXT PRIMARY KEY, kind TEXT NOT NULL, balance TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL)`,
		`CREATE TABLE treasury_entries (
		   id TEXT PRIMARY KEY, kind TEXT NOT NULL, program TEXT NOT NULL DEFAULT '', ref TEXT NOT NULL,
		   memo TEXT NOT NULL DEFAULT '', amount TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL,
		   UNIQUE(kind, program, ref))`,
		`CREATE TABLE treasury_postings (
		   id INTEGER PRIMARY KEY AUTOINCREMENT, entry_id TEXT NOT NULL REFERENCES treasury_entries(id),
		   account TEXT NOT NULL, amount TEXT NOT NULL DEFAULT '0', created_at INTEGER NOT NULL)`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("migrate ddl %q: %w", stmt, err)
		}
	}

	// entries: amount_cents (int) → amount (18-decimal integer text)
	if err := convertRows(tx,
		`SELECT id,kind,program,ref,memo,amount_cents,created_at FROM treasury_entries_legacy`,
		func(scan func(...any) error) error {
			var id, kind, program, ref, memo string
			var cents, created int64
			if err := scan(&id, &kind, &program, &ref, &memo, &cents, &created); err != nil {
				return err
			}
			_, err := tx.Exec(
				`INSERT INTO treasury_entries (id,kind,program,ref,memo,amount,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				id, kind, program, ref, memo, money.FromCents(cents).AttoString(), created)
			return err
		}); err != nil {
		return err
	}

	// postings: amount (int cents) → amount (18-decimal integer text), accumulating each account's balance
	balances := map[string]money.Amount{}
	if err := convertRows(tx,
		`SELECT entry_id,account,amount,created_at FROM treasury_postings_legacy ORDER BY id`,
		func(scan func(...any) error) error {
			var entryID, account string
			var cents, created int64
			if err := scan(&entryID, &account, &cents, &created); err != nil {
				return err
			}
			amt := money.FromCents(cents)
			if _, err := tx.Exec(
				`INSERT INTO treasury_postings (entry_id,account,amount,created_at) VALUES ($1,$2,$3,$4)`,
				entryID, account, amt.AttoString(), created); err != nil {
				return err
			}
			balances[account] = balances[account].Add(amt)
			return nil
		}); err != nil {
		return err
	}

	// accounts: carry kind/created_at, set the materialized balance
	if err := convertRows(tx,
		`SELECT id,kind,created_at FROM treasury_accounts_legacy`,
		func(scan func(...any) error) error {
			var id, kind string
			var created int64
			if err := scan(&id, &kind, &created); err != nil {
				return err
			}
			_, err := tx.Exec(
				`INSERT INTO treasury_accounts (id,kind,balance,created_at) VALUES ($1,$2,$3,$4)`,
				id, kind, balances[id].AttoString(), created)
			return err
		}); err != nil {
		return err
	}

	for _, drop := range []string{
		`DROP TABLE treasury_postings_legacy`,
		`DROP TABLE treasury_entries_legacy`,
		`DROP TABLE treasury_accounts_legacy`,
	} {
		if _, err := tx.Exec(drop); err != nil {
			return fmt.Errorf("migrate drop %q: %w", drop, err)
		}
	}
	return nil
}

// convertRows runs q and calls fn for each row with its Scan; used only by the one-shot
// legacy migration so its per-row insert reads inside the same tx.
func convertRows(tx *sql.Tx, q string, fn func(scan func(...any) error) error) error {
	rows, err := tx.Query(q)
	if err != nil {
		return fmt.Errorf("migrate query %q: %w", q, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows.Scan); err != nil {
			return fmt.Errorf("migrate row: %w", err)
		}
	}
	return rows.Err()
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// ── ledger.Store (read path, no tx) ──────────────────────────────────────────

func (s *Store) Balance(ctx context.Context, account string) (money.Amount, error) {
	return balanceOf(s.db.QueryRowContext(ctx,
		`SELECT balance FROM treasury_accounts WHERE id=$1`, account), account)
}

func (s *Store) BalancesWithPrefix(ctx context.Context, prefix string) (map[string]money.Amount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, balance FROM treasury_accounts WHERE id LIKE $1`, prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("balances prefix %q: %w", prefix, err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]money.Amount)
	for rows.Next() {
		var acct, bal string
		if err := rows.Scan(&acct, &bal); err != nil {
			return nil, fmt.Errorf("scan balance: %w", err)
		}
		amt, err := money.ParseInt(bal)
		if err != nil {
			return nil, fmt.Errorf("parse balance %q: %w", acct, err)
		}
		out[acct] = amt
	}
	return out, rows.Err()
}

// Balances reads several accounts' signed balances in ONE statement, so they are one
// snapshot of the file: a write cannot land between two of them. An account never
// posted to reads zero, exactly as [Store.Balance] answers it.
func (s *Store) Balances(ctx context.Context, accounts ...string) (map[string]money.Amount, error) {
	out := make(map[string]money.Amount, len(accounts))
	if len(accounts) == 0 {
		return out, nil
	}
	args := make([]any, len(accounts))
	for i, a := range accounts {
		out[a] = money.Zero()
		args[i] = a
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, balance FROM treasury_accounts WHERE id IN (`+dialect.Marks(1, len(accounts))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("balances: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var acct, bal string
		if err := rows.Scan(&acct, &bal); err != nil {
			return nil, fmt.Errorf("scan balance: %w", err)
		}
		amt, err := money.ParseInt(bal)
		if err != nil {
			return nil, fmt.Errorf("parse balance %q: %w", acct, err)
		}
		out[acct] = amt
	}
	return out, rows.Err()
}

// Entries is the whole journal: every account, newest first, bounded.
func (s *Store) Entries(ctx context.Context, limit int) ([]ledger.JournalEntry, error) {
	return s.EntriesOn(ctx, limit)
}

// EntriesOn is [Store.Entries] narrowed to the entries that POST to any of the
// given accounts — the movements of a single wallet out of a file that holds many.
//
// The narrowing is in the QUERY and not in the caller, because limit is a page
// size and the two orders give different answers: filtering after the limit pages
// the FILE and then keeps whatever survives, so a quiet account in a busy file
// reads back empty while its money sits there. Filtering first pages the account,
// which is the page the caller asked for. treasury_postings is indexed on account,
// so the narrow read is the cheap one.
//
// No account is every account, which is what makes [Store.Entries] this function
// rather than a second copy of it.
func (s *Store) EntriesOn(ctx context.Context, limit int, accounts ...string) ([]ledger.JournalEntry, error) {
	// limit <= 0 means EVERY entry (the ledger.Root full scan); a positive limit
	// bounds the admin journal read.
	q := `SELECT id,kind,program,ref,memo,project,service,amount,created_at FROM treasury_entries`
	args := []any{}
	if len(accounts) > 0 {
		q += ` WHERE EXISTS (SELECT 1 FROM treasury_postings p
		                      WHERE p.entry_id = treasury_entries.id AND p.account IN (` +
			dialect.Marks(1, len(accounts)) + `))`
		for _, a := range accounts {
			args = append(args, a)
		}
	}
	// Ties within one second break on seq, the order the entries were written in:
	// an id is random, and a replay of a wallet's history must see it as it happened.
	q += ` ORDER BY created_at DESC, seq DESC`
	if limit > 0 {
		q += " LIMIT $" + strconv.Itoa(len(args)+1)
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ledger.JournalEntry
	for rows.Next() {
		var e ledger.JournalEntry
		var amt string
		if err := rows.Scan(&e.ID, &e.Kind, &e.Program, &e.Ref, &e.Memo, &e.Project, &e.Service, &amt, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan entry: %w", err)
		}
		if e.Amount, err = money.ParseInt(amt); err != nil {
			return nil, fmt.Errorf("parse entry amount: %w", err)
		}
		if e.Memo, err = openMemo(s.fields, e.ID, e.Memo); err != nil {
			return nil, fmt.Errorf("open entry memo: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ps, err := postingsOf(ctx, s.db, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Postings = ps
	}
	return out, nil
}

// EntriesOfKind is every entry of one kind with its postings, oldest first, in two
// statements however many there are — the journal read that a question about one
// kind (every deposit, every source entry) asks, without loading the metered debits
// that make up nearly all of a busy file.
func (s *Store) EntriesOfKind(ctx context.Context, kind string) ([]ledger.JournalEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,kind,program,ref,memo,project,service,amount,created_at FROM treasury_entries
		  WHERE kind = $1 ORDER BY seq`, kind)
	if err != nil {
		return nil, fmt.Errorf("entries of kind %q: %w", kind, err)
	}
	var out []ledger.JournalEntry
	at := map[string]int{}
	for rows.Next() {
		var e ledger.JournalEntry
		var amt string
		if err := rows.Scan(&e.ID, &e.Kind, &e.Program, &e.Ref, &e.Memo, &e.Project, &e.Service, &amt, &e.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan entry: %w", err)
		}
		if e.Amount, err = money.ParseInt(amt); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("parse entry amount: %w", err)
		}
		if e.Memo, err = openMemo(s.fields, e.ID, e.Memo); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("open entry memo: %w", err)
		}
		at[e.ID] = len(out)
		out = append(out, e)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	legs, err := s.db.QueryContext(ctx,
		`SELECT p.entry_id, p.account, p.amount FROM treasury_postings p
		   JOIN treasury_entries e ON e.id = p.entry_id WHERE e.kind = $1 ORDER BY p.id`, kind)
	if err != nil {
		return nil, fmt.Errorf("postings of kind %q: %w", kind, err)
	}
	defer func() { _ = legs.Close() }()
	for legs.Next() {
		var id, acct, amt string
		if err := legs.Scan(&id, &acct, &amt); err != nil {
			return nil, fmt.Errorf("scan posting: %w", err)
		}
		a, err := money.ParseInt(amt)
		if err != nil {
			return nil, fmt.Errorf("parse posting amount: %w", err)
		}
		if i, ok := at[id]; ok {
			out[i].Postings = append(out[i].Postings, ledger.Posting{Account: acct, Amount: a})
		}
	}
	return out, legs.Err()
}

// Leg is one posting read with the entry it belongs to.
type Leg struct {
	Entry       string
	Kind        string
	Ref         string
	EntryAmount money.Amount
	Account     string
	Amount      money.Amount
}

// Legs reads every posting to the given accounts inside the transaction, in the
// order they were written — one account's history, read where no write can land
// between the read and what the caller posts on the strength of it.
func (t *txAdapter) Legs(accounts ...string) ([]Leg, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	args := make([]any, len(accounts))
	for i, a := range accounts {
		args[i] = a
	}
	rows, err := t.tx.QueryContext(t.ctx,
		`SELECT e.id, e.kind, e.ref, e.amount, p.account, p.amount
		   FROM treasury_postings p JOIN treasury_entries e ON e.id = p.entry_id
		  WHERE p.account IN (`+dialect.Marks(1, len(accounts))+`)
		  ORDER BY e.seq, p.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("legs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Leg
	for rows.Next() {
		var l Leg
		var ea, pa string
		if err := rows.Scan(&l.Entry, &l.Kind, &l.Ref, &ea, &l.Account, &pa); err != nil {
			return nil, fmt.Errorf("scan leg: %w", err)
		}
		if l.EntryAmount, err = money.ParseInt(ea); err != nil {
			return nil, fmt.Errorf("parse entry amount: %w", err)
		}
		if l.Amount, err = money.ParseInt(pa); err != nil {
			return nil, fmt.Errorf("parse posting amount: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SumProgram sums, inside the transaction, the amounts of the entries of one kind
// filed under program.
func (t *txAdapter) SumProgram(kind, program string) (money.Amount, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT amount FROM treasury_entries WHERE kind=$1 AND program=$2`, kind, program)
	if err != nil {
		return money.Zero(), fmt.Errorf("sum program: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return sumAmounts(rows)
}

// SumProgramsSince sums the amounts of the entries of one kind filed under a program
// starting with prefix, created at/after since.
func (s *Store) SumProgramsSince(ctx context.Context, kind, prefix string, since int64) (money.Amount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT amount FROM treasury_entries WHERE kind=$1 AND program LIKE $2 AND created_at >= $3`, kind, prefix+"%", since)
	if err != nil {
		return money.Zero(), fmt.Errorf("sum programs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return sumAmounts(rows)
}

// SumProgramSince sums the amounts of the entries of one kind filed under exactly
// program, created at/after since.
func (s *Store) SumProgramSince(ctx context.Context, kind, program string, since int64) (money.Amount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT amount FROM treasury_entries WHERE kind=$1 AND program=$2 AND created_at >= $3`, kind, program, since)
	if err != nil {
		return money.Zero(), fmt.Errorf("sum program: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return sumAmounts(rows)
}

func sumAmounts(rows *sql.Rows) (money.Amount, error) {
	sum := money.Zero()
	for rows.Next() {
		var amt string
		if err := rows.Scan(&amt); err != nil {
			return money.Zero(), fmt.Errorf("scan amount: %w", err)
		}
		a, err := money.ParseInt(amt)
		if err != nil {
			return money.Zero(), fmt.Errorf("parse amount: %w", err)
		}
		sum = sum.Add(a)
	}
	return sum, rows.Err()
}

// Prefixed sums, inside the transaction, the balances of every account whose id
// starts with prefix.
func (t *txAdapter) Prefixed(prefix string) (money.Amount, error) {
	rows, err := t.tx.QueryContext(t.ctx, `SELECT balance FROM treasury_accounts WHERE id LIKE $1`, prefix+"%")
	if err != nil {
		return money.Zero(), fmt.Errorf("prefixed %q: %w", prefix, err)
	}
	defer func() { _ = rows.Close() }()
	sum := money.Zero()
	for rows.Next() {
		var bal string
		if err := rows.Scan(&bal); err != nil {
			return money.Zero(), fmt.Errorf("scan balance: %w", err)
		}
		a, err := money.ParseInt(bal)
		if err != nil {
			return money.Zero(), fmt.Errorf("parse balance: %w", err)
		}
		sum = sum.Add(a)
	}
	return sum, rows.Err()
}

// SumLegsSince sums, over the entries of one kind created at/after since, their
// postings to the accounts named root or root:<anything> — the part of those entries
// one family of accounts carried.
func (s *Store) SumLegsSince(ctx context.Context, kind string, since int64, root string) (money.Amount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.amount FROM treasury_postings p JOIN treasury_entries e ON e.id = p.entry_id
		  WHERE e.kind = $1 AND e.created_at >= $2 AND (p.account = $3 OR p.account LIKE $4)`,
		kind, since, root, root+":%")
	if err != nil {
		return money.Zero(), fmt.Errorf("sum legs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	sum := money.Zero()
	for rows.Next() {
		var amt string
		if err := rows.Scan(&amt); err != nil {
			return money.Zero(), fmt.Errorf("scan amount: %w", err)
		}
		a, perr := money.ParseInt(amt)
		if perr != nil {
			return money.Zero(), fmt.Errorf("parse amount: %w", perr)
		}
		sum = sum.Add(a)
	}
	return sum, rows.Err()
}

// SumByKindSince sums the entry amounts of ONE kind created at/after `since` (unix
// seconds) within a (project, service) scope — a read-only aggregate over the indexed
// created_at. An empty project or service does not narrow that axis, so ("", "") is
// every entry of the kind. Amounts are 18-decimal TEXT (SQLite INTEGER overflows past
// ~$9.20), so the fold is in Go. This is the usage-cap's period-spend source: sum kind
// "finance.usage" for the org store since the start of the UTC month in the cap's own
// scope, so a cap enforces on real ledger spend rather than a bounded, truncatable
// journal scan.
func (s *Store) SumByKindSince(ctx context.Context, kind, project, service string, since int64) (money.Amount, error) {
	q := `SELECT amount FROM treasury_entries WHERE kind = $1 AND created_at >= $2`
	args := []any{kind, since}
	if project != "" {
		args = append(args, project)
		q += ` AND project = $` + strconv.Itoa(len(args))
	}
	if service != "" {
		args = append(args, service)
		q += ` AND service = $` + strconv.Itoa(len(args))
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return money.Zero(), fmt.Errorf("sum by kind: %w", err)
	}
	defer func() { _ = rows.Close() }()
	sum := money.Zero()
	for rows.Next() {
		var amt string
		if err := rows.Scan(&amt); err != nil {
			return money.Zero(), fmt.Errorf("scan amount: %w", err)
		}
		a, perr := money.ParseInt(amt)
		if perr != nil {
			return money.Zero(), fmt.Errorf("parse amount: %w", perr)
		}
		sum = sum.Add(a)
	}
	return sum, rows.Err()
}

// querier is the one read a *sql.DB and a *sql.Tx both answer, so the postings of an
// entry are fetched by ONE query whether the caller is the journal listing (on the
// connection) or the idempotency lookup (inside its own transaction, where a row the
// same transaction just wrote must be visible).
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func postingsOf(ctx context.Context, q querier, entryID string) ([]ledger.Posting, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT account, amount FROM treasury_postings WHERE entry_id=$1 ORDER BY id`, entryID)
	if err != nil {
		return nil, fmt.Errorf("list postings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ledger.Posting
	for rows.Next() {
		var p ledger.Posting
		var amt string
		if err := rows.Scan(&p.Account, &amt); err != nil {
			return nil, fmt.Errorf("scan posting: %w", err)
		}
		if p.Amount, err = money.ParseInt(amt); err != nil {
			return nil, fmt.Errorf("parse posting amount: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Policy(ctx context.Context) (ledger.SharePolicy, error) {
	var p ledger.SharePolicy
	err := s.db.QueryRowContext(ctx,
		`SELECT revenue_share_bps, updated_at FROM treasury_policy WHERE id=1`).Scan(&p.RevenueShareBps, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// Unset → the canonical 20% default (not 0%): a fresh treasury sweeps the same
		// creator/revenue share the rest of the platform uses. A SuperAdmin who wants a
		// different rate (including 0) writes an explicit policy row via SetPolicy.
		return ledger.SharePolicy{RevenueShareBps: ledger.DefaultRevenueShareBps}, nil
	}
	if err != nil {
		return ledger.SharePolicy{}, fmt.Errorf("read policy: %w", err)
	}
	return p, nil
}

func (s *Store) SetPolicy(ctx context.Context, p ledger.SharePolicy) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO treasury_policy (id, revenue_share_bps, updated_at) VALUES (1,$1,$2)
		 ON CONFLICT(id) DO UPDATE SET revenue_share_bps=excluded.revenue_share_bps, updated_at=excluded.updated_at`,
		p.RevenueShareBps, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("set policy: %w", err)
	}
	return nil
}

// ── the book's credit limit ──────────────────────────────────────────────────

// Limit is how far below zero the book's own account may go: the credit line its
// owner pays in arrears. Unset reads zero — a book has no line until one is set.
func (s *Store) Limit(ctx context.Context) (money.Amount, error) {
	return limitOf(s.db.QueryRowContext(ctx, `SELECT amount FROM treasury_limit WHERE id=1`))
}

// SetLimit records the book's credit limit, replacing the one before.
func (s *Store) SetLimit(ctx context.Context, amount money.Amount, now int64) error {
	if amount.IsNeg() {
		return fmt.Errorf("set limit: %s is below zero", amount)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO treasury_limit (id, amount, updated_at) VALUES (1,$1,$2)
		 ON CONFLICT(id) DO UPDATE SET amount=excluded.amount, updated_at=excluded.updated_at`,
		amount.AttoString(), now); err != nil {
		return fmt.Errorf("set limit: %w", err)
	}
	return nil
}

// Limit is [Store.Limit] read inside the transaction, so a debit weighs the line
// it posts against.
func (t *txAdapter) Limit() (money.Amount, error) {
	return limitOf(t.tx.QueryRowContext(t.ctx, `SELECT amount FROM treasury_limit WHERE id=1`))
}

// Owing is when the book's own account last went from owing nothing to owing, in
// unix seconds; 0 when the book never recorded it.
func (s *Store) Owing(ctx context.Context) (int64, error) {
	var since int64
	err := s.db.QueryRowContext(ctx, `SELECT since FROM treasury_owing WHERE id=1`).Scan(&since)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read owing: %w", err)
	}
	return since, nil
}

// Owe records, inside the transaction, that the book's own account began to owe at
// (unix seconds), replacing the moment before.
func (t *txAdapter) Owe(at int64) error {
	if _, err := t.tx.ExecContext(t.ctx,
		`INSERT INTO treasury_owing (id, since) VALUES (1,$1) ON CONFLICT(id) DO UPDATE SET since=excluded.since`, at); err != nil {
		return fmt.Errorf("record owing: %w", err)
	}
	return nil
}

func limitOf(row *sql.Row) (money.Amount, error) {
	var amt string
	err := row.Scan(&amt)
	if errors.Is(err, sql.ErrNoRows) {
		return money.Zero(), nil
	}
	if err != nil {
		return money.Zero(), fmt.Errorf("read limit: %w", err)
	}
	a, perr := money.ParseInt(amt)
	if perr != nil {
		return money.Zero(), fmt.Errorf("parse limit: %w", perr)
	}
	return a, nil
}

// ── the usage log: every served call, whether or not money moved ─────────────

// Use is one served call as the book counts it. A call moves money only when a
// wallet pays for it, and Entry then names the journal entry that moved it; a call
// a plan covered or the platform absorbed is counted here and posts nothing.
// Amount is what the call was priced at, Cost what serving it cost the platform.
type Use struct {
	ID       string
	Program  string // the wallet the ref is unique within
	Ref      string
	Entry    string // the journal entry, when money moved
	Model    string
	Provider string
	Class    string
	Units    int64
	Amount   money.Amount
	Cost     money.Amount
	PaidBy   string
	Project  string
	Service  string
	// Actor is the member who made the call, "<org>/<name>", and Key the API key it
	// arrived on, "<org>/<id>": both empty where the call named none, and for every use
	// counted before the log recorded them.
	Actor     string
	Key       string
	CreatedAt int64
}

// UseByRef answers the use already counted under (program, ref), inside the
// transaction that would count it again.
func (t *txAdapter) UseByRef(program, ref string) (Use, bool, error) {
	u, err := scanUse(t.tx.QueryRowContext(t.ctx, `SELECT `+useCols+` FROM treasury_usage WHERE program=$1 AND ref=$2`, program, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return Use{}, false, nil
	}
	if err != nil {
		return Use{}, false, fmt.Errorf("use by ref: %w", err)
	}
	return u, true, nil
}

// InsertUse counts one use.
func (t *txAdapter) InsertUse(u Use) error {
	if _, err := t.tx.ExecContext(t.ctx,
		`INSERT INTO treasury_usage (`+useCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		u.ID, u.Program, u.Ref, u.Entry, u.Model, u.Provider, u.Class, u.Units,
		u.Amount.AttoString(), u.Cost.AttoString(), u.PaidBy, u.Project, u.Service, u.Actor, u.Key, u.CreatedAt); err != nil {
		return fmt.Errorf("insert use: %w", err)
	}
	return nil
}

// Uses lists the book's served calls, newest first, up to limit (limit <= 0 is all).
//
// A debit of usageKind posted before the usage log existed has no row there, and it
// is listed from the journal as the use it was — its amount, its model, nothing it
// never recorded — so the list is every call the book has ever billed, once.
func (s *Store) Uses(ctx context.Context, usageKind string, limit int) ([]Use, error) {
	q := `SELECT ` + useCols + `, 0 FROM treasury_usage
	      UNION ALL
	      SELECT e.id, e.program, e.ref, e.id, e.memo, '', '', 0, e.amount, '0', '', e.project, e.service, '', '', e.created_at, 1
	        FROM treasury_entries e
	       WHERE e.kind = $1 AND NOT EXISTS (SELECT 1 FROM treasury_usage u WHERE u.entry_id = e.id)
	      ORDER BY created_at DESC`
	args := []any{usageKind}
	if limit > 0 {
		q += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list uses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Use
	for rows.Next() {
		var journal int
		u, err := scanUse(withFlag{rows, &journal})
		if err != nil {
			return nil, fmt.Errorf("scan use: %w", err)
		}
		// A use read from the journal carries the entry's memo as its model, sealed
		// under the entry's row.
		if journal == 1 {
			if u.Model, err = openMemo(s.fields, u.ID, u.Model); err != nil {
				return nil, fmt.Errorf("open entry memo: %w", err)
			}
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// withFlag is a row with one more column after the ones scanUse reads.
type withFlag struct {
	row  interface{ Scan(...any) error }
	flag *int
}

func (w withFlag) Scan(dest ...any) error { return w.row.Scan(append(dest, w.flag)...) }

const useCols = `id, program, ref, entry_id, model, provider, class, units, amount, cost, paid_by, project, service, actor, api_key, created_at`

func scanUse(row interface{ Scan(...any) error }) (Use, error) {
	var u Use
	var amt, cost string
	if err := row.Scan(&u.ID, &u.Program, &u.Ref, &u.Entry, &u.Model, &u.Provider, &u.Class, &u.Units,
		&amt, &cost, &u.PaidBy, &u.Project, &u.Service, &u.Actor, &u.Key, &u.CreatedAt); err != nil {
		return Use{}, err
	}
	var err error
	if u.Amount, err = money.ParseInt(amt); err != nil {
		return Use{}, fmt.Errorf("parse use amount: %w", err)
	}
	if u.Cost, err = money.ParseInt(cost); err != nil {
		return Use{}, fmt.Errorf("parse use cost: %w", err)
	}
	return u, nil
}

// ── ledger.Store.Tx + ledger.Tx (write path) ─────────────────────────────────

func (s *Store) Tx(ctx context.Context, fn func(ledger.Tx) error) error {
	// One writer at a time on the store, as the file's single connection made it
	// (dialect.BeginWrite): a balance read, weighed against a limit, then written
	// back is a lost update the moment a second writer can read between them.
	tx, err := dialect.Of(s.db).BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	// A panic in fn must not leave the transaction open: on the file it held the
	// one connection for good, and on a pool it holds the write lock and a slot.
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(&txAdapter{ctx: ctx, tx: tx, fields: s.fields}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// txAdapter binds ledger.Tx to a live *sql.Tx.
type txAdapter struct {
	ctx    context.Context
	tx     *sql.Tx
	fields *tenure.Fields
}

func (t *txAdapter) EntryByRef(kind, program, ref string) (ledger.JournalEntry, bool, error) {
	var e ledger.JournalEntry
	var amt string
	err := t.tx.QueryRowContext(t.ctx,
		`SELECT id,kind,program,ref,memo,project,service,amount,created_at
		   FROM treasury_entries WHERE kind=$1 AND program=$2 AND ref=$3`, kind, program, ref).
		Scan(&e.ID, &e.Kind, &e.Program, &e.Ref, &e.Memo, &e.Project, &e.Service, &amt, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.JournalEntry{}, false, nil
	}
	if err != nil {
		return ledger.JournalEntry{}, false, fmt.Errorf("entry by ref: %w", err)
	}
	if e.Amount, err = money.ParseInt(amt); err != nil {
		return ledger.JournalEntry{}, false, fmt.Errorf("parse entry amount: %w", err)
	}
	if e.Memo, err = openMemo(t.fields, e.ID, e.Memo); err != nil {
		return ledger.JournalEntry{}, false, fmt.Errorf("open entry memo: %w", err)
	}
	// WITH ITS LEGS, because (kind, program, ref) does not say WHOSE money it was. The
	// entry row carries the amount and nothing about the accounts, so a caller checking
	// that a ref it is about to reuse belongs to the same posting it is making has only
	// half the fact — and half was enough to hand one payer another payer's entry. The
	// legs are where the subject lives, so the idempotency lookup returns the whole entry
	// the way [Store.Entries] does, rather than a header a caller must complete.
	if e.Postings, err = postingsOf(t.ctx, t.tx, e.ID); err != nil {
		return ledger.JournalEntry{}, false, err
	}
	return e, true, nil
}

func (t *txAdapter) EntryByID(id string) (ledger.JournalEntry, bool, error) {
	var e ledger.JournalEntry
	var amt string
	err := t.tx.QueryRowContext(t.ctx,
		`SELECT id,kind,program,ref,memo,project,service,amount,created_at
		   FROM treasury_entries WHERE id=$1`, id).
		Scan(&e.ID, &e.Kind, &e.Program, &e.Ref, &e.Memo, &e.Project, &e.Service, &amt, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ledger.JournalEntry{}, false, nil
	}
	if err != nil {
		return ledger.JournalEntry{}, false, fmt.Errorf("entry by id: %w", err)
	}
	if e.Amount, err = money.ParseInt(amt); err != nil {
		return ledger.JournalEntry{}, false, fmt.Errorf("parse entry amount: %w", err)
	}
	if e.Memo, err = openMemo(t.fields, e.ID, e.Memo); err != nil {
		return ledger.JournalEntry{}, false, fmt.Errorf("open entry memo: %w", err)
	}
	if e.Postings, err = postingsOf(t.ctx, t.tx, e.ID); err != nil {
		return ledger.JournalEntry{}, false, err
	}
	return e, true, nil
}

func (t *txAdapter) Balance(account string) (money.Amount, error) {
	return balanceOf(t.tx.QueryRowContext(t.ctx,
		`SELECT balance FROM treasury_accounts WHERE id=$1`, account), account)
}

func (t *txAdapter) Insert(e ledger.JournalEntry, postings []ledger.Posting) error {
	for _, p := range postings {
		if _, err := t.tx.ExecContext(t.ctx,
			`INSERT INTO treasury_accounts (id, kind, balance, created_at) VALUES ($1, $2, '0', $3)
			 ON CONFLICT (id) DO NOTHING`,
			p.Account, accountKind(p.Account), e.CreatedAt); err != nil {
			return fmt.Errorf("upsert account %q: %w", p.Account, err)
		}
	}
	memo, err := sealMemo(t.fields, e.ID, e.Memo)
	if err != nil {
		return fmt.Errorf("seal entry memo: %w", err)
	}
	if _, err := t.tx.ExecContext(t.ctx,
		`INSERT INTO treasury_entries (seq,id,kind,program,ref,memo,project,service,amount,created_at)
		 VALUES ((SELECT COALESCE(MAX(seq), 0) + 1 FROM treasury_entries), $1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		e.ID, e.Kind, e.Program, e.Ref, memo, e.Project, e.Service, e.Amount.AttoString(), e.CreatedAt); err != nil {
		return fmt.Errorf("insert entry: %w", err)
	}
	for _, p := range postings {
		if _, err := t.tx.ExecContext(t.ctx,
			`INSERT INTO treasury_postings (entry_id, account, amount, created_at) VALUES ($1,$2,$3,$4)`,
			e.ID, p.Account, p.Amount.AttoString(), e.CreatedAt); err != nil {
			return fmt.Errorf("insert posting: %w", err)
		}
		// Maintain the account's running balance in the SAME tx, so a read never re-sums
		// the journal and can never drift from it.
		cur, err := balanceOf(t.tx.QueryRowContext(t.ctx,
			`SELECT balance FROM treasury_accounts WHERE id=$1`, p.Account), p.Account)
		if err != nil {
			return err
		}
		if _, err := t.tx.ExecContext(t.ctx,
			`UPDATE treasury_accounts SET balance=$1 WHERE id=$2`, cur.Add(p.Amount).AttoString(), p.Account); err != nil {
			return fmt.Errorf("update balance %q: %w", p.Account, err)
		}
	}
	return nil
}

// balanceOf scans a single balance TEXT column into a money.Amount (a missing row is a
// zero balance, not an error — an account exists only once it has been posted to).
func balanceOf(row *sql.Row, account string) (money.Amount, error) {
	var bal string
	err := row.Scan(&bal)
	if errors.Is(err, sql.ErrNoRows) {
		return money.Zero(), nil
	}
	if err != nil {
		return money.Zero(), fmt.Errorf("balance %q: %w", account, err)
	}
	amt, perr := money.ParseInt(bal)
	if perr != nil {
		return money.Zero(), fmt.Errorf("parse balance %q: %w", account, perr)
	}
	return amt, nil
}

// accountKind derives an account's kind from its id namespace (the segment before
// the first ':'), so the accounts table stays queryable by kind without the engine
// having to pass it.
func accountKind(account string) string {
	if i := strings.IndexByte(account, ':'); i > 0 {
		return account[:i]
	}
	return "other"
}
