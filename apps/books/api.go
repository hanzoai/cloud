package books

// api.go — the /v1/books read surface + the ingestion trigger. Every op resolves the
// caller's OWN org from the validated principal (typed.go's tenant) and reads ONLY that
// org's books. Money is never cached: the group's noStore carries Cache-Control for the
// whole surface, matching the finance surface.
//
// Each op's In IS its query string — one field per parameter, named by its json tag — and
// each Out IS the JSON the route has always answered with. A list route answers a bare
// JSON array, so its Out is a NAMED slice: a name is what makes the response describable
// (an anonymous type has none, and zip documents it as no content at all), and the wire
// stays the array it has always been.

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// ledgerIn selects which of the caller's two books to read. It is the whole input of the
// reads that take no other parameter.
type ledgerIn struct {
	// Sandbox reads the org's SANDBOX ledger when it is exactly "true"; anything else
	// reads the live one.
	Sandbox string `json:"sandbox"`
}

// periodIn is a report over a posting-time window, on the caller's chosen ledger.
type periodIn struct {
	// Sandbox reads the org's SANDBOX ledger when it is exactly "true".
	Sandbox string `json:"sandbox"`
	// From is the RFC3339 start of the window, exclusive. Empty means all time.
	From string `json:"from"`
	// To is the RFC3339 end of the window, inclusive. Empty means up to now.
	To string `json:"to"`
}

// accountList is the chart of accounts as the route answers it: a bare JSON array.
type accountList []Account

// glList is a page of GL Entry rows as the route answers it: a bare JSON array.
type glList []GLRow

// ListAccounts returns the org's chart of accounts.
// It is the seeded fixed chart every posting key in the ledger refers to.
//
// Example: {"sandbox": "false"}
func (o booksOps) listAccounts(ctx context.Context, in *ledgerIn) (*accountList, error) {
	st, err := o.ledger(ctx, in.Sandbox, "view books")
	if err != nil {
		return nil, err
	}
	accts, err := st.listAccounts(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "books accounts read failed")
	}
	out := accountList(accts)
	return &out, nil
}

// glIn is a page of the general ledger.
type glIn struct {
	// Sandbox reads the org's SANDBOX ledger when it is exactly "true".
	Sandbox string `json:"sandbox"`
	// Limit caps how many rows come back; 500 when absent or not positive.
	Limit int `json:"limit"`
}

// ListGL returns the org's most recent GL Entry rows, newest first. This is the raw
// double-entry detail behind every statement: one row per leg, with its debit, credit,
// posting time and the source that booked it.
//
// Example: {"limit": 100}
func (o booksOps) listGL(ctx context.Context, in *glIn) (*glList, error) {
	st, err := o.ledger(ctx, in.Sandbox, "view books")
	if err != nil {
		return nil, err
	}
	rows, err := st.listGL(ctx, limitOr(in.Limit, 500))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "books gl read failed")
	}
	out := glList(rows)
	return &out, nil
}

// TrialBalance returns the org's trial balance over an optional [from, to] window.
// The window is RFC3339 posting times, and the answer carries the opening/closing
// columns and the TotalDebit == TotalCredit proof that the books balance.
//
// Example: {"from": "2026-01-01T00:00:00Z", "to": "2026-03-31T23:59:59Z"}
func (o booksOps) trialBalance(ctx context.Context, in *periodIn) (*TrialBalance, error) {
	st, err := o.ledger(ctx, in.Sandbox, "view books")
	if err != nil {
		return nil, err
	}
	tb, err := trialBalance(ctx, st, in.From, in.To)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "books trial-balance failed")
	}
	return &tb, nil
}

// ProfitAndLoss returns the org's accrual-basis Profit & Loss.
// The optional (from, to] window is RFC3339 posting times; the answer is recognized
// revenue, matched cost, and the net.
//
// Example: {"from": "2026-01-01T00:00:00Z", "to": "2026-03-31T23:59:59Z"}
func (o booksOps) profitAndLoss(ctx context.Context, in *periodIn) (*PnL, error) {
	st, err := o.ledger(ctx, in.Sandbox, "view books")
	if err != nil {
		return nil, err
	}
	p, err := profitAndLoss(ctx, st, in.From, in.To)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "books pnl failed")
	}
	return &p, nil
}

// asOfIn is a point-in-time statement: everything posted up to and including To.
type asOfIn struct {
	// Sandbox reads the org's SANDBOX ledger when it is exactly "true".
	Sandbox string `json:"sandbox"`
	// To is the RFC3339 instant the statement is struck as of. Empty means all time.
	To string `json:"to"`
}

// BalanceSheet returns the org's Balance Sheet as of `to` (empty = all time).
// It carries the Assets == Liabilities + Equity equation proof.
//
// Example: {"to": "2026-03-31T23:59:59Z"}
func (o booksOps) balanceSheet(ctx context.Context, in *asOfIn) (*BalanceSheet, error) {
	st, err := o.ledger(ctx, in.Sandbox, "view books")
	if err != nil {
		return nil, err
	}
	bs, err := balanceSheet(ctx, st, in.To)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "books balance-sheet failed")
	}
	return &bs, nil
}

