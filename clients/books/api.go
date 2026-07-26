package books

// api.go — the /v1/books read surface + the ingestion trigger. Every handler resolves
// the caller's OWN org from the validated principal and reads ONLY that org's books.
// Money is never cached (no-store), matching the finance surface.

import (
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// accountsHandler returns the org's chart of accounts (the seeded fixed chart).
func accountsHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view books")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	accts, err := st.listAccounts(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books accounts read failed")
	}
	return booksJSON(c, accts)
}

// metricsHandler returns the org's deterministic SaaS-metrics snapshot over an optional
// (?from, ?to] window — MRR/ARR/revenue/COGS/burn/margin/cash/deferred/runway — as the raw
// int64-cent figures AND their formatted forms. It is the ONE grounded read the unified
// /v1/ask advisor replays in-process (under the caller's own creds), so a figure it surfaces
// is the ledger, not a model's guess. Read-only, no-store, scoped to the caller's own org.
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

// glHandler returns the org's most recent GL Entry rows (newest first, ?limit=).
func glHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view books")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	rows, err := st.listGL(c.Context(), atoiDefault(c.Query("limit"), 500))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books gl read failed")
	}
	return booksJSON(c, rows)
}

// trialBalanceHandler returns the org's trial balance over an optional [?from, ?to]
// window (RFC3339 posting times), including the opening/closing columns and the
// TotalDebit==TotalCredit balance proof.
func trialBalanceHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view books")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	tb, err := trialBalance(c.Context(), st, c.Query("from"), c.Query("to"))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books trial-balance failed")
	}
	return booksJSON(c, tb)
}

// pnlHandler returns the org's accrual-basis Profit & Loss over an optional (?from, ?to]
// window (RFC3339 posting times): recognized revenue minus matched cost, and the net.
func pnlHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view books")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	p, err := profitAndLoss(c.Context(), st, c.Query("from"), c.Query("to"))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books pnl failed")
	}
	return booksJSON(c, p)
}

// balanceSheetHandler returns the org's Balance Sheet as of ?to ("" = all time), with the
// Assets == Liabilities + Equity equation proof.
func balanceSheetHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view books")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	bs, err := balanceSheet(c.Context(), st, c.Query("to"))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books balance-sheet failed")
	}
	return booksJSON(c, bs)
}

// exportHandler returns the complete financial package (trial balance + P&L + balance sheet
// + GL) for the caller's org over (?from, ?to]. ?format=json (the default and only format).
func exportHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to export books")
	}
	if f := c.Query("format"); f != "" && f != "json" {
		return zip.Errorf(http.StatusBadRequest, "books export supports format=json only")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	pkg, err := financialPackage(c.Context(), st, org, c.Query("from"), c.Query("to"), atoiDefault(c.Query("limit"), 5000))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books export failed")
	}
	return booksJSON(c, pkg)
}

// syncHandler ingests the caller's OWN org from commerce into BOTH ledgers (live +
// sandbox) and reports how many new vouchers posted. Idempotent — a repeat posts nothing.
func syncHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to sync books")
	}
	live, err := s.State.syncLedger(c.Context(), org, false)
	if err != nil {
		s.State.log.Warn("books sync (live) failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "books sync failed")
	}
	sandbox, err := s.State.syncLedger(c.Context(), org, true)
	if err != nil {
		s.State.log.Warn("books sync (sandbox) failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "books sync failed")
	}
	return booksJSON(c, map[string]int{"live": live, "sandbox": sandbox})
}

// booksJSON writes a books payload with no-store (per-org money must never be cached).
func booksJSON(c *zip.Ctx, v any) error {
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, v)
}

// atoiDefault parses a positive int query param, falling back to dflt on empty/invalid.
func atoiDefault(s string, dflt int) int {
	n := 0
	if s == "" {
		return dflt
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return dflt
		}
		n = n*10 + int(r-'0')
	}
	if n == 0 {
		return dflt
	}
	return n
}
