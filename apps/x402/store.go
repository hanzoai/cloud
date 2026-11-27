package x402

// store.go is the settlement ledger's index: ONE row per CLAIMED authorization,
// keyed by a DETERMINISTIC id derived from (payer address, nonce). That id is
// both the replay-dedup key (a PRIMARY KEY, so a second insert of the same
// authorization is atomically refused) AND the metering/finance idempotency ref,
// so the fast in-pod dedup and the ledger's own idempotency agree on ONE key —
// settle-once is enforced in two independent places. One database for the deployment
// (a SQLite file, or a schema on hanzo-sql), in the dialect both engines share
// (sdk/dialect); its tests run on both (internal/sqltest).
//
// A row is written when the settlement is CLAIMED and flipped when it is SETTLED,
// so the two states an in-flight payment can be in are a column rather than the
// presence or absence of a row. That is what makes an interrupted settlement
// recoverable: the claim names the payer, the payee and the amount, and both money
// writes are idempotent on the id it is keyed by (see settle + Reconcile).
//
// A settled row is also the rail's OUTBOX to the event plane until `stated` says
// the plane has landed it (economic.go): the flip to settled is the write that
// makes the statement owed, so the two can never disagree.

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/account"
	"github.com/hanzoai/namespace"
)

// Settlement is one x402 authorization this rail has accepted: claimed when the
// row is written, Settled once both halves of the money have moved.
type Settlement struct {
	ID string // deterministic: settlementID(from, nonce)
	// PayerOrg is the books the payment is drawn in and PayerSubject the wallet
	// within them: together the account principal.Payer resolved for the buyer. In
	// the signup org they differ — every member is their own wallet beside the
	// platform's pool — and a draw keyed on the org alone took a stranger's purchase
	// from Hanzo's books. Read the wallet through [Settlement.wallet].
	PayerOrg     string
	PayerSubject string
	From         string
	Nonce        string
	Resource     string
	Payee        string
	PayeeOrg     string
	// PayeeSubject is the recipient WALLET's ledger subject — where the credit
	// actually lands. It is on the row, and not re-resolved from wallets when a
	// settlement is completed later, because a claim has to be sufficient on its
	// own: re-resolving would make the sweep depend on the wallets subsystem being
	// reachable, and on the wallet still resolving to the same subject it did when
	// the payer was charged. Neither is guaranteed, and both would silently pay the
	// wrong account.
	PayeeSubject string
	// Agent is the principal that acted for the payer when the settlement was
	// claimed. It is on the row for PayeeSubject's reason: the economic event a
	// settlement states is derived from this row alone, so stating it again after
	// a restart says exactly what was said the first time.
	Agent string
	// Category is what the settlement paid for, as the price table said it when
	// the settlement was claimed (client.Categories). On the row for Agent's
	// reason: the economic event is derived from this row alone.
	Category   string
	Amount     string // exact 18-dp USD (money.Amount.IntString)
	Network    string // CAIP-2
	SettledVia string
	TxHash     string
	Settled    bool
	CreatedAt  int64
}

// wallet is the account within PayerOrg's books this settlement draws from. A row
// written before payer_subj existed carries none, and its wallet is the org's own:
// that is the account every such row was actually drawn from.
func (st *Settlement) wallet() string { return cmp.Or(st.PayerSubject, st.PayerOrg) }

// paidBy reports whether payer is the account this settlement was drawn from:
// the same books and the same wallet within them.
func (st *Settlement) paidBy(payer account.Account) bool {
	return !payer.Zero() && st.PayerOrg == payer.Org() && st.wallet() == payer.Subject()
}

type store struct {
	db *sql.DB
	d  dialect.Dialect
}

func openStore(dir string) (*store, error) {
	return locus.Use(context.Background(), nil, namespace.System(), "x402", dir, func(db *sql.DB) (*store, error) {
		s := &store{db: db, d: dialect.Of(db)}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.d.Migrate(ctx, db, func(tx *sql.Tx) error { return s.migrate(ctx, tx) }); err != nil {
			return nil, err
		}
		return s, nil
	})
}

