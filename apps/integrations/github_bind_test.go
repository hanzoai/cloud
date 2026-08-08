package integrations

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// github_bind_test.go holds the one property the connect flow used to get wrong:
// an installation is bound to an org by GitHub's consent, never by an id a caller
// wrote in a URL.
//
// The callback is public and state-authed, and the state binds {org, provider,
// nonce, exp}. It cannot cover the installation id — the install does not exist
// when the state is minted — so the id arrived unsigned, and the App JWT that read
// it back can read EVERY tenant's installation. An id that resolved therefore
// proved only that the App was installed somewhere. Any org could mint a state for
// itself, name another tenant's install, and mint live installation tokens against
// it. These tests keep that door shut.

// githubState mints a genuine signed state and live nonce for org, exactly as the
// connect leg does. Nothing here is forged: an attacker is ENTITLED to a valid
// state for its own org, which is precisely why the state could never stop this.
func githubState(t *testing.T, org string) string {
	t.Helper()
	nonce, err := genToken()
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	if err := mounted.State.store.PutNonce(context.Background(), nonce, org, "github"); err != nil {
		t.Fatalf("put nonce: %v", err)
	}
	state, err := sign(mounted, org, "github", nonce)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return state
}

// TestCallbackRefusesNamedInstallation is the attack, run whole: a self-service
// principal in its own org names another tenant's installation on the callback and
// must come away with nothing — no row, no token, no repository.
func TestCallbackRefusesNamedInstallation(t *testing.T) {
	withGithubApp(t, mockGitHub(t, []map[string]any{{
		"name": "secrets", "full_name": "victim-gh/secrets", "private": true,
		"default_branch": "main", "clone_url": "https://github.com/victim-gh/secrets.git",
	}}))
	app := newApp(t, newKMS(t))
	ctx := context.Background()

	// The victim connected honestly: installation 4242 is its account.
	if err := mounted.State.store.Upsert(ctx, Connection{
		Org: "victim", Provider: "github", Label: "victim-gh",
		ExternalID: "4242", AccountLabel: "victim-gh",
	}); err != nil {
		t.Fatalf("victim upsert: %v", err)
	}

	r := req(t, app, http.MethodGet,
		"/v1/integrations/github/callback?state="+url.QueryEscape(githubState(t, "attacker"))+
			"&installation_id=4242&setup_action=install", "", nil)
	if r.Code != http.StatusFound || !strings.Contains(r.Location, "error=github") {
		t.Fatalf("a named installation must be refused, got %d %q", r.Code, r.Location)
	}
	// The id came from the caller, so it never travels back to the browser.
	if strings.Contains(r.Location, "4242") {
		t.Fatalf("the refusal must not echo the installation id: %q", r.Location)
	}
	if conns := Connections("attacker", "github"); len(conns) != 0 {
		t.Fatalf("a refused callback must write no row, got %+v", conns)
	}
	// Unconnected, so the repo surface never mints a token for it.
	if rr := req(t, app, http.MethodGet, "/v1/integrations/github/repos", "attacker", nil); rr.Code != http.StatusConflict {
		t.Fatalf("attacker must stay unconnected (409), got %d (%s)", rr.Code, rr.Body)
	}
	if rr := req(t, app, http.MethodPost, "/v1/integrations/github/repos/import", "attacker",
		map[string]any{"repos": []string{"victim-gh/secrets"}}); rr.Code != http.StatusConflict {
		t.Fatalf("attacker import must be refused (409), got %d (%s)", rr.Code, rr.Body)
	}
	// The victim's own binding is untouched by the attempt.
	if conns := Connections("victim", "github"); len(conns) != 1 || conns[0].ExternalID != "4242" {
		t.Fatalf("the victim's binding must be unchanged, got %+v", conns)
	}
}

