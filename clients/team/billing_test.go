package team

// The billing plane's contract: the embedded wallet page and the plan read are
// SESSION-GATED (an anonymous caller gets 401, never the page or a tenant's
// numbers) and ORG-SCOPED (seats/guests count ONLY the caller's verified token
// org — never another tenant's).

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/clients/team/token"
	"github.com/hanzoai/cloud/clients/team/wallet"
	"github.com/hanzoai/cloud/types"
)

// billingApp registers the billing plane directly (no Mount) with a fake
// commerce + plan seam and an isolated store — the same harness shape as
// gateApp (entitle_test.go).
func billingApp(t *testing.T, commerce types.CommerceClient, planEnt func(context.Context, string) (map[string]any, error)) (*zip.App, *accountStore) {
	t.Helper()
	store, err := openAccountStore(t.TempDir() + "/account.db")
	if err != nil {
		t.Fatalf("openAccountStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	b := &billingService{accounts: store, commerce: commerce, planEnt: planEnt, secret: testSecret}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	b.register(app.Group("/v1/team"), func(h zip.Handler) zip.Handler { return h })
	return app, store
}

// TestBillingRequiresSession: every billing route is 401 without a verified
// session — anonymous, garbage bearer, and org-less token alike — and the
// routes are live through the REAL Mount wiring.
func TestBillingRequiresSession(t *testing.T) {
	app := mountTeam(t)
	orgless, err := token.Generate(gateAcct, "", nil, expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path    string
		headers map[string]string
	}{
		{"/v1/team/billing/ui/", nil},
		{"/v1/team/billing/ui/index.html", nil},
		{"/v1/team/billing/plan", nil},
		{"/v1/team/billing/plan", map[string]string{"Authorization": "Bearer not-a-token"}},
		{"/v1/team/billing/plan", map[string]string{"Authorization": "Bearer " + orgless}},
	} {
		if code, body := call(t, app, http.MethodGet, tc.path, tc.headers, nil); code != http.StatusUnauthorized {
			t.Fatalf("GET %s (auth=%v) = %d, want 401 (%s)", tc.path, tc.headers != nil, code, body)
		}
	}
}

// TestBillingUIServesEmbeddedPage: an authed caller gets the real embedded
// page — the shell at the mount root AND at any client route (SPA fallback),
// plus the fingerprinted bundle with immutable caching.
func TestBillingUIServesEmbeddedPage(t *testing.T) {
	app := mountTeam(t)
	auth := bearerFor(t, gateAcct, gateOrg)

	for _, p := range []string{"/v1/team/billing/ui", "/v1/team/billing/ui/", "/v1/team/billing/ui/deep/link"} {
		code, body := call(t, app, http.MethodGet, p, auth, nil)
		if code != http.StatusOK || !strings.Contains(string(body), `<div id="root">`) {
			t.Fatalf("GET %s = %d, want the SPA shell (%.80s)", p, code, body)
		}
	}

	// The committed Vite bundle is genuinely embedded and served with the
	// immutable cache hint fingerprinted assets warrant.
	assets, err := fs.Glob(wallet.FS(), "assets/*.js")
	if err != nil || len(assets) == 0 {
		t.Fatalf("no embedded wallet bundle: %v", err)
	}
	code, body := call(t, app, http.MethodGet, "/v1/team/billing/ui/"+assets[0], auth, nil)
	if code != http.StatusOK || len(body) == 0 {
		t.Fatalf("GET %s = %d, want the bundle", assets[0], code)
	}
}

// TestBillingPlanOrgScoped: seats/guests count the caller's OWN org only, and
// plan/cap flow through the commerce + plans seams. A nil commerce (not
// co-resident) leaves plan honestly empty rather than fabricating a tier.
func TestBillingPlanOrgScoped(t *testing.T) {
	entitled := &fakeCommerce{ent: &types.LicenseEntitlement{ProductID: productTeam, Active: true, Plan: "pro"}}
	planEnt := func(_ context.Context, id string) (map[string]any, error) {
		return map[string]any{guestCapKey: float64(3)}, nil
	}
	app, store := billingApp(t, entitled, planEnt)
	ctx := context.Background()

	// Org A: the owner + one member + one guest (3 seats, 1 guest) + a bot
	// (never a seat). Org B: a different tenant with its own members.
	uid := func(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-0000000000%02d", i) }
	wsA, _ := store.EnsureWorkspace(ctx, gateOrg, gateAcct, "Ada")
	seed := func(ws workspace, user, role string, bot int) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO members (workspace_id,user_id,role,display_name,is_bot,active,joined_at)
			 VALUES (?,?,?,?,?,1,1)`, ws.ID, user, role, "m", bot); err != nil {
			t.Fatal(err)
		}
	}
	seed(wsA, uid(1), "member", 0)
	seed(wsA, uid(2), roleGuest, 0)
	seed(wsA, uid(3), "member", 1) // bot — not a seat
	wsB, _ := store.EnsureWorkspace(ctx, "other", uid(9), "Bob")
	seed(wsB, uid(4), "member", 0)

	code, body := call(t, app, http.MethodGet, "/v1/team/billing/plan", bearerFor(t, gateAcct, gateOrg), nil)
	if code != http.StatusOK {
		t.Fatalf("plan = %d (%s)", code, body)
	}
	var out planInfo
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	want := planInfo{Plan: "pro", Active: true, Seats: 3, Guests: 1, GuestLimit: 3, UpgradeURL: upgradeURL}
	if out != want {
		t.Fatalf("plan = %+v, want %+v", out, want)
	}

	// The OTHER tenant sees only its own two seats — never org A's three.
	code, body = call(t, app, http.MethodGet, "/v1/team/billing/plan", bearerFor(t, uid(9), "other"), nil)
	if code != http.StatusOK {
		t.Fatalf("plan(other) = %d (%s)", code, body)
	}
	out = planInfo{}
	_ = json.Unmarshal(body, &out)
	if out.Seats != 2 || out.Guests != 0 {
		t.Fatalf("other org seats/guests = %d/%d, want 2/0", out.Seats, out.Guests)
	}

	// Commerce not co-resident → plan honestly empty, seats still real.
	appNil, storeNil := billingApp(t, nil, nil)
	wsN, _ := storeNil.EnsureWorkspace(ctx, gateOrg, gateAcct, "Ada")
	_ = wsN
	code, body = call(t, appNil, http.MethodGet, "/v1/team/billing/plan", bearerFor(t, gateAcct, gateOrg), nil)
	if code != http.StatusOK {
		t.Fatalf("plan(nil commerce) = %d (%s)", code, body)
	}
	out = planInfo{}
	_ = json.Unmarshal(body, &out)
	if out.Plan != "" || out.Active || out.Seats != 1 {
		t.Fatalf("nil-commerce plan = %+v, want empty plan + 1 seat", out)
	}
}