func (s *store) migrate(ctx context.Context, tx *sql.Tx) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS settlements (
  id          TEXT PRIMARY KEY,
  payer_org   TEXT NOT NULL,
  from_addr   TEXT NOT NULL,
  nonce       TEXT NOT NULL,
  resource    TEXT NOT NULL,
  payee       TEXT NOT NULL,
  payee_org   TEXT NOT NULL,
  payee_subj  TEXT NOT NULL DEFAULT '',
  agent       TEXT NOT NULL DEFAULT '',
  amount      TEXT NOT NULL,
  network     TEXT NOT NULL DEFAULT '',
  settled_via TEXT NOT NULL,
  tx_hash     TEXT NOT NULL DEFAULT '',
  settled     BIGINT NOT NULL DEFAULT 0,
  created_at  BIGINT NOT NULL,
  stated      BIGINT NOT NULL DEFAULT 0,
  tries       BIGINT NOT NULL DEFAULT 0,
  due         BIGINT NOT NULL DEFAULT 0,
  category    TEXT NOT NULL DEFAULT 'service',
  payer_subj  TEXT NOT NULL DEFAULT ''
);
-- An authorization agreed before it is settled — a job's, signed when the work
-- begins and settled when it is released — reserves its nonce here for the one
-- resource it pays. settle refuses that nonce for anything else, so the payment
-- a seller started work on cannot be spent elsewhere first. Nothing here is
-- money — the amount a hold sets aside is in the payer's own wallet, the one
-- payer_org and payer_subj name, under the settlement id (finance hold) — and
-- Reconcile never reads it. A hold given up
-- (x402_void) was returned first, and stays, given, until it expires: the
-- authorization pays nothing, and a hold asked for it after the void — one sent
-- before it and delivered after — is refused rather than setting the amount aside
-- again for nobody. One neither settled nor given up stays until it is, so an id
-- no row holds never set anything aside.
CREATE TABLE IF NOT EXISTS holds (
  id         TEXT PRIMARY KEY,
  resource   TEXT NOT NULL,
  until      BIGINT NOT NULL,
  given      BIGINT NOT NULL DEFAULT 0,
  payer_org  TEXT NOT NULL DEFAULT '',
  payer_subj TEXT NOT NULL DEFAULT ''
);
`
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("x402 migrate: %w", err)
	}

	// `CREATE TABLE IF NOT EXISTS` is a NO-OP on a database that already has the
	// table, so a column added to the DDL above never reaches one — and the DDL
	// is the only place anyone thinks to add it. Every column below carries a
	// DEFAULT because every one of them was added after the table shipped; they
	// exist in the statement above and existed nowhere in production.
	//
	// The index then indexes one of them, so the migration did not fail quietly
	// — it failed the whole mount: `x402 migrate: no such column: settled`,
	// Mount returns an error, and a lazy plugin that cannot mount answers 503
	// `no instance running` for every /v1/x402 request, forever. It has been
	// doing that in production.
	//
	// Neither engine has one statement that adds a column only if it is missing, so
	// ask the table what it has.
	// Adding a NOT NULL column to a populated table is legal precisely because
	// each of these has a DEFAULT. Ordered, so a fresh database and a migrated
	// one end up identical.
	if err := s.addColumns(ctx, tx, "settlements", []column{
		{"payee_subj", "TEXT NOT NULL DEFAULT ''"},
		{"network", "TEXT NOT NULL DEFAULT ''"},
		{"tx_hash", "TEXT NOT NULL DEFAULT ''"},
		{"settled", "BIGINT NOT NULL DEFAULT 0"},
		{"agent", "TEXT NOT NULL DEFAULT ''"},
		// The outbox. A row settled before these existed was never stated, so it is
		// owed like any other: the drainer states the history too, and a tax year
		// read off the plane misses no settlement this rail ever made.
		{"stated", "BIGINT NOT NULL DEFAULT 0"},
		{"tries", "BIGINT NOT NULL DEFAULT 0"},
		{"due", "BIGINT NOT NULL DEFAULT 0"},
		// Every settlement before the category existed paid for a tool call, which
		// is a service; a row written since carries what its price table said.
		{"category", "TEXT NOT NULL DEFAULT 'service'"},
		// The wallet a payment is drawn from inside payer_org's books. A row
		// written before it was drawn from the org's own wallet, which is what the
		// empty default reads as (Settlement.wallet).
		{"payer_subj", "TEXT NOT NULL DEFAULT ''"},
	}); err != nil {
		return err
	}
	// The account a hold set its amount aside in. A hold written before these
	// names none, and was set aside in the org's own wallet of whoever asked.
	if err := s.addColumns(ctx, tx, "holds", []column{
		{"payer_org", "TEXT NOT NULL DEFAULT ''"},
		{"payer_subj", "TEXT NOT NULL DEFAULT ''"},
	}); err != nil {
		return err
	}

	// AFTER the columns exist — ix_settlements_pending is partial on `settled`,
	// and that is the statement the missing column killed.
	const idx = `
