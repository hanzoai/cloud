// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"strings"
	"testing"

	commerceresources "github.com/hanzoai/commerce/api/resources"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// THE MERCHANT RESOURCES MUST REGISTER, AND THEY MUST REGISTER HERE.
//
// Three failures this guards, none of which a compiler or a status check sees:
//
//  1. REGISTRATION PANICS. This is a shared router: byte-identical patterns
//     merge silently, but two equal-specificity params with different names
//     panic at REGISTRATION — inside Mount, at boot, long after a green build.
//     Nothing else in this package calls commerceresources.Route.
//
//  2. THE ROUTES GO MISSING. Every admin data view 404'd in production for
//     exactly this reason: commerce is a PLUGIN of this binary, there is no
//     commerce backend pod, and the embed carried no resource bundle.
//
//  3. STRICT ROUTING GETS TURNED ON. The bundle binds the collection path WITH
//     a trailing slash (`/v1/commerce/product/`); every client calls it WITHOUT
//     one. Those are the same route only because fiber's StrictRouting defaults
//     to false and zip never sets it. Flip it and all of (2) comes back, from a
//     change that has nothing to do with commerce.
//
// The absence assertion is proven to fire (a bogus kind fails it). The strict-
// routing one is NOT falsifiable today — zip.Config exposes no StrictRouting
// field, so it pins fiber's default rather than a setting we can flip.
//
// It reads the route TABLE rather than driving a request: a request would
// exercise commerce's datastore and tenant resolution, which need a store this
// test has no business standing up — and a panic in there reads as "the route
// is missing" when the route is fine.
func TestMerchantResourcesRegisterAtV1Commerce(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})

	// The same call Mount makes, with the gates as pass-throughs: this asks
	// whether the TABLE binds, which is orthogonal to who may write a product.
	pass := func(c *zip.Ctx) error { return c.Next() }
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("commerceresources.Route panicked at registration: %v\n"+
					"two equal-specificity params with different names collide in this shared router — "+
					"this is a BOOT panic, and it passes every build check", r)
			}
		}()
		commerceresources.Route(app.Group("/v1/commerce"), pass, pass, pass, nil)
	}()

	bound := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes() {
		bound[strings.TrimSuffix(r.Path, "/")] = true
	}

	// The kinds the admin dashboard reads. Every one of these 404'd in production.
	for _, kind := range []string{"product", "collection", "variant", "webhook", "saleschannel", "stocklocation"} {
		if path := "/v1/commerce/" + kind; !bound[path] {
			t.Errorf("%s is not bound — the merchant resource is ABSENT, not gated; "+
				"api.hanzo.ai/v1/commerce is THE commerce endpoint and this binary is the only thing serving it", path)
		}
	}

	// The trailing slash above is trimmed because the bundle binds the
	// collection path as `/v1/commerce/product/` while every caller omits it.
	// That equivalence is fiber's, not ours, and it is the whole reason the
	// slash-less client call resolves — so it is pinned, not assumed.
	if app.Fiber().Config().StrictRouting {
		t.Error("StrictRouting is on: the bundle binds `/v1/commerce/<kind>/` and every client " +
			"calls `/v1/commerce/<kind>` — under strict routing those stop being the same route " +
			"and every admin data view 404s again")
	}
}
