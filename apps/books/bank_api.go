package books

// bank_api.go — the /v1/books/bank surface: file import, connector sync, the transaction
// and unreconciled reads, and the Plaid/Teller link plumbing (stubs the connectors fill).
// Every handler resolves the caller's OWN org from the validated principal and touches
// ONLY that org's books.db. There is NO money-movement endpoint here — the bank engine is
// read-only against the bank; it ingests, it never sends.

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// bankRoutes registers the bank surface on the existing books app (called from routes()).
// The group is built here, from a literal prefix, because that is what makes each typed
// op's path — and therefore its identity in every projection — resolvable from this file.
// It sits under the /v1/books group's Bridge + noStore, which match by prefix.
func bankRoutes(app cloud.Router, s *cloud.Service[*state]) {
	g := app.Group("/v1/books/bank")
	o := booksOps{s: s}
	zip.Get(g, "/transactions", o.listBankTxns)
	zip.Get(g, "/unreconciled", o.listUnreconciled)
	// The connector pull. It takes nothing off the wire but the ?sandbox selector,
	// which stays on the URL (query, typed.go) rather than moving into the body a
	// typed POST's In is documented as.
	zip.Post(g, "/sync", o.syncBank)

	// UNTYPED, each for a stated reason. import takes RAW statement bytes (OFX/QFX/CSV)
	// as its body: zip's typed decoder unmarshals the body as JSON, so an OFX upload
	// would answer 400 instead of importing — there is no JSON In that names a file.
	// link-token and exchange always answer 501, and a typed op publishes a SUCCESS
	// response (its Out schema, or the 204 a void op declares) that neither has ever
	// sent — an invented contract every generated SDK would carry a return type for.
	app.Post("/v1/books/bank/import", cloud.Handle(s, bankImportHandler))
	app.Post("/v1/books/bank/link-token", cloud.Handle(s, bankLinkTokenHandler))
	app.Post("/v1/books/bank/exchange", cloud.Handle(s, bankExchangeHandler))
}

// bankTxnList is the org's normalized bank rows as the route answers them: a bare array.
type bankTxnList []BankTxnRow

// unreconciledOut pairs the unmatched inflows with the open questions they raised.
type unreconciledOut struct {
	// Questions is the open clarifying question per unmatched inflow.
	Questions []BankQuestion `json:"questions"`
	// Transactions is every bank row still unmatched against the ledger.
	Transactions []BankTxnRow `json:"transactions"`
}

// bankLimitIn is a page of the org's bank rows.
type bankLimitIn struct {
	// Sandbox reads the org's SANDBOX ledger when it is exactly "true".
	Sandbox string `json:"sandbox"`
	// Limit caps how many rows come back; 500 when absent or not positive.
	Limit int `json:"limit"`
}

// ListBankTransactions returns the org's normalized bank transactions, newest first —
// every row the import and connector paths have ingested, with its amount in exact cents,
// its direction, and whether it has been matched to a voucher yet.
//
// Example: {"limit": 100}
func (o booksOps) listBankTxns(ctx context.Context, in *bankLimitIn) (*bankTxnList, error) {
	st, err := o.ledger(ctx, in.Sandbox, "view bank transactions")
	if err != nil {
		return nil, err
	}
	rows, err := st.listBankTxns(ctx, limitOr(in.Limit, 500))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "bank transactions read failed")
	}
	out := bankTxnList(rows)
	return &out, nil
}

// ListUnreconciled returns the org's unmatched bank inflows and their open clarifying
// questions — the queue a human answers so an unexplained deposit is never guessed into
// revenue.
//
// Example: {"sandbox": "false"}
func (o booksOps) listUnreconciled(ctx context.Context, in *ledgerIn) (*unreconciledOut, error) {
	st, err := o.ledger(ctx, in.Sandbox, "view unreconciled bank items")
	if err != nil {
		return nil, err
	}
	txns, err := st.listUnreconciled(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "bank unreconciled read failed")
	}
	questions, err := st.listQuestions(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "bank questions read failed")
	}
	return &unreconciledOut{Questions: questions, Transactions: txns}, nil
}

// bankImportHandler ingests an uploaded OFX/QFX/CSV file body: it parses it with the
// import connector, then maps + posts each row idempotently. The parser is the import
// connector's Importer capability; until that build lands the connector is not an Importer
// and this returns 501 rather than mishandling the file.
func bankImportHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to import bank statements")
	}
	imp := importer()
	if imp == nil {
		return zip.Errorf(http.StatusNotImplemented, "bank import parser not yet available")
	}
	body := c.Body()
	if len(body) == 0 {
		return zip.ErrBadRequest("empty upload")
	}
	txns, err := imp.Parse(body)
	if err != nil {
		return zip.ErrBadRequest("could not parse statement: " + err.Error())
	}
	tally, err := s.State.mapBank(c.Context(), org, sandboxQuery(c), txns)
	if err != nil {
		s.State.log.Warn("books bank import failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "bank import failed")
	}
	return booksJSON(c, tally)
}

