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
