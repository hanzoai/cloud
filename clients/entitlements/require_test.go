package entitlements

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"testing"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/flags"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ── paywall harness ────────────────────────────────────────────────────────────

// setFlag overrides a registered switch for ONE test by re-registering its Def with
// a new literal default, restoring the original on cleanup. It drives the REAL read
// path (flags.Bool → the registry; no platform store is mounted in a unit test, so
// the literal default is what resolves), which is the point: these tests prove the
// gate reads the ONE flag engine, not a bool handed to it.
func setFlag(t *testing.T, key, value string) {
	t.Helper()
	var orig flags.Def
	for _, d := range flags.Defs() {
		if d.Key == key {
			orig = d
			break
		}
	}
	if orig.Key == "" {
		t.Fatalf("flag %q is not registered — the gate must register its switches in init", key)
	}
	next := orig
	next.Default = value
	flags.Register(next)
	t.Cleanup(func() { flags.Register(orig) })
}

// atto builds an exact 18-decimal USD credit from a raw atto magnitude, so a test
// can sit ON the admit boundary (0 vs 1 atto) with no cents and no float anywhere.
func atto(n int64) money.Amount { return money.FromAtto(big.NewInt(n)) }

// fakeLedger is an in-memory finance ledger. It answers every address with the same
// balance (the tables are about the VERDICT), but records the address it was asked
// for so a dedicated test can assert the gate reads the wallet the debit writes.
type fakeLedger struct {
	credit  money.Amount
	err     error
	reads   int
	ledger  string // last Balance() org
	account string // last Balance() subject
}

var _ types.FinanceClient = (*fakeLedger)(nil)

func (f *fakeLedger) Balance(_ context.Context, org, subject, _ string, _ bool) (money.Amount, error) {
	f.reads++
	f.ledger, f.account = org, subject
	if f.err != nil {
		return money.Zero(), f.err
	}
	return f.credit, nil
}

func (f *fakeLedger) Deposit(context.Context, types.DepositInput) (string, error) { return "", nil }
func (f *fakeLedger) RecordUsage(context.Context, types.UsageInput) error         { return nil }
func (f *fakeLedger) SumUsageSince(context.Context, string, bool, int64) (int64, error) {
	return 0, nil
}

// publish installs a fake ledger as the process-wide money seam (finance.Current(),
// the SAME seam the ai gate and the edge meter resolve through), restoring the prior
// one on cleanup. Passing nil models a split deploy: no co-resident money layer.
func publish(t *testing.T, f *fakeLedger) {
	t.Helper()
	prev := finance.Current()
	if f == nil {
		finance.Publish(nil)
	} else {
		finance.Publish(f)
	}
	t.Cleanup(func() { finance.Publish(prev) })
}

// probe mounts a single route guarded by RequireProduct(commerce, product) and a
// terminal 200 handler, so a test asserts the gate's verdict by the status code:
// 200 = admitted (reached the handler), 402/403 = refused by the gate.
func probe(commerce cloud.CommerceClient, product string) *zip.App {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/probe", RequireProduct(commerce, product), func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{"ok": true})
	})
	return app
}

// ── RequireProduct: the gate ───────────────────────────────────────────────────

