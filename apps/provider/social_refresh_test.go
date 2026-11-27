package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// A consent that yields several accounts keeps what each one needs to go on
// working: the member's accounts share the consent's refresh token and expiry, a
// Page-style account holds a token that never expires, and a long-lived token
// carries the refresh it renews with. And a token near its end is traded for a new
// one at the next post, through the one refresh engine.
func TestAConsentKeepsEachAccountsRefreshAndExpiryAndRenewsAStaleToken(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		srv := tokenEndpoint(t)
		t.Setenv("DEMO_ID", "id")
		t.Setenv("DEMO_SECRET", "sec")
		sp := refreshing(srv, false)
		sp.perTarget = true
		farOff := time.Now().Add(240 * time.Hour).Unix()
		sp.expand = func(context.Context, OAuthConfig, string, identity) ([]socialGrant, error) {
			return []socialGrant{
				{ExternalID: "mem", AccountLabel: "Ada", Kind: "personal"},
				{ExternalID: "pg", AccountLabel: "A Page", Token: "page-token", Kind: "page"},
				{ExternalID: "long", AccountLabel: "Long", Token: "long-token", Refresh: "long-token", ExpiresAt: farOff, Kind: "personal"},
			}, nil
		}
		registry["demo"] = oauthProvider(sp)
		t.Cleanup(func() { delete(registry, "demo") })
		app := newApp(t, newKMS(t))

		r := cfReq(t, app, http.MethodPost, "/v1/provider/demo/connect", "acme", true, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("connect = %d %s", r.Code, r.Body)
		}
		authorize := mustQuery(t, authorizeURLOf(t, r.Body))
		cb := req(t, app, http.MethodGet, "/v1/provider/demo/callback?code=c&state="+url.QueryEscape(authorize.Get("state")), "", nil)
		if cb.Code != http.StatusFound {
			t.Fatalf("callback = %d %s", cb.Code, cb.Body)
		}

		byID := map[string]Connection{}
		for _, c := range Connections("acme", "demo") {
			byID[c.ExternalID] = c
		}
		if len(byID) != 3 {
			t.Fatalf("connections = %+v", byID)
		}
		now := time.Now().Unix()
		if e := byID["mem"].ExpiresAt; e < now+3500 || e > now+3700 {
			t.Fatalf("the member's token expires at %d, want about an hour from %d", e, now)
		}
		if byID["pg"].ExpiresAt != 0 {
			t.Fatalf("a Page token was given an expiry: %d", byID["pg"].ExpiresAt)
		}
		if byID["long"].ExpiresAt != farOff {
			t.Fatalf("the long-lived token expires at %d, want %d", byID["long"].ExpiresAt, farOff)
		}
		sealed := func(id, name string) string {
			v, err := kmsGet(mounted, custodyPath(registry["demo"], "acme", byID[id].Label), name)
			if err != nil {
				return ""
			}
			return string(v)
		}
		if sealed("mem", refreshSecret) != "refresh-1" || sealed("long", refreshSecret) != "long-token" || sealed("pg", refreshSecret) != "" {
			t.Fatalf("refresh sealed: member %q, long %q, page %q", sealed("mem", refreshSecret), sealed("long", refreshSecret), sealed("pg", refreshSecret))
		}
		if sealed("pg", accessSecret) != "page-token" || sealed("long", accessSecret) != "long-token" {
			t.Fatal("an account's own token was not the one sealed for it")
		}

		// The member's token is near its end: the next read trades the refresh token.
		stale := byID["mem"]
		stale.ExpiresAt = now + 5
		if err := mounted.State.store.Upsert(context.Background(), stale); err != nil {
			t.Fatal(err)
		}
		tok, err := tokenForTarget(mounted, context.Background(), "acme", "demo", "mem", accessSecret)
		if err != nil || string(tok) != "access-2" {
			t.Fatalf("token = %q, %v", tok, err)
		}
		if now2 := time.Now().Unix(); mustGet(t, "mem").ExpiresAt < now2+3500 {
			t.Fatalf("the expiry did not move: %d", mustGet(t, "mem").ExpiresAt)
		}
		if sealed("mem", accessSecret) != "access-2" || sealed("mem", refreshSecret) != "refresh-1" {
			t.Fatalf("the renewed pair was not sealed: %q %q", sealed("mem", accessSecret), sealed("mem", refreshSecret))
		}
		// A Page token has no expiry, so it is never traded.
		if tok, err := tokenForTarget(mounted, context.Background(), "acme", "demo", "pg", accessSecret); err != nil || string(tok) != "page-token" {
			t.Fatalf("page token = %q, %v", tok, err)
		}
	})
}

func authorizeURLOf(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AuthorizeURL == "" {
		t.Fatalf("authorizeUrl in %s: %v", body, err)
	}
	return out.AuthorizeURL
}

func mustGet(t *testing.T, externalID string) Connection {
	t.Helper()
	for _, c := range Connections("acme", "demo") {
		if c.ExternalID == externalID {
			return c
		}
	}
	t.Fatalf("%s is not connected", externalID)
	return Connection{}
}
