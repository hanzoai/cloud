package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/kms"
)

// TikTok Login Kit names the app client_key on the consent URL. client_id is
// the ads connector, a different provider.
func TestTikTokAuthorizeUsesClientKey(t *testing.T) {
	t.Setenv("TIKTOK_CLIENT_ID", "tt-id")
	t.Setenv("TIKTOK_CLIENT_SECRET", "tt-sec")
	p := registry["tiktok"]
	if p == nil || p.RedirectPath != "/v1/provider/tiktok/callback" {
		t.Fatalf("tiktok callback = %v", p)
	}
	raw, err := p.Authorize(p.Creds(), "https://api.hanzo.ai"+p.RedirectPath, "st")
	if err != nil {
		t.Fatal(err)
	}
	q := mustQuery(t, raw)
	if q.Get("client_key") != "tt-id" || q.Get("client_id") != "" {
		t.Fatalf("authorize = %s", raw)
	}
	if q.Get("redirect_uri") != "https://api.hanzo.ai/v1/provider/tiktok/callback" {
		t.Fatalf("redirect = %s", raw)
	}
}

func TestXAuthorizeStaysOnTheXCallback(t *testing.T) {
	p := registry["x"]
	if p == nil || p.RedirectPath != "/v1/provider/x/callback" {
		t.Fatalf("x callback = %v", p)
	}
}

// A Facebook consent keeps each Page's own token. The user id is not a page,
// and a page listed without a token is not publishable.
func TestFacebookKeepsPageTokensNotTheUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "access_token"):
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "long-user"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{
				{"id": "user-1", "name": "Ada", "access_token": "user-as-page"},
				{"id": "pg-bare", "name": "Bare"},
				{"id": "pg-1", "name": "Acme", "access_token": "page-token"},
			}})
		}
	}))
	t.Cleanup(srv.Close)
	old := facebookGraph
	facebookGraph = srv.URL
	t.Cleanup(func() { facebookGraph = old })

	grants, err := expandFacebook(context.Background(), OAuthConfig{ClientID: "id", ClientSecret: "sec"}, "short", identity{ExternalID: "user-1", Label: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 || grants[0].ExternalID != "pg-1" || grants[0].Token != "page-token" || grants[0].Kind != "page" {
		t.Fatalf("grants = %+v", grants)
	}
	if grantKey(grants[0]) != "pg-1" {
		t.Fatalf("key = %q", grantKey(grants[0]))
	}
}

// A LinkedIn company is a second account. Its key cannot equal the member's,
// and the post is made with the member token.
func TestLinkedInCompanyDoesNotReplaceTheMember(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer member-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"elements": []map[string]any{{
			"organizationalTarget":  "urn:li:organization:99",
			"organizationalTarget~": map[string]string{"localizedName": "Acme"},
		}}})
	}))
	t.Cleanup(srv.Close)
	old := linkedinAPIOf
	linkedinAPIOf = srv.URL
	t.Cleanup(func() { linkedinAPIOf = old })

	grants, err := expandLinkedIn(context.Background(), OAuthConfig{}, "member-token", identity{ExternalID: "mem", Label: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 {
		t.Fatalf("grants = %+v", grants)
	}
	if grantKey(grants[0]) != "mem" || grants[0].Kind != "personal" || grants[0].Token != "member-token" {
		t.Fatalf("member = %+v", grants[0])
	}
	if grantKey(grants[1]) != "org-99" || grants[1].ExternalID != "99" || grants[1].Kind != "page" || grants[1].Token != "member-token" {
		t.Fatalf("company = %+v", grants[1])
	}
	if grantKey(grants[0]) == grantKey(grants[1]) {
		t.Fatal("the company replaced the member's key")
	}
}

// A company listing the app cannot make still leaves the personal account.
func TestLinkedInKeepsTheMemberWhenCompaniesRefuse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	old := linkedinAPIOf
	linkedinAPIOf = srv.URL
	t.Cleanup(func() { linkedinAPIOf = old })
	grants, err := expandLinkedIn(context.Background(), OAuthConfig{}, "member-token", identity{ExternalID: "mem"})
	if err != nil || len(grants) != 1 || grants[0].Kind != "personal" {
		t.Fatalf("grants=%+v err=%v", grants, err)
	}
}

// An org that sealed only its client id must not be paired with the deployment secret.
func TestAnOrgAppDoesNotBorrowTheDeploymentSecret(t *testing.T) {
	t.Setenv("X_CLIENT_ID", "env-id")
	t.Setenv("X_CLIENT_SECRET", "env-secret")
	newApp(t, newKMS(t))
	path := kms.OrgPath("acme", "social", "apps", "x")
	if err := kmsPut(mounted, path, "client_id", []byte("org-id")); err != nil {
		t.Fatal(err)
	}
	got := credsFor(mounted, "acme", registry["x"])
	if got.ClientID != "org-id" || got.ClientSecret != "" {
		t.Fatalf("org app = %+v", got)
	}
	env := credsFor(mounted, "other", registry["x"])
	if env.ClientID != "env-id" || env.ClientSecret != "env-secret" {
		t.Fatalf("deployment app = %+v", env)
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}
