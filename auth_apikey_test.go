// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
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
	c := k.resolve(context.Background(), "sk-abc123")
	if c == nil {
		t.Fatal("resolve returned nil for a valid key")
	}
	if c.Owner != "hanzo" || c.Name != "z" || c.Email != "z@hanzo.ai" || !c.IsAdmin {
		t.Fatalf("claims = %+v, want owner=hanzo name=z email=z@hanzo.ai isAdmin=true", c)
	}
	// The key is passed as accessKey and the confidential credential is sent.
	if gotKey != "sk-abc123" {
		t.Errorf("IAM got accessKey=%q, want sk-abc123", gotKey)
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
	if c := k.resolve(context.Background(), "sk-abc"); c != nil {
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
	if c := k.resolve(context.Background(), "sk-bad"); c != nil {
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
	for range 3 {
		if k.resolve(context.Background(), "sk-x") == nil {
			t.Fatal("resolve nil")
		}
	}
	if calls != 1 {
		t.Fatalf("IAM called %d times, want 1 (cached)", calls)
	}
}

// A PUBLISHABLE key must resolve to its org through resolve-key — IAM's org-only
// endpoint — and NOT through get-user?accessKey, which refuses a pk- by design.
//
// Cloud sent every key prefix down the get-user endpoint, so a publishable key
// resolved to nothing at all: the ingest path a pk- exists for could never attribute
// a beacon to its tenant. A publishable key that resolves to nobody is a publishable
// key that does not work, which is the other half of why no surface used one.
func TestOrgForKey_PublishableResolvesThroughTheOrgOnlyEndpoint(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/iam/keys/org":
			// The real envelope: an org and a scope, and NO principal.
			_, _ = w.Write([]byte(`{"status":"ok","data":{"org":"acme","scope":"publish"}}`))
		default:
			// get-user?accessKey refuses a pk- exactly as IAM does.
			_, _ = w.Write([]byte(`{"status":"error","msg":"the entity does not exist"}`))
		}
	}))
	defer srv.Close()

	k := &iamKeys{base: srv.URL, auth: "Basic test", http: srv.Client(),
		cache: newCache[string, *idClaims](time.Minute), orgs: newCache[string, string](time.Minute)}
	if org := k.resolveOrg(context.Background(), "pk-live-abc"); org != "acme" {
		t.Fatalf("resolveOrg = %q, want acme", org)
	}
	if len(paths) != 1 || paths[0] != "/v1/iam/keys/org" {
		t.Fatalf("a publishable key must be resolved at the org-only endpoint, got %v", paths)
	}
	// A publishable key yields an ORG and never a principal: there is no idClaims on
	// this path at all, which is what keeps a browser key from becoming a read grant.
	if c := k.resolve(context.Background(), "pk-live-abc"); c != nil {
		t.Fatalf("a publishable key resolved to a principal %+v — it must never authenticate", c)
	}
}

