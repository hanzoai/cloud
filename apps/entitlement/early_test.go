package entitlement

// Early access: an org whose live plan carries it (@hanzo/plans
// licensing.early_access, every Max rung) is given each product in beta on its
// first look, the way an applicant is, and the enablement stays the one predicate.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/plane"
	luxlog "github.com/luxfi/log"
)

func mountEarly(t *testing.T, fc *fakeCommerce) *installs {
	t.Helper()
	prev := mounted
	mounted = &service{store: openTestStore(t), commerce: fc, log: luxlog.NewNoOpLogger()}
	t.Cleanup(func() { mounted = prev })
	return stubInstalls(t)
}

// A Max org holds the CRM without applying: its first look grants it, with the
// module grown in its own Base, and every later look reads the enablement alone.
// An org whose plan carries no early access is refused, and nothing is written.
func TestEarlyAccessOpensTheBetaWithoutApplying(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fc := newFakeCommerce()
		fc.early["acme"] = true
		installed := mountEarly(t, fc)

		for range 2 {
			out, err := holds(plane.For(context.Background(), "acme"), &client.ProductIn{Product: "crm"})
			if err != nil || !out.On {
				t.Fatalf("holds(acme, crm) = %+v, %v; a Max org was refused the beta", out, err)
			}
		}
		if fc.calls != 1 {
			t.Fatalf("commerce asked %d times; once the org holds the beta its enablement answers alone", fc.calls)
		}
		if on, _ := mounted.store.Holds(context.Background(), "acme", "crm"); !on {
			t.Fatal("early access opened the CRM without writing the enablement every other reader asks")
		}
		if got := installed.all(); !slices.Equal(got, []string{"acme:crm"}) {
			t.Fatalf("installs = %v, want the crm module grown once, in acme's Base", got)
		}

		out, err := holds(plane.For(context.Background(), "initech"), &client.ProductIn{Product: "crm"})
		if err != nil || out.On {
			t.Fatalf("holds(initech, crm) = %+v, %v; a plan without early access opened the beta", out, err)
		}
		if on, _ := mounted.store.Holds(context.Background(), "initech", "crm"); on {
			t.Fatal("a refused org was written an enablement")
		}
	})
}

// Early access opens what is in beta and nothing else: a product out of beta is
// still the plan's to grant and the org's to switch on.
func TestEarlyAccessOpensOnlyTheBeta(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fc := newFakeCommerce()
		fc.early["acme"] = true
		mountEarly(t, fc)
		out, err := holds(plane.For(context.Background(), "acme"), &client.ProductIn{Product: "team"})
		if err != nil || out.On {
			t.Fatalf("holds(acme, team) = %+v, %v; early access opened a product not in beta", out, err)
		}
		if fc.calls != 0 {
			t.Fatalf("commerce asked %d times about a product not in beta", fc.calls)
		}
	})
}

// A shared or brand org is never given the beta by a plan: one person's Max
// subscription in the sign-up org is not that org's, and would open its CRM to
// everyone in it.
func TestEarlyAccessSkipsAHeldOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fc := newFakeCommerce()
		fc.early["hanzo-id"] = true
		mountEarly(t, fc)
		out, err := holds(plane.For(context.Background(), "hanzo-id"), &client.ProductIn{Product: "crm"})
		if err != nil || out.On {
			t.Fatalf("holds(hanzo-id, crm) = %+v, %v; a shared org was given the beta by a plan", out, err)
		}
	})
}

// A plan that cannot be read refuses, and so does a grant whose module cannot be
// installed: the org is left without the beta either way, and is told why.
func TestEarlyAccessFailsClosed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fc := newFakeCommerce()
		fc.early["acme"] = true
		fc.err = errors.New("commerce: no peer")
		installed := mountEarly(t, fc)
		if _, err := holds(plane.For(context.Background(), "acme"), &client.ProductIn{Product: "crm"}); err == nil {
			t.Fatal("holds answered though the plan could not be read")
		}

		fc.err = nil
		installed.fail = errors.New("base: hosted Bases are not embedded in this deployment")
		if _, err := holds(plane.For(context.Background(), "acme"), &client.ProductIn{Product: "crm"}); err == nil {
			t.Fatal("holds answered though the CRM's module could not be installed")
		}
		if on, _ := mounted.store.Holds(context.Background(), "acme", "crm"); on {
			t.Fatal("acme holds the CRM without its collections")
		}
	})
}

// The console's check is the org's own enablement list: a Max member reading it is
// given the beta there, and a SuperAdmin reading the org sees it as stored.
func TestEarlyAccessIsListedForTheOrgsOwnRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		fc := newFakeCommerce()
		fc.early["acme"] = true
		fc.early["globex"] = true
		app, s := mount(t, fc)
		installed := stubInstalls(t)

		code, body := send(t, app, superAdmin("GET", "/v1/entitlement/orgs/globex", nil))
		if code != http.StatusOK || len(decodeEnabled(t, body)) != 0 {
			t.Fatalf("a SuperAdmin reading globex: want 200 and what is stored (nothing), got %d (body=%s)", code, body)
		}
		if on, _ := s.store.Holds(context.Background(), "globex", "crm"); on {
			t.Fatal("a SuperAdmin's read granted globex the beta")
		}

		code, body = send(t, app, orgMember("GET", "/v1/entitlement/orgs/acme", "acme", nil))
		if code != http.StatusOK || !slices.Equal(decodeEnabled(t, body), []string{"crm"}) {
			t.Fatalf("a Max member reading acme: want 200 [crm], got %d (body=%s)", code, body)
		}
		if got := installed.all(); !slices.Equal(got, []string{"acme:crm"}) {
			t.Fatalf("installs = %v, want the crm module grown in acme's Base", got)
		}

		code, body = send(t, app, orgMember("GET", "/v1/entitlement/orgs/initech", "initech", nil))
		if code != http.StatusOK || len(decodeEnabled(t, body)) != 0 {
			t.Fatalf("an org without early access: want 200 and nothing, got %d (body=%s)", code, body)
		}
	})
}