// SyncBank pulls every connected bank (Plaid/Teller) for the caller's org, maps each
// fetched transaction to a posting and books it idempotently, then advances that
// connector's cursor so the next sync resumes where this one stopped. One connector's
// outage is skipped rather than failing the whole sync. It reports the batch: how many
// transactions were seen, how many vouchers posted, how many inflows reconciled against
// the processor clearing account, how many raised a question, how many were own-account
// transfers, and how many were already-processed no-ops. It is READ-ONLY against the
// bank — it ingests, it never sends money.
func (o booksOps) syncBank(ctx context.Context, _ *syncIn) (*BankTally, error) {
	org, err := tenant(ctx, "sync bank")
	if err != nil {
		return nil, err
	}
	tally, err := o.s.State.syncBank(ctx, org, sandboxOf(query(ctx, "sandbox")))
	if err != nil {
		o.s.State.log.Warn("books bank sync failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "bank sync failed")
	}
	return &tally, nil
}

// bankLinkTokenHandler / bankExchangeHandler are the Plaid/Teller link-flow endpoints, and
// both answer 501 unconditionally.
//
// The connectors BEHIND them are written: plaidConn.LinkToken mints the browser Link
// session's link_token, plaidConn.Exchange trades Link's public_token for the durable
// access_token and seals it into KMS, and tellerConn.exchange/linkConfig are the Teller
// half. Nothing on the HTTP path calls any of them — only their tests do — so the link
// flow is implemented end to end and unreachable, and no org can connect a bank through
// the API. Wiring them is a WIRE change (a route that has only ever answered 501 would
// start answering 200 with a body nothing has specified), which is why these two are the
// books routes left untyped: a typed op must state what it answers on success, and the
// honest answer today is that neither ever succeeds.
func bankLinkTokenHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return zip.ErrUnauthorized("sign in to link a bank")
	}
	return zip.Errorf(http.StatusNotImplemented, "bank link-token not yet available")
}

func bankExchangeHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return zip.ErrUnauthorized("sign in to link a bank")
	}
	return zip.Errorf(http.StatusNotImplemented, "bank exchange not yet available")
}

// importer returns the import connector as an Importer, or nil if its parsing capability is
// not yet built (the stub is not an Importer).
func importer() Importer {
	for _, cn := range connectors() {
		if cn.Name() == "import" {
			if imp, ok := cn.(Importer); ok {
				return imp
			}
			return nil
		}
	}
	return nil
}

// BankTally is the per-request summary of a bank ingest (import or sync).
type BankTally struct {
	Ingested   int `json:"ingested"`   // transactions seen
	Posted     int `json:"posted"`     // vouchers newly posted (outflow + reconciled)
	Reconciled int `json:"reconciled"` // inflows cleared against Square-clearing
	Questions  int `json:"questions"`  // unmatched inflows that raised a question
	Transfers  int `json:"transfers"`  // own-account moves recorded (no P&L)
	Skipped    int `json:"skipped"`    // already-processed idempotent no-ops
}

// mapBank maps + posts a batch of already-fetched transactions (the import path), then
// ships the store to its durable object best-effort so the postings survive a redeploy.
func (s *state) mapBank(ctx context.Context, org string, sandbox bool, txns []BankTxn) (BankTally, error) {
	st, err := s.storeFor(org, sandbox)
	if err != nil {
		return BankTally{}, err
	}
	tally, err := s.applyTxns(ctx, st, txns)
	if err != nil {
		return tally, err
	}
	s.syncDurable(org, sandbox, tally.Posted+tally.Reconciled)
	return tally, nil
}

// syncBank pulls every pull-based connector for one org, maps + posts, advances cursors,
// then ships the store to its durable object best-effort.
func (s *state) syncBank(ctx context.Context, org string, sandbox bool) (BankTally, error) {
	st, err := s.storeFor(org, sandbox)
	if err != nil {
		return BankTally{}, err
	}
	var tally BankTally
	for _, cn := range connectors() {
		cur, err := st.bankCursor(ctx, cn.Name())
		if err != nil {
			return tally, err
		}
		txns, next, err := cn.Fetch(ctx, org, cur)
		if err != nil {
			s.log.Warn("books bank connector fetch failed", "connector", cn.Name(), "org", org, "err", err)
			continue // one connector's outage never fails the whole sync
		}
		t, err := s.applyTxns(ctx, st, txns)
		if err != nil {
			return tally, err
		}
		addTally(&tally, t)
		if next != "" && next != cur {
			if err := st.setBankCursor(ctx, cn.Name(), next); err != nil {
				return tally, err
			}
		}
	}
	s.syncDurable(org, sandbox, tally.Posted+tally.Reconciled)
	return tally, nil
}

// applyTxns runs a batch of BankTxns through mapAndPost, tallying the outcome per case.
func (s *state) applyTxns(ctx context.Context, st *store, txns []BankTxn) (BankTally, error) {
	var tally BankTally
	for _, bt := range txns {
		res, err := mapAndPost(ctx, st, bt)
		if err != nil {
			return tally, err
		}
		tally.Ingested++
		switch {
		case res.Skipped:
			tally.Skipped++
		case res.Status == statusReconciled:
			tally.Reconciled++
			if res.VoucherPosted {
				tally.Posted++
			}
		case res.Status == statusPosted:
			if res.VoucherPosted {
				tally.Posted++
			}
		case res.QuestionRaised:
			tally.Questions++
		case res.Status == statusTransfer:
			tally.Transfers++
		}
	}
	return tally, nil
}

func addTally(dst *BankTally, t BankTally) {
	dst.Ingested += t.Ingested
	dst.Posted += t.Posted
	dst.Reconciled += t.Reconciled
	dst.Questions += t.Questions
	dst.Transfers += t.Transfers
	dst.Skipped += t.Skipped
}

// syncDurable ships one org's store to its durable object best-effort when postings landed,
// matching syncLedger's durability discipline.
func (s *state) syncDurable(org string, sandbox bool, posted int) {
	if posted <= 0 {
		return
	}
	store := s.live
	if sandbox {
		store = s.sandbox
	}
	if _, err := store.Sync(org, ""); err != nil {
		s.log.Warn("books bank durable sync degraded", "org", org, "sandbox", sandbox, "err", err)
	}
}
