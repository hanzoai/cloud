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
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// mountFinance registers the six commerce-projected /v1/finance/* reads on app. Called
// from Mount, so the finance surface ships with the billing surface (same commerceProxy).
func mountFinance(s *cloud.Service[state], app *zip.App) {
	app.Get("/v1/finance/balance", cloud.Handle(s, financeBalance))                // prepaid available + holds + due
	app.Get("/v1/finance/credits", cloud.Handle(s, financeCredits))                // credit grants (deposits)
	app.Get("/v1/finance/usage", cloud.Handle(s, financeUsage))                    // metered spend over ?range=
	app.Get("/v1/finance/invoices", cloud.Handle(s, financeInvoices))              // issued invoices (honest empty today)
	app.Get("/v1/finance/payment-methods", cloud.Handle(s, financePaymentMethods)) // masked saved cards (brand+last4)
	app.Get("/v1/finance/ledger", cloud.Handle(s, financeLedger))                  // per-org double-entry postings over ?range=
}

// ── the finance contract (typed, USD cents, matching @hanzo/finance-ui types.ts) ──

// financeBalanceView is the GET /v1/finance/balance response.
type financeBalanceView struct {
	Currency       string `json:"currency"`
	AvailableCents int64  `json:"availableCents"`
	PendingCents   int64  `json:"pendingCents"`
	DueCents       int64  `json:"dueCents"`
	AsOf           string `json:"asOf"`
}

// financeCredit is one row of GET /v1/finance/credits — a credit grant on the org's
// wallet. cents is positive (a grant); a renamed/absent field degrades to a safe zero.
type financeCredit struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	Cents          int64  `json:"cents"`
	GrantedAt      string `json:"grantedAt,omitempty"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
	RemainingCents *int64 `json:"remainingCents,omitempty"`
}

type usagePoint struct {
	Date  string `json:"date"`
	Cents int64  `json:"cents"`
}

type usageLine struct {
	Label  string `json:"label"`
	Units  int64  `json:"units,omitempty"`
	Tokens int64  `json:"tokens,omitempty"`
	Cents  int64  `json:"cents"`
}

// financeUsageView is the GET /v1/finance/usage?range= response.
type financeUsageView struct {
	TotalCents int64        `json:"totalCents"`
	Currency   string       `json:"currency"`
	Start      string       `json:"start,omitempty"`
	End        string       `json:"end,omitempty"`
	Series     []usagePoint `json:"series"`
	Lines      []usageLine  `json:"lines"`
}

// financeInvoice is one row of GET /v1/finance/invoices.
type financeInvoice struct {
	ID       string `json:"id"`
	Number   string `json:"number,omitempty"`
	Date     string `json:"date,omitempty"`
	DueDate  string `json:"dueDate,omitempty"`
	Cents    int64  `json:"cents"`
	Currency string `json:"currency"`
	Status   string `json:"status,omitempty"`
	URL      string `json:"url,omitempty"`
}

// financePaymentMethod is one row of GET /v1/finance/payment-methods — the MASKED
// descriptor only. A PAN/CVV/gateway token is never present in this shape.
type financePaymentMethod struct {
	ID        string `json:"id"`
	Type      string `json:"type,omitempty"`
	Brand     string `json:"brand,omitempty"`
	Last4     string `json:"last4,omitempty"`
	ExpMonth  int    `json:"expMonth,omitempty"`
	ExpYear   int    `json:"expYear,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

// financeLedgerEntry is one posting of GET /v1/finance/ledger?range= — a signed move
// on the org's wallet (deposit positive, withdraw negative).
type financeLedgerEntry struct {
	ID           string `json:"id"`
	Date         string `json:"date,omitempty"`
	Account      string `json:"account,omitempty"`
	Description  string `json:"description,omitempty"`
	Cents        int64  `json:"cents"`
	Currency     string `json:"currency"`
	BalanceCents *int64 `json:"balanceCents,omitempty"`
}

// ── commerce wire shapes (only the fields we project) ──

