// Copyright © 2026 Hanzo AI. MIT License.

package finance

import (
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	ledger "github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// Deposit answers POST /v1/admin/finance/deposit — a SuperAdmin credit into an ARBITRARY
// subject's native prepaid wallet. Where the credit-grant + the backfill fund the org POOL
// (subject == the org slug), this funds a SPECIFIC wallet: an org pool ("hanzo") or a human
// ("hanzo/z" → wallet:z in orgs/hanzo/finance.db). It posts a balanced double-entry credit
// on the ONE finance ledger and is additive (no idempotency ref, so distinct grants stack).
// SuperAdmin only (core.Guard).
//
// Params arrive as a JSON body OR query: org, subject, cents (>0), notes?, currency? (usd).
// 503 when no finance ledger is co-resident on this deployment; 400 on a missing/invalid
// arg or non-positive cents.
func Deposit(s *cloud.Service[core.State], c *zip.Ctx) error {
	fin := ledger.Current()
	if fin == nil {
		return zip.Errorf(503, "finance ledger not co-resident on this deployment")
	}

	// Body is optional — params may arrive as query instead — so a non-JSON/empty body is
	// not an error; the query values below fill or override it.
	var body struct {
		Org      string `json:"org"`
		Subject  string `json:"subject"`
		Cents    int64  `json:"cents"`
		Notes    string `json:"notes"`
		Currency string `json:"currency"`
	}
	_ = c.Bind(&body)

	org := pick(c.Query("org"), body.Org)
	subject := pick(c.Query("subject"), body.Subject)
	notes := pick(c.Query("notes"), body.Notes)
	currency := strings.ToLower(pick(c.Query("currency"), body.Currency))
	if currency == "" {
		currency = "usd"
	}

	cents := body.Cents
	if q := strings.TrimSpace(c.Query("cents")); q != "" {
		n, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			return zip.ErrBadRequest("cents must be an integer")
		}
		cents = n
	}

	if org == "" || subject == "" {
		return zip.ErrBadRequest("org and subject are required")
	}
	if cents <= 0 {
		return zip.ErrBadRequest("cents must be positive")
	}

	entryID, err := fin.Deposit(c.Context(), types.DepositInput{
		Org:      org,
		Subject:  subject,
		Cents:    cents,
		Currency: currency,
		Notes:    notes,
	})
	if err != nil {
		return core.Fail(c, "finance deposit: "+err.Error())
	}
	return core.OK(c, map[string]any{
		"org":     org,
		"subject": subject,
		"cents":   cents,
		"entryId": entryID,
	})
}

// pick returns the first non-empty, trimmed value — a query param preferred over the body,
// so either channel funds a wallet with one handler.
func pick(query, body string) string {
	if q := strings.TrimSpace(query); q != "" {
		return q
	}
	return strings.TrimSpace(body)
}
