package host_test

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
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// serveFlags publishes the flag read on the plane, answering from held: the orgs
// that have been let into each capability. It is the real op contract
// (client.FlagsHold, client.FlagIn → client.Flag) reached the real way, so what the
// refusal is tested against is what it will call in production.
func serveFlags(t *testing.T, held map[string][]string) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))

	app := zip.New(zip.Config{AppName: "flag"})
	app.Post("/flags/hold",
		func(ctx context.Context, in *client.FlagIn) (*client.Flag, error) {
			// The same refusal apps/flags makes: no caller, no answer.
			org := cloud.Who(ctx).Org
			if org == "" {
				return nil, zip.ErrUnauthorized("hold: no org on the call")
			}
			return &client.Flag{On: slices.Contains(held[org], in.Key)}, nil
		}, zip.WithOperationID(client.FlagsHold))

	go func() { _ = app.Listen(zip.SocketPath("flag")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "flag")
}

// staged is one capability's surface behind its own refusal, plus an address
// outside every staged prefix. 200 means the request reached the app.
func staged(names ...string) *zip.App {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	for _, name := range names {
		app.Use(cloud.Stage(name))
	}
	served := func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"served": c.Path()}) }
	app.Raw(http.MethodGet, "/v1/worlds/runs", served) // worlds is a staged (alpha) capability
	app.Raw(http.MethodGet, "/v1/iam/keys", served)    // ga, serves without a flag
	app.Raw(http.MethodGet, "/v1/health", served)
	app.Raw(http.MethodPost, "/v1/worlds/runs", served)
	app.Raw(http.MethodGet, "/v1/worlds/totals", served)
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
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
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
	app := staged("worlds")

	code, body := fetch(t, app, "/v1/worlds/runs", member)
	if code != http.StatusNotFound {
		t.Fatalf("GET /v1/worlds/runs = %d %s, want 404 — a staged capability answered an org that does not hold it", code, body)
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
	serveFlags(t, map[string][]string{"acme": {"worlds"}})
	app := staged("worlds")

	if code, body := fetch(t, app, "/v1/worlds/runs", member); code != http.StatusOK {
		t.Fatalf("GET /v1/worlds/runs = %d %s, want 200 — the org holds `worlds` and was refused anyway", code, body)
	}
	// Another org's flag is not this org's. The org is read off the validated
	// principal and carried as the CALLER, so there is no argument to confuse.
	other := map[string]string{"X-User-Id": "initech/ceo@initech.test", "X-Org-Id": "initech"}
	if code, _ := fetch(t, app, "/v1/worlds/runs", other); code != http.StatusNotFound {
		t.Errorf("GET /v1/worlds/runs as initech = %d, want 404 — one org's flag admitted another", code)
	}
}

// A flags outage fails CLOSED. The alternative — admit when we cannot ask —
// opens every unfinished product to every customer at once, which is the one
// failure this refusal exists to prevent.
func TestFlagsUnreachableRefuses(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t)) // nothing is listening in it
	app := staged("worlds")

	if code, body := fetch(t, app, "/v1/worlds/runs", member); code != http.StatusNotFound {
		t.Fatalf("GET /v1/worlds/runs with no flags peer = %d %s, want 404", code, body)
	}
}

// No validated principal is 404, not 401: a stranger is owed no confirmation
// that the address is real.
func TestNoPrincipalIsNotThereEither(t *testing.T) {
	serveFlags(t, map[string][]string{"acme": {"worlds"}})
	app := staged("worlds")

	// The org header alone, with nothing that validated it — the shape a forged
	// client header arrives in.
	if code, _ := fetch(t, app, "/v1/worlds/runs", map[string]string{"X-Org-Id": "acme"}); code != http.StatusNotFound {
		t.Errorf("GET /v1/worlds/runs unvalidated = %d, want 404", code)
	}
}

