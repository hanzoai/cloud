package cloud

// Tests for the ONE spend predicate and the gate that applies it.
//
// They drive real requests through the zip/fiber stack against a fake finance
// ledger published on the SAME process-wide seam the ai gate and the edge meter
// resolve through (finance.Publish), so the address the gate reads is the address a
// debit would write. Nothing about the predicate is mocked.

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// ── harness ─────────────────────────────────────────────────────────────────────

// spendLedger is an in-memory finance ledger. It answers every address with the same
// balance (the tables are about the VERDICT), but records the address it was asked
// for so a dedicated test can assert the gate reads the wallet the debit writes.
type spendLedger struct {
	credit  money.Amount
	err     error
	reads   int
	ledger  string // last Balance() org
	account string // last Balance() subject
}

var _ types.FinanceClient = (*spendLedger)(nil)

func (f *spendLedger) Balance(_ context.Context, org, subject, _ string, _ bool) (money.Amount, error) {
	f.reads++
	f.ledger, f.account = org, subject
	if f.err != nil {
		return money.Zero(), f.err
	}
	return f.credit, nil
}

func (f *spendLedger) Deposit(context.Context, types.DepositInput) (string, error) { return "", nil }
func (f *spendLedger) RecordUsage(context.Context, types.UsageInput) error         { return nil }
func (f *spendLedger) SumUsageSince(context.Context, string, bool, int64) (int64, error) {
	return 0, nil
}

// publishLedger installs a fake ledger as the process-wide money seam, restoring the
// prior one on cleanup. nil models a split deploy: no co-resident money layer.
func publishLedger(t *testing.T, f *spendLedger) {
	t.Helper()
	prev := finance.Current()
	if f == nil {
		finance.Publish(nil)
	} else {
		finance.Publish(f)
	}
	t.Cleanup(func() { finance.Publish(prev) })
}

// atto builds an exact 18-decimal credit amount from a whole-atto integer, so the
// admit boundary is asserted at the true unit — no cents, no rounding.
func atto(n int64) money.Amount { return money.FromAtto(big.NewInt(n)) }

// switches installs a platform-switch reader for the duration of a test. This is the
// ONLY way enforcement turns on, which is itself the property under test.
func switches(t *testing.T, on map[string]bool) {
	t.Helper()
	SetSwitchReader(func(k string) bool { return on[k] })
	t.Cleanup(func() { SetSwitchReader(nil) })
}

// planStub is the subscription authority. paid/err drive the licence leg.
type planStub struct {
	paid bool
	err  error
	// CommerceClient is embedded (nil) purely so planStub satisfies the interface
	// SpendGate takes; only ActivePaidPlan is ever called.
	CommerceClient
}

func (p *planStub) ActivePaidPlan(context.Context, string) (string, bool, error) {
	if p.err != nil {
		return "", false, p.err
	}
	return "pro", p.paid, nil
}

// spendProbe mounts SpendGate ahead of a terminal 200 handler, so a test asserts the
// verdict by status: 200 = admitted (reached the handler), 402 = refused by the gate.
func spendProbe(commerce CommerceClient) *zip.App {
	app := zip.New(zip.Config{})
	app.Use(SpendGate(commerce))
	h := func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "served"}) }
	for _, p := range []string{"/v1/chat/completions", "/v1/models", "/v1/billing/credit", "/v1/ml/train", "/v1/health"} {
		app.Post(p, h)
		app.Get(p, h)
	}
	return app
}

// call drives one request and returns status + body.
func call(t *testing.T, app *zip.App, method, path string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, buf[:n]
}

// member is a plain validated, non-admin principal in org `hanzo` — precisely the
// self-serve signup shape the free-inference hole lives in.
var member = map[string]string{
	"X-User-Id":    "u_1",
	"X-User-Name":  "stranger",
	"X-Org-Id":     "hanzo",
	"X-User-Owner": "hanzo",
}

// ── the split: billable(path) is NOT price(path) ────────────────────────────────

// TestBillableIsNotPrice is the regression that names the whole bug. Authorization
// and pricing were ONE int64: DefaultPrice returns 0 for every inference path (so the
// subsystems can self-meter without double-billing) and BillingGate read `cents <= 0`
// as "do not gate". Pricing at zero therefore silently un-AUTHORIZED the path. If
// these two ever agree again, the leak is back.
func TestBillableIsNotPrice(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/ai/chat", "/v1/ml/train"} {
		app := zip.New(zip.Config{})
		var priced int64
		app.Post(path, func(c *zip.Ctx) error { priced = DefaultPrice(c); return c.JSON(200, "") })
		if _, _ = call(t, app, http.MethodPost, path, nil); priced != 0 {
			t.Fatalf("%s: DefaultPrice = %d, want 0 (the subsystem self-meters)", path, priced)
		}
		if !Billable(http.MethodPost, path) {
			t.Fatalf("%s: priced at 0 AND not billable — that fusion is the free-inference hole", path)
		}
	}
}