// commerceBalance is commerce GET /v1/billing/balance ({balance,holds,available} cents).
type commerceBalance struct {
	Balance   int64 `json:"balance"`
	Holds     int64 `json:"holds"`
	Available int64 `json:"available"`
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

// financeBalance projects commerce's wallet balance into the finance Balance shape:
// prepaid `available` → availableCents, authorized `holds` → pendingCents. dueCents is
// an honest 0 — commerce is prepaid (no open-invoice debt), so nothing is owed. The
// aggregate the finance UI shows is the org's spendable wallet; per-org treasury
// balances (currently empty) fold in here the day the ledger projection posts them.
func financeBalance(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := financeCaller(s, c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view finance")
	}
	// The ONE balance read (balance.go) — the same wallet /v1/billing/balance answers, so
	// the two surfaces can never disagree. Co-resident this is the finance ledger; only a
	// split deploy falls through to the commerce S2S read below.
	if cents, coResident, err := availableCents(c.Context(), org, subjectFor(c, org)); err != nil {
		s.Log.Warn("finance balance read failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	} else if coResident {
		return financeJSON(s, c, financeBalanceView{
			Currency:       "usd",
			AvailableCents: cents,
			PendingCents:   0,
			DueCents:       0,
			AsOf:           time.Now().UTC().Format(time.RFC3339),
		})
	}
	if !s.State.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	var b commerceBalance
	if err := financeGet(s, c, "/v1/billing/balance", org, url.Values{"currency": {"usd"}}, &b); err != nil {
		return err
	}
	available := b.Available
	if available == 0 && b.Balance != 0 {
		available = b.Balance
	}
	return financeJSON(s, c, financeBalanceView{
		Currency:       "usd",
		AvailableCents: available,
		PendingCents:   b.Holds,
		DueCents:       0,
		AsOf:           time.Now().UTC().Format(time.RFC3339),
	})
}

// financeCredits projects the DEPOSIT rows of the commerce ledger — each staff/promo
// grant or top-up is a positive credit. Consumption (withdraws) is usage, not a credit,
// so it is excluded here (it appears in usage + ledger). Honest empty when the org has
// no grants yet.
func financeCredits(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := financeCaller(s, c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view finance")
	}
	if !s.State.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	txns, err := financeTxns(s, c, org)
	if err != nil {
		return err
	}
	credits := make([]financeCredit, 0, len(txns))
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
	return financeJSON(s, c, credits)
}

// financeUsage projects the WITHDRAW rows within the ?range= window into a metered-spend
// view: a time series (hourly for 24h, daily otherwise), a per-tag line breakdown, and
// the window total. This is the SAME priced-usage ledger the o11y/analytics folds read —
// projected, never re-metered.
func financeUsage(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := financeCaller(s, c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view finance")
	}
	if !s.State.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	txns, err := financeTxns(s, c, org)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	window := rangeWindow(c.Query("range"))
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

	return financeJSON(s, c, financeUsageView{
		TotalCents: total,
		Currency:   "usd",
		Start:      cutoff.Format(time.RFC3339),
		End:        now.Format(time.RFC3339),
		Series:     series,
		Lines:      lines,
	})
}

// financeInvoices is an HONEST empty typed array: commerce is a prepaid wallet
// (deposits + withdraws), it issues no customer invoices, so there is no per-org
// invoice ledger to project today. The shape is stable, so the day an invoice ledger
// exists this becomes real with ZERO UI change.
func financeInvoices(s *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := financeCaller(s, c); !ok {
		return zip.ErrUnauthorized("sign in to view finance")
	}
	return financeJSON(s, c, []financeInvoice{})
}

// financePaymentMethods projects the commerce portal's masked card descriptors. It
// re-masks defensively (keeps at most the trailing four digits, whatever the wire sent),
// so a PAN can never leak even if commerce were to over-return.
func financePaymentMethods(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := financeCaller(s, c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view finance")
	}
	if !s.State.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	// Portal read filters on customerId; the subject is pinned to the caller's own org.
	body, status, err := s.State.commerce.get(c.Context(), "/v1/billing/portal/payment-methods", org, financeSubject(subjectFor(c, org), nil))
	if err != nil {
		s.Log.Warn("commerce payment-methods read failed", "org", org, "err", err)
		return zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	if status < 200 || status >= 300 {
		return zip.Errorf(http.StatusBadGateway, "billing upstream status %d", status)
	}
	raw := arrayFrom(body, "paymentMethods", "payment_methods", "methods", "data", "rows")
	methods := make([]financePaymentMethod, 0, len(raw))
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
	return financeJSON(s, c, methods)
}

// financeLedger projects the commerce ledger within ?range= into signed double-entry
// postings: a deposit credits the org's wallet (+), a withdraw debits it (−). This is
// the org's OWN money movement — the honest per-org ledger the finance UI renders.
func financeLedger(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := financeCaller(s, c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view finance")
	}
	if !s.State.commerce.configured() {
		return zip.Errorf(http.StatusNotImplemented, "billing is not configured")
	}
	txns, err := financeTxns(s, c, org)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().Add(-rangeWindow(c.Query("range")))
	entries := make([]financeLedgerEntry, 0, len(txns))
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
	return financeJSON(s, c, entries)
}

// ── finance helpers ──

// financeCaller resolves the caller's OWN org from the validated principal — the ONE
// tenant gate every finance read shares.
func financeCaller(s *cloud.Service[state], c *zip.Ctx) (string, bool) {
	return principal.Org(c)
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

// financeJSON writes a finance payload as bare JSON with no-store (per-org money must
// never be cached by the browser or an intermediary).
func financeJSON(s *cloud.Service[state], c *zip.Ctx, v any) error {
	c.SetHeader("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, v)
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
