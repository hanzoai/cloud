package share

import (
	"context"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// fakeController is an in-memory controller: no network, records the accounts
// it was asked for and asked to rotate.
type fakeController struct {
	configuredV bool
	tokenV      string
	overviewV   overviewResp
	tokenErr    error
	rotateErr   error
	asked       []holding
	rotated     []holding
}

func (f *fakeController) configured() bool { return f.configuredV }
func (f *fakeController) token(_ context.Context, h holding, _ bool) (string, error) {
	f.asked = append(f.asked, h)
	if f.tokenErr != nil {
		return "", f.tokenErr
	}
	return f.tokenV, nil
}
func (f *fakeController) rotate(_ context.Context, h holding) (int, error) {
	f.rotated = append(f.rotated, h)
	if f.rotateErr != nil {
		return 0, f.rotateErr
	}
	return 2, nil
}
func (f *fakeController) overview(_ context.Context, _ string) (overviewResp, error) {
	return f.overviewV, nil
}

// holder is the account a caller holds first: the one enable provisions.
func holder(ctx context.Context, org string) (holding, error) {
	hs, err := holders(ctx, org)
	if err != nil {
		return holding{}, err
	}
	return hs[0], nil
}

// TestAccountDeterminism — the per-org email + password are pure functions of
// the org, so provisioning is stateless and idempotent (same org → same creds).
func TestAccountDeterminism(t *testing.T) {
	c := &httpController{secret: []byte("k")}
	// email is per-org, deterministic, and carries an HMAC freshness suffix.
	e1, e2 := c.accountEmail("acme"), c.accountEmail("acme")
	if e1 != e2 {
		t.Error("email not deterministic")
	}
	if !strings.HasPrefix(e1, "share-acme-") || !strings.HasSuffix(e1, "@hanzo.ai") {
		t.Errorf("email shape = %q", e1)
	}
	if c.accountEmail("acme") == c.accountEmail("other") {
		t.Error("email not org-scoped")
	}
	if c.accountPassword("acme") != c.accountPassword("acme") {
		t.Error("password not deterministic")
	}
	if c.accountPassword("acme") == c.accountPassword("other") {
		t.Error("password not org-scoped")
	}
	if len(c.accountPassword("acme")) != 32 {
		t.Errorf("password length = %d, want 32", len(c.accountPassword("acme")))
	}
}

// TestConfiguredGate — an unconfigured controller reports not-configured, so the
// gate can fail closed.
func TestConfiguredGate(t *testing.T) {
	if (&httpController{adminToken: ""}).configured() {
		t.Error("empty admin token must be unconfigured")
	}
	if !(&httpController{adminToken: "x"}).configured() {
		t.Error("present admin token must be configured")
	}
}

// TestShareURL — the token renders into the public URL via the template.
func TestShareURL(t *testing.T) {
	t.Setenv("SHARE_URL_TEMPLATE", "https://share-{token}.hanzo.app")
	if got := shareURL("abc123"); got != "https://share-abc123.hanzo.app" {
		t.Errorf("shareURL = %q", got)
	}
}

// In a customer's org the tunnel account is the org's, shared by its members. In
// a held org — where every self-serve signup lands beside the platform's staff —
// it is each person's own, so one stranger is never handed the credential that
// lists and opens another's shells. An admin of the held org and a SuperAdmin
// hold their own there too: the org's account is held by nobody.
func TestAHeldOrgsTunnelAccountIsThePersons(t *testing.T) {
	as := func(org, user string, admin bool) context.Context {
		return zip.WithCaller(context.Background(), zip.Caller{Org: org, User: user, OrgAdmin: admin})
	}
	acct := func(ctx context.Context, org string) holding {
		t.Helper()
		h, err := holder(ctx, org)
		if err != nil {
			t.Fatalf("holder(%s): %v", org, err)
		}
		return h
	}
	if a, b := acct(as("acme", "ada", false), "acme"), acct(as("acme", "bob", false), "acme"); a != (holding{org: "acme"}) || b != a {
		t.Fatalf("a customer org's members do not share its account: %v, %v", a, b)
	}
	ada, bob := acct(as("hanzo", "ada", false), "hanzo"), acct(as("hanzo", "bob", false), "hanzo")
	if ada.person == "" || bob.person == "" || ada == bob {
		t.Fatalf("two strangers in a held org share a tunnel account: %v, %v", ada, bob)
	}
	if len(ada.person) != 64 {
		t.Fatalf("a person's account is keyed on %d hex of their subject, want the full hash", len(ada.person))
	}
	if again := acct(as("hanzo", "ada", false), "hanzo"); again != ada {
		t.Fatalf("a person's own account is not stable: %v then %v", ada, again)
	}
	if staff := acct(as("hanzo", "op", true), "hanzo"); staff.person == "" || staff == ada {
		t.Fatalf("the held org's admin holds %v, want an account of their own", staff)
	}
	super := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: "root", Admin: true})
	if got := acct(super, "hanzo"); got.person == "" {
		t.Fatalf("a SuperAdmin in a held org holds %v, want an account of their own", got)
	}
	if boss := acct(as("acme", "boss", true), "acme"); boss != (holding{org: "acme"}) {
		t.Fatalf("a customer org's admin lost the org's account: %v", boss)
	}
	if _, err := holder(context.Background(), "hanzo"); err == nil {
		t.Fatal("a call naming no person was handed a held org's tunnel account")
	}
}

// NO ORG NAME REACHES A PERSON'S ACCOUNT. Anyone can found an org with nearly
// any name, so an org's derivation must never produce a person's address or
// password — not from the name the person's key spells, not from the old
// `hanzo-<hash>` shape, not from the address itself.
func TestNoOrgNameDerivesAPersonsCredential(t *testing.T) {
	c := &httpController{secret: []byte("k")}
	ada, err := holder(zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: "ada"}), "hanzo")
	if err != nil {
		t.Fatal(err)
	}
	email, password := c.credential(ada)
	if !strings.HasPrefix(email, "person-") || !strings.HasSuffix(email, "@hanzo.ai") || len(password) != 32 {
		t.Fatalf("a person's credential has the wrong shape: %q / %d", email, len(password))
	}
	local := strings.TrimSuffix(email, "@hanzo.ai")
	for _, org := range []string{
		"hanzo", ada.String(), "hanzo-" + ada.person[:12], "hanzo-" + ada.person,
		"person:hanzo:" + ada.person, "person-hanzo-" + ada.person, local, strings.TrimPrefix(local, "person-"),
	} {
		e, p := c.credential(holding{org: org})
		if e == email || p == password {
			t.Fatalf("an org named %q derives the person's credential", org)
		}
		if !strings.HasPrefix(e, "share-") {
			t.Fatalf("an org's address %q does not begin share-, so the two kinds could meet", e)
		}
	}
	// The org's own account is derived as it always was, so no org loses its
	// tunnels to this.
	if e, p := c.credential(holding{org: "acme"}); e != c.accountEmail("acme") || p != c.accountPassword("acme") {
		t.Fatal("an org's own account moved")
	}
	// Two people, and one person in two held orgs, hold different accounts.
	bob, _ := holder(zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: "bob"}), "hanzo")
	lux, _ := holder(zip.WithCaller(context.Background(), zip.Caller{Org: "lux", User: "ada"}), "lux")
	for _, other := range []holding{bob, lux} {
		if e, _ := c.credential(other); e == email {
			t.Fatalf("%v and %v share an address", ada, other)
		}
	}
}