CREATE INDEX IF NOT EXISTS ix_settlements_payer ON settlements(payer_org, created_at);
-- GET /v1/x402/settlements as a payer: one wallet's receipts inside its org's books.
CREATE INDEX IF NOT EXISTS ix_settlements_wallet ON settlements(payer_org, payer_subj, created_at);
-- GET /v1/x402/settlements?role=payee: what an org was paid, receipt by receipt.
CREATE INDEX IF NOT EXISTS ix_settlements_payee ON settlements(payee_org, created_at);
-- The sweep's index: the unsettled rows are the ones Reconcile has to find, and
-- they are a vanishing fraction of the table, so a partial index is the whole scan.
CREATE INDEX IF NOT EXISTS ix_settlements_pending ON settlements(created_at) WHERE settled = 0;
-- The drainer's index: settled payments between two orgs the event plane has not
-- landed. Partial for the sweep's reason, and a self-payment is never in it.
CREATE INDEX IF NOT EXISTS ix_settlements_owed ON settlements(due)
  WHERE settled = 1 AND stated = 0 AND payer_org != payee_org;
`
	if _, err := tx.ExecContext(ctx, idx); err != nil {
		return fmt.Errorf("x402 migrate: indexes: %w", err)
	}
	return nil
}

// column is one column a table gained after it shipped, with the DEFAULT that lets
// it be added to a populated table.
type column struct{ name, def string }

// addColumns adds to table every column in cols it does not have yet.
func (s *store) addColumns(ctx context.Context, tx *sql.Tx, table string, cols []column) error {
	for _, c := range cols {
		have, err := s.d.HasColumn(ctx, tx, table, c.name)
		if err != nil {
			return fmt.Errorf("x402 migrate: read %s columns: %w", table, err)
		}
		if have {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+c.name+` `+c.def); err != nil {
			return fmt.Errorf("x402 migrate: add %s.%s: %w", table, c.name, err)
		}
	}
	return nil
}

func (s *store) Close() error { return s.db.Close() }

// claim atomically claims a settlement id, UNSETTLED. It returns claimed=true when
// THIS call won the row (the caller must move the money), or claimed=false with the
// existing row when the id was already taken (the same settlement, or a replay —
// the caller decides which by comparing terms). The PRIMARY KEY makes the claim
// atomic across concurrent writers: exactly one INSERT succeeds.
func (s *store) claim(ctx context.Context, st *Settlement) (claimed bool, existing *Settlement, err error) {
	if err := dialect.Text(st.ID, st.PayerOrg, st.PayerSubject, st.From, st.Nonce, st.Resource, st.Payee, st.PayeeOrg,
		st.PayeeSubject, st.Agent, st.Category, st.Amount, st.Network, st.SettledVia, st.TxHash); err != nil {
		return false, nil, fmt.Errorf("claim settlement: %w", err)
	}
	var won string
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO settlements
		 (id, payer_org, payer_subj, from_addr, nonce, resource, payee, payee_org, payee_subj, agent, category, amount, network, settled_via, tx_hash, settled, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,0,$16)
		 ON CONFLICT (id) DO NOTHING RETURNING id`,
		st.ID, st.PayerOrg, st.PayerSubject, st.From, st.Nonce, st.Resource, st.Payee, st.PayeeOrg, st.PayeeSubject,
		st.Agent, st.Category, st.Amount, st.Network, st.SettledVia, st.TxHash, st.CreatedAt).Scan(&won)
	switch {
	case err == nil:
		return true, nil, nil // this call inserted the row
	case !errors.Is(err, sql.ErrNoRows):
		return false, nil, fmt.Errorf("claim settlement: %w", err)
	}
	ex, _, err := s.get(ctx, st.ID)
	if err != nil {
		return false, nil, err
	}
	return false, ex, nil
}

// errClaimMoved is a write to a claim that found it not as the caller took it: gone,
// because another replica withdrew it, or settled, because another finished it. The
// caller's view of the row is stale, and nothing it was about to say about the
// settlement (that it was refused, that it was paid) is so.
var errClaimMoved = errors.New("x402: the claim is not the unsettled one this call took")

// onlyOne is nil when res changed exactly the one claim the caller named, and
// errClaimMoved when it changed none.
func onlyOne(res sql.Result, what, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s %s: %w", what, id, err)
	}
	if n != 1 {
		return fmt.Errorf("%s %s: %w", what, id, errClaimMoved)
	}
	return nil
}

