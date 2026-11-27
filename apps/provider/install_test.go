package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// install_test.go walks self-service installation as a customer meets it: an org
// admin installs Slack or the GitHub App, the account binds to that org and to no
// other, and removing the app on the provider's side unbinds it.

// TestAnAccountAnotherOrgHoldsLeavesTheCallerUntouched is the custody half of
// "one account, one org". globex holds its own workspace and then completes an
// install on acme's. The row write refuses it, and that refusal must come before
// anything is sealed: the bot token lives at one path per (org, provider), so a
// token sealed first overwrote globex's own, and every read through globex's row
// then spoke to acme's workspace with acme's bot.
func TestAnAccountAnotherOrgHoldsLeavesTheCallerUntouched(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackBridge(t, make(chan string, 4))
	kc := newKMS(t)
	app := newApp(t, kc)
	ctx := context.Background()

	if cb := connectSlack(t, app, "acme", "acmecode"); !strings.Contains(cb.Location, "connected=slack") {
		t.Fatalf("acme connect: %d %q", cb.Code, cb.Location)
	}
	if cb := connectSlack(t, app, "globex", "globexcode"); !strings.Contains(cb.Location, "connected=slack") {
		t.Fatalf("globex connect: %d %q", cb.Code, cb.Location)
	}

	cb := connectSlack(t, app, "globex", "acmecode")
	if !strings.Contains(cb.Location, "error=slack") {
		t.Fatalf("binding a workspace acme holds must fail, got %d %q", cb.Code, cb.Location)
	}
	if tok, err := TokenFor(ctx, "globex", "slack", slackBotTokenSecret); err != nil || string(tok) != "xoxb-globex" {
		t.Fatalf("globex's token = %q (%v), want its own xoxb-globex", tok, err)
	}
	if !strings.Contains(cb.Location, "another+organization") {
		t.Fatalf("the refusal must say why, got %q", cb.Location)
	}
	if conn, ok := ConnectionFor("globex", "slack", ""); !ok || conn.ExternalID != "TGLOBEX" {
		t.Fatalf("globex's row must be unchanged, got %+v ok=%v", conn, ok)
	}
	if org, ok := OrgForExternalID("slack", "TACME"); !ok || org != "acme" {
		t.Fatalf("TACME must still resolve to acme, got %q ok=%v", org, ok)
	}

	// An org with no workspace of its own gets nothing sealed either.
	if cb := connectSlack(t, app, "initech", "acmecode"); !strings.Contains(cb.Location, "error=slack") {
		t.Fatalf("initech binding acme's workspace must fail, got %q", cb.Location)
	}
	if _, err := kc.Get(kmsPath("initech", "slack"), slackBotTokenSecret, kmsEnv); err == nil {
		t.Fatal("a refused install must seal nothing into the refused org")
	}
}

// TestTwoOrgsRacingForOneAccountBindOne holds the store's refusal under load: the
// check and the write are one step, so orgs racing for the same account cannot
// both pass the check before either has written.
func TestTwoOrgsRacingForOneAccountBindOne(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		const rounds, n = 20, 16
		for r := range rounds {
			team := fmt.Sprintf("TRACE%d", r)
			var wg sync.WaitGroup
			errs := make([]error, n)
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs[i] = s.Upsert(ctx, Connection{Org: fmt.Sprintf("org%d", i), Provider: "slack", ExternalID: team})
				}()
			}
			wg.Wait()
			won := 0
			for _, err := range errs {
				if err == nil {
					won++
				}
			}
			if won != 1 {
				t.Fatalf("round %d: %d orgs bound one workspace, want exactly 1", r, won)
			}
		}
		if got, err := s.Collisions(ctx); err != nil || len(got) != 0 {
			t.Fatalf("a race must leave no shared account, got %+v (%v)", got, err)
		}
	})
}