// The two endpoints answer different questions, so a secret key must NOT be sent to
// the org-only one (it would learn an org for a credential whose whole point is that
// it names a user), and a publishable key must not be sent to the principal one.
func TestOrgForKey_EachPrefixUsesItsOwnEndpoint(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/iam/keys/org":
			_, _ = w.Write([]byte(`{"status":"ok","data":{"org":"pub-org","scope":"publish"}}`))
		default:
			_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"secret-org","name":"z"}}`))
		}
	}))
	defer srv.Close()

	sharedKeysOnce = sync.Once{}
	t.Setenv("IAM_URL", srv.URL)
	t.Setenv("IAM_MINT_CLIENT_ID", "hanzo-console")
	t.Setenv("IAM_MINT_CLIENT_SECRET", "s3cr3t")
	sharedKeysInst = nil
	t.Cleanup(func() { sharedKeysOnce = sync.Once{}; sharedKeysInst = nil })

	if org, ok := OrgForKey(context.Background(), "sk-live-abc"); !ok || org != "secret-org" {
		t.Fatalf("secret key resolved to (%q,%v), want secret-org", org, ok)
	}
	if org, ok := OrgForKey(context.Background(), "pk-live-abc"); !ok || org != "pub-org" {
		t.Fatalf("publishable key resolved to (%q,%v), want pub-org — this is the pk- ingest path", org, ok)
	}
	// get-user?accessKey is the SECRET-key endpoint: it resolves sk- to the owning
	// user behind CapKeyResolve, and refuses a pk- by design. resolve-key is the
	// publishable endpoint, org-only. /v1/iam/users/get is neither — it is the typed
	// (owner, name) read, which carries no accessKey and cannot answer this
	// question at all.
	want := []string{"/v1/iam/keys/principal", "/v1/iam/keys/org"}
	if len(paths) != 2 || paths[0] != want[0] || paths[1] != want[1] {
		t.Fatalf("endpoints used = %v, want %v (one question each, never interchangeable)", paths, want)
	}
}

// resolveOrg fails CLOSED: an unconfigured resolver, an unknown/expired/
// non-publishable key, or a malformed envelope yields "" — never a default tenant,
// so a bad browser key can never write into someone else's partition.
func TestResolveOrg_FailsClosed(t *testing.T) {
	if k := (&iamKeys{base: "http://iam", auth: "", orgs: newCache[string, string](time.Minute)}); k.resolveOrg(context.Background(), "pk-x") != "" {
		t.Fatal("an unconfigured resolver must resolve nothing")
	}
	for _, body := range []string{
		`{"status":"error","msg":"the entity does not exist"}`, // unknown / not publishable / expired
		`{"status":"ok","data":{"org":""}}`,                    // present but empty
		`{"status":"ok"}`,                                      // no data
		`not json at all`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		k := &iamKeys{base: srv.URL, auth: "Basic test", http: srv.Client(), orgs: newCache[string, string](time.Minute)}
		if org := k.resolveOrg(context.Background(), "pk-live-abc"); org != "" {
			t.Fatalf("body %q resolved to org %q, want \"\"", body, org)
		}
		srv.Close()
	}
}

// A KEY AND A TOKEN FOR ONE IDENTITY NAME THE SAME PAYER.
//
// IAM answers `billing_account` when it validates a key exactly as it answers it
// in a token's claim — which ledger this credential spends from, decided where the
// grant shape is visible. Dropping it here did not leave the payer unknown, it
// left it WRONG: account.Payer falls out of its named-account arm to the shape
// rule, which answers Person(org, name) and never pools the signup org. A
// service account then names a wallet no funding path addresses — an admin grant
// credits the pool, a deposit names a member — so a first-party key reads $0
// beside the balance it is entitled to spend.
//
// It lands on the same authz.Claims field the token path fills, so
// renderBillingAccount mints X-Billing-Account-Id identically either way.
func TestIAMKeyCarriesThePayerItWasIssuedFor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"hanzo","name":"svc","billing_account":"org:hanzo"}}`))
	}))
	defer srv.Close()

	k := &iamKeys{base: srv.URL, auth: "Basic test", http: srv.Client(), cache: newCache[string, *idClaims](time.Minute)}
	c := k.resolve(context.Background(), "sk-payer")
	if c == nil {
		t.Fatal("resolve returned nil for a valid key")
	}
	if c.BillingAccount != "org:hanzo" {
		t.Errorf("BillingAccount = %q, want org:hanzo — a key that drops its payer is "+
			"billed as a person against a wallet nothing funds", c.BillingAccount)
	}
}

// An IAM that does not answer the field leaves it empty rather than inventing
// one, which is exactly the behaviour that preceded reading it.
func TestIAMKeyWithoutAPayerNamesNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":{"owner":"hanzo","name":"svc"}}`))
	}))
	defer srv.Close()

	k := &iamKeys{base: srv.URL, auth: "Basic test", http: srv.Client(), cache: newCache[string, *idClaims](time.Minute)}
	c := k.resolve(context.Background(), "sk-nopayer")
	if c == nil {
		t.Fatal("resolve returned nil for a valid key")
	}
	if c.BillingAccount != "" {
		t.Errorf("BillingAccount = %q, want empty", c.BillingAccount)
	}
}
