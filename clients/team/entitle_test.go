package team

// The entitle gate's contract, end to end over the real selectWorkspace route:
// definitive "no entitlement" → 402 + upgradeUrl; infra errors (commerce or
// plans down) → admitted (the gate can never brick login mid-rollout); guest
// logins beyond the plan's team.guests cap → 402; guests within it → admitted.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/clients/team/token"
	"github.com/hanzoai/cloud/types"
)

type fakeCommerce struct {
	ent *types.LicenseEntitlement
	err error

	mu     sync.Mutex
	checks int // CheckEntitlement call count — lets a test prove the gate actually ran
}

func (f *fakeCommerce) GetOrgConfig(_ context.Context, orgID string) (*types.OrgConfig, error) {
	return &types.OrgConfig{OrgID: orgID}, nil
}

func (f *fakeCommerce) CheckEntitlement(context.Context, string, string) (*types.LicenseEntitlement, error) {
	f.mu.Lock()
	f.checks++
	f.mu.Unlock()
	return f.ent, f.err
}

func (f *fakeCommerce) checkCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks
}

// gateApp registers the account API directly (no Mount) with a fake commerce +
// plan seam, an isolated store, and no route guard — the gate under test is
// entitle itself.
func gateApp(t *testing.T, commerce types.CommerceClient, planEnt func(context.Context, string) (map[string]any, error)) (*zip.App, *accountStore) {
	t.Helper()
	store, err := openAccountStore(t.TempDir() + "/account.db")
	if err != nil {
		t.Fatalf("openAccountStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	g := &api{
		accounts: store,
		cfg:      config{serverSecret: testSecret},
		log:      luxlog.New("test"),
		commerce: commerce,
		planEnt:  planEnt,
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	g.register(app.Group("/v1/team"), func(h zip.Handler) zip.Handler { return h })
	return app, store
}

// selectWS drives the selectWorkspace RPC as `acct` in `org` for `slug`.
func selectWS(t *testing.T, app *zip.App, org, acct, slug string) (int, []byte) {
	t.Helper()
	sess, err := token.Generate(acct, "", map[string]any{"org": org}, expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatalf("mint session: %v", err)
	}
	return call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + sess},
		map[string]any{"method": "selectWorkspace", "params": map[string]any{"workspaceUrl": slug}})
}

const gateOrg = "acme"
const gateAcct = "550e8400-e29b-41d4-a716-446655440000"

// TestEntitleUnentitledAdmitsObserve: a definitive "no entitlement" ADMITS in
// observe mode — the blocking 402 form of this gate broke every login live on
// 2026-07-20 (no org had a subscription row and no self-serve checkout exists
// to escape the 402). Enforcement returns with the card-on-file subscribe path;
// when it does, this test flips back to asserting the 402 + upgradeUrl body.
func TestEntitleUnentitledAdmitsObserve(t *testing.T) {
	app, store := gateApp(t, &fakeCommerce{ent: &types.LicenseEntitlement{ProductID: productTeam, Active: false}}, nil)
	ws, _ := store.EnsureWorkspace(context.Background(), gateOrg, gateAcct, "Ada")
	code, body := selectWS(t, app, gateOrg, gateAcct, ws.Slug)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — unentitled must admit in observe mode (%s)", code, body)
	}
	var out struct {
		Result *WorkspaceLoginInfo `json:"result"`
	}
	if json.Unmarshal(body, &out) != nil || out.Result == nil || out.Result.Workspace != ws.UUID {
		t.Fatalf("observe mode must return a workspace login: %s", body)
	}
}

// TestEntitleInfraErrorAdmits is the deploy-coupling invariant: an ERRORING
// commerce (not a definitive no) admits — the gate cannot brick login.
func TestEntitleInfraErrorAdmits(t *testing.T) {
	app, store := gateApp(t, &fakeCommerce{err: fmt.Errorf("commerce not co-resident")}, nil)
	ws, _ := store.EnsureWorkspace(context.Background(), gateOrg, gateAcct, "Ada")
	code, body := selectWS(t, app, gateOrg, gateAcct, ws.Slug)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", code, body)
	}
	var out struct {
		Result *WorkspaceLoginInfo `json:"result"`
	}
	if json.Unmarshal(body, &out) != nil || out.Result == nil || out.Result.Workspace != ws.UUID {
		t.Fatalf("infra error must admit: %s", body)
	}
}

// TestEntitleGuestCap: on an entitled plan carrying team.guests=3, the first 3
// guests (join order) are admitted and the 4th is 402; a plans-service ERROR
// admits (infra, not a definitive no); the owner is never capped.
func TestEntitleGuestCap(t *testing.T) {
	entitled := &fakeCommerce{ent: &types.LicenseEntitlement{ProductID: productTeam, Active: true, Plan: "pro"}}
	planEnt := func(_ context.Context, id string) (map[string]any, error) {
		if id != "pro" {
			return nil, fmt.Errorf("unknown plan %q", id)
		}
		return map[string]any{guestCapKey: float64(3)}, nil // JSON numbers are float64
	}
	app, store := gateApp(t, entitled, planEnt)
	ctx := context.Background()
	ws, _ := store.EnsureWorkspace(ctx, gateOrg, gateAcct, "Ada")
	guest := func(i int) string { return fmt.Sprintf("00000000-0000-4000-8000-00000000000%d", i) }
	for i := 1; i <= 4; i++ {
		if _, err := store.db.ExecContext(ctx,
			`INSERT INTO members (workspace_id,user_id,role,display_name,is_bot,active,joined_at)
			 VALUES (?,?,?,?,0,1,?)`, ws.ID, guest(i), roleGuest, "g", int64(i)); err != nil {
			t.Fatalf("seed guest %d: %v", i, err)
		}
	}

	if code, body := selectWS(t, app, gateOrg, gateAcct, ws.Slug); code != http.StatusOK {
		t.Fatalf("owner status = %d, want 200 (%s)", code, body)
	}
	for i := 1; i <= 3; i++ {
		if code, body := selectWS(t, app, gateOrg, guest(i), ws.Slug); code != http.StatusOK {
			t.Fatalf("guest %d status = %d, want 200 (%s)", i, code, body)
		}
	}
	// Guest 4 exceeds the cap but ADMITS in observe mode (denial logged, not
	// enforced) — flips back to a 402 assertion when enforcement returns.
	if code, body := selectWS(t, app, gateOrg, guest(4), ws.Slug); code != http.StatusOK {
		t.Fatalf("guest 4 status = %d, want 200 in observe mode (%s)", code, body)
	}

	// Plans service down → guest admitted (infra error, never a lockout).
	appDown, storeDown := gateApp(t, entitled, func(context.Context, string) (map[string]any, error) {
		return nil, fmt.Errorf("plans not mounted")
	})
	wsD, _ := storeDown.EnsureWorkspace(ctx, gateOrg, gateAcct, "Ada")
	if _, err := storeDown.db.ExecContext(ctx,
		`INSERT INTO members (workspace_id,user_id,role,display_name,is_bot,active,joined_at)
		 VALUES (?,?,?,?,0,1,1)`, wsD.ID, guest(1), roleGuest, "g"); err != nil {
		t.Fatal(err)
	}
	if code, body := selectWS(t, appDown, gateOrg, guest(1), wsD.Slug); code != http.StatusOK {
		t.Fatalf("plans-down guest status = %d, want 200 (%s)", code, body)
	}
}
