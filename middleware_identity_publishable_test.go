// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

// THE read-gate proof for the write-only PUBLISHABLE key (pk-).
//
// A publishable key is PUBLIC — it ships in client JS. The load-bearing safety
// property is that it can NEVER read: it must never become a principal, so it can
// never mint the X-User-Id / X-Org-Id every read gates on. These tests prove the
// identity boundary refuses a pk- a principal EVEN when the key resolver would happily
// resolve it, so the refusal is structural (the boundary), not incidental (a resolver
// that happens to return nil).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// alwaysResolve is a deliberately OVER-PERMISSIVE keyResolver: it resolves EVERY key
// to a full, read-capable, org-admin principal. If the read-gate were merely "the
// resolver returned nil for a pk-", this stub would defeat it — so a pk- refused
// against THIS resolver proves the boundary itself refuses it, before the resolver.
type alwaysResolve struct{ owner string }

func (a alwaysResolve) resolve(_ context.Context, _ string) *idClaims {
	return &idClaims{Owner: a.owner, Name: "svc", IsAdmin: true}
}

// A pk- (write-only publishable) presented as a bearer mints NO principal: the probe
// handler sees no org, no user, no admin — so every downstream read gate (which
// requires a validated principal) fails closed. The SAME all-resolving resolver DOES
// mint a principal for a read-capable hk-, proving the refusal is specific to the
// write-only publishable key, not a blanket block on all keys.
func TestSanitizeIdentity_PublishableKeyMintsNoPrincipal(t *testing.T) {
	v := &identityValidator{keys: alwaysResolve{owner: "victim-org"}}
	app, got := newIdentityApp(t, v)

	// pk- as a bearer → NO identity minted. (The gate keys on the token VALUE, so it
	// holds for every callerToken slot — Bearer / Basic-password / session cookie —
	// that could carry a pk-; the bearer here exercises the primary browser path.)
	*got = captured{}
	probe(t, app, bearer("pk-live-deadbeef"))
	if got.org != "" || got.user != "" || got.admin {
		t.Fatalf("pk- bearer must mint NO principal (read-gate), got org=%q user=%q admin=%v",
			got.org, got.user, got.admin)
	}

	// Control: a read-capable hk- STILL resolves through the same resolver, so the
	// gate is pk--specific — it does not break key auth for the read-capable family.
	*got = captured{}
	probe(t, app, bearer("hk-live-abc"))
	if got.org != "victim-org" {
		t.Fatalf("hk- must still resolve to its org (gate is pk--specific), got org=%q", got.org)
	}
}

// validatedPrincipal itself returns nil for a pk- even when the resolver would resolve
// it — the same fact at the function level, so a future reorder of the boundary that
// slips a pk- past the short-circuit is caught here too.
func TestValidatedPrincipal_RefusesPublishableKey(t *testing.T) {
	v := &identityValidator{keys: alwaysResolve{owner: "victim-org"}}
	app := zip.New(zip.Config{})
	var sawPrincipal bool
	app.Get("/vp", func(c *zip.Ctx) error {
		sawPrincipal = validatedPrincipal(c, v) != nil
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	hit := func(tok string) {
		req := httptest.NewRequest(http.MethodGet, "/vp", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if _, err := app.Fiber().Test(req); err != nil {
			t.Fatalf("Test request: %v", err)
		}
	}
	hit("pk-live-xyz")
	if sawPrincipal {
		t.Fatal("validatedPrincipal must return nil for a pk- (write-only, never a principal)")
	}
	hit("hk-live-xyz")
	if !sawPrincipal {
		t.Fatal("validatedPrincipal must resolve a read-capable hk- (gate is pk--specific)")
	}
}
