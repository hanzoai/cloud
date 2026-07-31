// finance.go mounts the customer-facing, org-scoped /v1/finance/* PROJECTION of the
// commerce billing plane — the data the finance.hanzo.ai app shell and the console
// Finance module (both render the SAME @hanzo/finance-ui components) consume.
//
// WHY IT LIVES IN THE BILLING PACKAGE. These endpoints do not add a billing system;
// they PROJECT the one that already exists. The commerce wallet (prepaid balance +
// the deposit/withdraw transaction ledger + saved cards) is the single source of
// truth for what a customer holds and spends; /v1/finance/* is a per-org read-only
// VIEW of it in the shape the finance UI expects. This package already owns the
// commerceProxy + the per-org subject-pinning (the whole tenant-isolation argument in
// billing.go), so the finance reads reuse that machinery verbatim rather than standing
// up a second commerce client — one and only one commerce read path.
//
// DIVISION OF THE /v1/finance/* SURFACE. The surface has two data planes:
//
//   - the CUSTOMER's commerce wallet — balance, credits, usage, invoices,
//     payment-methods, ledger — mounted HERE (commerce-projected), and
//   - the PLATFORM's reserve fund — treasury — mounted in clients/treasury (the
//     ledger-of-record it owns).
//
// They compose on the same app under one prefix; the routes are disjoint so there is
// no collision. The treasury lane already serves GET /v1/finance/treasury (+ the
// admin reserve mutations); this lane adds the six commerce-projected reads.
//
// TENANT ISOLATION. Identical to the /v1/billing/* reads: the org is the VALIDATED IAM
// owner (principal.Org), the commerce billing subject is PINNED server-side to it on
// every subject key, and NO client-supplied subject/org is forwarded — so a caller
// reads ONLY its OWN org's wallet and can never widen scope.
//
// SHAPE. Unlike the /v1/billing/* passthrough, these RESHAPE commerce's raw wire into
// the typed finance contract (USD cents, optional-safe), because the finance UI's shape
// differs from commerce's (e.g. commerce `holds` → pendingCents; a withdraw → a signed
// ledger posting). The reshape is the whole value this lane adds over the raw ledger.
//
// HONEST GAPS. There is no per-org customer-invoice ledger in commerce today (it is a
// prepaid wallet: deposits + withdraws, not issued invoices), so /v1/finance/invoices
// returns an honest empty typed array rather than a fabricated figure. It becomes real
// the day an invoice ledger exists — the shape is already stable for the UI.
package billing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/money"
	"github.com/zap-proto/zip"
)

// mountFinance registers the six commerce-projected /v1/finance/* reads on the typed
// registry. Called from routes, so the finance surface ships with the billing surface
// (same commerceProxy). Every one RESHAPES commerce's wire into this package's own
// typed contract and answers 200, so every one is a typed op.
func mountFinance(o ops, reg zip.OpTarget) {
	zip.Get(reg, "/v1/finance/balance", o.financeBalance)                // prepaid available + holds + due
	zip.Get(reg, "/v1/finance/credits", o.financeCredits)                // credit grants (deposits)
	zip.Get(reg, "/v1/finance/usage", o.financeUsage)                    // metered spend over ?range=
	zip.Get(reg, "/v1/finance/invoices", o.financeInvoices)              // issued invoices (honest empty today)
	zip.Get(reg, "/v1/finance/payment-methods", o.financePaymentMethods) // masked saved cards (brand+last4)
	zip.Get(reg, "/v1/finance/ledger", o.financeLedger)                  // per-org double-entry postings over ?range=
}

// financeWindow is the one input the ranged finance reads take.
type financeWindow struct {
	// Range is how far back to look: 24h, 7d, 30d or 90d. Anything else, including
	// an absent value, means 30d.
	Range string `json:"range"`
}

// creditList is GET /v1/finance/credits on the wire — a bare array, named so the
// document can carry its element schema.
type creditList []financeCredit

// invoiceList is GET /v1/finance/invoices on the wire — a bare array.
type invoiceList []financeInvoice