func TestRequireProduct(t *testing.T) {
	const org, product = "acme", "world"
	down := errors.New("commerce datastore unreachable")

	// commerce fixtures.
	granting := func() *fakeCommerce { fc := newFakeCommerce(); fc.grant(org, product); return fc }
	refusing := func() *fakeCommerce { return newFakeCommerce() } // resolves, grants nothing
	broken := func() *fakeCommerce { fc := newFakeCommerce(); fc.err = down; return fc }

	for _, tc := range []struct {
		name     string
		enforced bool
		strict   bool
		commerce func() *fakeCommerce
		ledger   *fakeLedger // nil ⇒ money layer not co-resident
		want     int
		reason   string // expected Refusal.Reason on a 402
	}{
		// THE MOST IMPORTANT TEST IN THE SUITE: with the kill switch off the gate is a
		// pure passthrough that consults NO authority — so flipping it off in the
		// cockpit instantly and completely restores access, whatever else is broken.
		{
			name:     "kill switch OFF admits an org with neither subscription nor credit",
			enforced: false, commerce: refusing, ledger: &fakeLedger{credit: atto(0)},
			want: http.StatusOK,
		},
		{
			name:     "kill switch OFF admits even when both authorities are down",
			enforced: false, commerce: broken, ledger: &fakeLedger{err: down},
			want: http.StatusOK,
		},

		// The two admit legs, each on its own.
		{
			name:     "subscription, no credit → admit",
			enforced: true, commerce: granting, ledger: &fakeLedger{credit: atto(0)},
			want: http.StatusOK,
		},
		{
			name:     "credit, no subscription → admit",
			enforced: true, commerce: refusing, ledger: &fakeLedger{credit: atto(1)},
			want: http.StatusOK,
		},

		// The one proven refusal.
		{
			name:     "neither → 402 unpaid",
			enforced: true, commerce: refusing, ledger: &fakeLedger{credit: atto(0)},
			want: http.StatusPaymentRequired, reason: reasonUnpaid,
		},

		// EXACT atto boundary — no cents, no threshold, no rounding.
		{
			name:     "balance == 0 atto denies",
			enforced: true, commerce: refusing, ledger: &fakeLedger{credit: atto(0)},
			want: http.StatusPaymentRequired, reason: reasonUnpaid,
		},
		{
			name:     "balance == 1 atto admits (a 10^-18 USD credit is credit)",
			enforced: true, commerce: refusing, ledger: &fakeLedger{credit: atto(1)},
			want: http.StatusOK,
		},

		// Fail posture. An authority that could not ANSWER is never a "no".
		{
			name:     "entitlement oracle down, no ledger → unresolvable, admit",
			enforced: true, commerce: broken, ledger: nil,
			want: http.StatusOK,
		},
		{
			name:     "ledger read fails, plan says no → unresolvable, admit",
			enforced: true, commerce: refusing, ledger: &fakeLedger{err: down},
			want: http.StatusOK,
		},
		{
			name:     "commerce nil, no ledger → unresolvable, admit",
			enforced: true, commerce: nil, ledger: nil,
			want: http.StatusOK,
		},
		{
			name:     "strict: unresolvable → 402 unresolved",
			enforced: true, strict: true, commerce: broken, ledger: nil,
			want: http.StatusPaymentRequired, reason: reasonUnresolved,
		},
		{
			name:     "strict does NOT refuse a funded caller whose plan is unreadable",
			enforced: true, strict: true, commerce: broken, ledger: &fakeLedger{credit: atto(1)},
			want: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setFlag(t, enforceKey, boolText(tc.enforced))
			setFlag(t, strictKey, boolText(tc.strict))
			publish(t, tc.ledger)

			var commerce cloud.CommerceClient
			if tc.commerce != nil {
				commerce = tc.commerce()
			}
			code, body := send(t, probe(commerce, product), orgMember("GET", "/v1/probe", org, nil))
			if code != tc.want {
				t.Fatalf("want %d, got %d (body=%s)", tc.want, code, body)
			}
			if tc.reason != "" {
				if got := decodeRefusal(t, body).Reason; got != tc.reason {
					t.Fatalf("Refusal.Reason = %q, want %q (body=%s)", got, tc.reason, body)
				}
			}
		})
	}
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func decodeRefusal(t *testing.T, body []byte) Refusal {
	t.Helper()
	var r Refusal
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode Refusal: %v (body=%s)", err, body)
	}
	return r
}