// TestOnlyAnOrgAdminInstalls: an install binds a workspace or a GitHub account to
// the org and bills the org for everything said there, so it is the org admin's
// act. A plain member can neither install nor remove one.
func TestOnlyAnOrgAdminInstalls(t *testing.T) {
	slackConfiguredEnv(t)
	withGithubApp(t, mockInstallations(t, twoAccounts()))
	app := newApp(t, newKMS(t))

	for _, p := range []string{"slack", "github"} {
		if r := cfReq(t, app, http.MethodPost, "/v1/provider/"+p+"/connect", "acme", false, nil); r.Code != http.StatusForbidden {
			t.Errorf("a member connecting %s: %d, want 403", p, r.Code)
		}
		if r := cfReq(t, app, http.MethodPost, "/v1/provider/"+p+"/disconnect", "acme", false, nil); r.Code != http.StatusForbidden {
			t.Errorf("a member disconnecting %s: %d, want 403", p, r.Code)
		}
		if r := cfReq(t, app, http.MethodPost, "/v1/provider/"+p+"/connect", "acme", true, nil); r.Code != http.StatusOK {
			t.Errorf("an admin connecting %s: %d (%s), want 200", p, r.Code, r.Body)
		}
	}
}

// slackGone posts a signed Events API envelope carrying ev for team.
func slackGone(t *testing.T, app *zip.App, team, ev string) httpResult {
	t.Helper()
	body := `{"type":"event_callback","team_id":"` + team + `","event_id":"Ev` + team + `","event":` + ev + `}`
	return slackPost(t, app, "/v1/provider/slack/events", "gone-secret", "application/json", body)
}

// TestUninstallingFromSlackUnbindsTheWorkspace: removing the app in Slack is the
// customer's uninstall, and Slack tells us with app_uninstalled (and
// tokens_revoked, in either order). The workspace stops resolving to the org, its
// dead token leaves custody, and the workspace is free to install into another org.
func TestUninstallingFromSlackUnbindsTheWorkspace(t *testing.T) {
	for _, ev := range []string{
		`{"type":"app_uninstalled"}`,
		`{"type":"tokens_revoked","tokens":{"bot":["BACME"]}}`,
	} {
		t.Run(ev, func(t *testing.T) {
			slackConfiguredEnv(t)
			stubSlackBridge(t, make(chan string, 4))
			kc := newSigningKMS(t, "gone-secret")
			app := newApp(t, kc)

			if cb := connectSlack(t, app, "acme", "acmecode"); !strings.Contains(cb.Location, "connected=slack") {
				t.Fatalf("acme connect: %q", cb.Location)
			}
			if r := slackGone(t, app, "TACME", ev); r.Code != http.StatusOK {
				t.Fatalf("uninstall event: %d (%s)", r.Code, r.Body)
			}
			if org, ok := OrgForExternalID("slack", "TACME"); ok {
				t.Fatalf("an uninstalled workspace must resolve to no org, got %q", org)
			}
			if _, ok := ConnectionFor("acme", "slack", ""); ok {
				t.Fatal("an uninstalled workspace must leave no connection row")
			}
			if _, err := kc.Get(kmsPath("acme", "slack"), slackBotTokenSecret, kmsEnv); err == nil {
				t.Fatal("an uninstalled workspace's token must leave custody")
			}
			// Delivered twice (Slack sends both events, in either order): still fine.
			if r := slackGone(t, app, "TACME", ev); r.Code != http.StatusOK {
				t.Fatalf("a repeated uninstall must ack, got %d", r.Code)
			}
			if cb := connectSlack(t, app, "globex", "acmecode"); !strings.Contains(cb.Location, "connected=slack") {
				t.Fatalf("a released workspace must install into another org, got %q", cb.Location)
			}
		})
	}
}

// TestRevokingOnlyAPersonsTokenKeepsTheWorkspace: tokens_revoked for user tokens
// alone leaves the bot — and so the org's connection — standing.
func TestRevokingOnlyAPersonsTokenKeepsTheWorkspace(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackBridge(t, make(chan string, 4))
	app := newApp(t, newSigningKMS(t, "gone-secret"))
	if cb := connectSlack(t, app, "acme", "acmecode"); !strings.Contains(cb.Location, "connected=slack") {
		t.Fatalf("acme connect: %q", cb.Location)
	}
	if r := slackGone(t, app, "TACME", `{"type":"tokens_revoked","tokens":{"oauth":["Uacme"]}}`); r.Code != http.StatusOK {
		t.Fatalf("revoke event: %d", r.Code)
	}
	if org, ok := OrgForExternalID("slack", "TACME"); !ok || org != "acme" {
		t.Fatalf("a user-token revoke must keep the workspace bound, got %q ok=%v", org, ok)
	}
}

// ── GitHub App: the installer's authorization is the proof ───────────────────

