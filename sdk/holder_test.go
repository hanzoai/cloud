package cloud

// The HOLDER is who holds an access token, and it is the one name KMS's grant
// rule keys a secret value on (apps/kms/policy.go). It is minted by the identity
// boundary from signed claims alone, so these pin both halves: which claims name
// a holder, and that the boundary is the only thing that ever writes one.

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

func TestHolderNamesWhoHoldsTheAccessToken(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	machine := func() idClaims {
		c := machineClaims("hanzo-operator", "hanzo", exp)
		c.TokenType = "access-token"
		return c
	}
	person := func() idClaims {
		c := tokenClaims("hanzo-console", "hanzo", "z@hanzo.ai", false, exp)
		c.TokenType, c.PreferredUsername = "access-token", "z"
		return c
	}
	for _, tc := range []struct {
		what string
		c    func() idClaims
		want string
	}{
		{"a machine is its clientId", machine, "hanzo-operator"},
		{"a person is home org and username", person, "hanzo/z"},
		{"a token that names an actor (as()) holds nothing", func() idClaims {
			c := person()
			c.acted = true
			return c
		}, ""},
		{"a machine token that names an actor holds nothing", func() idClaims {
			c := machine()
			c.acted = true
			return c
		}, ""},
		{"a person's home is the membership set, never the app's owner", func() idClaims {
			c := person()
			c.Owner = "lux" // the APPLICATION's org
			return c
		}, "hanzo/z"},
		{"a person acting through a token exchange is still the person", func() idClaims {
			c := person()
			c.Azp = "hanzo-bot"
			return c
		}, "hanzo/z"},
		{"an id_token holds nothing", func() idClaims {
			c := person()
			c.TokenType = "id-token"
			return c
		}, ""},
		{"a token that says neither holds nothing", func() idClaims {
			c := machine()
			c.TokenType = ""
			return c
		}, ""},
		{"a display name is never a holder", func() idClaims {
			c := person()
			c.PreferredUsername, c.Name = "", "Zach Kelling"
			return c
		}, ""},
		{"an sk- key resolves to a person's row and holds nothing", func() idClaims {
			c := person()
			c.subjectOrg = "hanzo"
			return c
		}, ""},
		{"a machine with no azp holds nothing", func() idClaims {
			c := machine()
			c.Azp = ""
			return c
		}, ""},
	} {
		c := tc.c()
		got, client := holderOf(&c)
		if got != tc.want {
			t.Errorf("%s: holder %q, want %q", tc.what, got, tc.want)
		}
		// The client is the token's azp, named exactly when a holder is.
		if want := map[bool]string{true: c.Azp, false: ""}[got != ""]; client != want {
			t.Errorf("%s: client %q, want %q", tc.what, client, want)
		}
	}
	if h, c := holderOf(nil); h != "" || c != "" {
		t.Error("no claims, no holder")
	}
}

// actorOf reads `act` from the payload the verifier already proved, and a
// payload it cannot read counts as naming one — the answer that holds nothing.
func TestActorOfReadsTheVerifiedPayload(t *testing.T) {
	enc := func(payload string) string {
		return "h." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".s"
	}
	for _, tc := range []struct {
		what, raw string
		want      actor
	}{
		{"no act", enc(`{"sub":"hanzo/z"}`), actor{}},
		{"act null", enc(`{"sub":"hanzo/z","act":null}`), actor{}},
		{"act names the org key", enc(`{"sub":"hanzo/z","act":{"sub":"hanzo/key-1"}}`), actor{acted: true, sub: "hanzo/key-1"}},
		{"an impersonation names its operator", enc(`{"sub":"u-ada","imp":true,"act":{"sub":"admin/z"}}`), actor{acted: true, imp: true, sub: "admin/z"}},
		{"an operator signed as a bare string", enc(`{"sub":"u-ada","imp":true,"act":"admin/z"}`), actor{acted: true, imp: true, sub: "admin/z"}},
		{"an impersonation that names nobody", enc(`{"sub":"u-ada","imp":true}`), actor{imp: true}},
		{"an impersonation spelled as a string is still one", enc(`{"sub":"u-ada","imp":"true","act":{"sub":"admin/z"}}`), actor{acted: true, imp: true, sub: "admin/z"}},
		{"imp false is no impersonation", enc(`{"sub":"u-ada","imp":false,"act":{"sub":"acme/key-1"}}`), actor{acted: true, sub: "acme/key-1"}},
		{"a payload that is not base64", "h.%%%.s", actor{acted: true}},
		{"a payload that is not JSON", enc(`not json`), actor{acted: true}},
		{"not three segments", "h.s", actor{acted: true}},
	} {
		if got := actorOf(tc.raw); got != tc.want {
			t.Errorf("%s: actorOf = %+v, want %+v", tc.what, got, tc.want)
		}
	}
}