// paymentMethodList is GET /v1/finance/payment-methods on the wire — a bare array.
type paymentMethodList []financePaymentMethod

// ledgerEntryList is GET /v1/finance/ledger on the wire — a bare array.
type ledgerEntryList []financeLedgerEntry

// ── the finance contract (typed, USD cents, matching @hanzo/finance-ui types.ts) ──

// financeBalanceView is the GET /v1/finance/balance response.
type financeBalanceView struct {
	// Currency is always "usd" — the wallet's only denomination today.
	Currency string `json:"currency"`
	// AvailableCents is the spendable prepaid balance, net of holds, never negative.
	AvailableCents int64 `json:"availableCents"`
	// PendingCents is prepaid funds authorized but not yet settled.
	PendingCents int64 `json:"pendingCents"`
	// DueCents is what the org owes; always 0 on a prepaid wallet.
	DueCents int64 `json:"dueCents"`
	// AsOf is when the balance was read, RFC3339 UTC.
	AsOf string `json:"asOf"`
}

// financeCredit is one row of GET /v1/finance/credits — a credit grant on the org's
// wallet. cents is positive (a grant); a renamed/absent field degrades to a safe zero.
type financeCredit struct {
	// ID is the ledger row this grant came from.
	ID string `json:"id"`
	// Label is the grant's note or tag, else "Credit".
	Label string `json:"label"`
	// Cents is the grant's magnitude, always positive.
	Cents int64 `json:"cents"`
	// GrantedAt is when the grant posted, RFC3339.
	GrantedAt string `json:"grantedAt,omitempty"`
	// ExpiresAt is when the grant lapses; absent when it does not.
	ExpiresAt string `json:"expiresAt,omitempty"`
	// RemainingCents is what is left of the grant when the ledger tracks it.
	RemainingCents *int64 `json:"remainingCents,omitempty"`
}

type usagePoint struct {
	// Date is the bucket's start, RFC3339 — hourly over a 24h range, else daily.
	Date string `json:"date"`
	// Cents is the spend inside that bucket.
	Cents int64 `json:"cents"`
}

type usageLine struct {
	// Label is the charge tag the spend was grouped under, else "Usage".
	Label string `json:"label"`
	// Units is how many charges made up the line.
	Units int64 `json:"units,omitempty"`
	// Tokens is the token count when the upstream reports one.
	Tokens int64 `json:"tokens,omitempty"`
	// Cents is the line's total spend.
	Cents int64 `json:"cents"`
}

// financeUsageView is the GET /v1/finance/usage?range= response.
type financeUsageView struct {
	// TotalCents is the metered spend across the whole window.
	TotalCents int64 `json:"totalCents"`
	// Currency is always "usd".
	Currency string `json:"currency"`
	// Start is the window's opening instant, RFC3339.
	Start string `json:"start,omitempty"`
	// End is the window's closing instant, RFC3339.
	End string `json:"end,omitempty"`
	// Series is the spend over time, oldest bucket first.
	Series []usagePoint `json:"series"`
	// Lines is the same spend broken out per charge tag.
	Lines []usageLine `json:"lines"`
}

// financeInvoice is one row of GET /v1/finance/invoices.
type financeInvoice struct {
	// ID is the invoice's identifier.
	ID string `json:"id"`
	// Number is the human-facing invoice number.
	Number string `json:"number,omitempty"`
	// Date is when the invoice was issued.
	Date string `json:"date,omitempty"`
	// DueDate is when payment is due.
	DueDate string `json:"dueDate,omitempty"`
	// Cents is the invoice total.
	Cents int64 `json:"cents"`
	// Currency is the invoice's denomination.
	Currency string `json:"currency"`
	// Status is the invoice's state, e.g. paid or open.
	Status string `json:"status,omitempty"`
	// URL links to the rendered invoice.
	URL string `json:"url,omitempty"`
}

