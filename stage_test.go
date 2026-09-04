package cloud_test

// The runtime half of HIP-0139 §8: a capability that is not ga answers 404 to an
// org that has not been let into it.
//
// It is driven through the real zip stack against a real flags peer on a real
// socket, because the two things that can go wrong here are both about the
// crossing. If the org does not reach the flag evaluation, every org is refused
// and a beta product is reachable by nobody; if a failure to reach flags reads as
// permission, every beta product is reachable by everybody. Neither shows up in a
// unit test of the predicate.
//
// The rows are the fleet's own — `ads` is beta and `iam` is ga in
// manifest/apps.go — so these also fail if the seeding is undone.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// serveFlags publishes the flag read on the plane, answering from held: the orgs
// that have been let into each capability. It is the real op contract
// (client.FlagsHold, client.FlagIn → client.Flag) reached the real way, so what the
// refusal is tested against is what it will call in production.
func serveFlags(t *testing.T, held map[string][]string) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))

	app := zip.New(zip.Config{AppName: "flags"})
	zip.Post[client.FlagIn, client.Flag](app, "/flags/hold",
		func(ctx context.Context, in *client.FlagIn) (*client.Flag, error) {
			// The same refusal apps/flags makes: no caller, no answer.
			org := cloud.Who(ctx).Org
			if org == "" {
				return nil, zip.ErrUnauthorized("hold: no org on the call")
			}
			return &client.Flag{On: slices.Contains(held[org], in.Key)}, nil
		}, zip.WithOperationID(client.FlagsHold))

	go func() { _ = app.Listen(zip.SocketPath("flags")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "flags")
}

// staged is one capability's surface behind its own refusal, plus an address
// outside every staged prefix. 200 means the request reached the app.
func staged(names ...string) *zip.App {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	for _, name := range names {
		app.Use(cloud.Stage(name))
	}
	served := func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"served": c.Path()}) }
	app.Get("/v1/research/runs", served) // research is a staged (alpha) capability
	app.Get("/v1/iam/keys", served)      // ga, serves without a flag
	app.Get("/v1/health", served)
	return app
}

// member is a validated principal in org acme. The identity middleware is what
// mints these from a verified credential; here they are set directly, which is
// how every other middleware test in this package states a caller.
var member = map[string]string{"X-User-Id": "acme/z@acme.test", "X-Org-Id": "acme"}

func fetch(t *testing.T, app *zip.App, path string, hdr map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// An org that has not been let in cannot tell the capability apart from a path
// nobody registered — which is the whole point of answering 404 rather than 403.
func TestBetaWithoutTheFlagIsNotThere(t *testing.T) {
	serveFlags(t, map[string][]string{"acme": {"crm"}}) // acme holds a DIFFERENT flag
	app := staged("research")

	code, body := fetch(t, app, "/v1/research/runs", member)
	if code != http.StatusNotFound {
		t.Fatalf("GET /v1/ad/campaigns = %d %s, want 404 — a beta capability answered an org that does not hold it", code, body)
	}

	// Byte-identical to a miss, so the response is not itself the oracle 404 was
	// chosen over 403 to close. Measured against this app's own unrouted path
	// rather than against a literal, so it stays true across a zip bump.
	missCode, missBody := fetch(t, app, "/v1/nosuchthing", member)
	if code != missCode || body != missBody {
		t.Errorf("refusal is distinguishable from a miss:\n  staged: %d %s\n  unrouted: %d %s",
			code, body, missCode, missBody)
	}
}

// The org that holds the flag reaches the app. Without this the refusal is just
// a way to turn a product off.
func TestTheFlagLetsTheOrgIn(t *testing.T) {
	serveFlags(t, map[string][]string{"acme": {"research"}})
	app := staged("research")

	if code, body := fetch(t, app, "/v1/research/runs", member); code != http.StatusOK {
		t.Fatalf("GET /v1/ad/campaigns = %d %s, want 200 — the org holds `research` and was refused anyway", code, body)
	}
	// Another org's flag is not this org's. The org is read off the validated
	// principal and carried as the CALLER, so there is no argument to confuse.
	other := map[string]string{"X-User-Id": "initech/ceo@initech.test", "X-Org-Id": "initech"}
	if code, _ := fetch(t, app, "/v1/research/runs", other); code != http.StatusNotFound {
		t.Errorf("GET /v1/ad/campaigns as initech = %d, want 404 — one org's flag admitted another", code)
	}
}

// A flags outage fails CLOSED. The alternative — admit when we cannot ask —
// opens every unfinished product to every customer at once, which is the one
// failure this refusal exists to prevent.
func TestFlagsUnreachableRefuses(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t)) // nothing is listening in it
	app := staged("research")

	if code, body := fetch(t, app, "/v1/research/runs", member); code != http.StatusNotFound {
		t.Fatalf("GET /v1/ad/campaigns with no flags peer = %d %s, want 404", code, body)
	}
}