// The kill switch must short-circuit BEFORE any authority is touched — otherwise a
// dark gate still costs a commerce query and a ledger read on every request, and a
// broken authority could still surface as latency or error noise.
func TestKillSwitchConsultsNoAuthority(t *testing.T) {
	setFlag(t, enforceKey, "false")
	fc := newFakeCommerce()
	led := &fakeLedger{credit: atto(0)}
	publish(t, led)

	code, body := send(t, probe(fc, "world"), orgMember("GET", "/v1/probe", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("dark gate: want 200, got %d (body=%s)", code, body)
	}
	if fc.calls != 0 || led.reads != 0 {
		t.Fatalf("dark gate must consult nothing: commerce calls=%d ledger reads=%d", fc.calls, led.reads)
	}
}

func TestRequireProductIdentityAndSudo(t *testing.T) {
	const org, product = "acme", "world"

	t.Run("unvalidated principal → 403, no authority consulted", func(t *testing.T) {
		setFlag(t, enforceKey, "true")
		fc := newFakeCommerce()
		fc.grant(org, product)
		led := &fakeLedger{credit: atto(1)}
		publish(t, led)

		// Forged: X-Org-Id present but NO X-User-Id ⇒ not a validated principal.
		req := jsonReq("GET", "/v1/probe", nil)
		req.Header.Set("X-Org-Id", org)
		code, body := send(t, probe(fc, product), req)
		if code != http.StatusForbidden {
			t.Fatalf("unvalidated: want 403, got %d (body=%s)", code, body)
		}
		if fc.calls != 0 || led.reads != 0 {
			t.Fatalf("no authority may be consulted for an unvalidated caller: calls=%d reads=%d", fc.calls, led.reads)
		}
	})

	t.Run("super admin bypasses — platform sudo is not a purchasable tier", func(t *testing.T) {
		setFlag(t, enforceKey, "true")
		fc := newFakeCommerce() // grants nothing
		led := &fakeLedger{credit: atto(0)}
		publish(t, led)

		code, body := send(t, probe(fc, product), superAdmin("GET", "/v1/probe", nil))
		if code != http.StatusOK {
			t.Fatalf("super admin: want 200, got %d (body=%s)", code, body)
		}
		if fc.calls != 0 || led.reads != 0 {
			t.Fatalf("super admin must bypass every authority: calls=%d reads=%d", fc.calls, led.reads)
		}
	})
}

// ★★ The invariant that outranks the goal: an unpaid caller must still REACH the
// paths that let them pay. A gate in front of the pay button is a total revenue stop
// that looks like success — every response is a 402.
func TestPayPathStaysReachable(t *testing.T) {
	setFlag(t, enforceKey, "true")
	fc := newFakeCommerce() // grants nothing
	publish(t, &fakeLedger{credit: atto(0)})

	for _, path := range []string{
		"/v1/billing/plans",               // what to buy
		"/v1/billing/subscribe",           // buying it
		"/v1/billing/webhooks/stripe",     // the INBOUND payment callback — gating it loses money
		"/v1/plans",                       // the @hanzo/plans catalog
		"/v1/entitlements",                // the shell's own upgrade projection
		"/v1/iam/login",                   // signing in to pay at all
		"/v1/ai/signin", "/v1/ai/account", // session bootstrap + the read AuthGate needs
		"/v1/orgs/acme/entitlements",   // which org am I buying for
		"/v1/admin/flags",              // the cockpit holding this gate's kill switch
		"/v1/waitlist",                 // admission's join API
		"/v1/probe/health", "/healthz", // liveness must never be hidden by a paywall
		"/", "/plans", "/assets/app.js", // the SPA that renders the paywall screen
	} {
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		app.Get(path, RequireProduct(fc, "world"), func(c *zip.Ctx) error {
			return c.JSON(http.StatusOK, map[string]any{"ok": true})
		})
		code, body := send(t, app, orgMember("GET", path, "acme", nil))
		if code != http.StatusOK {
			t.Fatalf("%s must stay reachable without any entitlement: got %d (body=%s)", path, code, body)
		}
	}
}

// The 402 must be ACTIONABLE: a bare status is useless to the console shell, which
// has to render WHAT is gated and WHERE to cure it.
func TestRefusalIsActionable(t *testing.T) {
	setFlag(t, enforceKey, "true")
	publish(t, &fakeLedger{credit: atto(0)})

	code, body := send(t, probe(newFakeCommerce(), "world"), orgMember("GET", "/v1/probe", "acme", nil))
	if code != http.StatusPaymentRequired {
		t.Fatalf("want 402, got %d (body=%s)", code, body)
	}
	r := decodeRefusal(t, body)
	if r.Error != "payment_required" || r.Product != "world" || r.Reason != reasonUnpaid || r.Message == "" {
		t.Fatalf("refusal is not self-describing: %+v", r)
	}
	// One cure per admit leg — the body is the structural mirror of the gate.
	kinds := map[string]string{}
	for _, c := range r.Cure {
		kinds[c.Kind] = c.URL
	}
	for _, want := range []string{"subscribe", "credit"} {
		if kinds[want] == "" {
			t.Fatalf("refusal must name where to %s: %+v", want, r.Cure)
		}
		if !reachable(kinds[want]) {
			t.Fatalf("cure %q points at %q, which the gate itself would refuse", want, kinds[want])
		}
	}
}

// ★ The credit leg must read the wallet the DEBIT writes. Keyed on the org POOL
// instead, every member of the shared signup org would be gated against Hanzo's OWN
// balance and sail straight through — the bypass build.go and middleware_billing.go
// each document having shipped once already.
func TestCreditReadsThePayingWallet(t *testing.T) {
	setFlag(t, enforceKey, "true")

	for _, tc := range []struct{ org, user, wantLedger, wantAccount string }{
		// A real tenant pays from its own pool.
		{"acme", "u_acme", "acme", "acme"},
		// A person in the shared signup org holds their OWN account — NOT the pool.
		{account.SignupOrg, "alice", account.SignupOrg, account.SignupOrg + "/alice"},
	} {
		led := &fakeLedger{credit: atto(0)}
		publish(t, led)
		req := jsonReq("GET", "/v1/probe", nil)
		req.Header.Set("X-Org-Id", tc.org)
		req.Header.Set("X-User-Id", tc.user)
		send(t, probe(newFakeCommerce(), "world"), req)

		if led.reads == 0 {
			t.Fatalf("%s/%s: the credit leg never read the ledger", tc.org, tc.user)
		}
		if led.ledger != tc.wantLedger || led.account != tc.wantAccount {
			t.Fatalf("%s/%s: read address %s/%s, want %s/%s — the gate and the debit must key ONE wallet",
				tc.org, tc.user, led.ledger, led.account, tc.wantLedger, tc.wantAccount)
		}
	}
}

func TestGroupFormGatesChildren(t *testing.T) {
	// The real wiring shape — app.Group(prefix, RequireProduct(...)) — gates a child
	// route exactly as the inline form does.
	setFlag(t, enforceKey, "true")
	publish(t, &fakeLedger{credit: atto(0)})

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	g := app.Group("/v1/probe", RequireProduct(newFakeCommerce(), "world"))
	g.Get("/child", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]any{"ok": true}) })
	code, body := send(t, app, orgMember("GET", "/v1/probe/child", "acme", nil))
	if code != http.StatusPaymentRequired {
		t.Fatalf("group-gated child: want 402, got %d (body=%s)", code, body)
	}
}