// financePaymentMethod is one row of GET /v1/finance/payment-methods — the MASKED
// descriptor only. A PAN/CVV/gateway token is never present in this shape.
type financePaymentMethod struct {
	// ID is the saved method's identifier at the payment processor.
	ID string `json:"id"`
	// Type is the instrument kind, e.g. "card".
	Type string `json:"type,omitempty"`
	// Brand is the card network, e.g. visa.
	Brand string `json:"brand,omitempty"`
	// Last4 is at most the trailing four digits — the only digits ever surfaced.
	Last4 string `json:"last4,omitempty"`
	// ExpMonth is the expiry month, 1-12.
	ExpMonth int `json:"expMonth,omitempty"`
	// ExpYear is the four-digit expiry year.
	ExpYear int `json:"expYear,omitempty"`
	// IsDefault marks the method a charge falls back to.
	IsDefault bool `json:"isDefault,omitempty"`
}

// financeLedgerEntry is one posting of GET /v1/finance/ledger?range= — a signed move
// on the org's wallet (deposit positive, withdraw negative).
type financeLedgerEntry struct {
	// ID is the ledger row.
	ID string `json:"id"`
	// Date is when the posting landed, RFC3339.
	Date string `json:"date,omitempty"`
	// Account is the org-scoped book the posting hit: "credits:<org>" or "usage:<org>".
	Account string `json:"account,omitempty"`
	// Description is the posting's note, tag, or its type.
	Description string `json:"description,omitempty"`
	// Cents is the signed move: positive for a deposit, negative for a withdraw.
	Cents int64 `json:"cents"`
	// Currency is the posting's denomination, lowercased.
	Currency string `json:"currency"`
	// BalanceCents is the running balance after the posting, when known.
	BalanceCents *int64 `json:"balanceCents,omitempty"`
}

// ── commerce wire shapes (only the fields we project) ──

// commerceBalance is commerce GET /v1/billing/balance ({balance,holds,available} cents).
//
// Account names WHICH wallet the cents belong to — the billing subject this binary
// resolved with the one rule (account.Payer), the same subject the ai spend gate debits
// and a top-up credits. It is cloud-resolved, never decoded from upstream (commerce does
// not send it), so it is empty on the split-deploy proxy path and omitted from the JSON
// there rather than rendered as a blank account.
//
// It exists because "which account am I funding?" had no answer a client could trust: a
// browser could only guess by decoding its own token, and a guess that disagrees with the
// server is exactly how money lands in an account the gate never reads. Echoing the
// resolved subject lets a checkout SHOW the payer before the customer pays, from the same
// resolution that will actually be credited.
type commerceBalance struct {
	Balance   int64  `json:"balance"`
	Holds     int64  `json:"holds"`
	Available int64  `json:"available"`
	Account   string `json:"account,omitempty"`
}

// commerceTxn is one commerce ledger row (GET /v1/billing/transactions). Type is
// "deposit" (a credit/grant) or "withdraw" (usage/consumption); Amount is the magnitude
// in cents. This ONE row shape backs credits, usage, and ledger — a single upstream read
// projected three ways, never a second meter.
type commerceTxn struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Amount    int64  `json:"amount"`
	Currency  string `json:"currency"`
	Tags      string `json:"tags"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"createdAt"`
}

// commercePaymentMethod tolerates both the flat descriptor and a nested `card` object,
// so a masked card reshapes regardless of which shape commerce's portal returns.
type commercePaymentMethod struct {
	ID              string `json:"id"`
	PaymentMethodID string `json:"paymentMethodId"`
	Type            string `json:"type"`
	Brand           string `json:"brand"`
	Last4           string `json:"last4"`
	ExpMonth        int    `json:"expMonth"`
	ExpYear         int    `json:"expYear"`
	IsDefault       bool   `json:"isDefault"`
	Default         bool   `json:"default"`
	Card            struct {
		Brand    string `json:"brand"`
		Network  string `json:"network"`
		Last4    string `json:"last4"`
		LastFour string `json:"last_four"`
		ExpMonth int    `json:"exp_month"`
		ExpYear  int    `json:"exp_year"`
	} `json:"card"`
}

// ── handlers ──

