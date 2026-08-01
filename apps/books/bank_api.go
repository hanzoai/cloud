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
	"github.com/hanzoai/cloud/openapi"
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
	// which stays on the URL (sandboxFrom, typed.go) rather than moving into the body a
	// typed POST's In is documented as.
	zip.Post(g, "/sync", o.syncBank)

	// UNTYPED, each for a stated reason. import takes RAW statement bytes (OFX/QFX/CSV)
	// as its body: zip's typed decoder unmarshals the body as JSON, so an OFX upload
	// would answer 400 instead of importing — there is no JSON In that names a file.
	// token and exchange always answer 501, and a typed op publishes a SUCCESS
	// response (its Out schema, or the 204 a void op declares) that neither has ever
	// sent — an invented contract every generated SDK would carry a return type for.
	app.Post("/v1/books/bank/import", cloud.Handle(s, bankImportHandler))
	app.Post("/v1/books/bank/token", cloud.Handle(s, bankTokenHandler))
	app.Post("/v1/books/bank/exchange", cloud.Handle(s, bankExchangeHandler))
}

// import cannot be a typed op, but it can still SAY what it takes: its request is
// [openapi.Binary] — an OFX/QFX/CSV statement, bytes as uploaded — and its response is
// the BankTally the handler marshals on success. Without this the route published no
// request and no response at all, indistinguishable in the document from one that
// takes neither, so every generated SDK offered a statement import with nowhere to put
// the statement.
//
// token and exchange are declared NOWHERE, deliberately: they answer 501
// unconditionally (see below), so there is no success body to state and no request they
// read. A declaration for either would be invention, not description.
//
// init, not bankRoutes: Register panics on a duplicate declaration, and bankRoutes
// runs once per Mount.
// Declaring the wire is not the same as explaining it. Register says what the bytes
// are; Describe is the prose a typed op would have carried in its doc comment, and
// without it these three publish an operationId and nothing else — an SDK method
// that cannot say what it does, and a CLI command with no help. Stated beside the
// declaration so the two cannot drift apart.
func init() {
	openapi.Register("/v1/books/bank/import", "POST", openapi.Binary{}, BankTally{})
	openapi.Describe("/v1/books/bank/import", http.MethodPost,
		"Import a bank statement file into your books",
		"Takes a bank statement as RAW BYTES — the file exactly as downloaded, OFX, QFX or "+
			"CSV, not wrapped in JSON — parses every row, books it against the caller org's "+
			"own ledger, and answers the tally: how many rows were seen, how many vouchers "+
			"posted, how many inflows reconciled, how many raised a question, how many were "+
			"own-account transfers, and how many were skipped.\n\n"+
			"RE-IMPORTING THE SAME STATEMENT DOES NOT DOUBLE-BOOK. Every row goes through the "+
			"same posting choke point every other source uses, keyed idempotently, so an "+
			"overlapping statement — the usual case, since exports overlap at the month "+
			"boundary — lands its new rows and counts the rest as skipped. Skipped is the "+
			"number to read on a second import.\n\n"+
			"It is READ-ONLY against the bank: this ingests, it never sends money. Scoped to "+
			"the caller's own org from the validated principal, and refused without one; "+
			"`sandbox=true` writes the org's sandbox ledger instead of its real books. An "+
			"empty body is a 400, and a file the parser cannot read is a 400 carrying the "+
			"parser's reason rather than a partial import. On a deployment whose import "+
			"parser is not built, this answers 501 rather than mishandling the file.")
	openapi.Describe("/v1/books/bank/token", http.MethodPost,
		"Begin connecting a bank account (not yet available)",
		"ANSWERS 501 UNCONDITIONALLY. It is the intended first hop of the bank-linking "+
			"handshake — mint the short-lived session token a browser hands to the provider's "+
			"link widget — and nothing on the HTTP path reaches an implementation today.\n\n"+
			"The connectors behind it are written and tested; only the wiring is missing, so "+
			"an org cannot connect a bank through the API at all. Until that lands, bank data "+
			"reaches the books by statement import.\n\n"+
			"It is documented as refusing rather than declared with a success body precisely "+
			"because it has never sent one. A response schema here would be invention: every "+
			"generated SDK would carry a return type for a call that has only ever failed. A "+
			"caller with no principal gets 401 before the 501.")
	openapi.Describe("/v1/books/bank/exchange", http.MethodPost,
		"Finish connecting a bank account (not yet available)",
		"ANSWERS 501 UNCONDITIONALLY. It is the intended second hop of the bank-linking "+
			"handshake — trade the provider's short-lived public token for the durable access "+
			"credential and seal that credential into KMS — and nothing on the HTTP path "+
			"reaches an implementation today.\n\n"+
			"The durable bank credential is the reason this hop exists: it is meant to be "+
			"sealed server-side and never handed back to the caller. Since the route never "+
			"succeeds, no credential is stored by it and no bank is connected through it.\n\n"+
			"Documented as refusing rather than declared with a success body, for the same "+
			"reason as the first hop: it has never sent one, and stating a shape it has never "+
			"produced would put a return type in every SDK for a call that always fails. A "+
			"caller with no principal gets 401 before the 501.")
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
	tally, err := o.s.State.syncBank(ctx, org, sandboxFrom(ctx))
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
func bankTokenHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return zip.ErrUnauthorized("sign in to link a bank")
	}
	return zip.Errorf(http.StatusNotImplemented, "bank token not yet available")
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
