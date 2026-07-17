package main

// The drift guard for GET /v1/openapi.json.
//
// A spec that CAN drift is worthless, so the value here is not "the generator
// runs" — it is that the document and the live router are pinned to each other
// in BOTH directions over the real, fully-mounted binary (every subsystem in
// apps.Wire(), not a hand-picked subset):
//
//	forward — every live route appears as an operation (nothing silently dropped
//	          by a translation bug), and
//	reverse — every operation is backed by a live route (nothing invented).
//
// A route added, removed, renamed, or re-parameterized anywhere in the fleet
// moves both sides together and stays green. A generator bug moves only one and
// fails here.

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// fullyMountedApp assembles the REAL binary: every subsystem apps.Wire() lists,
// mounted through the same cloud.MountAll the server uses. Staged subsystems are
// opt-in (cfg.Enabled), so this is the default production surface.
//
// Mounted exactly ONCE per process, and shared. Mounting the full Wire() is not
// a pure act: subsystems bind real ports (git starts an SSH listener on :2222,
// pubsub an embedded NATS, kafka its adaptor) and those listeners outlive a
// single test, so a second full mount fails with "address already in use". The
// route table is immutable after mount and every test here only reads it, so one
// app is also the honest shape — each test observes the same router the server
// would serve.
//
// The openapi endpoint is registered here too: it is itself a route the document
// must name.
var (
	mountOnce sync.Once
	mountedTo *zip.App
	mountErr  error
)

func fullyMountedApp(t *testing.T) *zip.App {
	t.Helper()
	mountOnce.Do(func() {
		dir, err := os.MkdirTemp("", "openapi-spec-*")
		if err != nil {
			mountErr = err
			return
		}
		cfg := &cloud.Config{Brand: "hanzo", Domain: "api.hanzo.ai", DataDir: dir}
		deps := cloud.BuildDeps(cfg)
		app := zip.New(zip.Config{Logger: deps.Logger, DisableStartupMessage: true})
		if mountErr = cloud.MountAll(app, apps.Wire(), cfg, deps); mountErr != nil {
			return
		}
		openapi.Mount(app, testInfo(), openapi.Server{URL: "https://api.hanzo.ai"})
		mountedTo = app
	})
	if mountErr != nil {
		t.Fatalf("MountAll(apps.Wire()): %v", mountErr)
	}
	return mountedTo
}

func testInfo() openapi.Info {
	return openapi.Info{Title: "hanzo cloud API", Version: "test"}
}

