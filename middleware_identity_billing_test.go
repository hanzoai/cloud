package cloud

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// WHO PAYS IS NOT A CLIENT'S TO NAME.
//
// X-Billing-Account-Id used to be forwarded from whatever the client sent: it was
// only an attribution hint, and nothing debited on it. ai/object.Payer now resolves
// the PAYING account from it, so a restored client copy would be a caller naming
// its own payer. These tests pin the boundary: the header is minted from the
// validated `billing_account` claim, and a forged copy never survives.

// billingClaims builds a validated claim set carrying a signed billing_account.
func billingClaims(owner, name, billingAccount string) idClaims {
	c := tokenClaims("hanzo-console", owner, name+"@"+owner+".io", false, time.Now().Add(time.Hour))
	c.Name = name
	c.BillingAccount = billingAccount
	return c
}

// identityProbe drives one request through SanitizeIdentity and reports the
// X-Billing-Account-Id a downstream handler observes.
func identityProbe(t *testing.T, key *rsa.PrivateKey, jwksURL string, tok string, mutate func(*http.Request)) string {
	t.Helper()
	v := newIdentityValidator(testIssuer, jwksURL, []string{"hanzo-console"}, 0)
	var got string
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v, "admin"))
	app.Get("/probe", func(cx *zip.Ctx) error {
		got = cx.Header("X-Billing-Account-Id")
		return cx.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	probe(t, app, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
		if mutate != nil {
			mutate(r)
		}
	})
	return got
}

// TestSanitizeIdentity_MintsBillingAccountFromTheClaim: the signed claim reaches
// the header the payer is read from — end to end, through a real validated token.
func TestSanitizeIdentity_MintsBillingAccountFromTheClaim(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)

	for _, tc := range []struct {
		name           string
		owner          string
		billingAccount string
		want           account.Account
	}{
		{"person", "hanzo", "person:hanzo/alice", account.Person("hanzo", "alice")},
		{"org", "acme", "org:acme", account.Org("acme")},
		{"project", "acme", "project:acme/website", account.Project("acme", "website")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tok := signWith(t, key, billingClaims(tc.owner, "alice", tc.billingAccount))
			got := identityProbe(t, key, jwks.URL, tok, nil)
			if got != tc.billingAccount {
				t.Fatalf("X-Billing-Account-Id = %q, want the minted claim %q", got, tc.billingAccount)
			}
			// The header is not decoration: it must resolve to the Account that pays.
			if acct := account.Payer(account.Credential{Owner: tc.owner, Name: "alice", Account: got}); acct != tc.want {
				t.Fatalf("Payer(minted header) = %+v, want %+v", acct, tc.want)
			}
		})
	}
}

// TestSanitizeIdentity_ForgedBillingAccountIsStripped is the money test: a client
// that names its own payer must not be believed. The forged value is deleted on
// ingress and the validated claim is minted over it.
func TestSanitizeIdentity_ForgedBillingAccountIsStripped(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)

	// A member of the shared signup org, whose token says they pay for themselves,
	// forging the header to name the org's POOLED account.
	tok := signWith(t, key, billingClaims("hanzo", "mallory", "person:hanzo/mallory"))
	got := identityProbe(t, key, jwks.URL, tok, func(r *http.Request) {
		r.Header.Set("X-Billing-Account-Id", "org:hanzo") // forged: the shared pool
	})
	if got == "org:hanzo" {
		t.Fatal("a client-forged X-Billing-Account-Id survived — the caller named the shared pool as its payer")
	}
	if got != "person:hanzo/mallory" {
		t.Fatalf("X-Billing-Account-Id = %q, want the validated claim %q", got, "person:hanzo/mallory")
	}
	if acct := account.Payer(account.Credential{Owner: "hanzo", Name: "mallory", Account: got}); acct != account.Person("hanzo", "mallory") {
		t.Fatalf("Payer = %+v, want the person account — the forgery must not move the payer", acct)
	}

	// Forging another tenant's account is likewise not believed.
	got = identityProbe(t, key, jwks.URL, tok, func(r *http.Request) {
		r.Header.Set("X-Billing-Account-Id", "org:victim")
	})
	if got != "person:hanzo/mallory" {
		t.Fatalf("X-Billing-Account-Id = %q, want the validated claim (a cross-tenant forgery must not survive)", got)
	}
}

// TestSanitizeIdentity_NoClaimMintsNoBillingAccount: a token minted before the
// claim shipped carries no account, so the header is OMITTED rather than invented
// — and a forged copy on such a token is still stripped, never restored. Payer's
// legacy rule answers for these, so they bill the account they always did.
func TestSanitizeIdentity_NoClaimMintsNoBillingAccount(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)

	tok := signWith(t, key, billingClaims("hanzo", "alice", "")) // pre-claim token

	if got := identityProbe(t, key, jwks.URL, tok, nil); got != "" {
		t.Fatalf("X-Billing-Account-Id = %q, want omitted for a token that names no payer", got)
	}
	// The forgery is the point: with no claim to mint over, a restored client copy
	// would be the ONLY value present — a caller naming its own payer outright.
	got := identityProbe(t, key, jwks.URL, tok, func(r *http.Request) {
		r.Header.Set("X-Billing-Account-Id", "org:hanzo")
	})
	if got != "" {
		t.Fatalf("X-Billing-Account-Id = %q on a pre-claim token — the client's own value was restored", got)
	}
}

// TestSanitizeIdentity_AnonymousCarriesNoBillingAccount: no validated principal ⟹
// no payer. An unauthenticated caller cannot name one.
func TestSanitizeIdentity_AnonymousCarriesNoBillingAccount(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, []string{"hanzo-console"}, 0)

	var got string
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v, "admin"))
	app.Get("/probe", func(cx *zip.Ctx) error {
		got = cx.Header("X-Billing-Account-Id")
		return cx.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	probe(t, app, func(r *http.Request) {
		r.Header.Set("X-Billing-Account-Id", "org:hanzo") // forged, no credential
	})
	if got != "" {
		t.Fatalf("X-Billing-Account-Id = %q for an anonymous caller — want none", got)
	}
}
