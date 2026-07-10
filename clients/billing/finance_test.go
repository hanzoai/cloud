package billing

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// financeFake is a per-ORG, per-PATH commerce stand-in for the /v1/finance/* projection
// tests. It answers DIFFERENT data per X-Org-Id — so a cross-tenant leak would surface as
// the WRONG org's numbers — and records the pinned subject query, so scope-widening is
// provable. It is deliberately distinct from billing_test's single-body fakeCommerce
// (these endpoints reshape, and each needs its own upstream body).
type financeFake struct {
	mu            sync.Mutex
	gotOrg        string
	gotUser       string
	gotCustomerID string
	gotOrgQuery   string
	gotPath       string
	gotLimit      string
}

// txnsJSON is the org's commerce ledger with timestamps computed relative to now, so the
// ?range= filtering is deterministic regardless of when the test runs. acme carries two
// deposits (grants) + three withdraws (one 40 days old, to prove the window excludes it);
// globex carries a distinct set so an isolation break shows as globex's numbers.
func txnsJSON(org string) string {
	now := time.Now().UTC()
	ts := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	switch org {
	case "acme":
		return fmt.Sprintf(`{"transactions":[
			{"id":"d1","type":"deposit","amount":50000,"currency":"usd","notes":"Startup grant","tags":"trial","createdAt":%q},
			{"id":"d2","type":"deposit","amount":10000,"currency":"usd","notes":"Top-up","tags":"prepaid","createdAt":%q},
			{"id":"w1","type":"withdraw","amount":1200,"currency":"usd","tags":"zen","createdAt":%q},
			{"id":"w2","type":"withdraw","amount":800,"currency":"usd","tags":"embeddings","createdAt":%q},
			{"id":"w3","type":"withdraw","amount":300,"currency":"usd","tags":"zen","createdAt":%q}
		]}`, ts(48*time.Hour), ts(24*time.Hour), ts(2*time.Hour), ts(3*time.Hour), ts(40*24*time.Hour))
	case "globex":
		return fmt.Sprintf(`{"transactions":[
			{"id":"gd1","type":"deposit","amount":900000,"currency":"usd","notes":"Enterprise credit","tags":"prepaid","createdAt":%q},
			{"id":"gw1","type":"withdraw","amount":5000,"currency":"usd","tags":"gpu","createdAt":%q}
		]}`, ts(24*time.Hour), ts(1*time.Hour))
	}
	return `{"transactions":[]}`
}