// unclaim withdraws a claim no money moved under — a draw the payer's wallet
// refused — so the authorization it named may be presented again. A settled row is
// never withdrawn, and a row that is not there to withdraw is not withdrawn either:
// both are errClaimMoved, and the caller must not tell the payer its wallet was
// short of a payment another replica may have drawn.
func (s *store) unclaim(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM settlements WHERE id = $1 AND settled = 0`, id)
	if err != nil {
		return fmt.Errorf("withdraw claim: %w", err)
	}
	return onlyOne(res, "withdraw claim", id)
}

// markSettled flips an unsettled claim to settled once BOTH halves of the money have
// moved. It is the last write of the flow, so until it lands the row stays
// sweepable. A claim that is gone or already settled is errClaimMoved, never success:
// the caller serves nothing on a row it did not settle.
func (s *store) markSettled(ctx context.Context, id, txHash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE settlements SET settled=1, tx_hash=$1 WHERE id=$2 AND settled=0`, txHash, id)
	if err != nil {
		return fmt.Errorf("mark settled: %w", err)
	}
	return onlyOne(res, "mark settled", id)
}

const settlementCols = `id, payer_org, payer_subj, from_addr, nonce, resource, payee, payee_org,
	payee_subj, agent, category, amount, network, settled_via, tx_hash, settled, created_at`