// The bijection. This is the drift test.
func TestSpecDoesNotDriftFromLiveRouter(t *testing.T) {
	if testing.Short() {
		t.Skip("mounts every subsystem in apps.Wire(); slow by construction")
	}
	app := fullyMountedApp(t)
	doc, err := openapi.Spec(app, testInfo())
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}

	// What the router says, as "METHOD /open/api/path" keys.
	live := map[string]bool{}
	for _, r := range openapi.Live(app) {
		p, _ := openapiPath(r.Path)
		live[r.Method+" "+p] = true
	}

	// What the document says.
	spec := map[string]bool{}
	for path, item := range doc.Paths {
		for method := range item {
			spec[strings.ToUpper(method)+" "+path] = true
		}
	}

	// forward: no live route missing from the document.
	var missing []string
	for k := range live {
		if !spec[k] {
			missing = append(missing, k)
		}
	}
	// reverse: no operation without a live route.
	var invented []string
	for k := range spec {
		if !live[k] {
			invented = append(invented, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(invented)

	if len(missing) > 0 {
		t.Errorf("%d live route(s) MISSING from the spec (the router serves them; the document denies them):\n  %s",
			len(missing), strings.Join(head(missing, 20), "\n  "))
	}
	if len(invented) > 0 {
		t.Errorf("%d spec operation(s) with NO live route (the document invents them):\n  %s",
			len(invented), strings.Join(head(invented, 20), "\n  "))
	}
	if len(live) == 0 {
		t.Fatal("no live routes — the harness mounted nothing, so this guard proves nothing")
	}
	t.Logf("bijection holds over %d operations / %d paths / %d products",
		len(live), len(doc.Paths), len(doc.Tags))
}

// The document must be reachable and well-formed over the wire, unauthenticated
// — the CLI fetches it before any login exists.
func TestOpenAPIEndpointServesUnauthenticated(t *testing.T) {
	if testing.Short() {
		t.Skip("mounts every subsystem in apps.Wire(); slow by construction")
	}
	app := fullyMountedApp(t)

	// No Authorization header, no cookie, no gateway-minted identity.
	resp, err := app.Fiber().Test(httptest.NewRequest("GET", openapi.Path, nil))
	if err != nil {
		t.Fatalf("Test(%s): %v", openapi.Path, err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s = %d, want 200 unauthenticated", openapi.Path, resp.StatusCode)
	}

	var doc openapi.Document
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("served document is not valid JSON: %v", err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", doc.OpenAPI)
	}
	// It must describe itself: proof the lazy build read the final table.
	if _, ok := doc.Paths[openapi.Path]; !ok {
		t.Errorf("document omits its own endpoint %s", openapi.Path)
	}
	// Real operations from real products.
	for _, want := range []string{"/v1/kms/health", "/v1/billing/usage"} {
		if _, ok := doc.Paths[want]; !ok {
			t.Errorf("document omits %s, which the binary serves", want)
		}
	}
}

// Every operation carries its product tag, and the tag is the first /v1/
// segment — the axis the CLI builds `hanzo <product> ...` from. Routes outside
// /v1 legitimately have none.
func TestEveryV1OperationIsTaggedWithItsProduct(t *testing.T) {
	if testing.Short() {
		t.Skip("mounts every subsystem in apps.Wire(); slow by construction")
	}
	app := fullyMountedApp(t)
	doc, err := openapi.Spec(app, testInfo())
	if err != nil {
		t.Fatalf("Spec: %v", err)
	}

	declared := map[string]bool{}
	for _, tag := range doc.Tags {
		declared[tag.Name] = true
	}

	for path, item := range doc.Paths {
		want := openapi.Product(fiberPath(path))
		for method, op := range item {
			switch {
			case want == "":
				if len(op.Tags) != 0 {
					t.Errorf("%s %s: tagged %v but has no product", method, path, op.Tags)
				}
			case len(op.Tags) != 1 || op.Tags[0] != want:
				t.Errorf("%s %s: tags = %v, want [%s]", method, path, op.Tags, want)
			case !declared[want]:
				t.Errorf("%s %s: product %q is not in the document's tag list", method, path, want)
			}
		}
	}
}

// openapiPath converts a fiber pattern to its OpenAPI template, mirroring the
// generator's own translation. It is intentionally a SEPARATE implementation:
// reusing the generator's would make the drift test agree with a translation bug
// instead of catching it.
func openapiPath(pattern string) (string, []string) {
	segs := strings.Split(pattern, "/")
	var params []string
	stars := 0
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, ":"):
			name := strings.TrimSuffix(strings.TrimPrefix(s, ":"), "?")
			segs[i] = "{" + name + "}"
			params = append(params, name)
		case s == "*" || s == "+":
			stars++
			name := "wildcard" + strconv.Itoa(stars)
			segs[i] = "{" + name + "}"
			params = append(params, name)
		}
	}
	return strings.Join(segs, "/"), params
}

// fiberPath is openapiPath's inverse for product lookup: {x} → :x. Product only
// reads the first /v1/ segment, which is never a param on a real product route,
// so this is exact for the purpose.
func fiberPath(path string) string {
	r := strings.NewReplacer("{", ":", "}", "")
	return r.Replace(path)
}

func head(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return append(s[:n:n], "... +"+strconv.Itoa(len(s)-n)+" more")
}
