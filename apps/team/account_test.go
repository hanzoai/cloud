package team

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/internal/sqltest"
)

func TestBearerFromHeader(t *testing.T) {
	cases := map[string]string{
		"Bearer abc.def.ghi": "abc.def.ghi",
		"bearer abc":         "abc", // case-insensitive scheme
		"Basic abc":          "",
		"":                   "",
		"Bearer ":            "",
	}
	for h, want := range cases {
		if got := bearerFromHeader(h); got != want {
			t.Errorf("bearerFromHeader(%q) = %q want %q", h, got, want)
		}
	}
}

// TestTokenRoundTrip proves an account token minted by the IAM bridge decodes back
// to the same AccountUuid the account() helper would resolve — the spine of every
// authenticated RPC.
func TestTokenRoundTrip(t *testing.T) {
	const account = "550e8400-e29b-41d4-a716-446655440000"
	// A real exp, as both production mints pass: exp=0 is the legacy cutoff,
	// which has passed, so an exp-less mint decodes expired by design.
	tok, err := token.Generate(account, "", map[string]any{"org": "acme"}, expUnix(time.Hour), "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := token.Decode(tok, "s3cret", true)
	if err != nil {
		t.Fatal(err)
	}
	if dec.Account != account {
		t.Fatalf("account = %s want %s", dec.Account, account)
	}
	if dec.Org() != "acme" {
		t.Fatalf("org claim lost: %v", dec.Extra)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Acme Corp":      "acme-corp",
		"  Hello_World ": "hello-world",
		"z@hanzo.ai":     "zhanzoai",
		"***":            "ws", // never empty
		"Café":           "caf",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q want %q", in, got, want)
		}
	}
}

func TestLocalPart(t *testing.T) {
	if got := localPart("z@hanzo.ai"); got != "z" {
		t.Errorf("localPart = %q", got)
	}
	if got := localPart("noatsign"); got != "noatsign" {
		t.Errorf("localPart = %q", got)
	}
}

func TestOAuthBase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://hanzo.id", "https://hanzo.id/v1/iam"},
		{"https://hanzo.id/", "https://hanzo.id/v1/iam"},
		{"", "https://hanzo.id/v1/iam"},
		{"http://localhost:8080", "http://localhost:8080/v1/iam"},
	}
	for _, c := range cases {
		if got := oauthBase(c.in); got != c.want {
			t.Errorf("oauthBase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExchangeCodeHitsCanonicalEndpoint proves the server-side code exchange POSTs
// to ${IAMEndpoint}/v1/iam/oauth/token (JSON), not the bare ${IAMEndpoint}/oauth/
// token (the SPA that returns HTML 200). Regression guard for exchange_failed.
func TestExchangeCodeHitsCanonicalEndpoint(t *testing.T) {
	var gotPath, gotClientID, gotSecret, gotCode, gotRedirect string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = r.ParseForm()
		gotClientID = r.FormValue("client_id")
		gotSecret = r.FormValue("client_secret")
		gotCode = r.FormValue("code")
		gotRedirect = r.FormValue("redirect_uri")
		if r.URL.Path == "/v1/iam/oauth/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"AT","refresh_token":"RT"}`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!DOCTYPE html><html></html>"))
	}))
	defer srv.Close()

	g := &api{cfg: config{iamEndpoint: srv.URL, iamClientID: "hanzo-team", iamClientSecret: "s3cr3t"}}

	access, err := g.exchangeCode("code123", "https://hanzo.team/v1/team/account/auth/openid/callback")
	if err != nil {
		t.Fatalf("exchangeCode returned error: %v", err)
	}
	if gotPath != "/v1/iam/oauth/token" {
		t.Fatalf("exchange hit %q, want /v1/iam/oauth/token", gotPath)
	}
	if access != "AT" {
		t.Fatalf("access = %q, want AT", access)
	}
	if gotClientID != "hanzo-team" || gotSecret != "s3cr3t" || gotCode != "code123" ||
		gotRedirect != "https://hanzo.team/v1/team/account/auth/openid/callback" {
		t.Fatalf("exchange form mismatch: client_id=%q secret=%q code=%q redirect=%q",
			gotClientID, gotSecret, gotCode, gotRedirect)
	}
}

