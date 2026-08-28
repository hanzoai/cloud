package cloud_test

// The runtime half of the opt-in: a capability an org has not turned on answers
// 404 to that org, and the same 404 a path nobody registered gets.
//
// It is driven through the real zip stack against a real entitlement peer on a
// real socket, for the same reason its sibling in stage_test.go is: both failures
// live in the CROSSING. If the org does not reach the enablement read, an elective
// product is reachable by nobody including the customers paying for it; if a
// failure to reach entitlement reads as permission, every elective product is
// reachable by everybody and the opt-in is decoration. Neither shows up in a unit
// test of the predicate.
//
// The row is the fleet's own — `crm` carries Elective in manifest/apps.go — so
// these also fail if that marking is undone.

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// serveEntitlement publishes the enablement read on the plane, answering from on:
// the products each org has turned on. It is the real op contract
// (plane.EntitlementHolds, plane.ProductIn → plane.Held) reached the real way, so
// what the refusal is tested against is what it calls in production.
func serveEntitlement(t *testing.T, on map[string][]string) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))

	app := zip.New(zip.Config{AppName: "entitlement"})
	zip.Post[plane.ProductIn, plane.Held](app, "/entitlement/holds",
		func(ctx context.Context, in *plane.ProductIn) (*plane.Held, error) {
			// The same refusal apps/entitlement makes: no caller, no answer.
			org := cloud.Who(ctx).Org
			if org == "" {
				return nil, zip.ErrUnauthorized("holds: no org on the call")
			}
			return &plane.Held{On: slices.Contains(on[org], in.Product)}, nil
		}, zip.WithOperationID(plane.EntitlementHolds))

	go func() { _ = app.Listen(zip.SocketPath("entitlement")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "entitlement")
}

// elective is one capability's surface behind its own refusal, plus an address
// outside every elective prefix. 200 means the request reached the app.
func elective(names ...string) *zip.App {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	for _, name := range names {
		app.Use(cloud.Elective(name))
	}
	served := func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"served": c.Path()}) }
	app.Get("/v1/crm/contacts", served) // crm is elective
	app.Get("/v1/iam/keys", served)     // universal, serves without an enablement
	app.Get("/v1/health", served)
	return app
}

// An org that has not asked for the product cannot tell it apart from a path
// nobody registered. Answering 403 would advertise a product list to a stranger.
func TestElectiveNotEnabledIsNotThere(t *testing.T) {
	serveEntitlement(t, map[string][]string{"acme": {"team"}}) // acme has a DIFFERENT product
	app := elective("crm")

	code, body := fetch(t, app, "/v1/crm/contacts", member)
	if code != http.StatusNotFound {
		t.Fatalf("GET /v1/crm/contacts = %d %s, want 404 — an elective capability answered an org that never enabled it", code, body)
	}

	// Byte-identical to a miss, measured against this app's own unrouted path
	// rather than a literal, so it stays true across a zip bump.
	missCode, missBody := fetch(t, app, "/v1/nosuchthing", member)
	if code != missCode || body != missBody {
		t.Errorf("refusal is distinguishable from a miss:\n  elective: %d %s\n  unrouted: %d %s",
			code, body, missCode, missBody)
	}
}

// The org that turned it on reaches the app, and its neighbour does not. Without
// this the opt-in is just a way to turn a product off for everyone.
func TestEnablingLetsTheOrgIn(t *testing.T) {
	serveEntitlement(t, map[string][]string{"acme": {"crm"}})
	app := elective("crm")

	if code, body := fetch(t, app, "/v1/crm/contacts", member); code != http.StatusOK {
		t.Fatalf("GET /v1/crm/contacts = %d %s, want 200 — acme enabled crm and was refused anyway", code, body)
	}
	other := map[string]string{"X-User-Id": "initech/ceo@initech.test", "X-Org-Id": "initech"}
	if code, _ := fetch(t, app, "/v1/crm/contacts", other); code != http.StatusNotFound {
		t.Errorf("GET /v1/crm/contacts as initech = %d, want 404 — one org's enablement admitted another", code)
	}
}

// An entitlement outage fails CLOSED. The alternative — admit when we cannot ask
// — makes every elective product free for everyone for the duration, which is the
// billing failure the refusal exists to prevent.
func TestEntitlementUnreachableRefuses(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t)) // nothing is listening in it
	app := elective("crm")

	if code, body := fetch(t, app, "/v1/crm/contacts", member); code != http.StatusNotFound {
		t.Fatalf("GET /v1/crm/contacts with no entitlement peer = %d %s, want 404", code, body)
	}
}

// A universal capability installs NOTHING. Not a middleware that always admits —
// a nil component, so the 124 rows that are not elective cost no predicate, no
// plane call and no socket.
func TestUniversalInstallsNoRefusal(t *testing.T) {
	if h := cloud.Elective("iam"); h != nil {
		t.Error("a universal row produced a handler — every request to the fleet would ask entitlement about it")
	}
	if h := cloud.Elective("nosuchapp"); h != nil {
		t.Error("a name with no row produced a handler")
	}
	if h := cloud.Elective("crm"); h == nil {
		t.Fatal("the elective row produced no handler — crm is open to everyone")
	}

	// And it is live: with no entitlement peer anywhere, a universal row serves.
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	app := elective("iam", "crm")
	if code, body := fetch(t, app, "/v1/iam/keys", member); code != http.StatusOK {
		t.Errorf("GET /v1/iam/keys = %d %s, want 200 — a universal capability was made to depend on entitlement", code, body)
	}
}

// The two refusals are INDEPENDENT. An elective row that is also staged must
// satisfy both, and neither may stand in for the other — the failure this catches
// is one authority's answer being read as the other's, which would let a flag
// grant a product nobody paid for.
func TestStageAndElectiveDoNotSubstitute(t *testing.T) {
	if cloud.Stage("crm") != nil {
		t.Error("crm is ga; the stage refusal must compose nothing for it")
	}
	if cloud.Elective("research") != nil {
		t.Error("research is staged but not elective; the elective refusal must compose nothing for it")
	}
}
