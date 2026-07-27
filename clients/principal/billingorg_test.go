package principal

// Tests for BillingOrg — the ONE "who pays" resolver.
//
// Every metered subsystem (~35 call sites) reaches the ledger key through
// Ledger -> BillingOrg, so this function alone decides which org a debit lands
// on. The cases below are the whole policy: a member pays the org they are
// acting in, a SuperAdmin masquerading pays their OWN ledger, and an unvalidated
// request pays nothing.
//
// These drive real zip contexts with the header set the identity boundary would
// have minted. They do NOT re-test the boundary itself (that is
// middleware_identity_switch_test.go); they test what the money layer does with
// a given, already-validated identity — including the identity the boundary
// would only ever mint after verifying membership.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// resolve runs BillingOrg inside a real request carrying the given minted
// identity headers, and reports (payer, ok).
func resolve(t *testing.T, headers map[string]string) (string, bool) {
	t.Helper()
	var got string
	var ok bool
	app := zip.New(zip.Config{})
	app.Get("/who", func(c *zip.Ctx) error {
		got, ok = BillingOrg(c)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodGet, "/who", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatalf("Test request: %v", err)
	}
	return got, ok
}

func TestBillingOrg(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    string
		wantOK  bool
	}{
		{
			// The overwhelmingly common case, and the regression guard: a caller
			// who has not switched must bill exactly what it billed before.
			name: "member who did not switch pays home (unchanged behavior)",
			headers: map[string]string{
				"X-User-Id":    "u-1",
				"X-Org-Id":     "acme",
				"X-User-Owner": "acme",
			},
			want:   "acme",
			wantOK: true,
		},
		{
			// THE FIX. The boundary only mints this combination after verifying
			// membership, so effective != home here means "a member switched into
			// an org they belong to" — and the wallet must follow.
			name: "member who switched pays the SELECTED org",
			headers: map[string]string{
				"X-User-Id":    "u-1",
				"X-Org-Id":     "beta",
				"X-User-Owner": "acme",
			},
			want:   "beta",
			wantOK: true,
		},
		{
			// THE CARVE-OUT. Viewing a customer's account is not permission to
			// spend their money.
			name: "SuperAdmin masquerade pays the ADMIN ledger, not the customer",
			headers: map[string]string{
				"X-User-Id":      "u-z",
				"X-Org-Id":       "maxpower",
				"X-User-Owner":   "admin",
				"X-User-IsAdmin": "true",
			},
			want:   "admin",
			wantOK: true,
		},
		{
			// An org ADMIN is not a platform admin. If one ever presents
			// effective != home, they are a switched member, and members pay the
			// org they are in — the IsOrgAdmin bit must not buy the masquerade.
			name: "org admin does NOT get the masquerade carve-out",
			headers: map[string]string{
				"X-User-Id":         "u-2",
				"X-Org-Id":          "beta",
				"X-User-Owner":      "acme",
				"X-User-IsOrgAdmin": "true",
			},
			want:   "beta",
			wantOK: true,
		},
		{
			// SuperAdmin at home (not masquerading) pays home, which is also the
			// effective org — both branches agree, as they must.
			name: "SuperAdmin at home pays home",
			headers: map[string]string{
				"X-User-Id":      "u-z",
				"X-Org-Id":       "admin",
				"X-User-Owner":   "admin",
				"X-User-IsAdmin": "true",
			},
			want:   "admin",
			wantOK: true,
		},
		{
			// Rollout fallback: a gateway that has not yet minted X-User-Owner.
			// Answer is the effective org — exactly the pre-change behavior.
			name: "no home header falls back to the effective org",
			headers: map[string]string{
				"X-User-Id": "u-1",
				"X-Org-Id":  "acme",
			},
			want:   "acme",
			wantOK: true,
		},
		{
			// No validated principal (X-User-Id is minted ONLY from a verified
			// credential). An off-gateway forge must bill nothing at all.
			name: "unvalidated request bills nothing even with both org headers",
			headers: map[string]string{
				"X-Org-Id":     "victim",
				"X-User-Owner": "victim",
			},
			want:   "",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolve(t, tc.headers)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("BillingOrg = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestLedger_TracksBillingOrg guards the bare-string form the ~35 resource-meter
// call sites actually use: it must never disagree with BillingOrg, or the debit
// and the gate would key different ledgers.
func TestLedger_TracksBillingOrg(t *testing.T) {
	for _, h := range []map[string]string{
		{"X-User-Id": "u-1", "X-Org-Id": "beta", "X-User-Owner": "acme"},
		{"X-User-Id": "u-z", "X-Org-Id": "maxpower", "X-User-Owner": "admin", "X-User-IsAdmin": "true"},
		{"X-Org-Id": "victim"},
	} {
		var billing, home string
		var ok bool
		app := zip.New(zip.Config{})
		app.Get("/who", func(c *zip.Ctx) error {
			billing, ok = BillingOrg(c)
			home = Ledger(c)
			return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
		})
		req := httptest.NewRequest(http.MethodGet, "/who", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		if _, err := app.Fiber().Test(req); err != nil {
			t.Fatalf("Test request: %v", err)
		}
		want := billing
		if !ok {
			want = "" // Ledger reports the unbillable case as the empty string.
		}
		if home != want {
			t.Fatalf("Ledger = %q but BillingOrg = (%q, %v) for %v", home, billing, ok, h)
		}
	}
}