// The boundary mints the holder from the verified token and from nothing a
// request carries.
func TestTheBoundaryMintsTheHolder(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	v := newIdentityValidator(testIssuer, jwksServer(t, &key.PublicKey).URL, 0)
	exp := time.Now().Add(time.Hour)

	op := machineClaims("hanzo-operator", "hanzo", exp)
	op.TokenType = "access-token"
	z := tokenClaims("hanzo-console", "hanzo", "z@hanzo.ai", false, exp)
	z.TokenType, z.PreferredUsername, z.Azp = "access-token", "z", "hanzo-console"
	zid := z
	zid.TokenType = "id-token"
	// An as() token: the member's own claims, signed with the org key that
	// acted for them as `act` (iam internal/oidc/jwt.go SignAct).
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, actClaims{Claims: z.Claims, Act: map[string]string{"sub": "hanzo/key-1"}})
	tok.Header["kid"] = "test-key"
	zact, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	for _, tc := range []struct {
		what         string
		bearer       string
		want, client string
	}{
		{"machine access token", signWith(t, key, op), "hanzo-operator", "hanzo-operator"},
		{"person access token", signWith(t, key, z), "hanzo/z", "hanzo-console"},
		{"person id_token", signWith(t, key, zid), "", ""},
		{"an org key acting for the person (act)", zact, "", ""},
		{"no credential", "", "", ""},
		{"a credential that does not verify", "not-a-jwt", "", ""},
	} {
		var got principal.Principal
		var minted bool
		app := zip.New(zip.Config{})
		app.Use(SanitizeIdentity(v))
		app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error {
			got, minted = principal.Minted(c)
			return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
		})
		req := httptest.NewRequest("GET", "/probe", nil)
		if tc.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+tc.bearer)
		}
		// Nothing a caller sends names a holder: not a header, not an org.
		req.Header.Set("X-Holder", "hanzo-operator")
		req.Header.Set("X-User-Id", "hanzo-operator")
		if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		if !minted || got.Holder != tc.want || got.Client != tc.client {
			t.Errorf("%s: minted=%v holder=%q client=%q, want %q %q", tc.what, minted, got.Holder, got.Client, tc.want, tc.client)
		}
	}

	// A machine in the reserved admin org is a holder like any other — and never
	// SuperAdmin, which is what would let it past the grant rule.
	adm := machineClaims("admin-platform-kms", authz.AdminOrg, exp)
	adm.TokenType = "access-token"
	if h, _ := holderOf(&adm); !appPrincipal(&adm) || sudo(&adm.Claims) || h != "admin-platform-kms" {
		t.Fatal("an admin-org machine is a holder and is not SuperAdmin")
	}
}

// actClaims is an IAM access token that names an actor.
type actClaims struct {
	authz.Claims
	Act map[string]string `json:"act"`
}

// A SuperAdmin's own access token switched into acme with X-Org-Id (the
// masquerade BillingOrg names) is minted Org=acme, Kind=Person, Holder=admin/z,
// and principal.Signed refuses it: an operator inspecting an org neither seats,
// authorizes nor commits as one of its people — also when the request names its
// own X-User-Owner acme, which the boundary mints from the token alone. The same
// token at home is Signed, so the refusal is the switch and not the token; and a
// member of acme whose home is another org is acme's person there.
func TestSignedRefusesASuperAdminInAnotherOrg(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v := newIdentityValidator(testIssuer, jwksServer(t, &key.PublicKey).URL, 0)
	exp := time.Now().Add(time.Hour)
	z := tokenClaims("hanzo-console", authz.AdminOrg, "z@hanzo.ai", false, exp)
	z.TokenType, z.PreferredUsername, z.Azp = "access-token", "z", "hanzo-console"
	ada := tokenClaims("hanzo-console", "hanzo", "ada@hanzo.ai", false, exp)
	ada.TokenType, ada.PreferredUsername, ada.Azp = "access-token", "ada", "hanzo-console"
	ada.Orgs = append(ada.Orgs, authz.Membership{Org: "acme", Role: authz.Member})

	probe := func(c idClaims, hdr map[string]string) (principal.Principal, bool, bool) {
		var got principal.Principal
		var signed, super bool
		app := zip.New(zip.Config{})
		app.Use(SanitizeIdentity(v))
		app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error {
			got, signed = principal.Signed(c)
			super = principal.Super(c)
			return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
		})
		req := httptest.NewRequest("GET", "/probe", nil)
		req.Header.Set("Authorization", "Bearer "+signWith(t, key, c))
		for k, x := range hdr {
			req.Header.Set(k, x)
		}
		if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
			t.Fatal(err)
		}
		return got, signed, super
	}
	if got, signed, super := probe(z, map[string]string{"X-Org-Id": "acme"}); signed || !super {
		t.Errorf("a SuperAdmin acting in acme: Signed = %v (org %q, holder %q), Super = %v; want refused", signed, got.Org, got.Holder, super)
	}
	if got, signed, _ := probe(z, map[string]string{"X-Org-Id": "acme", "X-User-Owner": "acme"}); signed {
		t.Errorf("a SuperAdmin in acme naming its own X-User-Owner acme is Signed (org %q)", got.Org)
	}
	if got, signed, _ := probe(z, nil); !signed || got.Org != authz.AdminOrg {
		t.Errorf("a SuperAdmin at home: Signed = %v in org %q, want a person in %s", signed, got.Org, authz.AdminOrg)
	}
	if got, signed, _ := probe(ada, map[string]string{"X-Org-Id": "acme"}); !signed || got.Org != "acme" {
		t.Errorf("a member of acme from hanzo acting in acme: Signed = %v in org %q, want acme's person", signed, got.Org)
	}
}
