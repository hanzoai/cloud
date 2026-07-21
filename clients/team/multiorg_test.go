package team

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/clients/team/token"
	model "github.com/hanzoai/iam/pkg/model"
)

// newTestApp registers a standalone api's account routes on a fresh app with no
// guard — the same wiring Mount does, for the invite/refresh tests that inject a
// mock IAM endpoint directly into the api's config.
func newTestApp(g *api) *zip.App {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	g.register(app.Group("/v1/team"), func(h zip.Handler) zip.Handler { return h })
	return app
}

// orgsExtra builds the session-token extra a real login mints: home org + the
// full membership set (as the decode side reads it back). One place so the tests
// mirror orgsClaim's shape exactly.
func orgsExtra(home string, refs ...model.OrgRef) map[string]any {
	return map[string]any{"org": home, "orgs": orgsClaim(refs, home)}
}

// seedMember inserts a workspace with an EXPLICIT slug plus an active member row
// for account — the lower-level seed the multi-org/ambiguity tests need (the
// public EnsureWorkspace auto-generates a random slug, so it cannot produce the
// same slug in two orgs). Same-package test reaches s.db directly.
func seedMember(t *testing.T, s *accountStore, org, account, slug, role string) workspace {
	t.Helper()
	ctx := context.Background()
	w := workspace{
		ID: uuid.NewString(), Slug: slug, Name: slug, UUID: uuid.NewString(),
		Owner: account, OwnerOrg: org,
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO workspaces (`+wsCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		w.ID, w.Slug, w.Name, w.UUID, w.Owner, w.OwnerOrg, w.DataID, w.Region, int64(1)); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if err := s.AddMember(ctx, w.ID, account, role, slug); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	return w
}

// TestOrgsClaimRoundTrip locks the session-token orgs contract: orgsClaim →
// (JSON via token) → orgsFromExtra reproduces the membership set, a legacy token
// with only extra.org falls back to that single home org as admin, and an empty
// claim still yields the home org (never an empty set).
func TestOrgsClaimRoundTrip(t *testing.T) {
	refs := []model.OrgRef{{Org: "maxpower", Role: "admin"}, {Org: "acme", Role: "member"}}
	tok, err := token.Generate("550e8400-e29b-41d4-a716-446655440000", "",
		orgsExtra("maxpower", refs...), 0, "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := token.Decode(tok, "s3cret", false)
	if err != nil {
		t.Fatal(err)
	}
	got := orgsFromExtra(dec.Extra)
	if len(got) != 2 || got[0] != (model.OrgRef{Org: "maxpower", Role: "admin"}) || got[1] != (model.OrgRef{Org: "acme", Role: "member"}) {
		t.Fatalf("round-trip orgs = %+v, want [maxpower/admin acme/member]", got)
	}

	// Legacy token: only extra.org → single home org, admin.
	legacy := orgsFromExtra(map[string]any{"org": "acme"})
	if len(legacy) != 1 || legacy[0] != (model.OrgRef{Org: "acme", Role: "admin"}) {
		t.Fatalf("legacy fallback = %+v, want [acme/admin]", legacy)
	}

	// Empty claim still yields the home org (never empty).
	if c := orgsClaim(nil, "acme"); len(c) != 1 || c[0]["org"] != "acme" {
		t.Fatalf("orgsClaim(nil, acme) = %+v, want [{org:acme}]", c)
	}
}

// TestGetUserWorkspacesUnionsOrgs is the Slack-model bar: a multi-org session
// token lists the caller's workspaces from EVERY org they belong to, each tagged
// with its owning org, in one union.
func TestGetUserWorkspacesUnionsOrgs(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	const acct = "550e8400-e29b-41d4-a716-446655440000"
	wsHome, err := mounted.State.accounts.EnsureWorkspace(ctx, "davelorenzini", acct, "Dave")
	if err != nil {
		t.Fatal(err)
	}
	wsTeam, err := mounted.State.accounts.EnsureWorkspace(ctx, "maxpower", acct, "Max Power")
	if err != nil {
		t.Fatal(err)
	}

	sess, err := token.Generate(acct, "", orgsExtra("davelorenzini",
		model.OrgRef{Org: "davelorenzini", Role: "admin"}, model.OrgRef{Org: "maxpower", Role: "member"}),
		expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	auth := map[string]string{"Authorization": "Bearer " + sess}

	code, body := call(t, app, http.MethodPost, "/v1/team/account", auth, rpcRequest{Method: "getUserWorkspaces"})
	if code != http.StatusOK {
		t.Fatalf("getUserWorkspaces status %d: %s", code, body)
	}
	var wl struct {
		Result []WorkspaceInfo `json:"result"`
	}
	if err := json.Unmarshal(body, &wl); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(wl.Result) != 2 {
		t.Fatalf("workspaces = %d, want 2 (home + team org): %s", len(wl.Result), body)
	}
	byOrg := map[string]WorkspaceInfo{}
	for _, w := range wl.Result {
		byOrg[w.Org] = w
	}
	if byOrg["davelorenzini"].UUID != wsHome.UUID {
		t.Fatalf("home workspace not tagged davelorenzini: %+v", wl.Result)
	}
	if byOrg["maxpower"].UUID != wsTeam.UUID {
		t.Fatalf("team workspace not tagged maxpower: %+v", wl.Result)
	}
}

// TestGetUserWorkspacesLegacyToken proves a pre-orgs-claim session token (only
// extra.org) still resolves the home org's workspaces — no regression for live
// sessions minted before this change.
func TestGetUserWorkspacesLegacyToken(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureWorkspace(ctx, org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	// Legacy shape: extra.org only, NO extra.orgs.
	sess, err := token.Generate(acct, "", map[string]any{"org": org}, expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	code, body := call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + sess}, rpcRequest{Method: "getUserWorkspaces"})
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	var wl struct {
		Result []WorkspaceInfo `json:"result"`
	}
	if err := json.Unmarshal(body, &wl); err != nil || len(wl.Result) != 1 || wl.Result[0].UUID != ws.UUID {
		t.Fatalf("legacy getUserWorkspaces = %s (err %v)", body, err)
	}
	if wl.Result[0].Org != org {
		t.Fatalf("legacy workspace org tag = %q, want %q", wl.Result[0].Org, org)
	}
}

// TestSelectWorkspaceCrossOrg proves a multi-org caller can select a workspace in
// a NON-home org they belong to — the resolver searches the whole membership set,
// and the minted workspace token carries THAT org (not the home org).
func TestSelectWorkspaceCrossOrg(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	const acct = "550e8400-e29b-41d4-a716-446655440000"
	if _, err := mounted.State.accounts.EnsureWorkspace(ctx, "davelorenzini", acct, "Dave"); err != nil {
		t.Fatal(err)
	}
	teamWS, err := mounted.State.accounts.EnsureWorkspace(ctx, "maxpower", acct, "Max Power")
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := token.Generate(acct, "", orgsExtra("davelorenzini",
		model.OrgRef{Org: "davelorenzini", Role: "admin"}, model.OrgRef{Org: "maxpower", Role: "member"}),
		expUnix(sessionTokenTTL), testSecret)

	code, body := call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + sess},
		map[string]any{"method": "selectWorkspace", "params": map[string]any{"workspaceUrl": teamWS.Slug}})
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	var sw struct {
		Result WorkspaceLoginInfo `json:"result"`
		Error  *Status            `json:"error"`
	}
	if err := json.Unmarshal(body, &sw); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if sw.Error != nil {
		t.Fatalf("cross-org select errored: %+v", sw.Error)
	}
	if sw.Result.Workspace != teamWS.UUID {
		t.Fatalf("selected workspace = %q, want %q", sw.Result.Workspace, teamWS.UUID)
	}
	// The workspace token carries the NON-home org (maxpower), so the transactor
	// routes to the right tenant.
	dec, err := token.Decode(sw.Result.Token, testSecret, true)
	if err != nil || dec.Extra["org"] != "maxpower" {
		t.Fatalf("workspace token org = %v (err %v), want maxpower", dec.Extra["org"], err)
	}
}

// TestSelectWorkspaceNoDefault proves the no-silent-default contract: an absent
// workspaceUrl is a clean BadRequest, never a first-workspace default.
func TestSelectWorkspaceNoDefault(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	if _, err := mounted.State.accounts.EnsureWorkspace(ctx, org, acct, "Ada"); err != nil {
		t.Fatal(err)
	}
	sess, _ := token.Generate(acct, "", orgsExtra(org, model.OrgRef{Org: org, Role: "admin"}),
		expUnix(sessionTokenTTL), testSecret)

	// No workspaceUrl → BadRequest, and NO workspace token leaked.
	code, body := call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + sess},
		map[string]any{"method": "selectWorkspace", "params": map[string]any{}})
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	var sw struct {
		Result *WorkspaceLoginInfo `json:"result"`
		Error  *Status             `json:"error"`
	}
	_ = json.Unmarshal(body, &sw)
	if sw.Error == nil || sw.Error.Code != "account:status:BadRequest" {
		t.Fatalf("no-workspaceUrl select = %s, want BadRequest", body)
	}
	if sw.Result != nil && sw.Result.Token != "" {
		t.Fatalf("no-default violated: a token was minted for an unspecified workspace: %+v", sw.Result)
	}
}

// TestSelectWorkspaceAmbiguous proves an explicit slug that resolves in TWO of the
// caller's orgs is refused (Ambiguous) — the server never silently picks one.
func TestSelectWorkspaceAmbiguous(t *testing.T) {
	app := mountTeam(t)
	const acct = "550e8400-e29b-41d4-a716-446655440000"
	s := mounted.State.accounts
	// SAME slug "shared" in two orgs the caller is a member of.
	seedMember(t, s, "org-a", acct, "shared", "admin")
	seedMember(t, s, "org-b", acct, "shared", "member")

	sess, _ := token.Generate(acct, "", orgsExtra("org-a",
		model.OrgRef{Org: "org-a", Role: "admin"}, model.OrgRef{Org: "org-b", Role: "member"}),
		expUnix(sessionTokenTTL), testSecret)

	code, body := call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + sess},
		map[string]any{"method": "selectWorkspace", "params": map[string]any{"workspaceUrl": "shared"}})
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	var sw struct {
		Result *WorkspaceLoginInfo `json:"result"`
		Error  *Status             `json:"error"`
	}
	_ = json.Unmarshal(body, &sw)
	if sw.Error == nil || sw.Error.Code != "account:status:WorkspaceAmbiguous" {
		t.Fatalf("ambiguous select = %s, want WorkspaceAmbiguous", body)
	}
	if sw.Result != nil && sw.Result.Token != "" {
		t.Fatalf("ambiguous select must not mint a token: %+v", sw.Result)
	}
}

// TestGuestSelectUnaffected proves the guest-cap path is untouched by the
// cross-org rewrite: a guest member resolves + selects their workspace, the role
// surfaces as GUEST, and the entitle gate still runs on the resolved (org, role)
// exactly as before (admitting here — no commerce wired).
func TestGuestSelectUnaffected(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	s := mounted.State.accounts
	ws := seedMember(t, s, org, acct, "guest-space", roleGuest)

	sess, _ := token.Generate(acct, "", orgsExtra(org, model.OrgRef{Org: org, Role: "member"}),
		expUnix(sessionTokenTTL), testSecret)
	code, body := call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + sess},
		map[string]any{"method": "selectWorkspace", "params": map[string]any{"workspaceUrl": ws.Slug}})
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	var sw struct {
		Result WorkspaceLoginInfo `json:"result"`
		Error  *Status            `json:"error"`
	}
	if err := json.Unmarshal(body, &sw); err != nil || sw.Error != nil {
		t.Fatalf("guest select = %s (err %v)", body, err)
	}
	if sw.Result.Role != "GUEST" || sw.Result.Workspace != ws.UUID {
		t.Fatalf("guest select role=%q ws=%q, want GUEST + %s", sw.Result.Role, sw.Result.Workspace, ws.UUID)
	}
}

// TestGetWorkspaceInfoNoDefault proves getWorkspaceInfo resolves the token's
// explicit workspace claim and NEVER falls back to the caller's first workspace: a
// token with no workspace claim is WorkspaceNotFound, and a workspace-scoped token
// resolves exactly that workspace.
func TestGetWorkspaceInfoNoDefault(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureWorkspace(ctx, org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}

	// Account-scoped token (NO workspace claim) → no default, WorkspaceNotFound.
	acctTok, _ := token.Generate(acct, "", orgsExtra(org, model.OrgRef{Org: org, Role: "admin"}),
		expUnix(sessionTokenTTL), testSecret)
	code, body := call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + acctTok}, rpcRequest{Method: "getWorkspaceInfo"})
	var r struct {
		Result *WorkspaceInfo `json:"result"`
		Error  *Status        `json:"error"`
	}
	_ = json.Unmarshal(body, &r)
	if code != http.StatusOK || r.Error == nil || r.Error.Code != "account:status:WorkspaceNotFound" {
		t.Fatalf("no-workspace-claim getWorkspaceInfo = %s, want WorkspaceNotFound", body)
	}
	if r.Result != nil && r.Result.UUID != "" {
		t.Fatalf("no-default violated: getWorkspaceInfo returned a workspace off an account token: %+v", r.Result)
	}

	// Workspace-scoped token → resolves exactly that workspace.
	wsTok, _ := token.Generate(acct, ws.UUID, map[string]any{"org": org}, expUnix(workspaceTokenTTL), testSecret)
	code, body = call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + wsTok}, rpcRequest{Method: "getWorkspaceInfo"})
	_ = json.Unmarshal(body, &r)
	if code != http.StatusOK || r.Result == nil || r.Result.UUID != ws.UUID {
		t.Fatalf("ws-scoped getWorkspaceInfo = %s, want the token's workspace", body)
	}
}

// TestSendInviteWritesMembershipAndRow is the invite bar: an owner invites an
// email; the RPC resolves the invitee in IAM (mock), POSTs add-membership as the
// hanzo-team app, and writes the local member row. Asserts BOTH the IAM call shape
// AND the resulting roster row.
func TestSendInviteWritesMembershipAndRow(t *testing.T) {
	const org = "maxpower"
	const inviterAcct = "550e8400-e29b-41d4-a716-446655440000"
	const inviteeSub = "113d4dd4-2486-40de-be2b-88d6e3e0b718"

	var gotAddBody map[string]string
	var gotUserOwner, gotUserEmail, gotAuth string
	iamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/iam/get-user":
			gotUserOwner = r.URL.Query().Get("owner")
			gotUserEmail = r.URL.Query().Get("email")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": map[string]string{
				"owner": org, "name": "eve", "id": inviteeSub, "email": gotUserEmail, "displayName": "Eve",
			}})
		case "/v1/iam/add-membership":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotAddBody = body
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer iamSrv.Close()

	store, err := openAccountStore(t.TempDir() + "/account.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ws, err := store.EnsureWorkspace(context.Background(), org, inviterAcct, "Max Power")
	if err != nil {
		t.Fatal(err)
	}

	g := &api{
		accounts: store,
		cfg:      config{serverSecret: testSecret, iamEndpoint: iamSrv.URL, iamClientID: "hanzo-team", iamClientSecret: "team-secret", provider: "openid"},
		log:      luxlog.New("test"),
	}
	app := newTestApp(g)

	sess, _ := token.Generate(inviterAcct, "", orgsExtra(org, model.OrgRef{Org: org, Role: "admin"}),
		expUnix(sessionTokenTTL), testSecret)
	code, body := call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + sess},
		map[string]any{"method": "sendInvite", "params": map[string]any{"email": "Eve@Example.com", "workspaceUrl": ws.Slug}})
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	var r struct {
		Result map[string]any `json:"result"`
		Error  *Status        `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if r.Error != nil {
		t.Fatalf("invite errored: %+v", r.Error)
	}

	// IAM get-user was org-scoped + lower-cased email.
	if gotUserOwner != org || gotUserEmail != "eve@example.com" {
		t.Fatalf("get-user query owner=%q email=%q, want %q / eve@example.com", gotUserOwner, gotUserEmail, org)
	}
	// add-membership carried {user:<owner>/<name>, org, role:member} as the app.
	if gotAddBody["user"] != org+"/eve" || gotAddBody["org"] != org || gotAddBody["role"] != "member" {
		t.Fatalf("add-membership body = %+v, want maxpower/eve, maxpower, member", gotAddBody)
	}
	if gotAuth != "Basic "+basicToken("hanzo-team", "team-secret") {
		t.Fatalf("IAM auth = %q, want the hanzo-team client_secret_basic", gotAuth)
	}
	// The local member row exists for the invitee's derived account uuid.
	inviteeAcct := accountID(inviteeSub)
	role, ok := store.Membership(context.Background(), ws.ID, inviteeAcct)
	if !ok || role != "member" {
		t.Fatalf("invitee member row = %q,%v, want member,true", role, ok)
	}
}

// TestSendInviteRequiresAdmin proves a plain member cannot invite — only an
// owner/admin of the target workspace.
func TestSendInviteRequiresAdmin(t *testing.T) {
	const org = "maxpower"
	const ownerAcct = "550e8400-e29b-41d4-a716-446655440000"
	const memberAcct = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	iamHit := false
	iamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		iamHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer iamSrv.Close()

	store, err := openAccountStore(t.TempDir() + "/account.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	ws, _ := store.EnsureWorkspace(ctx, org, ownerAcct, "Max Power")
	if err := store.AddMember(ctx, ws.ID, memberAcct, "member", "Mel"); err != nil {
		t.Fatal(err)
	}

	g := &api{
		accounts: store,
		cfg:      config{serverSecret: testSecret, iamEndpoint: iamSrv.URL, iamClientID: "hanzo-team", iamClientSecret: "team-secret"},
		log:      luxlog.New("test"),
	}
	app := newTestApp(g)

	// The plain member tries to invite → Unauthorized, and IAM is never touched.
	sess, _ := token.Generate(memberAcct, "", orgsExtra(org, model.OrgRef{Org: org, Role: "member"}),
		expUnix(sessionTokenTTL), testSecret)
	code, body := call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + sess},
		map[string]any{"method": "sendInvite", "params": map[string]any{"email": "eve@example.com", "workspaceUrl": ws.Slug}})
	var r struct {
		Error *Status `json:"error"`
	}
	_ = json.Unmarshal(body, &r)
	if code != http.StatusOK || r.Error == nil || r.Error.Code != "account:status:Unauthorized" {
		t.Fatalf("member invite = %s, want Unauthorized", body)
	}
	if iamHit {
		t.Fatal("a non-admin invite must never reach IAM")
	}
}

// TestGetMembershipsRefresh proves the mid-session refresh reads the LIVE set from
// IAM (via extra.user) and, on an IAM error, falls back to the session's own set.
func TestGetMembershipsRefresh(t *testing.T) {
	const org = "maxpower"
	const acct = "550e8400-e29b-41d4-a716-446655440000"

	fail := false
	iamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if r.URL.Path == "/v1/iam/get-memberships" && r.URL.Query().Get("user") == "maxpower/dave" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "data": []model.OrgRef{
				{Org: "maxpower", Role: "admin"}, {Org: "acme", Role: "member"},
			}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer iamSrv.Close()

	store, err := openAccountStore(t.TempDir() + "/account.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	g := &api{
		accounts: store,
		cfg:      config{serverSecret: testSecret, iamEndpoint: iamSrv.URL, iamClientID: "hanzo-team", iamClientSecret: "team-secret"},
		log:      luxlog.New("test"),
	}
	app := newTestApp(g)

	// Session token carries extra.user (the IAM id) and a stale one-org set.
	extra := orgsExtra(org, model.OrgRef{Org: org, Role: "admin"})
	extra["user"] = "maxpower/dave"
	sess, _ := token.Generate(acct, "", extra, expUnix(sessionTokenTTL), testSecret)
	auth := map[string]string{"Authorization": "Bearer " + sess}

	// Live refresh returns the fuller set from IAM.
	code, body := call(t, app, http.MethodPost, "/v1/team/account", auth, rpcRequest{Method: "getMemberships"})
	var r struct {
		Result []model.OrgRef `json:"result"`
	}
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	_ = json.Unmarshal(body, &r)
	if len(r.Result) != 2 {
		t.Fatalf("live refresh = %+v, want 2 orgs (maxpower, acme)", r.Result)
	}

	// IAM down → falls back to the session's signed set (never strands the user).
	fail = true
	code, body = call(t, app, http.MethodPost, "/v1/team/account", auth, rpcRequest{Method: "getMemberships"})
	r.Result = nil
	_ = json.Unmarshal(body, &r)
	if code != http.StatusOK || len(r.Result) != 1 || r.Result[0].Org != org {
		t.Fatalf("IAM-down refresh = %s, want the session's single-org fallback", body)
	}
}