// The gate ships DARK. If this ever fails, a deploy silently starts refusing
// customers before an owner has flipped anything.
func TestSwitchesDefaultOff(t *testing.T) {
	for _, key := range []string{enforceKey, strictKey} {
		var d flags.Def
		for _, x := range flags.Defs() {
			if x.Key == key {
				d = x
				break
			}
		}
		if d.Key == "" {
			t.Fatalf("%s is not registered with the flag engine — it would never reach the admin cockpit", key)
		}
		if d.Default != "false" {
			t.Fatalf("%s default = %q, want %q (shipping it on is how you take down production)", key, d.Default, "false")
		}
		if d.Env != "" {
			t.Fatalf("%s carries env fallback %q — the cockpit must be the single source of truth", key, d.Env)
		}
	}
}

// ── GET /v1/entitlements projection ────────────────────────────────────────────

func decodeProjection(t *testing.T, body []byte) projectionView {
	t.Helper()
	var v projectionView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode projectionView: %v (body=%s)", err, body)
	}
	return v
}

// mountProjection wires just GET /v1/entitlements over the given commerce client.
func mountProjection(t *testing.T, commerce cloud.CommerceClient) *zip.App {
	t.Helper()
	s := &service{store: openTestStore(t), commerce: commerce, log: luxlog.New("test")}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/v1/entitlements", s.projection)
	return app
}