// A ga capability installs NOTHING. Not a middleware that always admits — a nil
// component, so a ga request costs no predicate, no plane call and no socket.
func TestGAInstallsNoRefusal(t *testing.T) {
	if h := cloud.Stage("iam"); h != nil {
		t.Error("a ga row produced a handler — every request to the ga fleet would ask flags about it")
	}
	if h := cloud.Stage("research"); h != nil {
		t.Error("research is ga and produced a stage handler")
	}
	if h := cloud.Stage("nosuchapp"); h != nil {
		t.Error("a name with no row produced a handler")
	}
	if h := cloud.Stage("worlds"); h == nil {
		t.Fatal("a staged row produced no handler — the capability is open to everyone")
	}

	// And it is live: with no flags peer anywhere, ga still serves.
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	app := staged("iam", "worlds")
	if code, body := fetch(t, app, "/v1/iam/keys", member); code != http.StatusOK {
		t.Errorf("GET /v1/iam/keys = %d %s, want 200 — a ga capability was made to depend on flags", code, body)
	}
}

// The refusal covers its own prefixes and nothing else. An app's middleware runs
// on every request the process serves, so a beta capability that answered for its
// neighbours would take the fleet down one prefix at a time.
func TestTheRefusalStaysOnItsOwnPrefixes(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t)) // flags is unreachable: a refusal would be loud
	app := staged("worlds")

	for _, path := range []string{"/v1/health", "/v1/iam/keys"} {
		if code, body := fetch(t, app, path, member); code != http.StatusOK {
			t.Errorf("GET %s = %d %s, want 200 — worlds's refusal answered for a path it does not serve", path, code, body)
		}
	}
}

// PREFIXES NEST, and a stage refusal must key on the deeper owner, not a byte
// prefix. The middleware asks manifest.OwnerOf — the router's own rule — so a
// staged capability under a ga prefix refuses only its own leaf and never the
// operator surface above it. No two live capabilities span two stages today
// (worlds and admission both nest under nothing), so the rule is
// asserted where it lives rather than through a fixture that cannot exist.
func TestTheRefusalYieldsToTheDeeperOwner(t *testing.T) {
	// worlds owns /v1/worlds and nothing shallower; admin owns /v1/admin,
	// which no other capability may answer for. A byte-prefix compare would let
	// a staged /v1/w* capability refuse /v1/worlds AND /v1/world; OwnerOf does not.
	if got := manifest.OwnerOf("/v1/worlds/runs"); got != "worlds" {
		t.Errorf("OwnerOf(/v1/worlds/runs) = %q, want worlds", got)
	}
	if got := manifest.OwnerOf("/v1/world"); got == "worlds" {
		t.Errorf("OwnerOf(/v1/world) = %q — a staged capability must not own another's prefix", got)
	}
}

// An open read is about the org its ?org= NAMES: a caller with no org reaches
// the one address a staged row opens exactly when that org holds the flag. Everything
// else stays as it was: a caller with an org is asked about its own, only a GET of
// the open address is open, and ?org= opens nothing else under the prefix.
func TestAnOpenReadAsksAboutTheOrgItNames(t *testing.T) {
	stageOpen(t, "worlds", "/v1/worlds/runs")
	serveFlags(t, map[string][]string{"acme": {"worlds"}})
	app := staged("worlds")
	initech := map[string]string{"X-User-Id": "initech/ceo@initech.test", "X-Org-Id": "initech"}

	for _, tc := range []struct {
		what, method, path string
		hdr                map[string]string
		want               int
	}{
		{"no org, naming an org that holds the flag", http.MethodGet, "/v1/worlds/runs?org=acme", nil, http.StatusOK},
		{"no org, naming one that does not", http.MethodGet, "/v1/worlds/runs?org=initech", nil, http.StatusNotFound},
		{"no org, naming none", http.MethodGet, "/v1/worlds/runs", nil, http.StatusNotFound},
		{"the key matched as the typed op matches it", http.MethodGet, "/v1/worlds/runs?ORG=acme", nil, http.StatusOK},
		{"naming two orgs", http.MethodGet, "/v1/worlds/runs?org=acme&ORG=initech", nil, http.StatusNotFound},
		{"a name that is no org", http.MethodGet, "/v1/worlds/runs?org=ac%20me", nil, http.StatusNotFound},
		{"an address worlds does not open", http.MethodGet, "/v1/worlds/totals?org=acme", nil, http.StatusNotFound},
		{"writing to the open address", http.MethodPost, "/v1/worlds/runs?org=acme", nil, http.StatusNotFound},
		{"a member of an org without the flag, naming one with it", http.MethodGet, "/v1/worlds/runs?org=acme", initech, http.StatusNotFound},
		{"a member of an org with it", http.MethodGet, "/v1/worlds/runs?org=initech", member, http.StatusOK},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		for k, v := range tc.hdr {
			req.Header.Set(k, v)
		}
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("%s: %s %s = %d, want %d", tc.what, tc.method, tc.path, resp.StatusCode, tc.want)
		}
	}
}