// TestCallbackBindsNoInstallation proves the refusal is about the CHANNEL, not
// about which id was named. An install the App genuinely holds is refused here
// too, so there is no id that binds and none worth guessing.
func TestCallbackBindsNoInstallation(t *testing.T) {
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))

	for _, id := range []string{"111", "222", "999999", "not-a-number", ""} {
		q := "/v1/integrations/github/callback?state=" + url.QueryEscape(githubState(t, "acme"))
		if id != "" {
			q += "&installation_id=" + url.QueryEscape(id)
		}
		r := req(t, app, http.MethodGet, q, "", nil)
		if r.Code != http.StatusFound || !strings.Contains(r.Location, "error=github") {
			t.Fatalf("installation_id=%q must be refused, got %d %q", id, r.Code, r.Location)
		}
	}
	if conns := Connections("acme", "github"); len(conns) != 0 {
		t.Fatalf("the callback binds nothing, got %+v", conns)
	}
}

// TestConnectFlowUnchanged is the regression guard. Refusing GitHub's id must not
// disturb the OAuth providers, whose code IS a grant the provider issued to this
// flow: Slack still connects end to end and writes its row.
func TestConnectFlowUnchanged(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackAPI(t)
	app := newApp(t, newKMS(t))

	cb := connectSlack(t, app, "acme", "goodcode")
	if cb.Code != http.StatusFound || !strings.Contains(cb.Location, "connected=slack") {
		t.Fatalf("slack connect must still succeed, got %d %q", cb.Code, cb.Location)
	}
	if _, ok := ConnectionFor("acme", "slack", ""); !ok {
		t.Fatal("slack connect must write the connection row")
	}
}

// TestStoreRefusesAccountHeldByAnotherOrg is the layer under the handler: one
// provider account, one tenant, enforced where every write passes.
func TestStoreRefusesAccountHeldByAnotherOrg(t *testing.T) {
	newApp(t, newKMS(t))
	s, ctx := mounted.State.store, context.Background()

	if err := s.Upsert(ctx, Connection{
		Org: "victim", Provider: "github", Label: "victim-gh", ExternalID: "4242",
	}); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	err := s.Upsert(ctx, Connection{
		Org: "attacker", Provider: "github", Label: "victim-gh", ExternalID: "4242",
	})
	if !errors.Is(err, errBound) {
		t.Fatalf("a second org binding the same install must be refused, got %v", err)
	}
	if conns := Connections("attacker", "github"); len(conns) != 0 {
		t.Fatalf("the refused write must leave no row, got %+v", conns)
	}

	// The holding org keeps every legitimate shape: a re-connect refreshes in
	// place, and the same account may also be held by a person in that org.
	if err := s.Upsert(ctx, Connection{
		Org: "victim", Provider: "github", Label: "victim-gh",
		ExternalID: "4242", AccountLabel: "victim-gh",
	}); err != nil {
		t.Fatalf("re-connect must still work: %v", err)
	}
	if err := s.Upsert(ctx, Connection{
		Org: "victim", User: "z", Provider: "github", Label: "victim-gh", ExternalID: "4242",
	}); err != nil {
		t.Fatalf("the holding org may bind its own account for a person too: %v", err)
	}
	// An empty external id is not an account, so it never collides.
	if err := s.Upsert(ctx, Connection{Org: "a", Provider: "google"}); err != nil {
		t.Fatalf("empty external id: %v", err)
	}
	if err := s.Upsert(ctx, Connection{Org: "b", Provider: "google"}); err != nil {
		t.Fatalf("empty external id must never collide: %v", err)
	}
}