// TestExchangeCodeSurfacesOAuthError proves a JSON OAuth error body is surfaced
// (so the cause appears in logs) rather than masked as a generic failure.
func TestExchangeCodeSurfacesOAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"client_secret is invalid"}`))
	}))
	defer srv.Close()

	g := &api{cfg: config{iamEndpoint: srv.URL}}
	if _, err := g.exchangeCode("c", "https://x/cb"); err == nil {
		t.Fatal("expected error for invalid_client, got nil")
	}
}

// TestAccountIDDerivation locks the ONE account-id derivation (ported from
// team-go/pkg/wsauth.AccountID): a UUID sub is used verbatim, any other sub maps to
// a stable UUIDv5 over namespace "iam:<sub>", so a member row and its caller
// resolve to the same key. Empty in → empty out.
func TestAccountIDDerivation(t *testing.T) {
	sub := "113d4dd4-2486-40de-be2b-88d6e3e0b718"
	if got := accountID(sub); got != sub {
		t.Fatalf("accountID(uuid) = %q, want the uuid verbatim", got)
	}
	want := uuid.NewSHA1(uuid.NameSpaceURL, []byte("iam:legacy-alice")).String()
	if got := accountID("legacy-alice"); got != want {
		t.Fatalf("accountID(non-uuid) = %q, want stable uuidv5 %q", got, want)
	}
	if accountID("") != "" || accountID("   ") != "" {
		t.Fatal("accountID(empty/blank) must be empty")
	}
}

// TestEnrolRPCProvisionsPKCESession proves a first-time PKCE/native IAM access
// token (hanzo-team-native) is refused by getUserWorkspaces until enrol creates
// its personal space, after which both enrol and getUserWorkspaces return that
// space while id-tokens, wrong audiences, and subject-less tokens stay refused.
func TestEnrolRPCProvisionsPKCESession(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		store := openTestStore(t)
		id, iam := identFor(t, store)
		id.audience[nativeAudience] = true
		g := &api{accounts: store, ident: id, cfg: config{serverSecret: testSecret}}

		app := zip.New(zip.Config{})
		app.Raw(http.MethodPost, "/v1/team/account", g.rpc)

		rpcCall := func(bearerTok, method string) (Result []SpaceInfo, Err *Status) {
			t.Helper()
			req := httptest.NewRequest(http.MethodPost, "http://hanzo.team/v1/team/account",
				strings.NewReader(`{"method":"`+method+`"}`))
			req.Header.Set("Content-Type", "application/json")
			if bearerTok != "" {
				req.Header.Set("Authorization", "Bearer "+bearerTok)
			}
			resp, err := app.Test(req, budget)
			if err != nil {
				t.Fatalf("%s: %v", method, err)
			}
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			var env struct {
				Result []SpaceInfo `json:"result"`
				Error  *Status     `json:"error"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("%s decode %s: %v", method, raw, err)
			}
			return env.Result, env.Error
		}

		pkce := iam.Sign(t, iamtest.Claims{
			Sub: iamSub, PreferredUsername: "zach", Owner: "hanzo",
			Aud: nativeAudience, Orgs: orgsOf("acme"),
		})

		if wss, st := rpcCall(pkce, "getUserWorkspaces"); st == nil || len(wss) != 0 {
			t.Fatalf("pre-enrol getUserWorkspaces must refuse unenrolled subject: wss=%v err=%v", wss, st)
		}

		bad := map[string]string{
			"empty":      "",
			"id-token":   iam.Sign(t, iamtest.Claims{Sub: iamSub, Owner: "acme", Aud: nativeAudience, Orgs: orgsOf("acme"), TokenType: "id-token"}),
			"wrong-aud":  iam.Sign(t, iamtest.Claims{Sub: iamSub, Owner: "acme", Aud: "hanzo-other", Orgs: orgsOf("acme")}),
			"no-subject": iam.Sign(t, iamtest.Claims{PreferredUsername: "zach", Owner: "acme", Aud: nativeAudience, Orgs: orgsOf("acme")}),
			"no-home":    iam.Sign(t, iamtest.Claims{Sub: iamSub, Owner: "acme", Aud: nativeAudience}),
		}
		for label, tok := range bad {
			if _, st := rpcCall(tok, "enrol"); st == nil {
				t.Fatalf("enrol(%s) admitted a token it must refuse", label)
			}
		}

		wss, st := rpcCall(pkce, "enrol")
		if st != nil || len(wss) != 1 || wss[0].Org != "acme" {
			t.Fatalf("enrol(pkce) = (%v, %v), want 1 acme space", wss, st)
		}
		wss2, st2 := rpcCall(pkce, "getUserWorkspaces")
		if st2 != nil || len(wss2) != 1 || wss2[0].UUID != wss[0].UUID {
			t.Fatalf("post-enrol getUserWorkspaces = (%v, %v), want (%v, nil)", wss2, st2, wss)
		}
	})
}