func (f *financeFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	h := func(w http.ResponseWriter, r *http.Request) {
		org := r.Header.Get("X-Org-Id")
		q := r.URL.Query()
		f.mu.Lock()
		f.gotOrg, f.gotUser, f.gotCustomerID = org, q.Get("user"), q.Get("customerId")
		f.gotOrgQuery, f.gotPath, f.gotLimit = q.Get("org"), r.URL.Path, q.Get("limit")
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/billing/balance":
			switch org {
			case "acme":
				_, _ = io.WriteString(w, `{"balance":100000,"holds":2500,"available":100000}`)
			case "globex":
				_, _ = io.WriteString(w, `{"balance":500000,"holds":0,"available":500000}`)
			default:
				_, _ = io.WriteString(w, `{"balance":0,"holds":0,"available":0}`)
			}
		case "/v1/billing/transactions":
			_, _ = io.WriteString(w, txnsJSON(org))
		case "/v1/billing/portal/payment-methods":
			if org == "acme" {
				// pm_2 is an OVER-returning upstream: it leaks a full 16-digit number under a
				// nested card. The projection MUST truncate it to the last four (defense in depth).
				_, _ = io.WriteString(w, `[
					{"id":"pm_1","brand":"visa","last4":"4242","expMonth":8,"expYear":2029,"isDefault":true},
					{"id":"pm_2","brand":"mastercard","card":{"last4":"4111111111111111","exp_month":3,"exp_year":2028}}
				]`)
			} else {
				_, _ = io.WriteString(w, `[]`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// ── balance: shape, cents (holds→pending), per-org isolation, subject pinning ──

func TestFinanceBalance_ShapeCentsAndIsolation(t *testing.T) {
	f := &financeFake{}
	app := mountApp(t, f.server(t).URL, "svc-token")

	// acme: available 100000, holds 2500 → pendingCents 2500, dueCents 0 (prepaid, nothing owed).
	code, body := call(t, app, http.MethodGet, "/v1/finance/balance", "acme/dave", "acme")
	if code != http.StatusOK {
		t.Fatalf("balance acme = %d (%s)", code, body)
	}
	var b financeBalanceView
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("decode balance: %v (%s)", err, body)
	}
	if b.Currency != "usd" || b.AvailableCents != 100000 || b.PendingCents != 2500 || b.DueCents != 0 {
		t.Fatalf("acme balance shape/cents wrong: %+v", b)
	}
	if b.AsOf == "" {
		t.Fatalf("asOf must be set")
	}
	// The subject was PINNED to the caller's own org (never a client value).
	if f.gotOrg != "acme" || f.gotUser != "acme" {
		t.Fatalf("subject not pinned to caller org: X-Org-Id=%q user=%q", f.gotOrg, f.gotUser)
	}

	// ISOLATION: org B sees ONLY its OWN wallet (500000), never acme's 100000.
	_, body = call(t, app, http.MethodGet, "/v1/finance/balance", "globex/pat", "globex")
	_ = json.Unmarshal(body, &b)
	if b.AvailableCents != 500000 {
		t.Fatalf("globex must see its own balance 500000, got %d (cross-tenant leak?)", b.AvailableCents)
	}
}

func TestFinanceBalance_ClientCannotWidenScope(t *testing.T) {
	f := &financeFake{}
	app := mountApp(t, f.server(t).URL, "svc-token")
	// A malicious client forges the subject + a foreign org in the query.
	code, _ := call(t, app, http.MethodGet, "/v1/finance/balance?user=victim&customerId=victim&org=evil", "acme/dave", "acme")
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if f.gotUser != "acme" || f.gotCustomerID != "acme" {
		t.Fatalf("forged subject must be overwritten with caller org: user=%q customerId=%q", f.gotUser, f.gotCustomerID)
	}
	if f.gotOrgQuery != "" {
		t.Fatalf("client-forged org must NOT reach commerce, got %q", f.gotOrgQuery)
	}
	if f.gotOrg != "acme" {
		t.Fatalf("X-Org-Id must be the caller's own org, got %q", f.gotOrg)
	}
}

// ── credits: deposits only, positive cents, labels from the grant reason ──

func TestFinanceCredits_DepositsOnlyPositiveCents(t *testing.T) {
	f := &financeFake{}
	app := mountApp(t, f.server(t).URL, "svc-token")
	code, body := call(t, app, http.MethodGet, "/v1/finance/credits", "acme/dave", "acme")
	if code != http.StatusOK {
		t.Fatalf("credits = %d (%s)", code, body)
	}
	var credits []financeCredit
	if err := json.Unmarshal(body, &credits); err != nil {
		t.Fatalf("decode credits: %v (%s)", err, body)
	}
	// Only the two DEPOSITS are credits; the three withdraws are usage, not credits.
	if len(credits) != 2 {
		t.Fatalf("want 2 credit grants (deposits only), got %d: %+v", len(credits), credits)
	}
	byLabel := map[string]int64{}
	for _, c := range credits {
		if c.Cents <= 0 {
			t.Fatalf("a credit grant must be positive: %+v", c)
		}
		byLabel[c.Label] = c.Cents
	}
	if byLabel["Startup grant"] != 50000 || byLabel["Top-up"] != 10000 {
		t.Fatalf("credit labels/cents wrong: %+v", byLabel)
	}
}

// ── usage: range window, series, per-tag lines, total (== series sum) ──

func TestFinanceUsage_RangeSeriesLinesTotal(t *testing.T) {
	f := &financeFake{}
	app := mountApp(t, f.server(t).URL, "svc-token")

	// 24h: only w1(1200)+w2(800) are inside the window (w3 is 40 days old) → total 2000.
	code, body := call(t, app, http.MethodGet, "/v1/finance/usage?range=24h", "acme/dave", "acme")
	if code != http.StatusOK {
		t.Fatalf("usage 24h = %d (%s)", code, body)
	}
	var u financeUsageView
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatalf("decode usage: %v (%s)", err, body)
	}
	if u.Currency != "usd" || u.TotalCents != 2000 {
		t.Fatalf("usage 24h total = %d (want 2000), currency=%q", u.TotalCents, u.Currency)
	}
	// Series sums to the total (no fabricated curve).
	var seriesSum int64
	for _, p := range u.Series {
		seriesSum += p.Cents
	}
	if seriesSum != u.TotalCents {
		t.Fatalf("series sum %d != total %d", seriesSum, u.TotalCents)
	}
	lines := map[string]usageLine{}
	for _, l := range u.Lines {
		lines[l.Label] = l
	}
	if lines["zen"].Cents != 1200 || lines["zen"].Units != 1 || lines["embeddings"].Cents != 800 {
		t.Fatalf("usage 24h lines wrong: %+v", u.Lines)
	}

	// 90d: the 40-day-old w3(300) is now inside the window → total 2300, zen 1500 (2 units).
	_, body = call(t, app, http.MethodGet, "/v1/finance/usage?range=90d", "acme/dave", "acme")
	_ = json.Unmarshal(body, &u)
	if u.TotalCents != 2300 {
		t.Fatalf("usage 90d total = %d, want 2300 (range window must widen)", u.TotalCents)
	}
	lines = map[string]usageLine{}
	for _, l := range u.Lines {
		lines[l.Label] = l
	}
	if lines["zen"].Cents != 1500 || lines["zen"].Units != 2 {
		t.Fatalf("usage 90d zen line = %+v, want cents 1500 units 2", lines["zen"])
	}
}

// ── invoices: honest empty typed array (no commerce invoice ledger today) ──

func TestFinanceInvoices_HonestEmpty(t *testing.T) {
	f := &financeFake{}
	app := mountApp(t, f.server(t).URL, "svc-token")
	code, body := call(t, app, http.MethodGet, "/v1/finance/invoices", "acme/dave", "acme")
	if code != http.StatusOK {
		t.Fatalf("invoices = %d (%s)", code, body)
	}
	var inv []financeInvoice
	if err := json.Unmarshal(body, &inv); err != nil {
		t.Fatalf("invoices must be a typed array: %v (%s)", err, body)
	}
	if len(inv) != 0 {
		t.Fatalf("invoices must be honest-empty until an invoice ledger exists, got %d", len(inv))
	}
}

// ── payment-methods: masked to brand+last4, defensive PAN truncation ──

func TestFinancePaymentMethods_MaskedBrandLast4(t *testing.T) {
	f := &financeFake{}
	app := mountApp(t, f.server(t).URL, "svc-token")
	code, body := call(t, app, http.MethodGet, "/v1/finance/payment-methods", "acme/dave", "acme")
	if code != http.StatusOK {
		t.Fatalf("payment-methods = %d (%s)", code, body)
	}
	// The response must NEVER carry the full PAN the upstream leaked.
	if strings.Contains(string(body), "4111111111111111") {
		t.Fatalf("full PAN leaked into the response: %s", body)
	}
	var methods []financePaymentMethod
	if err := json.Unmarshal(body, &methods); err != nil {
		t.Fatalf("decode methods: %v (%s)", err, body)
	}
	if len(methods) != 2 {
		t.Fatalf("want 2 methods, got %d: %+v", len(methods), methods)
	}
	byID := map[string]financePaymentMethod{}
	for _, m := range methods {
		byID[m.ID] = m
	}
	if got := byID["pm_1"]; got.Brand != "visa" || got.Last4 != "4242" || got.Type != "card" || !got.IsDefault {
		t.Fatalf("pm_1 masked descriptor wrong: %+v", got)
	}
	// The 16-digit leak is truncated to the trailing four; the nested card brand/expiry surface.
	if got := byID["pm_2"]; got.Brand != "mastercard" || got.Last4 != "1111" || got.ExpMonth != 3 || got.ExpYear != 2028 {
		t.Fatalf("pm_2 must be masked to last4 1111: %+v", got)
	}
	// The portal read is customerId-scoped to the caller's own org.
	if f.gotPath != "/v1/billing/portal/payment-methods" || f.gotCustomerID != "acme" {
		t.Fatalf("payment-methods scope: path=%q customerId=%q", f.gotPath, f.gotCustomerID)
	}
}

// ── ledger: signed postings (deposit +, withdraw −) into per-org accounts ──

func TestFinanceLedger_SignedPostings(t *testing.T) {
	f := &financeFake{}
	app := mountApp(t, f.server(t).URL, "svc-token")
	code, body := call(t, app, http.MethodGet, "/v1/finance/ledger?range=90d", "acme/dave", "acme")
	if code != http.StatusOK {
		t.Fatalf("ledger = %d (%s)", code, body)
	}
	var entries []financeLedgerEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("decode ledger: %v (%s)", err, body)
	}
	// All five postings are within 90d.
	if len(entries) != 5 {
		t.Fatalf("want 5 ledger postings, got %d: %+v", len(entries), entries)
	}
	byID := map[string]financeLedgerEntry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	// A deposit credits the wallet (+), into the credits account.
	if d := byID["d1"]; d.Cents != 50000 || d.Account != "credits:acme" {
		t.Fatalf("deposit posting wrong: %+v", d)
	}
	// A withdraw debits the wallet (−), into the usage account.
	if wdr := byID["w1"]; wdr.Cents != -1200 || wdr.Account != "usage:acme" {
		t.Fatalf("withdraw posting must be negative usage:acme: %+v", wdr)
	}
}

// ── auth + config gates (shared across every finance read) ──

func TestFinance_NoPrincipal_401_NeverTouchesCommerce(t *testing.T) {
	for _, path := range []string{"/v1/finance/balance", "/v1/finance/credits", "/v1/finance/usage", "/v1/finance/invoices", "/v1/finance/payment-methods", "/v1/finance/ledger"} {
		f := &financeFake{}
		app := mountApp(t, f.server(t).URL, "svc-token")
		// A forged X-Org-Id with NO validated principal (no X-User-Id) must be refused 401.
		code, _ := call(t, app, http.MethodGet, path, "", "victim")
		if code != http.StatusUnauthorized {
			t.Fatalf("%s no-principal: want 401, got %d", path, code)
		}
		if f.gotOrg != "" {
			t.Fatalf("%s must NOT reach commerce unauthenticated, saw org=%q", path, f.gotOrg)
		}
	}
}

func TestFinance_CommerceUnconfigured_501(t *testing.T) {
	app := mountApp(t, "", "") // no commerce base/token
	// invoices is honest-empty even unconfigured (no upstream); the rest need commerce → 501.
	for _, path := range []string{"/v1/finance/balance", "/v1/finance/credits", "/v1/finance/usage", "/v1/finance/payment-methods", "/v1/finance/ledger"} {
		code, _ := call(t, app, http.MethodGet, path, "acme/dave", "acme")
		if code != http.StatusNotImplemented {
			t.Fatalf("%s unconfigured: want 501, got %d", path, code)
		}
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/finance/invoices", "acme/dave", "acme"); code != http.StatusOK {
		t.Fatalf("invoices unconfigured: want 200 honest-empty, got %d", code)
	}
}