// The refusal reads the path as the router matches it. The router folds case and
// drops a trailing slash, so /V1/WORLDS/RUNS reaches the handler /v1/worlds/runs
// does, and a refusal that compared the raw spelling let any org into a staged
// capability by capitalizing one letter.
func TestTheRefusalReadsThePathAsTheRouterDoes(t *testing.T) {
	stageOpen(t, "worlds", "/v1/worlds/runs")
	serveFlags(t, map[string][]string{"acme": {"worlds"}})
	app := staged("worlds")
	initech := map[string]string{"X-User-Id": "initech/ceo@initech.test", "X-Org-Id": "initech"}

	for _, path := range []string{"/v1/Worlds/runs", "/V1/WORLDS/RUNS", "/v1/worlds/runs/", "/v1/Worlds/runs?org=acme"} {
		if code, body := fetch(t, app, path, initech); code != http.StatusNotFound {
			t.Errorf("GET %s as an org without the flag = %d %s, want 404", path, code, body)
		}
		if code, body := fetch(t, app, path, member); code != http.StatusOK {
			t.Errorf("GET %s as an org with it = %d %s, want 200", path, code, body)
		}
	}
	if code, body := fetch(t, app, "/V1/WORLDS/RUNS?org=acme", nil); code != http.StatusOK {
		t.Errorf("the open read spelled in capitals = %d %s, want 200", code, body)
	}
}

// The open read is what a page on hanzo.ai fetches with no credential, so every
// answer carries CORS for that origin: the runs, and the 404 a browser has to be
// able to read too. The edge sits in front of the refusal as it does in App.
func TestAnOpenReadAnswersTheSiteWithCORS(t *testing.T) {
	stageOpen(t, "worlds", "/v1/worlds/runs")
	serveFlags(t, map[string][]string{"acme": {"worlds"}})
	pol, _ := edge.New("", "admin", edge.Policy{CORSOrigins: []string{"hanzo.ai"}}) // no dataDir: the static policy alone
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Use(host.EdgeCORS(pol, nil))
	app.Use(cloud.Stage("worlds"))
	app.Raw(http.MethodGet, "/v1/worlds/runs", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"served": c.Path()}) })

	for org, want := range map[string]int{"acme": http.StatusOK, "initech": http.StatusNotFound} {
		req := httptest.NewRequest(http.MethodGet, "/v1/worlds/runs?org="+org, nil)
		req.Header.Set("Origin", "https://hanzo.ai")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("?org=%s = %d, want %d", org, resp.StatusCode, want)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://hanzo.ai" {
			t.Errorf("?org=%s Access-Control-Allow-Origin = %q, want https://hanzo.ai", org, got)
		}
		if got := resp.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("?org=%s Access-Control-Allow-Credentials = %q, want true", org, got)
		}
		if got := resp.Header.Get("Vary"); !strings.Contains(got, "Origin") {
			t.Errorf("?org=%s Vary = %q, want it to name Origin", org, got)
		}
	}
}