// financeBalance answers the caller org's prepaid wallet: what is spendable now,
// what is held, and what is owed (always 0 — the wallet is prepaid). It is the SAME
// wallet the spend gate reads, so the number shown is the number that admits or
// refuses a request.
//
// Response: {"currency": "usd", "availableCents": 998800, "pendingCents": 0, "dueCents": 0, "asOf": "2026-07-29T18:00:00Z"}
func (o ops) financeBalance(ctx context.Context, _ *noArgs) (*financeBalanceView, error) {
	c, org, err := caller(ctx, "finance")
	if err != nil {
		return nil, err
	}
	// The ONE balance read (balance.go) — the same wallet /v1/billing/balance answers, so
	// the two surfaces can never disagree. Co-resident this is the finance ledger; only a
	// split deploy falls through to the commerce S2S read below.
	if cents, coResident, err := availableCents(ctx, org, subjectFor(c, org)); err != nil {
		o.Log.Warn("finance balance read failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	} else if coResident {
		return financeOut(c, &financeBalanceView{
			Currency:       "usd",
			AvailableCents: cents,
			PendingCents:   0,
			DueCents:       0,
			AsOf:           time.Now().UTC().Format(time.RFC3339),
		})
	}
	if !o.State.commerce.configured() {
		return nil, zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	var b commerceBalance
	if err := financeGet(o.Service, c, "/v1/billing/balance", org, url.Values{"currency": {"usd"}}, &b); err != nil {
		return nil, err
	}
	return financeOut(c, &financeBalanceView{
		Currency:       "usd",
		AvailableCents: spendableCents(b),
		PendingCents:   b.Holds,
		DueCents:       0,
		AsOf:           time.Now().UTC().Format(time.RFC3339),
	})
}

// spendableCents is what the finance UI shows as spendable from a commerce balance:
// commerce's reported `available` when it populates that field, else balance NET OF
// holds when commerce reports only a balance. Held funds are never spendable and the
// result never goes negative — a fully-held wallet (balance == holds) reports 0, and
// holds beyond the balance clamp to 0 rather than rendering phantom money the gate
// would refuse. (The prior fallback used balance alone, over-reporting a fully-held
// balance as entirely available.)
func spendableCents(b commerceBalance) int64 {
	avail := b.Available
	if avail == 0 && b.Balance != 0 {
		avail = b.Balance - b.Holds
	}
	if avail < 0 {
		return 0
	}
	return avail
}

// financeCredits lists the credit grants on the caller org's wallet — every
// staff/promo grant and top-up, each positive. Consumption is usage, not a credit,
// and is excluded. An org with no grants gets an empty array, never a fabricated row.
//
// Response: [{"id": "txn_9f21", "label": "Launch credit", "cents": 50000, "grantedAt": "2026-05-01T00:00:00Z"}]
func (o ops) financeCredits(ctx context.Context, _ *noArgs) (*creditList, error) {
	c, org, err := caller(ctx, "finance")
	if err != nil {
		return nil, err
	}
	if !o.State.commerce.configured() {
		return nil, zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	txns, err := financeTxns(o.Service, c, org)
	if err != nil {
		return nil, err
	}
	credits := make(creditList, 0, len(txns))
	for _, t := range txns {
		if strings.ToLower(strings.TrimSpace(t.Type)) != "deposit" {
			continue
		}
		credits = append(credits, financeCredit{
			ID:        firstNonEmpty(t.ID, "credit"),
			Label:     firstNonEmpty(strings.TrimSpace(t.Notes), strings.TrimSpace(t.Tags), "Credit"),
			Cents:     abs64(t.Amount),
			GrantedAt: t.CreatedAt,
		})
	}
	return financeOut(c, &credits)
}

// financeUsage reports the caller org's metered spend over the window: a total, a
// time series (hourly for 24h, daily otherwise) and a per-tag breakdown. It projects
// the SAME priced ledger the wallet is debited from — never a second meter.
//
// Example: {"range": "7d"}
// Response: {"totalCents": 1240, "currency": "usd", "start": "2026-07-22T18:00:00Z", "end": "2026-07-29T18:00:00Z", "series": [{"date": "2026-07-22T00:00:00Z", "cents": 400}], "lines": [{"label": "ai", "units": 12, "cents": 1240}]}
func (o ops) financeUsage(ctx context.Context, in *financeWindow) (*financeUsageView, error) {
	c, org, err := caller(ctx, "finance")
	if err != nil {
		return nil, err
	}
	if !o.State.commerce.configured() {
		return nil, zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	txns, err := financeTxns(o.Service, c, org)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	window := rangeWindow(in.Range)
	cutoff := now.Add(-window)
	hourly := window <= 24*time.Hour

	buckets := map[time.Time]int64{}
	lineCents := map[string]int64{}
	lineUnits := map[string]int64{}
	var order []string
	var total int64
	for _, t := range txns {
		if strings.ToLower(strings.TrimSpace(t.Type)) != "withdraw" {
			continue
		}
		ts, perr := parseFinanceTime(t.CreatedAt)
		if perr != nil || ts.Before(cutoff) {
			continue
		}
		cents := abs64(t.Amount)
		total += cents
		buckets[bucketOf(ts, hourly)] += cents
		label := firstNonEmpty(strings.TrimSpace(t.Tags), "Usage")
		if _, seen := lineCents[label]; !seen {
			order = append(order, label)
		}
		lineCents[label] += cents
		lineUnits[label]++
	}

	series := make([]usagePoint, 0, len(buckets))
	for b, cents := range buckets {
		series = append(series, usagePoint{Date: b.Format(time.RFC3339), Cents: cents})
	}
	sort.Slice(series, func(i, j int) bool { return series[i].Date < series[j].Date })

	lines := make([]usageLine, 0, len(order))
	for _, label := range order {
		lines = append(lines, usageLine{Label: label, Units: lineUnits[label], Cents: lineCents[label]})
	}

	return financeOut(c, &financeUsageView{
		TotalCents: total,
		Currency:   "usd",
		Start:      cutoff.Format(time.RFC3339),
		End:        now.Format(time.RFC3339),
		Series:     series,
		Lines:      lines,
	})
}

// financeInvoices lists the caller org's issued invoices. It is empty by
// construction today: the wallet is prepaid (deposits and withdraws), so no invoice
// is ever issued — an honest empty array rather than a fabricated figure.
//
// Response: []
func (o ops) financeInvoices(ctx context.Context, _ *noArgs) (*invoiceList, error) {
	c, _, err := caller(ctx, "finance")
	if err != nil {
		return nil, err
	}
	return financeOut(c, &invoiceList{})
}

// financePaymentMethods lists the caller org's saved cards as MASKED descriptors —
// brand, last four and expiry. It re-masks whatever the processor returns, so a full
// card number can never reach the response.
//
// Response: [{"id": "pm_1QX", "type": "card", "brand": "visa", "last4": "4242", "expMonth": 4, "expYear": 2029, "isDefault": true}]
func (o ops) financePaymentMethods(ctx context.Context, _ *noArgs) (*paymentMethodList, error) {
	c, org, err := caller(ctx, "finance")
	if err != nil {
		return nil, err
	}
	if !o.State.commerce.configured() {
		return nil, zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	// Portal read filters on customerId; the subject is pinned to the caller's own org.
	body, status, err := o.State.commerce.get(ctx, "/v1/billing/portal/payment-methods", org, financeSubject(subjectFor(c, org), nil))
	if err != nil {
		o.Log.Warn("commerce payment-methods read failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	if status < 200 || status >= 300 {
		return nil, zip.Errorf(http.StatusBadGateway, "billing upstream status %d", status)
	}
	raw := arrayFrom(body, "paymentMethods", "payment_methods", "methods", "data", "rows")
	methods := make(paymentMethodList, 0, len(raw))
	for _, rm := range raw {
		var pm commercePaymentMethod
		if err := json.Unmarshal(rm, &pm); err != nil {
			continue
		}
		last4 := last4Of(firstNonEmpty(pm.Last4, pm.Card.Last4, pm.Card.LastFour))
		typ := pm.Type
		if typ == "" && last4 != "" {
			typ = "card"
		}
		methods = append(methods, financePaymentMethod{
			ID:        firstNonEmpty(pm.ID, pm.PaymentMethodID, "pm"),
			Type:      typ,
			Brand:     firstNonEmpty(pm.Brand, pm.Card.Brand, pm.Card.Network),
			Last4:     last4,
			ExpMonth:  firstNonZero(pm.ExpMonth, pm.Card.ExpMonth),
			ExpYear:   firstNonZero(pm.ExpYear, pm.Card.ExpYear),
			IsDefault: pm.IsDefault || pm.Default,
		})
	}
	return financeOut(c, &methods)
}

// financeLedger lists the caller org's money movements over the window as signed
// postings: a deposit credits the wallet (positive), a withdraw debits it (negative).
//
// Example: {"range": "30d"}
// Response: [{"id": "txn_9f21", "date": "2026-07-28T12:00:00Z", "account": "usage:acme", "description": "ai", "cents": -1240, "currency": "usd"}]
func (o ops) financeLedger(ctx context.Context, in *financeWindow) (*ledgerEntryList, error) {
	c, org, err := caller(ctx, "finance")
	if err != nil {
		return nil, err
	}
	if !o.State.commerce.configured() {
		return nil, zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	txns, err := financeTxns(o.Service, c, org)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().Add(-rangeWindow(in.Range))
	entries := make(ledgerEntryList, 0, len(txns))
	for _, t := range txns {
		if ts, perr := parseFinanceTime(t.CreatedAt); perr == nil && ts.Before(cutoff) {
			continue
		}
		deposit := strings.ToLower(strings.TrimSpace(t.Type)) == "deposit"
		cents := abs64(t.Amount)
		account := "usage:" + org
		if deposit {
			account = "credits:" + org
		} else {
			cents = -cents
		}
		entries = append(entries, financeLedgerEntry{
			ID:          firstNonEmpty(t.ID, "entry"),
			Date:        t.CreatedAt,
			Account:     account,
			Description: firstNonEmpty(strings.TrimSpace(t.Notes), strings.TrimSpace(t.Tags), t.Type),
			Cents:       cents,
			Currency:    firstNonEmpty(strings.ToLower(t.Currency), "usd"),
		})
	}
	return financeOut(c, &entries)
}

// ── finance helpers ──

// financeOut is the ONE exit every finance op takes: per-tenant money must never be
// cached by the browser or an intermediary, so the header is set in one place rather
// than at each return. zip writes the value; this only decorates the response.
func financeOut[T any](c *zip.Ctx, v *T) (*T, error) {
	c.SetHeader("Cache-Control", "no-store")
	return v, nil
}

// financeSubject builds the commerce query with every billing-subject key PINNED to
// subject (the client can never widen scope), plus any extra passthrough params.
func financeSubject(subject string, extra url.Values) url.Values {
	q := url.Values{}
	for _, k := range billingSubjectKeys {
		q.Set(k, subject)
	}
	for k, vs := range extra {
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	return q
}

// financeGet does one org-scoped commerce GET and decodes the 2xx body into out. A
// non-2xx or unreachable upstream is surfaced honestly (never masked as empty data).
func financeGet(s *cloud.Service[state], c *zip.Ctx, path, org string, extra url.Values, out any) error {
	body, status, err := s.State.commerce.get(c.Context(), path, org, financeSubject(subjectFor(c, org), extra))
	if err != nil {
		s.Log.Warn("commerce finance read failed", "org", org, "path", path, "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	if status < 200 || status >= 300 {
		return zip.Errorf(http.StatusBadGateway, "billing upstream status %d", status)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return zip.Errorf(http.StatusBadGateway, "billing upstream decode: %v", err)
	}
	return nil
}

// financeTxns reads the org's commerce ledger ONCE (the single transactions read the
// credits/usage/ledger projections share). Tolerates the wrapped {transactions:[…]}
// shape and a bare array.
func financeTxns(s *cloud.Service[state], c *zip.Ctx, org string) ([]commerceTxn, error) {
	// The ledger's own entries, from the process that holds them. Credits, usage and
	// the ledger page are three projections of this one list, and all three answered
	// 501 from a process without the ledger — which is every process but commerce.
	if rows, ok := peerTxns(c.Context(), org); ok {
		return rows, nil
	}
	body, status, err := s.State.commerce.get(c.Context(), "/v1/billing/transactions", org, financeSubject(subjectFor(c, org), url.Values{"limit": {"2000"}}))
	if err != nil {
		s.Log.Warn("commerce transactions read failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	if status < 200 || status >= 300 {
		return nil, zip.Errorf(http.StatusBadGateway, "billing upstream status %d", status)
	}
	var wrap struct {
		Transactions []commerceTxn `json:"transactions"`
	}
	if json.Unmarshal(body, &wrap) == nil && wrap.Transactions != nil {
		return wrap.Transactions, nil
	}
	var rows []commerceTxn
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "billing upstream decode: %v", err)
	}
	return rows, nil
}

// rangeWindow maps a finance range token to a duration; an absent/unknown range
// defaults to 30 days.
func rangeWindow(r string) time.Duration {
	switch strings.TrimSpace(r) {
	case "24h":
		return 24 * time.Hour
	case "7d":
		return 7 * 24 * time.Hour
	case "90d":
		return 90 * 24 * time.Hour
	default: // "30d" and anything unrecognized
		return 30 * 24 * time.Hour
	}
}

// bucketOf truncates a time to its series bucket (the hour for 24h, else the UTC day).
func bucketOf(t time.Time, hourly bool) time.Time {
	t = t.UTC()
	if hourly {
		return t.Truncate(time.Hour)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// parseFinanceTime parses a commerce RFC3339 timestamp (with or without sub-seconds).
func parseFinanceTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// arrayFrom returns the first JSON array found under any of keys, or a bare array.
func arrayFrom(body []byte, keys ...string) []json.RawMessage {
	var bare []json.RawMessage
	if json.Unmarshal(body, &bare) == nil {
		return bare
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return nil
	}
	for _, k := range keys {
		if raw, ok := root[k]; ok {
			var arr []json.RawMessage
			if json.Unmarshal(raw, &arr) == nil {
				return arr
			}
		}
	}
	return nil
}

// last4Of keeps at most the trailing four digits of any card-number fragment — the ONLY
// digits ever surfaced (defense in depth against an over-returning upstream).
func last4Of(v string) string {
	var digits strings.Builder
	for _, r := range v {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	if len(d) > 4 {
		return d[len(d)-4:]
	}
	return d
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(vs ...int) int {
	for _, v := range vs {
		if v != 0 {
			return v
		}
	}
	return 0
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// peerTxns reads the ledger over the internal plane. ok=false means no peer served
// it, and the caller falls back to the configured commerce URL — the split deploy,
// which is a real shape and not a failure.
//
// The amount arrives as its exact 18-decimal integer and is flattened to cents HERE,
// at the boundary where commerceTxn is already a cents-shaped view. The wire keeps
// the precision so the day that view stops being cents-shaped, nothing upstream has
// to be re-plumbed to find it.
func peerTxns(ctx context.Context, org string) ([]commerceTxn, bool) {
	ctx, cancel := context.WithTimeout(ctx, txnsPeerTimeout)
	defer cancel()
	reply, err := cloud.Dial("commerce").For(org).Call(ctx, "finance.txns",
		cloud.PutBalanceReq(org, "usd"))
	if err != nil {
		return nil, false
	}
	wire, err := cloud.Txns(reply)
	if err != nil {
		return nil, false
	}
	out := make([]commerceTxn, 0, len(wire))
	for _, t := range wire {
		amt, perr := money.ParseInt(t.Atto)
		if perr != nil {
			return nil, false // a total we cannot read exactly is not a total we report
		}
		out = append(out, commerceTxn{
			ID:        t.ID,
			Type:      t.Kind,
			Amount:    amt.Cents(),
			Currency:  "usd",
			Tags:      t.Ref,
			Notes:     t.Memo,
			CreatedAt: time.Unix(t.CreatedAt, 0).UTC().Format(time.RFC3339),
		})
	}
	return out, true
}

// txnsPeerTimeout bounds the ledger read behind an interactive billing page.
const txnsPeerTimeout = 10 * time.Second
