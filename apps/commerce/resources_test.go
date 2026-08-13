// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	commerceresources "github.com/hanzoai/commerce/api/resources"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// boundLeaves is the set of first segments under /v1/commerce that the merchant
// bundle actually binds, read from the live router rather than from a list.
//
// It trims the trailing slash because the bundle binds the collection path as
// `/v1/commerce/product/` while every caller omits it — the same route only
// because fiber's StrictRouting defaults false, which is pinned below.
func boundLeaves(t *testing.T) []string {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	pass := func(c *zip.Ctx) error { return c.Next() }

	// The same call Mount makes, with the gates as pass-throughs: this asks what
	// the router BINDS, which is orthogonal to who may write a product.
	//
	// Registration is where this fails, if it fails: the composer refuses a
	// program with two declarations of one address — at boot, inside Mount, long
	// after a green build. Nothing else in this package calls Route.
	// TestOneKindRegistersOnce below pins that refusal.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("commerceresources.Route was refused at registration: %v\n"+
				"the composer rejects a program that declares one address twice — "+
				"this is a BOOT panic, and it passes every build check", r)
		}
	}()
	commerceresources.Route(app.Group("/v1/commerce"), pass, pass, pass, nil)

	if app.Fiber().Config().StrictRouting {
		t.Error("StrictRouting is on: the bundle binds `/v1/commerce/<kind>/` and every client " +
			"calls `/v1/commerce/<kind>` — under strict routing those stop being the same route " +
			"and every admin data view 404s again")
	}

	seen := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes() {
		p := strings.TrimSuffix(r.Path, "/")
		rest, ok := strings.CutPrefix(p, "/v1/commerce/")
		if !ok {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			rest = rest[:i]
		}
		// A leaf, not a wildcard — `:id` and `*` are the segment BELOW a leaf.
		if rest != "" && !strings.HasPrefix(rest, ":") && !strings.HasPrefix(rest, "*") {
			seen[rest] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// TestEveryBoundMerchantLeafIsRoutedToCommerce is the assertion that matters, and
// it is NOT the one this file used to make.
//
// The earlier version proved the bundle BINDS /v1/commerce/product. It did, and
// production still answered 404 — because binding a route inside commerce says
// nothing about the host handing commerce the request. The light host routes by
// manifest.Apps: commerce's row names its leaves explicitly, `ai` holds the bare
// "/v1", and longest-prefix wins. So an unnamed leaf goes to ai's catch-all and
// answers ai's 404 while commerce sits there serving it to nobody. That is
// exactly what shipped: /v1/commerce/tenant (named) returned 200 from commerce
// in the same breath /v1/commerce/product (unnamed) returned 404 from ai.
//
// Two lists is deliberate here — the host states what it ROUTES, the app states
// what it SERVES, and manifest/apps.go explains why importing one into the other
// would re-fatten the host. What was missing is the oracle between them for THIS
// bundle: manifest/router_test.go compares against plugin/commerce/openapi.json,
// a GENERATED file, so a bundle whose spec has not been regenerated is invisible
// to it and it passes vacuously. This reads the live router instead, so a kind
// the bundle adds upstream fails here the moment it is linked, with no
// regeneration step in between.
func TestEveryBoundMerchantLeafIsRoutedToCommerce(t *testing.T) {
	claimed := map[string]bool{}
	for _, p := range manifest.PrefixesFor("commerce") {
		claimed[p] = true
	}
	if len(claimed) == 0 {
		t.Fatal(`manifest.PrefixesFor("commerce") is empty — commerce is not a routed app at all`)
	}

	for _, leaf := range boundLeaves(t) {
		if path := "/v1/commerce/" + leaf; !claimed[path] {
			t.Errorf("%s is bound by the merchant bundle and NOT named on commerce's manifest row — "+
				"the host gives it to ai's bare \"/v1\" and the caller gets ai's 404. "+
				"Add it to the commerce row in manifest/apps.go.", path)
		}
	}
}

// TestOneKindRegistersOnce pins the rule that makes this leaf load-bearing: an
// address may be DECLARED once, and a second declaration is refused at boot.
//
// It is here because the comment in mount.go used to claim the opposite —
// "byte-identical patterns merge silently, first wins" — and that reading makes
// adding a kind to the leaf look free when commerce's api.Route still registers
// it. It is not free: the composer rejects the whole program and the STANDALONE
// dies at boot, in a different binary from the one that changed.
//
// Registering the leaf twice is the cheapest faithful stand-in for that mistake:
// same kinds, same addresses, two declarations.
//
// WHERE the refusal lands depends on the zip in the graph, and both are before
// a request is served. Cloud resolves v1.25.1, which ACCEPTS the second Route()
// call and refuses when the program is COMPOSED — so the check must ask for the
// composition, not merely register. Commerce standalone pins v1.24.2, which
// refuses inside Route() itself. A test that only registered would have read
// "accepted" here and concluded the rule was gone.
func TestOneKindRegistersOnce(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	pass := func(c *zip.Ctx) error { return c.Next() }

	refused := func() (msg string) {
		defer func() {
			if r := recover(); r != nil {
				msg = fmt.Sprint(r)
			}
		}()
		g := app.Group("/v1/commerce")
		commerceresources.Route(g, pass, pass, pass, nil)
		commerceresources.Route(g, pass, pass, pass, nil)
		_ = app.Fiber().GetRoutes() // composes; this is where v1.25.1 refuses
		return ""
	}()

	if refused == "" {
		t.Fatal("a second declaration of the same addresses was ACCEPTED — the one-registration rule " +
			"no longer holds, so mount.go's warning is stale and a kind could be added to this leaf " +
			"while api.Route still registers it")
	}
	if !strings.Contains(refused, "does not compose") {
		t.Errorf("refused, but not by the composer: %q", refused)
	}
}