func TestBillable(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
		why          string
	}{
		// The free-inference hole: every path a paid upstream serves.
		{"POST", "/v1/chat/completions", true, "the default chat surface"},
		{"POST", "/v1/messages", true, "the Anthropic-shaped surface zen also claims"},
		{"POST", "/v1/completions", true, "legacy completions"},
		{"POST", "/v1/embeddings", true, "embeddings call a paid upstream too"},
		{"POST", "/v1/responses", true, "the responses surface"},
		{"POST", "/v1/ai/chat", true, "ai's own tree"},

		// The non-LLM auth-not-balance gap — the same predicate, not a second one.
		{"POST", "/v1/ml/train", true, "provisioned compute"},
		{"POST", "/v1/s3/bucket", true, "object storage data plane"},
		{"POST", "/v1/agents/run", true, "per-run agent fee"},
		{"POST", "/v1/security/scan", true, "scan fee"},

		// Reads are NEVER billable. Gating them has already caused one outage, and a
		// balance view that 402s is unusable.
		{"GET", "/v1/chat/completions", false, "a read never spends"},
		{"HEAD", "/v1/ml/train", false, "a read never spends"},
		{"OPTIONS", "/v1/chat/completions", false, "CORS preflight must never 402"},

		// The path to payment, and the surfaces that render it.
		{"POST", "/v1/billing/credit", false, "topping up must not require credit"},
		{"POST", "/v1/billing/webhooks/square", false, "gating an inbound payment webhook loses payments"},
		{"POST", "/v1/commerce/checkout", false, "the checkout plane"},
		{"POST", "/v1/iam/token", false, "you must be able to sign in"},
		{"POST", "/v1/admin/grants", false, "the cockpit holds this gate's kill switch"},
		{"POST", "/v1/signin", false, "session bootstrap"},
		{"POST", "/anything", false, "non-/v1 is the SPA shell"},

		// Not metered: telemetry ingest and liveness spend nobody's money.
		{"POST", "/v1/o11y/ingest", false, "telemetry ingest is not user-billable"},
		{"POST", "/v1/ml/health", false, "a probe is never billed"},
	}
	for _, tc := range cases {
		if got := Billable(tc.method, tc.path); got != tc.want {
			t.Errorf("Billable(%s %s) = %v, want %v — %s", tc.method, tc.path, got, tc.want, tc.why)
		}
	}
}

// ── the gate ────────────────────────────────────────────────────────────────────