// mockGitHubInstaller serves the three GitHub calls an install makes: the OAuth
// code exchange for the installing person's token, the installations that person
// can reach, and the App's own endpoints. codes maps an OAuth code to the
// installations its person can reach.
func mockGitHubInstaller(t *testing.T, codes map[string][]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_ = r.ParseForm()
			if r.FormValue("client_id") != "Iv1.test" || r.FormValue("client_secret") != "gh-client-secret" {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "incorrect_client_credentials"})
				return
			}
			if _, ok := codes[r.FormValue("code")]; !ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "bad_verification_code"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "ghu_" + r.FormValue("code"), "token_type": "bearer"})
		case "/user/installations":
			ins, ok := codes[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ghu_")]
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("page") != "1" {
				ins = nil
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(ins), "installations": ins})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withGithubInstaller is withGithubApp plus the App's OAuth client, which is what
// lets a person's install bind itself.
func withGithubInstaller(t *testing.T, srv *httptest.Server) {
	t.Helper()
	withGithubApp(t, srv)
	old := githubWebBase
	githubWebBase = srv.URL
	t.Cleanup(func() { githubWebBase = old })
	t.Setenv(githubClientIDEnv, "Iv1.test")
	t.Setenv(githubClientSecretEnv, "gh-client-secret")
}

func githubCallback(t *testing.T, app *zip.App, org, code, installation string) httpResult {
	t.Helper()
	q := url.Values{"state": {githubState(t, org)}, "setup_action": {"install"}}
	if code != "" {
		q.Set("code", code)
	}
	if installation != "" {
		q.Set("installation_id", installation)
	}
	return req(t, app, http.MethodGet, "/v1/provider/github/callback?"+q.Encode(), "", nil)
}

func acmeInstaller() map[string][]map[string]any {
	return map[string][]map[string]any{
		"acme-person": {{"id": 111, "account": map[string]any{"login": "hanzoai", "type": "Organization"}}},
	}
}

// TestAnInstallerBindsTheirOwnInstallation is the self-service connect: the admin
// installs the App on their GitHub org, GitHub hands back the installation beside
// the installer's authorization, and the installation binds to the org.
func TestAnInstallerBindsTheirOwnInstallation(t *testing.T) {
	withGithubInstaller(t, mockGitHubInstaller(t, acmeInstaller()))
	app := newApp(t, newKMS(t))

	r := githubCallback(t, app, "acme", "acme-person", "111")
	if !strings.Contains(r.Location, "connected=github") {
		t.Fatalf("an installer's own installation must bind, got %d %q", r.Code, r.Location)
	}
	conn, ok := ConnectionFor("acme", "github", "hanzoai")
	if !ok || conn.ExternalID != "111" {
		t.Fatalf("want acme bound to installation 111 as hanzoai, got %+v ok=%v", conn, ok)
	}
	if org, ok := OrgForExternalID("github", "111"); !ok || org != "acme" {
		t.Fatalf("installation 111 must resolve to acme, got %q ok=%v", org, ok)
	}
	// Idempotent: the same install completing again leaves one row.
	if r := githubCallback(t, app, "acme", "acme-person", "111"); !strings.Contains(r.Location, "connected=github") {
		t.Fatalf("a repeated install must succeed, got %q", r.Location)
	}
	if conns := Connections("acme", "github"); len(conns) != 1 {
		t.Fatalf("a repeated install must keep one row, got %+v", conns)
	}
}

// TestAnInstallerCannotNameSomeoneElsesInstallation: the installation id is a
// number in a URL. The installer's own token says which installations they can
// reach, and one outside that set binds nothing.
func TestAnInstallerCannotNameSomeoneElsesInstallation(t *testing.T) {
	withGithubInstaller(t, mockGitHubInstaller(t, acmeInstaller()))
	app := newApp(t, newKMS(t))

	for _, id := range []string{"222", "999999", "not-a-number", ""} {
		r := githubCallback(t, app, "acme", "acme-person", id)
		if !strings.Contains(r.Location, "error=github") {
			t.Fatalf("installation %q must be refused, got %q", id, r.Location)
		}
	}
	if r := githubCallback(t, app, "acme", "forged-code", "111"); !strings.Contains(r.Location, "error=github") {
		t.Fatalf("a code GitHub did not issue must be refused, got %q", r.Location)
	}
	if conns := Connections("acme", "github"); len(conns) != 0 {
		t.Fatalf("refused installs must bind nothing, got %+v", conns)
	}
}

