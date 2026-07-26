// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// iamKeys.lookup maps an IAM get-user?accessKey row to the SAME idClaims a JWT
// yields, so the one minting path serves a key and a session identically.
func TestIAMKeysLookup(t *testing.T) {
	var gotKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("accessKey")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"hanzo","name":"z","email":"z@hanzo.ai","isAdmin":true}}`))
	}))
	defer srv.Close()

	k := &iamKeys{base: srv.URL, auth: "Basic test", http: srv.Client(), cache: newCache[string, *idClaims](time.Minute)}
	c := k.resolve(context.Background(), "hk-abc123")
	if c == nil {
		t.Fatal("resolve returned nil for a valid key")
	}
	if c.Owner != "hanzo" || c.Name != "z" || c.Email != "z@hanzo.ai" || !c.IsAdmin {
		t.Fatalf("claims = %+v, want owner=hanzo name=z email=z@hanzo.ai isAdmin=true", c)
	}
	// The key is passed as accessKey and the confidential credential is sent.
	if gotKey != "hk-abc123" {
		t.Errorf("IAM got accessKey=%q, want hk-abc123", gotKey)
	}
	if gotAuth != "Basic test" {
		t.Errorf("IAM got auth=%q, want the confidential Basic credential", gotAuth)
	}
	// userID falls through to name (a key has no UUID subject) — the owner/name
	// path IAM's privileged lookups expect.
	if c.userID() != "z" || c.username() != "z" {
		t.Errorf("userID=%q username=%q, want both z", c.userID(), c.username())
	}
}

// An unconfigured resolver (no confidential credential) resolves nothing, so an
// API key stays anonymous rather than mis-resolved.
func TestIAMKeysUnconfigured(t *testing.T) {
	k := &iamKeys{base: "http://iam", auth: "", cache: newCache[string, *idClaims](time.Minute)}
	if c := k.resolve(context.Background(), "hk-abc"); c != nil {
		t.Fatalf("unconfigured resolver returned %+v, want nil", c)
	}
}

// An unknown key (IAM status != ok) resolves to nil — a bad key never grants trust.
func TestIAMKeysUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","msg":"Unauthorized operation"}`))
	}))
	defer srv.Close()
	k := &iamKeys{base: srv.URL, auth: "Basic test", http: srv.Client(), cache: newCache[string, *idClaims](time.Minute)}
	if c := k.resolve(context.Background(), "hk-bad"); c != nil {
		t.Fatalf("unknown key resolved to %+v, want nil", c)
	}
}

// The cache serves a resolved key without a second IAM call (and caches a miss too,
// so a bad key cannot hammer IAM).
func TestIAMKeysCache(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"hanzo","name":"z"}}`))
	}))
	defer srv.Close()
	k := &iamKeys{base: srv.URL, auth: "Basic test", http: srv.Client(), cache: newCache[string, *idClaims](time.Minute)}
	for i := 0; i < 3; i++ {
		if k.resolve(context.Background(), "hk-x") == nil {
			t.Fatal("resolve nil")
		}
	}
	if calls != 1 {
		t.Fatalf("IAM called %d times, want 1 (cached)", calls)
	}
}

func newPubKeys(base string, c *http.Client) *iamKeys {
	return &iamKeys{
		base: base, auth: "Basic test", http: c,
		cache:    newCache[string, *idClaims](time.Minute),
		pubCache: newCache[string, pubOrg](time.Minute),
	}
}

// orgForPublishable resolves a write-only pk- via IAM's resolve-key endpoint,
// returning ONLY the org — and ONLY when IAM vouches scope=="publish". A non-publish
// scope, empty org, or unknown key fails closed. The resolve-key response carries no
// principal fields, so nothing readable can leak through this write-only door.
func TestOrgForPublishable(t *testing.T) {
	cases := []struct {
		name, body string
		wantOrg    string
		wantOK     bool
	}{
		{"publish scope resolves to org", `{"status":"ok","data":{"org":"acme","scope":"publish"}}`, "acme", true},
		{"non-publish scope fails closed", `{"status":"ok","data":{"org":"acme","scope":"read"}}`, "", false},
		{"missing scope fails closed", `{"status":"ok","data":{"org":"acme"}}`, "", false},
		{"empty org fails closed", `{"status":"ok","data":{"org":"","scope":"publish"}}`, "", false},
		{"unknown key fails closed", `{"status":"error","msg":"not found"}`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotKey, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotKey, gotAuth = r.URL.Path, r.URL.Query().Get("accessKey"), r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			k := newPubKeys(srv.URL, srv.Client())
			org, ok := k.orgForPublishable(context.Background(), "pk-live-xyz")
			if ok != tc.wantOK || org != tc.wantOrg {
				t.Fatalf("orgForPublishable = (%q,%v), want (%q,%v)", org, ok, tc.wantOrg, tc.wantOK)
			}
			// The write-only door is resolve-key (NOT get-user), the key is passed as
			// accessKey, and the confidential credential is sent.
			if gotPath != "/v1/iam/resolve-key" {
				t.Errorf("path = %q, want /v1/iam/resolve-key (write-only door, not get-user)", gotPath)
			}
			if gotKey != "pk-live-xyz" {
				t.Errorf("accessKey = %q, want pk-live-xyz", gotKey)
			}
			if gotAuth != "Basic test" {
				t.Errorf("auth = %q, want the confidential Basic credential", gotAuth)
			}
		})
	}
}

// An unconfigured resolver (no confidential credential) resolves no publishable key —
// a deployment lacking the credential never fabricates a write-only org.
func TestOrgForPublishableUnconfigured(t *testing.T) {
	k := &iamKeys{base: "http://iam", auth: "", pubCache: newCache[string, pubOrg](time.Minute)}
	if org, ok := k.orgForPublishable(context.Background(), "pk-live-x"); ok || org != "" {
		t.Fatalf("unconfigured resolver returned (%q,%v), want (\"\",false)", org, ok)
	}
}

// The publishable cache serves a resolved pk- without a second IAM call, and caches a
// miss too, so a bad pk- (a public value anyone can spam) cannot hammer IAM.
func TestOrgForPublishableCachesHitAndMiss(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"hit", `{"status":"ok","data":{"org":"acme","scope":"publish"}}`},
		{"miss", `{"status":"ok","data":{"org":"acme","scope":"read"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			k := newPubKeys(srv.URL, srv.Client())
			for i := 0; i < 3; i++ {
				k.orgForPublishable(context.Background(), "pk-live-cache")
			}
			if calls != 1 {
				t.Fatalf("IAM called %d times, want 1 (cached)", calls)
			}
		})
	}
}