// stageOpen gives one staged row an Open list for the length of a test, since
// research graduated to ga and no live alpha row opens a public read today.
func stageOpen(t *testing.T, name string, open ...string) {
	t.Helper()
	prev := manifest.Apps
	apps := slices.Clone(prev)
	for i := range apps {
		if apps[i].Name == name {
			apps[i].Open = open
		}
	}
	manifest.Apps = apps
	t.Cleanup(func() { manifest.Apps = prev })
}

// restage gives one fleet row another stage for the length of a test. The fleet
// stages no row beta today, so the beta refusal is driven over an alpha row.
func restage(t *testing.T, name, stage string) {
	t.Helper()
	prev := manifest.Apps
	apps := slices.Clone(prev)
	for i := range apps {
		if apps[i].Name == name {
			apps[i].Stage = stage
		}
	}
	manifest.Apps = apps
	t.Cleanup(func() { manifest.Apps = prev })
}

// serveSwitch publishes both flag reads on the plane: the beta switch, on for the
// orgs in beta and asked about the validated principal exactly as apps/flags asks
// it, and the per-capability hold, held by the orgs in held.
func serveSwitch(t *testing.T, beta map[string]bool, held map[string][]string) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	app := zip.New(zip.Config{AppName: "flag"})
	app.Post("/flags/on", func(ctx context.Context, in *client.FlagIn) (*client.Flag, error) {
		org, err := principal.Acting(ctx)
		if err != nil {
			return nil, err
		}
		return &client.Flag{On: in.Key == client.Beta && beta[org]}, nil
	}, zip.WithOperationID(client.FlagsOn))
	app.Post("/flags/hold", func(ctx context.Context, in *client.FlagIn) (*client.Flag, error) {
		return &client.Flag{On: slices.Contains(held[cloud.Who(ctx).Org], in.Key)}, nil
	}, zip.WithOperationID(client.FlagsHold))
	go func() { _ = app.Listen(zip.SocketPath("flag")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitFor(t, "flag")
}

// A BETA CAPABILITY ANSWERS WHOEVER THE BETA SWITCH IS ON FOR, and nobody else:
// not an org that holds a flag named for the capability (that is how an alpha is
// entered, never a beta), not a forged org header, and not anyone while the
// switch cannot be asked.
func TestABetaCapabilityAnswersTheBetaSwitch(t *testing.T) {
	restage(t, "worlds", manifest.Beta)
	serveSwitch(t, map[string]bool{"acme": true}, map[string][]string{"initech": {"worlds"}})
	app := staged("worlds")

	if code, body := fetch(t, app, "/v1/worlds/runs", member); code != http.StatusOK {
		t.Fatalf("acme, whom the beta switch is on for, = %d %s, want 200", code, body)
	}
	initech := map[string]string{"X-User-Id": "initech/ceo@initech.test", "X-Org-Id": "initech"}
	if code, _ := fetch(t, app, "/v1/worlds/runs", initech); code != http.StatusNotFound {
		t.Errorf("initech, holding a flag named worlds and no beta, = %d, want 404", code)
	}
	if code, _ := fetch(t, app, "/v1/worlds/runs", map[string]string{"X-Org-Id": "acme"}); code != http.StatusNotFound {
		t.Errorf("a forged X-Org-Id: acme = %d, want 404", code)
	}

	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t)) // nothing listens here
	if code, _ := fetch(t, app, "/v1/worlds/runs", member); code != http.StatusNotFound {
		t.Errorf("acme with no switch to ask = %d, want 404", code)
	}
}

func waitFor(t *testing.T, app string) {
	t.Helper()
	for range 200 {
		if c, derr := net.Dial("unix", zip.SocketPath(app)); derr == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never began listening", zip.SocketPath(app))
}