// TestAnInstallationAnotherOrgHoldsStaysTheirs: a person who can reach an
// installation another org already holds still cannot move it.
func TestAnInstallationAnotherOrgHoldsStaysTheirs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		withGithubInstaller(t, mockGitHubInstaller(t, acmeInstaller()))
		app := newApp(t, newKMS(t))
		if err := mounted.State.store.Upsert(context.Background(), Connection{
			Org: "victim", Provider: "github", Label: "hanzoai", ExternalID: "111", AccountLabel: "hanzoai",
		}); err != nil {
			t.Fatalf("victim upsert: %v", err)
		}
		r := githubCallback(t, app, "acme", "acme-person", "111")
		if !strings.Contains(r.Location, "error=github") || !strings.Contains(r.Location, "another+organization") {
			t.Fatalf("a held installation must be refused with the reason, got %q", r.Location)
		}
		if org, _ := OrgForExternalID("github", "111"); org != "victim" {
			t.Fatalf("installation 111 must stay victim's, got %q", org)
		}
	})
}

// TestWithoutTheAppsClientAnOperatorBinds: until the App's OAuth client is
// configured, nothing can prove who installed, so the callback keeps refusing.
func TestWithoutTheAppsClientAnOperatorBinds(t *testing.T) {
	withGithubInstaller(t, mockGitHubInstaller(t, acmeInstaller()))
	t.Setenv(githubClientSecretEnv, "")
	app := newApp(t, newKMS(t))
	if r := githubCallback(t, app, "acme", "acme-person", "111"); !strings.Contains(r.Location, "error=github") {
		t.Fatalf("with no client secret the install must not bind, got %q", r.Location)
	}
	if conns := Connections("acme", "github"); len(conns) != 0 {
		t.Fatalf("got %+v", conns)
	}
}

// TestUninstallingTheAppUnbindsTheInstallation: GitHub reports an uninstall with a
// signed installation.deleted, and every org holding that installation loses it.
func TestUninstallingTheAppUnbindsTheInstallation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		withGithubApp(t, mockGitHub(t, nil))
		t.Setenv(githubWebhookSecretEnv, "hook-secret")
		app := newApp(t, newKMS(t))
		ctx := context.Background()
		for _, org := range []string{"hanzo", "lux"} {
			if err := mounted.State.store.Share(ctx, Connection{
				Org: org, Provider: "github", Label: "luxfi", ExternalID: "555", AccountLabel: "luxfi",
			}); err != nil {
				t.Fatalf("share %s: %v", org, err)
			}
		}
		if err := mounted.State.store.Upsert(ctx, Connection{
			Org: "acme", Provider: "github", Label: "acme-gh", ExternalID: "777", AccountLabel: "acme-gh",
		}); err != nil {
			t.Fatalf("acme upsert: %v", err)
		}

		suspend := []byte(`{"action":"suspend","installation":{"id":555,"account":{"login":"luxfi"}}}`)
		if r := webhookPost(t, app, "installation", ghSign("hook-secret", suspend), "", suspend); r.Code != http.StatusOK {
			t.Fatalf("suspend: %d (%s)", r.Code, r.Body)
		}
		if _, ok := OrgForExternalID("github", "555"); !ok {
			t.Fatal("a suspended installation comes back, so it stays bound")
		}

		deleted := []byte(`{"action":"deleted","installation":{"id":555,"account":{"login":"luxfi"}}}`)
		if r := webhookPost(t, app, "installation", "sha256=00", "", deleted); r.Code != http.StatusUnauthorized {
			t.Fatalf("an unsigned uninstall must be refused, got %d", r.Code)
		}
		if r := webhookPost(t, app, "installation", ghSign("hook-secret", deleted), "", deleted); r.Code != http.StatusOK {
			t.Fatalf("deleted: %d (%s)", r.Code, r.Body)
		}
		for _, org := range []string{"hanzo", "lux"} {
			if conns := Connections(org, "github"); len(conns) != 0 {
				t.Fatalf("%s must lose the uninstalled installation, got %+v", org, conns)
			}
		}
		if org, ok := OrgForExternalID("github", "777"); !ok || org != "acme" {
			t.Fatalf("another installation is untouched, got %q ok=%v", org, ok)
		}
	})
}