// TestClaimNeverTakesAHeldAccount proves the rule the sudo verb obeys: it may
// give this org an account another org holds, and never TAKES it from them.
// Sharing is how one installation serves two of our orgs; the holder's row
// standing afterwards is what keeps that from being a seizure.
func TestClaimNeverTakesAHeldAccount(t *testing.T) {
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))
	if err := mounted.State.store.Upsert(context.Background(), Connection{
		Org: "hanzo", Provider: "github", Label: "hanzoai",
		ExternalID: "111", AccountLabel: "hanzoai",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	r := postJSON(t, app, "/v1/integrations/github/claim", "lux", true,
		map[string]any{"accounts": []string{"hanzoai"}})
	if r.Code != http.StatusOK {
		t.Fatalf("claiming a held account want 200, got %d (%s)", r.Code, r.Body)
	}
	if got := claimOut(t, r.Body).Claimed; len(got) != 1 || got[0] != "hanzoai" {
		t.Errorf("claimed = %v, want [hanzoai] — the second org gains the account", got)
	}
	// The whole invariant: the org that was using it still is.
	if conns := Connections("hanzo", "github"); len(conns) != 1 {
		t.Fatalf("the holder must keep its binding, got %+v", conns)
	}
	if conns := Connections("lux", "github"); len(conns) != 1 {
		t.Fatalf("the claiming org must hold it too, got %+v", conns)
	}
}

// TestCollisionsReportsSharedAccounts covers the report that makes an existing
// cross-org binding visible. The row is written straight to the table, which is
// the only way to make one now — the shape a database carries from before the
// refusal existed.
func TestCollisionsReportsSharedAccounts(t *testing.T) {
	newApp(t, newKMS(t))
	s, ctx := mounted.State.store, context.Background()

	if err := s.Upsert(ctx, Connection{
		Org: "victim", Provider: "github", Label: "victim-gh", ExternalID: "4242",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Two orgs with no external id at all must never read as a collision.
	if err := s.Upsert(ctx, Connection{Org: "a", Provider: "google"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Upsert(ctx, Connection{Org: "b", Provider: "google"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, err := s.Collisions(ctx); err != nil || len(got) != 0 {
		t.Fatalf("a clean table reports nothing, got %+v (%v)", got, err)
	}

	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO connections (org,user,provider,label,external_id,account_label,bot_user_id,scopes_csv,expires_at,connected_at,updated_at)
		 VALUES ('attacker','','github','victim-gh','4242','victim-gh','','',0,1,1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := s.Collisions(ctx)
	if err != nil {
		t.Fatalf("collisions: %v", err)
	}
	if len(got) != 1 || got[0].Provider != "github" || got[0].ExternalID != "4242" {
		t.Fatalf("want the github 4242 collision, got %+v", got)
	}
	if len(got[0].Orgs) != 2 {
		t.Fatalf("want both orgs named, got %v", got[0].Orgs)
	}
}

// One installation may be held by SEVERAL of our orgs, and a tenant still cannot
// take one another org holds.
//
// The GitHub org `luxfi` is developed from the `hanzo` org and is also Lux's own,
// so a single binding leaves one of those two contexts unable to see its own
// repositories. Share permits that; Upsert — the write every self-service connect
// goes through — must keep refusing, because the App here is Hanzo's own and a
// customer installs it on THEIR GitHub org: if exclusivity were merely a
// parameter, one tenant could bind another tenant's installation and read their
// private repositories.
func TestOneInstallationCanBeHeldBySeveralOrgs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	row := func(org string) Connection {
		return Connection{Org: org, Provider: "github", Label: "luxfi",
			ExternalID: "55512345", AccountLabel: "luxfi"}
	}

	if err := s.Upsert(ctx, row("hanzo")); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	// The refusal that protects a customer's repositories is still there.
	if err := s.Upsert(ctx, row("lux")); !errors.Is(err, errBound) {
		t.Fatalf("Upsert into a second org = %v, want errBound — self-service must not take another org's account", err)
	}
	// Sudo sharing is what the second context needs.
	if err := s.Share(ctx, row("lux")); err != nil {
		t.Fatalf("Share into a second org: %v", err)
	}

	// BOTH orgs now see it, which is the whole point.
	for _, org := range []string{"hanzo", "lux"} {
		got, ok, err := s.Get(ctx, org, "", "github", "luxfi")
		if err != nil || !ok {
			t.Fatalf("%s cannot see the shared installation: ok=%v err=%v", org, ok, err)
		}
		if got.ExternalID != "55512345" {
			t.Errorf("%s got external id %q, want the installation's", org, got.ExternalID)
		}
	}
}
