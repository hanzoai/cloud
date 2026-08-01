package billing

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

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// mountFinance registers the six commerce-projected /v1/finance/* reads on app. Called
// from Mount, so the finance surface ships with the billing surface (same commerceProxy).
func mountFinance(s *cloud.Service[state], app cloud.Router) {
	app.Get("/v1/finance/balance", cloud.Handle(s, financeBalance))                // prepaid available + holds + due
	app.Get("/v1/finance/credits", cloud.Handle(s, financeCredits))                // credit grants (deposits)
	app.Get("/v1/finance/usage", cloud.Handle(s, financeUsage))                    // metered spend over ?range=
	app.Get("/v1/finance/invoices", cloud.Handle(s, financeInvoices))              // issued invoices (honest empty today)
	app.Get("/v1/finance/payment-methods", cloud.Handle(s, financePaymentMethods)) // masked saved cards (brand+last4)
	app.Get("/v1/finance/ledger", cloud.Handle(s, financeLedger))                  // per-org double-entry postings over ?range=
}

// The PROSE for the six, beside the route table that keeps them untyped. Each is a raw
// *zip.Ctx handler, so zipdoc has no doc comment to lift; without a Describe the
// document publishes an operationId and nothing else, and every generated SDK offers a
// MONEY call that cannot say whose books it reads or what the amounts mean.
//
// The one fact a reader of BOTH prefixes needs, stated once here and pointed at from the
// ops it changes: /v1/billing/* forwards the raw commerce wire (body and status verbatim,
// one row per billed call), while /v1/finance/* RESHAPES that same wallet into the typed
// finance contract the UI renders. They are two shapes of one ledger, not two ledgers —
// except payment-methods, which read one store under two different KEYS.
func init() {
	openapi.Describe("/v1/finance/balance", http.MethodGet,
		"Spendable prepaid for the caller's org, in the finance shape",
		"Answers the org's spendable prepaid balance typed for the finance surfaces: "+
			"`availableCents`, `pendingCents`, `dueCents` and the `asOf` instant it was read.\n\n"+
			"It is the SAME wallet read /v1/billing/balance answers — one function, called by "+
			"both, so the two surfaces cannot drift into disagreeing about a customer's money. "+
			"Reshaped, never re-metered. Co-resident the number comes straight out of the org's "+
			"own double-entry ledger file.\n\n"+
			"`dueCents` is a structural 0: this is a PREPAID wallet with no open-invoice debt, so "+
			"nothing is ever owed and a non-zero value here would be an invention. `pendingCents` "+
			"is 0 on the co-resident ledger, where authorization holds are never posted; only a "+
			"split-deploy upstream reports holds, and there spendable is the balance NET of them, "+
			"floored at 0 — a fully-held wallet reports 0 rather than money the gate would "+
			"refuse.\n\n"+
			"Cents are ROUNDED from the ledger's exact 18-decimal USD. Scoped to the caller's own "+
			"org from the validated IAM owner claim; 401 without a validated principal, and a "+
			"balance that cannot be read is 502 — never 0, because unknown is not broke.")

	openapi.Describe("/v1/finance/credits", http.MethodGet,
		"Credit grants and top-ups on the caller's org wallet",
		"Answers the money PUT IN to the org's wallet — each staff grant, promo and settled "+
			"top-up as a positive row with its id, label, cents and grant time.\n\n"+
			"Spend is not a credit. A posting counts here only when it moved money IN; debits "+
			"belong to /v1/finance/usage (aggregated) and /v1/finance/ledger (signed). All three "+
			"project ONE read of the same ledger through ONE vocabulary for what a posting means, "+
			"so they cannot disagree about a row — nor silently drop one, which is what an empty "+
			"credits page against a funded wallet was.\n\n"+
			"`label` falls back through the posting's notes, then its tags, then a bare Credit — "+
			"it is a description, never an identifier. `remainingCents` is OMITTED: the wallet is "+
			"one running balance, not per-grant buckets, so no grant has a remainder to report and "+
			"spend cannot be attributed to the credit that funded it.\n\n"+
			"Cents are ROUNDED from the ledger's exact 18-decimal USD. Scoped to the caller's own "+
			"org; 401 without a validated principal. An org with no grants gets an empty array — "+
			"honest, never a fabricated figure.")

	openapi.Describe("/v1/finance/usage", http.MethodGet,
		"What the caller's org spent over a window, as a series and by tag",
		"Answers metered spend inside `range=`: the window total, a time series to plot, and one "+
			"line per usage TAG. Aggregated from the same charged ledger the balance comes off — "+
			"projected, never re-metered.\n\n"+
			"Only DEBIT postings count; deposits are credits and are excluded. `range` is 24h, "+
			"7d, 30d or 90d, and anything else — including absent — is 30d, so a typo silently "+
			"widens the window to a month rather than failing. Buckets are hourly at 24h and daily "+
			"otherwise, in UTC; a posting whose timestamp will not parse is dropped rather than "+
			"mis-bucketed.\n\n"+
			"Lines group by the posting's tag (`Usage` where it carries none) and `units` counts "+
			"POSTINGS, not tokens. The dimensions here are time and tag. For per-request rows and "+
			"a per-PRODUCT breakdown, read /v1/billing/usage instead — the same money, cut a "+
			"different way.\n\n"+
			"Cents are ROUNDED from the ledger's exact 18-decimal USD, so a window made of "+
			"sub-cent token calls totals LOW here. Scoped to the caller's own org; 401 without a "+
			"validated principal.")

	openapi.Describe("/v1/finance/invoices", http.MethodGet,
		"Issued invoices — none exist, and that is the honest answer",
		"Answers an empty typed array, always. The fleet bills a PREPAID wallet — money in, "+
			"metered debits out — and issues no customer invoices, so there is no invoice ledger "+
			"to project. Nothing here is a fabricated figure and nothing is hidden behind a "+
			"filter.\n\n"+
			"The shape is fixed, so the finance UI renders this lane today and the day an invoice "+
			"ledger exists it fills with ZERO client change. Spend that actually happened is "+
			"/v1/finance/usage; money in and out is /v1/finance/ledger; what is left to spend is "+
			"/v1/finance/balance.\n\n"+
			"The gate is real even though the body is empty: 401 without a validated principal. It "+
			"is the only finance read that touches no store, so it is also the only one that "+
			"cannot 502.")

	openapi.Describe("/v1/finance/payment-methods", http.MethodGet,
		"Saved cards for the wallet the caller pays from",
		"Answers the masked card descriptors for the caller's resolved WALLET — id, brand, last "+
			"four, expiry, default flag — reshaped into the finance contract.\n\n"+
			"It re-masks defensively: whatever the upstream sends, at most the trailing four "+
			"DIGITS survive into `last4`. No card number, no security code and no processor token "+
			"exists in this shape at all, so an over-returning upstream still cannot leak one "+
			"through this lane.\n\n"+
			"Read the sibling difference before trusting a mismatch. This keys the store on the "+
			"resolved wallet; /v1/billing/methods keys it on the org SLUG, which is also "+
			"the key a card is SAVED under — identical for an org paying from its shared pool, "+
			"different wherever the payer is a person. When the two lists disagree, the billing "+
			"one is what was saved.\n\n"+
			"401 without a validated principal. An upstream that answers non-2xx or cannot be "+
			"reached is 502 — never an empty list, because no cards and could not ask must not "+
			"look alike.")

	openapi.Describe("/v1/finance/ledger", http.MethodGet,
		"Money in and out of the caller's org wallet, signed",
		"Answers the org's own postings inside `range=`, each as a signed entry: a DEPOSIT "+
			"CREDITS the wallet (positive, account `credits:<org>`) and every other posting DEBITS "+
			"it (negative, account `usage:<org>`), described by its notes or its tags. The sign is "+
			"the posting's own meaning, read through ONE vocabulary shared with the ledger that "+
			"wrote it — a reader with its own spelling for `deposit` rendered a customer's grant "+
			"as a charge.\n\n"+
			"This is the closest projection of the truth. The org's double-entry postings are the "+
			"source of record — balanced, only ever appended, one file per org — and this lane is "+
			"that list, widest of the three: /v1/finance/credits is its deposit half and "+
			"/v1/finance/usage is its withdrawal half rolled up. All three come from ONE read, "+
			"which is why they cannot contradict each other, and all three answer 501 where no "+
			"commerce link is configured rather than reporting an empty wallet.\n\n"+
			"`range` is 24h, 7d, 30d or 90d, defaulting to 30d. A row whose timestamp will not "+
			"parse is KEPT rather than dropped — a malformed date must show up in a money list, "+
			"not vanish from it. `balanceCents` is omitted: these are MOVEMENTS, and the standing "+
			"balance is /v1/finance/balance.\n\n"+
			"Cents are ROUNDED from the ledger's exact 18-decimal USD. Scoped to the caller's own "+
			"org, where the org's ledger file is the tenant boundary; 401 without a validated "+
			"principal.")
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

// commerceTxn is one ledger row as the three projections below read it. Amount is the
// magnitude in cents. This ONE row shape backs credits, usage, and ledger — a single
// read projected three ways, never a second meter.
//
// Kind is the ONE vocabulary (apps/finance owns it), never a string this file spells
// for itself. Two wires deliver these rows — the internal plane, carrying the ledger's
// own kinds, and commerce's S2S HTTP, carrying its own `deposit`/`withdraw` — and each
// is translated into finance.Kind at ITS OWN boundary, so the projections classify on
// one typed value and can never be handed a spelling they silently skip. They were:
// the reader matched commerce's words against the ledger's kinds, so on the peer path
// credits rendered empty, usage totalled 0, and a customer's own grant signed negative.
type commerceTxn struct {
	// Type is commerce's raw HTTP wire word, decoded on the S2S path only. It is
	// translated into Kind by commerceKind and never classified on directly.
	Type      string       `json:"type"`
	Kind      finance.Kind `json:"-"`
	ID        string       `json:"id"`
	Amount    int64        `json:"amount"`
	Currency  string       `json:"currency"`
	Tags      string       `json:"tags"`
	Notes     string       `json:"notes"`
	CreatedAt string       `json:"createdAt"`
}

// commerceKind translates commerce's OWN HTTP wire vocabulary into the ledger's. It is
// the one place those two words appear, because they belong to an upstream this fleet
// does not own; every reader downstream of it sees finance.Kind and nothing else.
func commerceKind(wire string) finance.Kind {
	switch strings.ToLower(strings.TrimSpace(wire)) {
	case "deposit":
		return finance.KindDeposit
	case "withdraw":
		return finance.KindUsage
	default:
		return finance.KindUnknown
	}
}

// kindLabel is the plain word a customer reads for a posting that carries neither
// notes nor tags. Rendering, not classification — which is why it lives here and not
// beside the kinds.
func kindLabel(k finance.Kind) string {
	if k == finance.KindDeposit {
		return "Credit"
	}
	return "Usage"
}

// commercePaymentMethod tolerates both the flat descriptor and a nested `card` object,
// so a masked card reshapes regardless of which shape commerce's portal returns.
type commercePaymentMethod struct {
	ID              string `json:"id"`
	ProviderRef     string `json:"providerRef"`
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
	return financeJSON(s, c, financeBalanceView{
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
		if t.Kind != finance.KindDeposit {
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
		if t.Kind != finance.KindUsage {
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
		deposit := t.Kind == finance.KindDeposit
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
			Description: firstNonEmpty(strings.TrimSpace(t.Notes), strings.TrimSpace(t.Tags), kindLabel(t.Kind)),
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
	// The ledger's own entries, from the process that holds them. Credits, usage and
	// the ledger page are three projections of this one list, and all three answered
	// 501 from a process without the ledger — which is every process but commerce.
	peer, served, err := peerTxns(c.Context(), org)
	if err != nil {
		s.Log.Warn("finance transactions read failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "billing upstream unreachable")
	}
	if served {
		return peer, nil
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
		return classify(wrap.Transactions), nil
	}
	var rows []commerceTxn
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "billing upstream decode: %v", err)
	}
	return classify(rows), nil
}

// classify is the S2S boundary: commerce's own wire words become the ONE vocabulary
// the projections read, ONCE, on the way in. Nothing downstream of it sees a raw type
// string, which is what makes a third spelling impossible to introduce quietly.
func classify(rows []commerceTxn) []commerceTxn {
	for i := range rows {
		rows[i].Kind = commerceKind(rows[i].Type)
	}
	return rows
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

// peerTxns reads the ledger over the internal plane. ok=false means this deployment
// runs no commerce, and the caller falls back to the configured commerce URL — the
// split deploy, which is a real shape and not a failure. A non-nil err is a REAL read
// failure: the peer is here and it did not answer, which is an outage and must reach
// the customer as one rather than as somebody else's ledger.
//
// Only the ROUTER may state absence (cloud.ErrNoPeer); it owns the manifest. Absence
// inferred from a failed call is how a dead peer became a phantom split deploy.
//
// The amount arrives as its exact 18-decimal integer and is flattened to cents HERE,
// at the boundary where commerceTxn is already a cents-shaped view. The wire keeps
// the precision so the day that view stops being cents-shaped, nothing upstream has
// to be re-plumbed to find it.
func peerTxns(ctx context.Context, org string) ([]commerceTxn, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, txnsPeerTimeout)
	defer cancel()
	reply, err := cloud.Ask[struct{}, plane.Txns](cloud.For(ctx, org), "commerce",
		plane.FinanceTxns, &struct{}{})
	if err != nil {
		if errors.Is(err, cloud.ErrNoPeer) {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("transactions: commerce ledger read: %w", err)
	}
	if reply == nil {
		// A void reply is not an empty ledger. Nothing was read, so nothing is known.
		return nil, true, errors.New("transactions: commerce answered nothing")
	}
	out := make([]commerceTxn, 0, len(reply.Rows))
	for _, t := range reply.Rows {
		amt, perr := money.ParseUSD(t.Amount.Decimal)
		if perr != nil {
			// A total we cannot read exactly is not a total we report — and it is the
			// peer's answer that is wrong, not the peer that is absent.
			return nil, true, fmt.Errorf("transactions: %w", perr)
		}
		out = append(out, commerceTxn{
			ID: t.ID,
			// The peer boundary: the ledger's own kind, parsed back into the ONE
			// vocabulary. It arrives as text and stops being text here.
			Kind:      finance.ParseKind(t.Kind),
			Amount:    amt.Cents(),
			Currency:  "usd",
			Tags:      t.Ref,
			Notes:     t.Memo,
			CreatedAt: time.Unix(t.CreatedAt, 0).UTC().Format(time.RFC3339),
		})
	}
	return out, true, nil
}

// txnsPeerTimeout bounds the ledger read behind an interactive billing page.
const txnsPeerTimeout = 10 * time.Second
