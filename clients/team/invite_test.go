package team

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/clients/team/token"
	"github.com/hanzoai/cloud/types"
	model "github.com/hanzoai/iam/pkg/model"
)

// TestOrgGrantRoleCaps proves a workspace invite can never confer an org-level
// IAM admin/owner: owner/admin collapse to member; member and guest pass through.
func TestOrgGrantRoleCaps(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"owner", "member"},
		{"admin", "member"},
		{"member", "member"},
		{roleGuest, roleGuest},
	} {
		if got := orgGrantRole(tc.in); got != tc.want {
			t.Fatalf("orgGrantRole(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// inviteIAM is the get-user/add-membership IAM mock the invite path calls. It always
// resolves the invitee and accepts the membership write, so the test exercises the
// ENTITLE gate at the add point rather than IAM behavior.
func inviteIAM(t *testing.T, org, inviteeSub string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/iam/get-user":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]string{
				"owner": org, "name": "eve", "id": inviteeSub, "email": r.URL.Query().Get("email"), "displayName": "Eve",
			}})
		case "/v1/iam/add-membership":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSendInviteGuestOverCapObserved proves the entitlement/guest-cap gate now runs at
// the ADD point (sendInvite), not only at the guest's later login. On an entitled plan
// with team.guests=1 and one guest already present, inviting a SECOND guest is over the
// cap: the gate RUNS (commerce + plan entitlements consulted), the invitee is genuinely
// over the cap, yet the invite ADMITS in observe mode (never bricks the invite). Under
// enforcement the same call 402s. Discriminating: without the gate wired, entitle is
// never consulted during the invite, so checkCount stays 0 and planEnt is never called.
func TestSendInviteGuestOverCapObserved(t *testing.T) {
	const org = "maxpower"
	const inviterAcct = "550e8400-e29b-41d4-a716-446655440000"
	const inviteeSub = "113d4dd4-2486-40de-be2b-88d6e3e0b718"
	iamSrv := inviteIAM(t, org, inviteeSub)

	store, err := openAccountStore(t.TempDir() + "/account.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	ws, _ := store.EnsureWorkspace(ctx, org, inviterAcct, "Max Power")
	// One guest already present (join order 1) — the plan's cap of 1 is now full.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO members (workspace_id,user_id,role,display_name,is_bot,active,joined_at)
		 VALUES (?,?,?,?,0,1,1)`, ws.ID, "00000000-0000-4000-8000-000000000001", roleGuest, "g1"); err != nil {
		t.Fatal(err)
	}

	commerce := &fakeCommerce{ent: &types.LicenseEntitlement{ProductID: productTeam, Active: true, Plan: "pro"}}
	planCalls := 0
	planEnt := func(_ context.Context, id string) (map[string]any, error) {
		planCalls++
		return map[string]any{guestCapKey: float64(1)}, nil
	}
	g := &api{
		accounts: store,
		cfg:      config{serverSecret: testSecret, iamEndpoint: iamSrv.URL, iamClientID: "hanzo-team", iamClientSecret: "team-secret", provider: "openid"},
		log:      luxlog.New("test"),
		commerce: commerce,
		planEnt:  planEnt,
	}
	app := newTestApp(g)

	sess, _ := token.Generate(inviterAcct, "", orgsExtra(org, model.OrgRef{Org: org, Role: "admin"}),
		expUnix(sessionTokenTTL), testSecret)
	code, body := call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + sess},
		map[string]any{"method": "sendInvite", "params": map[string]any{
			"email": "eve@example.com", "workspaceUrl": ws.Slug, "role": roleGuest}})

	// Observe mode: the over-cap guest invite is ADMITTED (never a 402 today).
	if code != http.StatusOK {
		t.Fatalf("over-cap guest invite = %d, want 200 in observe mode (%s)", code, body)
	}
	var r struct {
		Result map[string]any `json:"result"`
		Error  *Status        `json:"error"`
	}
	if json.Unmarshal(body, &r) != nil || r.Error != nil {
		t.Fatalf("invite must admit in observe mode, got %s", body)
	}
	// The gate RAN at the add point: entitle consulted commerce AND the plan cap.
	if commerce.checkCount() == 0 {
		t.Fatal("entitle gate did not run at sendInvite (commerce never consulted)")
	}
	if planCalls == 0 {
		t.Fatal("entitle guest-cap branch did not run at sendInvite (plan entitlements never read)")
	}
	// And this was a genuine over-cap add: the invitee's guest rank exceeds the cap of 1.
	inviteeAcct := accountID(inviteeSub)
	if rank := store.GuestRank(ctx, ws.ID, inviteeAcct); rank <= 1 {
		t.Fatalf("invitee guest rank = %d, want > 1 (a genuine over-cap add)", rank)
	}
}

// TestSendInviteGuestInfraErrorAdmits proves the add-point gate NEVER bricks an invite
// on a licensing outage: an ERRORING commerce (not a definitive no) admits, and the
// invite is recorded. The gate still ran (commerce was consulted) — it just degraded
// to open, exactly like the login gate.
func TestSendInviteGuestInfraErrorAdmits(t *testing.T) {
	const org = "maxpower"
	const inviterAcct = "550e8400-e29b-41d4-a716-446655440000"
	const inviteeSub = "113d4dd4-2486-40de-be2b-88d6e3e0b718"
	iamSrv := inviteIAM(t, org, inviteeSub)

	store, err := openAccountStore(t.TempDir() + "/account.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	ws, _ := store.EnsureWorkspace(ctx, org, inviterAcct, "Max Power")

	commerce := &fakeCommerce{err: fmt.Errorf("commerce not co-resident")}
	g := &api{
		accounts: store,
		cfg:      config{serverSecret: testSecret, iamEndpoint: iamSrv.URL, iamClientID: "hanzo-team", iamClientSecret: "team-secret", provider: "openid"},
		log:      luxlog.New("test"),
		commerce: commerce,
	}
	app := newTestApp(g)

	sess, _ := token.Generate(inviterAcct, "", orgsExtra(org, model.OrgRef{Org: org, Role: "admin"}),
		expUnix(sessionTokenTTL), testSecret)
	code, body := call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + sess},
		map[string]any{"method": "sendInvite", "params": map[string]any{
			"email": "eve@example.com", "workspaceUrl": ws.Slug, "role": roleGuest}})

	if code != http.StatusOK {
		t.Fatalf("infra-error invite = %d, want 200 (never brick an invite) (%s)", code, body)
	}
	if commerce.checkCount() == 0 {
		t.Fatal("gate did not run at sendInvite (commerce never consulted)")
	}
	// The invite was recorded despite the licensing outage.
	if _, ok := store.Membership(ctx, ws.ID, accountID(inviteeSub)); !ok {
		t.Fatal("invite must be recorded even when the licensing read errors")
	}
}