// exportIn is the financial-package export over a posting-time window.
type exportIn struct {
	// Sandbox reads the org's SANDBOX ledger when it is exactly "true".
	Sandbox string `json:"sandbox"`
	// From is the RFC3339 start of the window, exclusive. Empty means all time.
	From string `json:"from"`
	// To is the RFC3339 end of the window, inclusive. Empty means up to now.
	To string `json:"to"`
	// Format is the export encoding. Only "json" is supported; empty means json.
	Format string `json:"format"`
	// Limit caps the GL detail rows included as the audit trail; 5000 when absent
	// or not positive.
	Limit int `json:"limit"`
}

// ExportPackage returns the complete financial package for the caller's org.
// Over (from, to] it assembles the trial balance, the P&L, the balance sheet and the
// GL detail behind them — the four statements a tax preparer or an investor asks
// for, read out of the one ledger at once so they cannot disagree with each other.
//
// Example: {"from": "2026-01-01T00:00:00Z", "to": "2026-12-31T23:59:59Z", "format": "json"}
func (o booksOps) exportPackage(ctx context.Context, in *exportIn) (*FinancialPackage, error) {
	// Tenant before format, the order the route has always refused in: an
	// anonymous caller is 401 whatever it asks for, and never learns from a 400
	// that the parameter exists.
	org, err := tenant(ctx, "export books")
	if err != nil {
		return nil, err
	}
	if in.Format != "" && in.Format != "json" {
		return nil, zip.Errorf(http.StatusBadRequest, "books export supports format=json only")
	}
	st, err := o.s.State.storeFor(org, sandboxOf(in.Sandbox))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	pkg, err := financialPackage(ctx, st, org, in.From, in.To, limitOr(in.Limit, 5000))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "books export failed")
	}
	return &pkg, nil
}

// syncIn takes nothing off the wire: a sync acts on the caller's own org, so there is
// nothing left to name. It is the In of BOTH syncs — the commerce ingest here and the
// bank connector pull (bank_api.go) — because "no input" is one shape, and two empty
// structs would be one concept with two schema names in the published document.
type syncIn struct{}

// syncTally reports how many new vouchers each ledger posted.
type syncTally struct {
	// Live is the number of vouchers newly posted to the live ledger.
	Live int `json:"live"`
	// Sandbox is the number newly posted to the sandbox ledger.
	Sandbox int `json:"sandbox"`
}

// Sync ingests the caller's OWN org from commerce into BOTH ledgers (live and sandbox)
// and reports how many new vouchers posted to each. It is idempotent — money that has
// already been booked posts nothing on a repeat — and it is read-only against commerce:
// it never mints a deposit, a credit or a payout, only the accounting twin of money that
// already moved.
func (o booksOps) sync(ctx context.Context, _ *syncIn) (*syncTally, error) {
	org, err := tenant(ctx, "sync books")
	if err != nil {
		return nil, err
	}
	live, err := o.s.State.syncLedger(ctx, org, false)
	if err != nil {
		o.s.State.log.Warn("books sync (live) failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "books sync failed")
	}
	sandbox, err := o.s.State.syncLedger(ctx, org, true)
	if err != nil {
		o.s.State.log.Warn("books sync (sandbox) failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "books sync failed")
	}
	return &syncTally{Live: live, Sandbox: sandbox}, nil
}

// metricsHandler returns the org's deterministic SaaS-metrics snapshot over an optional
// (?from, ?to] window — MRR/ARR/revenue/COGS/burn/margin/cash/deferred/runway — as the raw
// int64-cent figures AND their formatted forms. It is the ONE grounded read the unified
// /v1/ask advisor replays in-process (under the caller's own creds), so a figure it surfaces
// is the ledger, not a model's guess. Read-only, no-store, scoped to the caller's own org.
//
// STILL UNTYPED, and the reason is MetricsResponse: it EMBEDS Metrics, and Go flattens an
// embedded struct onto the wire while zip v1.18.3's schema walk does not — it emits the
// embedded type as a nested property. Typing this op would publish a response schema no
// answer of this route matches, and every generated SDK would model it wrong. The route is
// correct; the generator has to learn embedding before the description can be true.
func metricsHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view books")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	m, err := computeMetrics(c.Context(), st, c.Query("from"), c.Query("to"))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books metrics failed")
	}
	return booksJSON(c, MetricsResponse{Metrics: m, Figures: metricsFigures(m)})
}

// booksJSON writes a books payload for the handlers still untyped. The no-store header it
// used to set moved to the group (noStore, typed.go), because a typed op has no response
// to set one on and money must never be cached on either path.
func booksJSON(c *zip.Ctx, v any) error { return c.JSON(http.StatusOK, v) }
