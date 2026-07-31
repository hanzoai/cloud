package cloud_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// The rule is one expression, so it gets one truth table. Every (scope,
// authority) pair is named here — including the pairs that must be REFUSED,
// which is where a privilege escalation would live.
func TestScopeAdmits(t *testing.T) {
	var (
		anon   = cloud.Authority{}
		member = cloud.Authority{Validated: true}
		orgAdm = cloud.Authority{Validated: true, OrgAdmin: true}
		super  = cloud.Authority{Validated: true, Super: true}
		// A caller carrying admin bits with NO validated principal: the shape a
		// forged header takes off the gateway. Every scope must refuse it.
		forged = cloud.Authority{Super: true, OrgAdmin: true}
	)
	for _, tc := range []struct {
		name  string
		scope cloud.Scope
		who   cloud.Authority
		want  bool
	}{
		{"member scope admits a validated member", cloud.Member, member, true},
		{"member scope admits an org admin", cloud.Member, orgAdm, true},
		{"member scope admits a superadmin", cloud.Member, super, true},
		{"member scope refuses anonymous", cloud.Member, anon, false},
		{"member scope refuses forged admin bits", cloud.Member, forged, false},

		{"admin scope refuses a plain member", cloud.Admin, member, false},
		{"admin scope admits an org admin", cloud.Admin, orgAdm, true},
		{"admin scope admits a superadmin", cloud.Admin, super, true},
		{"admin scope refuses anonymous", cloud.Admin, anon, false},
		{"admin scope refuses forged admin bits", cloud.Admin, forged, false},

		{"super scope refuses a plain member", cloud.Super, member, false},
		{"super scope refuses an org admin", cloud.Super, orgAdm, false},
		{"super scope admits a superadmin", cloud.Super, super, true},
		{"super scope refuses anonymous", cloud.Super, anon, false},
		{"super scope refuses forged admin bits", cloud.Super, forged, false},
	} {
		if got := tc.scope.Admits(tc.who); got != tc.want {
			t.Errorf("%s: Admits=%v, want %v", tc.name, got, tc.want)
		}
	}
}

// An org admin is NOT platform sudo. This is the pair the platform must never
// collapse — a customer-org admin holding full authority inside its own org, at
// a door that guards shared platform state.
func TestOrgAdminIsNotPlatformSudo(t *testing.T) {
	orgAdm := cloud.Authority{Validated: true, OrgAdmin: true}
	if cloud.Super.Admits(orgAdm) {
		t.Fatal("an org admin was admitted to the Super scope — the two admin scopes are conflated, which is a privilege escalation")
	}
}

// Guard refuses BEFORE the handler: an unauthorized request must not be observed
// by anything behind the gate, so "ran zero times" is the assertion, not the
// status code alone.
func TestGuardRefusesBeforeTheHandler(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scope   cloud.Scope
		headers map[string]string
		ran     bool
		code    int
	}{
		{"no principal", cloud.Member, nil, false, http.StatusForbidden},
		{"validated member", cloud.Member, map[string]string{"X-User-Id": "u1"}, true, http.StatusOK},
		{"member at an admin door", cloud.Admin, map[string]string{"X-User-Id": "u1"}, false, http.StatusForbidden},
		{"org admin at an admin door", cloud.Admin, map[string]string{"X-User-Id": "u1", "X-User-IsOrgAdmin": "true"}, true, http.StatusOK},
		{"org admin at a super door", cloud.Super, map[string]string{"X-User-Id": "u1", "X-User-IsOrgAdmin": "true"}, false, http.StatusForbidden},
		{"superadmin at a super door", cloud.Super, map[string]string{"X-User-Id": "u1", "X-User-IsAdmin": "true"}, true, http.StatusOK},
		// Admin bits with no validated principal — the forged-header shape.
		{"forged admin bits, no principal", cloud.Super, map[string]string{"X-User-IsAdmin": "true"}, false, http.StatusForbidden},
	} {
		ran := false
		app := zip.New(zip.Config{})
		app.Get("/probe", cloud.Guard(tc.scope, func(c *zip.Ctx) error {
			ran = true
			return c.JSON(http.StatusOK, map[string]any{"ok": true})
		}))
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if resp.StatusCode != tc.code {
			t.Errorf("%s: status=%d, want %d", tc.name, resp.StatusCode, tc.code)
		}
		if ran != tc.ran {
			t.Errorf("%s: handler ran=%v, want %v", tc.name, ran, tc.ran)
		}
	}
}

// AuthorityOf reads the three predicates off a request and nothing else, so the
// HTTP transport and any other transport can reach the same verdict.
func TestAuthorityOfReadsTheThreePredicates(t *testing.T) {
	app := zip.New(zip.Config{})
	var got cloud.Authority
	app.Get("/probe", func(c *zip.Ctx) error {
		got = cloud.AuthorityOf(c)
		return c.JSON(http.StatusOK, map[string]any{"ok": true})
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-User-Id", "u1")
	req.Header.Set("X-User-IsAdmin", "true")
	req.Header.Set("X-User-IsOrgAdmin", "true")
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	want := cloud.Authority{Validated: true, Super: true, OrgAdmin: true}
	if got != want {
		t.Errorf("AuthorityOf=%+v, want %+v", got, want)
	}
}