// No validated principal is 404, not 401: a stranger is owed no confirmation
// that the address is real.
func TestNoPrincipalIsNotThereEither(t *testing.T) {
	serveFlags(t, map[string][]string{"acme": {"research"}})
	app := staged("research")

	// The org header alone, with nothing that validated it — the shape a forged
	// client header arrives in.
	if code, _ := fetch(t, app, "/v1/research/runs", map[string]string{"X-Org-Id": "acme"}); code != http.StatusNotFound {
		t.Errorf("GET /v1/ad/campaigns unvalidated = %d, want 404", code)
	}
}

// A ga capability installs NOTHING. Not a middleware that always admits — a nil
// component, so a ga request costs no predicate, no plane call and no socket.
func TestGAInstallsNoRefusal(t *testing.T) {
	if h := cloud.Stage("iam"); h != nil {
		t.Error("a ga row produced a handler — every request to the ga fleet would ask flags about it")
	}
	if h := cloud.Stage("nosuchapp"); h != nil {
		t.Error("a name with no row produced a handler")
	}
	if h := cloud.Stage("research"); h == nil {
		t.Fatal("a staged row produced no handler — the capability is open to everyone")
	}

	// And it is live: with no flags peer anywhere, ga still serves.
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	app := staged("iam", "research")
	if code, body := fetch(t, app, "/v1/iam/keys", member); code != http.StatusOK {
		t.Errorf("GET /v1/iam/keys = %d %s, want 200 — a ga capability was made to depend on flags", code, body)
	}
}

// The refusal covers its own prefixes and nothing else. An app's middleware runs
// on every request the process serves, so a beta capability that answered for its
// neighbours would take the fleet down one prefix at a time.
func TestTheRefusalStaysOnItsOwnPrefixes(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t)) // flags is unreachable: a refusal would be loud
	app := staged("research")

	for _, path := range []string{"/v1/health", "/v1/iam/keys"} {
		if code, body := fetch(t, app, path, member); code != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200 — ads's refusal answered for a path it does not serve", path, code, body)
		}
	}
}

// PREFIXES NEST, and a stage refusal must key on the deeper owner, not a byte
// prefix. The middleware asks manifest.OwnerOf — the router's own rule — so a
// staged capability under a ga prefix refuses only its own leaf and never the
// operator surface above it. No two live capabilities span two stages today
// (research and admission both nest under nothing), so the rule is
// asserted where it lives rather than through a fixture that cannot exist.
func TestTheRefusalYieldsToTheDeeperOwner(t *testing.T) {
	// research owns /v1/research and nothing shallower; admin owns /v1/admin,
	// which no other capability may answer for. A byte-prefix compare would let
	// a staged /v1/r* capability refuse /v1/research AND anything sharing the
	// bytes; OwnerOf does not.
	if got := manifest.OwnerOf("/v1/research/runs"); got != "research" {
		t.Errorf("OwnerOf(/v1/research/runs) = %q, want research", got)
	}
	if got := manifest.OwnerOf("/v1/admin/author"); got == "research" {
		t.Errorf("OwnerOf(/v1/admin/author) = %q — a staged capability must not own another's prefix", got)
	}
}