func scanSettlement(sc interface{ Scan(...any) error }) (*Settlement, error) {
	var st Settlement
	err := sc.Scan(&st.ID, &st.PayerOrg, &st.PayerSubject, &st.From, &st.Nonce, &st.Resource, &st.Payee,
		&st.PayeeOrg, &st.PayeeSubject, &st.Agent, &st.Category, &st.Amount, &st.Network, &st.SettledVia, &st.TxHash,
		&st.Settled, &st.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// get fetches a settlement by id (the receipt lookup + the dedup read).
func (s *store) get(ctx context.Context, id string) (*Settlement, bool, error) {
	st, err := scanSettlement(s.db.QueryRowContext(ctx,
		`SELECT `+settlementCols+` FROM settlements WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get settlement: %w", err)
	}
	return st, true, nil
}

// pending lists claims that were never completed, oldest first — the sweep's whole
// input. `until` excludes rows still on a live request's path, so Reconcile never
// races the flow that owns them.
func (s *store) pending(ctx context.Context, until int64) ([]*Settlement, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+settlementCols+` FROM settlements
		   WHERE settled = 0 AND created_at <= $1 ORDER BY created_at`, until)
	if err != nil {
		return nil, fmt.Errorf("list pending settlements: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Settlement
	for rows.Next() {
		st, err := scanSettlement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending settlement: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// owed lists, soonest due first, the settled payments between two orgs whose
// statement is due by now and has not landed on the event plane.
func (s *store) owed(ctx context.Context, now int64, limit int) ([]owedStatement, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, payer_org, tries FROM settlements
		   WHERE settled = 1 AND stated = 0 AND payer_org != payee_org AND due <= $1
		   ORDER BY due, created_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list owed statements: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []owedStatement
	for rows.Next() {
		var o owedStatement
		if err := rows.Scan(&o.ID, &o.PayerOrg, &o.Tries); err != nil {
			return nil, fmt.Errorf("scan owed statement: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// landed clears one settlement's statement: the event plane has both parties' rows.
func (s *store) landed(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE settlements SET stated = 1 WHERE id = $1`, id); err != nil {
		return fmt.Errorf("mark stated: %w", err)
	}
	return nil
}

// retry records an attempt that did not land a statement, and when to try again.
func (s *store) retry(ctx context.Context, id string, tries, due int64) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE settlements SET tries = $1, due = $2 WHERE id = $3`, tries, due, id); err != nil {
		return fmt.Errorf("defer statement: %w", err)
	}
	return nil
}

// holding is what the rail keeps of one held authorization.
type holding struct {
	resource string // the one resource it pays
	given    bool   // given up: it pays nothing
	// payerOrg and payerSubject are the books and the wallet the amount was set
	// aside in, empty for a hold written before they were kept.
	payerOrg, payerSubject string
}

// payer is the account the hold set its amount aside in, and the zero Account for
// a hold that never recorded one.
func (h holding) payer() account.Account {
	if h.payerOrg == "" {
		return account.Account{}
	}
	return accountOf(h.payerOrg, h.payerSubject)
}

// accountOf reads an account back from the books and the wallet key it was stored
// as: the org's own account when the key is the org, a person's when it is
// "<org>/<name>", and nothing for a key outside those books.
func accountOf(org, subject string) account.Account {
	if subject == "" || subject == org {
		return account.Org(org)
	}
	if o, name, ok := strings.Cut(subject, "/"); ok && o == org {
		return account.Person(org, name)
	}
	return account.Account{}
}

// hold reserves the settlement id an authorization settles under for one
// resource, until the authorization expires, and records the account the amount is
// set aside in. It answers what the id is held for: the resource asked, or another
// it was already held for.
func (s *store) hold(ctx context.Context, id, resource string, until int64, payer account.Account) (holding, error) {
	// A hold that was settled or given up — returned, then marked — outlives its
	// authorization by a day and no longer: after it expires nothing can settle it
	// anywhere. One still holding money is kept until it is given up, so a void
	// however late finds it and returns what it set aside.
	if err := dialect.Text(id, resource, payer.Org(), payer.Subject()); err != nil {
		return holding{}, fmt.Errorf("hold authorization: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM holds WHERE until < $1 AND (given = 1 OR id IN
		(SELECT id FROM settlements WHERE settled = 1))`, nowUnix()-86400); err != nil {
		return holding{}, fmt.Errorf("hold authorization: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO holds (id, resource, until, payer_org, payer_subj) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO NOTHING`,
		id, resource, until, payer.Org(), payer.Subject()); err != nil {
		return holding{}, fmt.Errorf("hold authorization: %w", err)
	}
	h, _, err := s.held(ctx, id)
	return h, err
}

// giveUp marks an authorization held for resource as given up, holding its id for
// that resource first if nothing did.
func (s *store) giveUp(ctx context.Context, id, resource string, until int64) error {
	if err := dialect.Text(id, resource); err != nil {
		return fmt.Errorf("give up authorization: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO holds (id, resource, until, given) VALUES ($1,$2,$3,1)
		ON CONFLICT(id) DO UPDATE SET given = 1 WHERE holds.resource = excluded.resource`, id, resource, until); err != nil {
		return fmt.Errorf("give up authorization: %w", err)
	}
	return nil
}

// held answers what an authorization's settlement id is held for.
func (s *store) held(ctx context.Context, id string) (holding, bool, error) {
	var h holding
	err := s.db.QueryRowContext(ctx, `SELECT resource, given, payer_org, payer_subj FROM holds WHERE id = $1`, id).
		Scan(&h.resource, &h.given, &h.payerOrg, &h.payerSubject)
	if errors.Is(err, sql.ErrNoRows) {
		return holding{}, false, nil
	}
	if err != nil {
		return holding{}, false, fmt.Errorf("read hold: %w", err)
	}
	return h, true, nil
}

// paidBy lists the settled payments one account made, newest first, within
// [from, to) unix seconds, at most limit. It is scoped to the WALLET and not only to
// its books: the signup org's books hold every one of its members, and an org-wide
// read handed each of them everyone else's purchases.
func (s *store) paidBy(ctx context.Context, payer account.Account, from, to int64, limit int) ([]*Settlement, error) {
	return s.settled(ctx, `payer_org = $1 AND (payer_subj = $2 OR (payer_subj = '' AND payer_org = $2))`,
		[]any{payer.Org(), payer.Subject()}, from, to, limit)
}

// paidTo lists the settled payments one org received, newest first, within
// [from, to) unix seconds, at most limit.
func (s *store) paidTo(ctx context.Context, org string, from, to int64, limit int) ([]*Settlement, error) {
	return s.settled(ctx, `payee_org = $1`, []any{org}, from, to, limit)
}

// settled is the one read both sides of a receipt list share: the rows matching
// who, settled, in the window, newest first.
func (s *store) settled(ctx context.Context, who string, args []any, from, to int64, limit int) ([]*Settlement, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+settlementCols+` FROM settlements
		   WHERE `+who+` AND settled = 1 AND created_at >= $`+strconv.Itoa(len(args)+1)+` AND created_at < $`+strconv.Itoa(len(args)+2)+`
		   ORDER BY created_at DESC LIMIT $`+strconv.Itoa(len(args)+3), append(args, from, to, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list settlements: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []*Settlement{}
	for rows.Next() {
		st, err := scanSettlement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan settlement: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// getScoped fetches a settlement by id but ONLY when payer paid it — the
// isolation boundary for the receipt-lookup API, so one tenant can never read
// another's settlement, and one member of the signup org never reads another's.
func (s *store) getScoped(ctx context.Context, payer account.Account, id string) (*Settlement, bool, error) {
	st, found, err := s.get(ctx, id)
	if err != nil || !found || !st.paidBy(payer) {
		return nil, false, err
	}
	return st, true, nil
}
