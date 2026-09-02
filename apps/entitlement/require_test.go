package entitlement

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/money"
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

// publish installs a fake ledger as the process-wide money client (finance.Current(),
// the SAME client the ai gate and the edge meter resolve through), restoring the prior
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

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func decodeRefusal(t *testing.T, body []byte) cloud.Refusal {
	t.Helper()
	var r cloud.Refusal
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode Refusal: %v (body=%s)", err, body)
	}
	return r
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

// ── GET /v1/entitlement projection ────────────────────────────────────────────

func decodeProjection(t *testing.T, body []byte) projectionView {
	t.Helper()
	var v projectionView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode projectionView: %v (body=%s)", err, body)
	}
	return v
}

// mountProjection wires just GET /v1/entitlement over the given commerce client.
func mountProjection(t *testing.T, commerce cloud.CommerceClient) *zip.App {
	t.Helper()
	s := &service{store: openTestStore(t), commerce: commerce, log: luxlog.New("test")}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, s)
	return app
}

// wantShellKeys asserts the apps map carries EXACTLY the keys the console shell maps
// over, plus admin — the shell iterates them unconditionally, so a missing key is a
// contract break in the console.
//
// Derived from shellApps rather than a hand-copied list: the count was spelled "six"
// in a literal and in a length check, so changing the surface meant remembering two
// places that never mention each other.
func wantShellKeys(t *testing.T, apps map[string]bool) {
	t.Helper()
	for _, k := range append(append([]string{}, shellApps...), "admin") {
		if _, ok := apps[k]; !ok {
			t.Fatalf("apps missing key %q; got %v", k, apps)
		}
	}
	if want := len(shellApps) + 1; len(apps) != want {
		t.Fatalf("apps must have exactly %d keys, got %d: %v", want, len(apps), apps)
	}
}

func TestProjectionUnvalidatedForbidden(t *testing.T) {
	app := mountProjection(t, newFakeCommerce())
	req := jsonReq("GET", "/v1/entitlement", nil)
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

	code, body := send(t, app, orgMember("GET", "/v1/entitlement", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("projection: want 200, got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantShellKeys(t, v.Apps)
	if v.Tier != "test" { // fakeCommerce resolves every org to plan "test"
		t.Fatalf("tier = %q, want %q", v.Tier, "test")
	}
	if !v.Apps["team"] || !v.Apps["world"] {
		t.Fatalf("granted apps must be true: %v", v.Apps)
	}
	// studio/bot/platform are not gated by any plan, so the projection reports them
	// unlocked rather than claiming a lock no purchase can lift.
	if !v.Apps["studio"] || !v.Apps["bot"] || !v.Apps["platform"] {
		t.Fatalf("ungranted apps must be false: %v", v.Apps)
	}
	if v.Apps["admin"] {
		t.Fatalf("non-admin caller must have admin=false: %v", v.Apps)
	}
}

func TestProjectionAdminBit(t *testing.T) {
	// A super admin (X-User-IsAdmin=true) reports admin:true regardless of products.
	app := mountProjection(t, newFakeCommerce())
	code, body := send(t, app, superAdmin("GET", "/v1/entitlement", nil))
	if code != http.StatusOK {
		t.Fatalf("admin projection: want 200, got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantShellKeys(t, v.Apps)
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

	code, body := send(t, app, orgMember("GET", "/v1/entitlement", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("commerce error: want 200 (fail-safe), got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantShellKeys(t, v.Apps)
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
	code, body := send(t, app, orgMember("GET", "/v1/entitlement", "acme", nil))
	if code != http.StatusOK {
		t.Fatalf("nil commerce: want 200, got %d (body=%s)", code, body)
	}
	v := decodeProjection(t, body)
	wantShellKeys(t, v.Apps)
	for _, k := range appProducts {
		if v.Apps[k] {
			t.Fatalf("nil commerce app %q must be false: %v", k, v.Apps)
		}
	}
}
