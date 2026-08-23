// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type delegateIn struct{}
type delegateOut struct{}

// A DELEGATED CALL RESOLVES THE SAME TENANT WHEREVER THE CALLEE RUNS.
//
// As re-points the tenant for an operator acting on somebody else's books. That
// answer has to be the one a callee reads, and a callee reads it through
// principal.OrgFrom — which asks the PARKED slot first and the caller second,
// and calls them one fact.
//
// Only the caller crosses a socket, so re-pointing only the caller made those
// two disagree, and which one a callee saw turned on where it ran: a real hop
// reads the caller off headers and answers the new tenant; plane.Ask short-
// circuits a co-resident peer to zip.Here, which hands this very context to the
// handler, where the slot still held the ORIGINAL org and is asked first. Same
// call, two tenants, decided by whether two apps happened to be in one binary.
//
// So this asserts they AGREE, on the shape that hid it — a real request, whose
// boundary parked an org, delegated to another.
func TestADelegatedCallNamesOneTenant(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("astenant"), DisableStartupMessage: true})
	app.Use(zip.H(Bridge()))

	var acting, caller, from string
	var kept string
	zip.Get(app, "/probe", func(ctx context.Context, _ *delegateIn) (*delegateOut, error) {
		c, ok := Request(ctx)
		if !ok {
			t.Error("no request bound")
			return &delegateOut{}, nil
		}
		re := As(c, "other-org")
		acting, _ = principal.Acting(re)
		from, _ = principal.OrgFrom(re)
		caller = zip.CallerOf(re).Org
		// And the plain "delegate me" form keeps the caller's own tenant.
		kept, _ = principal.Acting(As(c, ""))
		return &delegateOut{}, nil
	}, zip.WithOperationID("asTenantProbe"))

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Org-Id", "own-org")
	req.Header.Set("X-User-Id", "u1")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30_000_000_000, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	_ = resp.Body.Close()

	for _, got := range []struct{ what, org string }{
		{"principal.Acting", acting}, {"principal.OrgFrom", from}, {"zip.CallerOf", caller},
	} {
		if got.org != "other-org" {
			t.Errorf("%s read %q on a call delegated to other-org — a callee that "+
				"asks this one resolves a different tenant from one that asks the others",
				got.what, got.org)
		}
	}
	if kept != "own-org" {
		t.Errorf("As(c, \"\") resolved %q, want the caller's own tenant", kept)
	}
}

// An org that could not be a namespace is not delegated to. Trimming it would
// fold two distinct orgs onto one, so the delegation falls back to the caller's
// own tenant rather than half-moving — the caller and the parked slot must not
// end up naming different things.
func TestADelegationWillNotHalfMove(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("ashalf"), DisableStartupMessage: true})
	app.Use(zip.H(Bridge()))

	var acting, caller string
	zip.Get(app, "/probe", func(ctx context.Context, _ *delegateIn) (*delegateOut, error) {
		c, _ := Request(ctx)
		re := As(c, "other-org ") // one trailing space
		acting, _ = principal.Acting(re)
		caller = zip.CallerOf(re).Org
		return &delegateOut{}, nil
	}, zip.WithOperationID("asHalfProbe"))

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Org-Id", "own-org")
	req.Header.Set("X-User-Id", "u1")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30_000_000_000, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	_ = resp.Body.Close()

	if acting != caller {
		t.Errorf("a refused delegation left the tenant split: Acting=%q CallerOf=%q", acting, caller)
	}
	if acting == "other-org" {
		t.Errorf("%q was folded onto other-org", "other-org ")
	}
}