func TestSpendGate(t *testing.T) {
	broke := errors.New("commerce unreachable")
	cases := []struct {
		name     string
		enforced bool
		strict   bool
		plans    *planStub
		ledger   *spendLedger
		hdr      map[string]string
		path     string
		method   string
		want     int
		reason   string
	}{
		{
			name: "DARK by default: a $0 stranger is admitted and NO authority is consulted",
			// This is the shipped posture. It must stay this way until a starter-credit
			// path exists, or enforcement 402s every new signup on day one.
			enforced: false, plans: &planStub{}, ledger: &spendLedger{credit: atto(0)},
			hdr: member, want: 200,
		},
		{
			name:     "THE HOLE: enforced, $0 stranger on the default chat surface → 402",
			enforced: true, plans: &planStub{}, ledger: &spendLedger{credit: atto(0)},
			hdr: member, want: http.StatusPaymentRequired, reason: ReasonUnpaid,
		},
		{
			name:     "subscription, no credit → admit",
			enforced: true, plans: &planStub{paid: true}, ledger: &spendLedger{credit: atto(0)},
			hdr: member, want: 200,
		},
		{
			name:     "credit, no subscription → admit (pay-as-you-go)",
			enforced: true, plans: &planStub{}, ledger: &spendLedger{credit: atto(1)},
			hdr: member, want: 200,
		},
		{
			name:     "ONE ATTO admits — the boundary is Sign() > 0, not a cent",
			enforced: true, plans: &planStub{}, ledger: &spendLedger{credit: atto(1)},
			hdr: member, want: 200,
		},
		{
			name:     "zero atto denies",
			enforced: true, plans: &planStub{}, ledger: &spendLedger{credit: atto(0)},
			hdr: member, want: http.StatusPaymentRequired, reason: ReasonUnpaid,
		},

		// UNKNOWN IS NOT UNPAID.
		{
			name:     "ledger unreadable + plan says no → UNKNOWN, admitted (default posture)",
			enforced: true, plans: &planStub{}, ledger: &spendLedger{err: broke},
			hdr: member, want: 200,
		},
		{
			name:     "no co-resident ledger + plan says no → UNKNOWN, admitted",
			enforced: true, plans: &planStub{}, ledger: nil,
			hdr: member, want: 200,
		},
		{
			name:     "strict: UNKNOWN refuses, and with the DISTINCT unresolved code",
			enforced: true, strict: true, plans: &planStub{err: broke}, ledger: nil,
			hdr: member, want: http.StatusPaymentRequired, reason: ReasonUnresolved,
		},
		{
			name:     "strict does NOT refuse a FUNDED caller whose plan is unreadable",
			enforced: true, strict: true, plans: &planStub{err: broke}, ledger: &spendLedger{credit: atto(1)},
			hdr: member, want: 200,
		},

		// Legitimate zero-balance paths that MUST survive enforcement.
		{
			name:     "SuperAdmin masquerade never 402s",
			enforced: true, plans: &planStub{}, ledger: &spendLedger{credit: atto(0)},
			hdr: map[string]string{
				"X-User-Id": "u_admin", "X-User-Name": "root", "X-Org-Id": "victim",
				"X-User-Owner": "admin", "X-User-IsAdmin": "true",
			},
			want: 200,
		},
		{
			name:     "anonymous is the route's own 401 to make, never a 402",
			enforced: true, plans: &planStub{}, ledger: &spendLedger{credit: atto(0)},
			hdr:  map[string]string{"X-Org-Id": "hanzo"}, // no X-User-Id ⟹ unvalidated
			want: 200,
		},
		{
			name:     "a READ is never gated, even at $0",
			enforced: true, plans: &planStub{}, ledger: &spendLedger{credit: atto(0)},
			hdr: member, method: http.MethodGet, want: 200,
		},
		{
			name:     "the pay path is never gated, even at $0",
			enforced: true, plans: &planStub{}, ledger: &spendLedger{credit: atto(0)},
			hdr: member, path: "/v1/billing/credit", want: 200,
		},
		{
			name:     "the model catalog stays readable at $0",
			enforced: true, plans: &planStub{}, ledger: &spendLedger{credit: atto(0)},
			hdr: member, path: "/v1/models", want: 200,
		},

		// The non-LLM gap closes through the SAME gate, not a second one.
		{
			name:     "non-LLM provisioning is gated by the same predicate",
			enforced: true, plans: &planStub{}, ledger: &spendLedger{credit: atto(0)},
			hdr: member, path: "/v1/ml/train", want: http.StatusPaymentRequired, reason: ReasonUnpaid,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			switches(t, map[string]bool{
				SwitchPaywallEnforced: tc.enforced,
				SwitchPaywallStrict:   tc.strict,
			})
			publishLedger(t, tc.ledger)

			path := tc.path
			if path == "" {
				path = "/v1/chat/completions"
			}
			method := tc.method
			if method == "" {
				method = http.MethodPost
			}
			code, body := call(t, spendProbe(tc.plans), method, path, tc.hdr)
			if code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", code, tc.want, body)
			}
			if tc.reason != "" {
				var r Refusal
				if err := json.Unmarshal(body, &r); err != nil {
					t.Fatalf("decode Refusal: %v (body=%s)", err, body)
				}
				if r.Reason != tc.reason {
					t.Fatalf("Refusal.Reason = %q, want %q", r.Reason, tc.reason)
				}
				if r.Error != "payment_required" || r.Message == "" || len(r.Cure) == 0 {
					t.Fatalf("refusal must be actionable: %+v", r)
				}
			}
		})
	}
}

