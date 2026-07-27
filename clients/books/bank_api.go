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
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// bankRoutes registers the bank surface on the existing books app (called from routes()).
func bankRoutes(app cloud.Router, s *cloud.Service[*state]) {
	app.Post("/v1/books/bank/import", cloud.Handle(s, bankImportHandler))
	app.Post("/v1/books/bank/sync", cloud.Handle(s, bankSyncHandler))
	app.Get("/v1/books/bank/transactions", cloud.Handle(s, bankTxnsHandler))
	app.Get("/v1/books/bank/unreconciled", cloud.Handle(s, bankUnreconciledHandler))
	// Link plumbing the Plaid/Teller connectors implement — stubbed 501 until then so the
	// route exists and the frontend contract is stable.
	app.Post("/v1/books/bank/link-token", cloud.Handle(s, bankLinkTokenHandler))
	app.Post("/v1/books/bank/exchange", cloud.Handle(s, bankExchangeHandler))
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

// bankSyncHandler pulls every pull-based connector (Plaid/Teller) for the caller's org,
// mapping + posting each transaction idempotently and advancing per-connector cursors.
func bankSyncHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to sync bank")
	}
	tally, err := s.State.syncBank(c.Context(), org, sandboxQuery(c))
	if err != nil {
		s.State.log.Warn("books bank sync failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "bank sync failed")
	}
	return booksJSON(c, tally)
}

// bankTxnsHandler returns the org's normalized bank transactions (newest first, ?limit=).
func bankTxnsHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view bank transactions")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	rows, err := st.listBankTxns(c.Context(), atoiDefault(c.Query("limit"), 500))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "bank transactions read failed")
	}
	return booksJSON(c, rows)
}

// bankUnreconciledHandler returns the org's unmatched inflows and their open clarifying
// questions — the queue a human answers so an unexplained deposit is never guessed into
// revenue.
func bankUnreconciledHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view unreconciled bank items")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	txns, err := st.listUnreconciled(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "bank unreconciled read failed")
	}
	questions, err := st.listQuestions(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "bank questions read failed")
	}
	return booksJSON(c, map[string]any{"transactions": txns, "questions": questions})
}

// bankLinkTokenHandler / bankExchangeHandler are the Plaid/Teller link-flow endpoints. They
// exist here so the route contract is stable; the connectors implement the token exchange
// (storing the resulting access_token in KMS, never books.db).
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