// wantSixKeys asserts the apps map carries EXACTLY the six shell keys — the shell
// maps over them unconditionally, so a missing key is a contract break.
func wantSixKeys(t *testing.T, apps map[string]bool) {
	t.Helper()
	for _, k := range []string{"studio", "bot", "world", "platform", "team", "admin"} {
		if _, ok := apps[k]; !ok {
			t.Fatalf("apps missing key %q; got %v", k, apps)
		}
	}
	if len(apps) != 6 {
		t.Fatalf("apps must have exactly 6 keys, got %d: %v", len(apps), apps)
	}
}

func TestProjectionUnvalidatedForbidden(t *testing.T) {
	app := mountProjection(t, newFakeCommerce())
	req := jsonReq("GET", "/v1/entitlements", nil)
	req.Header.Set("X-Org-Id", "acme") // no X-User-Id ⇒ unvalidated
	code, body := send(t, app, req)
	if code != http.StatusForbidden {
		t.Fatalf("unvalidated projection: want 403, got %d (body=%s)", code, body)
	}
}

func TestProjectionPerAppBool(t *testing.T) {
	fc := newFakeCommerce()
	fc.grant("acme", "team")  // acme's plan licenses team ...
	fc.grant("acme", "world") // ... and world
	app := mountProjection(t, fc)

	code, body := send(t, app, orgMember("GET", "/v1/entitlements", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("projection: want 200, got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantSixKeys(t, v.Apps)
	if v.Tier != "test" { // fakeCommerce resolves every org to plan "test"
		t.Fatalf("tier = %q, want %q", v.Tier, "test")
	}
	if !v.Apps["team"] || !v.Apps["world"] {
		t.Fatalf("granted apps must be true: %v", v.Apps)
	}
	if v.Apps["studio"] || v.Apps["bot"] || v.Apps["platform"] {
		t.Fatalf("ungranted apps must be false: %v", v.Apps)
	}
	if v.Apps["admin"] {
		t.Fatalf("non-admin caller must have admin=false: %v", v.Apps)
	}
}

func TestProjectionAdminBit(t *testing.T) {
	// A super admin (X-User-IsAdmin=true) reports admin:true regardless of products.
	app := mountProjection(t, newFakeCommerce())
	code, body := send(t, app, superAdmin("GET", "/v1/entitlements", nil))
	if code != http.StatusOK {
		t.Fatalf("admin projection: want 200, got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantSixKeys(t, v.Apps)
	if !v.Apps["admin"] {
		t.Fatalf("super admin must have admin=true: %v", v.Apps)
	}
}

func TestProjectionFailsSafeNotFiveHundred(t *testing.T) {
	// Commerce error must NOT 500 the endpoint: it returns 200 with every app locked
	// (fail-safe-to-locked), so the shell degrades to locked, never crashes.
	fc := newFakeCommerce()
	fc.err = errors.New("commerce down")
	app := mountProjection(t, fc)

	code, body := send(t, app, orgMember("GET", "/v1/entitlements", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("commerce error: want 200 (fail-safe), got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantSixKeys(t, v.Apps)
	for _, k := range appProducts {
		if v.Apps[k] {
			t.Fatalf("on commerce error app %q must be locked (false): %v", k, v.Apps)
		}
	}
	if v.Tier != "" {
		t.Fatalf("tier on commerce error = %q, want empty", v.Tier)
	}
}

func TestProjectionNilCommerceLocked(t *testing.T) {
	// Commerce not co-resident: every product app locked, admin still reflects the
	// caller's bit, 200 not 500.
	app := mountProjection(t, nil)
	code, body := send(t, app, orgMember("GET", "/v1/entitlements", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("nil commerce: want 200, got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantSixKeys(t, v.Apps)
	for _, k := range appProducts {
		if v.Apps[k] {
			t.Fatalf("nil commerce app %q must be false: %v", k, v.Apps)
		}
	}
}