// TestSpendGateReadsTheWalletTheDebitWrites is the property every prior recurrence of
// this bug violated: the gate read the ORG POOL while the debit spent the PERSON's
// wallet. In the shared signup org those are different addresses and the pool is
// funded, so a brand-new $0 account read a six-figure balance and sailed through —
// which is the live free-inference hole (apps/zen.go still gates the pool today).
//
// The address must be principal.WalletOf's: ledger "hanzo", account "hanzo/stranger"
// — NOT the bare org slug, which finance resolves to the pool.
func TestSpendGateReadsTheWalletTheDebitWrites(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: true})
	led := &spendLedger{credit: atto(0)}
	publishLedger(t, led)

	code, body := call(t, spendProbe(&planStub{}), http.MethodPost, "/v1/chat/completions", member)
	if code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402 (body=%s)", code, body)
	}
	if led.reads == 0 {
		t.Fatal("the gate never read a balance — it cannot be gating on one")
	}
	if led.ledger != "hanzo" {
		t.Fatalf("ledger = %q, want %q (the org whose books hold the wallet)", led.ledger, "hanzo")
	}
	if led.account != "hanzo/stranger" {
		t.Fatalf("account = %q, want %q — reading the bare org slug is the POOL, "+
			"and the funded pool of the shared signup org is exactly what admits every "+
			"free rider", led.account, "hanzo/stranger")
	}
}

// TestSpendGateDarkConsultsNothing pins the kill switch's cost and its honesty: OFF
// must not merely admit, it must consult NO authority at all. A dark gate that still
// reads the ledger would make the switch a performance lie and could take the money
// layer's outage into a path that is supposed to be inert.
func TestSpendGateDarkConsultsNothing(t *testing.T) {
	switches(t, map[string]bool{SwitchPaywallEnforced: false})
	led := &spendLedger{credit: atto(0)}
	publishLedger(t, led)

	if code, body := call(t, spendProbe(&planStub{}), http.MethodPost, "/v1/chat/completions", member); code != 200 {
		t.Fatalf("dark gate must admit: status = %d (body=%s)", code, body)
	}
	if led.reads != 0 {
		t.Fatalf("dark gate consulted the ledger %d times — it must touch no authority", led.reads)
	}
}

// TestSpendGateUnmountedSwitchNeverEnforces is the boot-order safety property: before
// the flag engine mounts, Switch reports false. If that ever inverted, every request
// served during startup would 402.
func TestSpendGateUnmountedSwitchNeverEnforces(t *testing.T) {
	SetSwitchReader(nil)
	publishLedger(t, &spendLedger{credit: atto(0)})
	if code, body := call(t, spendProbe(&planStub{}), http.MethodPost, "/v1/chat/completions", member); code != 200 {
		t.Fatalf("no flag engine mounted must mean no enforcement: status = %d (body=%s)", code, body)
	}
}

// ── the predicate, directly ─────────────────────────────────────────────────────

// TestStandNeverInventsAnUnpaid is the money-correctness invariant: Unpaid requires
// BOTH authorities to have answered no. Anything less is Unknown, and enforcement —
// not this predicate — decides what to do about that.
func TestStandNeverInventsAnUnpaid(t *testing.T) {
	w := principal.Wallet{Ledger: "hanzo", Account: "hanzo/stranger"}
	cases := []struct {
		name   string
		lic    Licence
		ledger *spendLedger
		want   Standing
	}{
		{"both said no → the only proven refusal", LicenceNone, &spendLedger{credit: atto(0)}, Unpaid},
		{"licence unknown, wallet empty → UNKNOWN, not unpaid", LicenceUnknown, &spendLedger{credit: atto(0)}, Unknown},
		{"licence says no, ledger unreadable → UNKNOWN", LicenceNone, &spendLedger{err: errors.New("down")}, Unknown},
		{"licence says no, no ledger published → UNKNOWN", LicenceNone, nil, Unknown},
		{"active licence short-circuits, ledger untouched", LicenceActive, nil, Subscribed},
		{"one atto is credit", LicenceNone, &spendLedger{credit: atto(1)}, Funded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publishLedger(t, tc.ledger)
			if got := Stand(context.Background(), tc.lic, w); got != tc.want {
				t.Fatalf("Stand = %v, want %v", got, tc.want)
			}
			if tc.want == Unknown && Unknown.Admits() {
				t.Fatal("Unknown must never admit on its own merits")
			}
		})
	}
}

// TestStandSubscribedNeverReadsTheLedger pins the cheapest-decisive-first order: an
// entitled caller must not pay a ledger round trip on every request.
func TestStandSubscribedNeverReadsTheLedger(t *testing.T) {
	led := &spendLedger{credit: atto(0)}
	publishLedger(t, led)
	if got := Stand(context.Background(), LicenceActive, principal.Wallet{Ledger: "acme", Account: "acme/bob"}); got != Subscribed {
		t.Fatalf("Stand = %v, want Subscribed", got)
	}
	if led.reads != 0 {
		t.Fatalf("a subscribed caller read the ledger %d times; the legs are ordered to avoid that", led.reads)
	}
}
