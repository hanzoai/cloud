package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/goa"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// The whole point of these tests is the SPLIT: Mount checks the manifest and
// registers routes, the first request builds the plugin. Everything below pins
// one half of that against the other.
//
// The build counter is the loader's own log. "plugin loaded" is written exactly
// once per successful build, inside the lock that makes the build singular, so
// counting that line counts builds — no test seam in the production path.

// logs collects a logger's output. Written from many request goroutines at
// once, so it holds a lock.
type logs struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logs) count(msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Count(l.b.String(), msg)
}

// builds is how many plugins have actually been built so far.
func (l *logs) builds() int { return l.count("plugin loaded") }

// failures is how many build ATTEMPTS have failed. It counts attempts, not
// plugins: a plugin that fails twice appears twice, which is what proves a
// failed build is retried rather than remembered.
func (l *logs) failures() int { return l.count("plugin build failed") }

// mount writes man to a fresh temp dir, points CLOUD_PLUGINS at it and mounts
// it. The returned dir is the manifest's base dir, so a test can drop (or
// withhold) a source file next to it.
func mount(t *testing.T, man Manifest) (*zip.App, *logs, string) {
	t.Helper()
	dir := t.TempDir()
	b, err := json.Marshal(man)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	path := filepath.Join(dir, "plugins.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	t.Setenv("CLOUD_PLUGINS", path)

	app, out := newApp(t)
	if err := Mount(app, cloud.Deps{Logger: luxlog.NewWriter(out), Brand: "hanzo"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app, out, dir
}

// newApp returns a bare app plus the sink its logs land in, and guarantees the
// package's global mount state is released when the test ends.
func newApp(t *testing.T) (*zip.App, *logs) {
	t.Helper()
	out := &logs{}
	app := zip.New(zip.Config{
		AppName:               "plugin-test",
		Logger:                luxlog.NewWriter(out),
		DisableStartupMessage: true,
	})
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app, out
}

// try runs one request. It returns an error rather than failing the test so it
// can be called from request goroutines, where t.Fatal is illegal.
func try(app *zip.App, method, path, body string) (int, string, error) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Generous timeout: the first request through a prefix compiles a wasm
	// module, and the rest of the burst waits on it.
	resp, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		return 0, "", fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	return resp.StatusCode, string(b), nil
}

func do(t *testing.T, app *zip.App, method, path, body string) (int, string) {
	t.Helper()
	code, out, err := try(app, method, path, body)
	if err != nil {
		t.Fatal(err)
	}
	return code, out
}

// loaded reads /v1/plugins and returns each plugin's Loaded flag by name.
func loaded(t *testing.T, app *zip.App) map[string]bool {
	t.Helper()
	code, body := do(t, app, "GET", "/v1/plugins", "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/plugins = %d: %s", code, body)
	}
	var out struct {
		Plugins []view `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	m := make(map[string]bool, len(out.Plugins))
	for _, v := range out.Plugins {
		m[v.Name] = v.Loaded
	}
	return m
}

// echoPlugin is the goa Rust guest in testdata: POST <prefix>/echo returns its
// own JSON body back under "echo".
func echoPlugin(name, prefix, source string) Plugin {
	return Plugin{
		Name: name, Kind: "wasm", Lang: "rust", Source: source, Pool: 1, Prefix: prefix,
		Routes: []goa.Route{{Method: "POST", Path: "/echo", Func: "echo"}},
	}
}

// dropSource puts the compiled echo guest where the manifest says it is. Tests
// that want an unbuildable plugin simply never call it.
func dropSource(t *testing.T, dir, name string) {
	t.Helper()
	b, err := os.ReadFile("testdata/echo.wasm")
	if err != nil {
		t.Fatalf("read testdata/echo.wasm: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// An unset manifest mounts nothing at all — not even the introspection route.
// cloud has to run with zero plugins, so this is the default, not a fallback.
func TestMountWithoutManifestMountsNothing(t *testing.T) {
	t.Setenv("CLOUD_PLUGINS", "")
	app, out := newApp(t)
	if err := Mount(app, cloud.Deps{Logger: luxlog.NewWriter(out), Brand: "hanzo"}); err != nil {
		t.Fatalf("Mount with no manifest: %v", err)
	}
	if code, _ := do(t, app, "GET", "/v1/plugins", ""); code != http.StatusNotFound {
		t.Fatalf("GET /v1/plugins = %d with no manifest; nothing should be mounted", code)
	}
}

// The headline: a plugin whose source does not exist still mounts, still boots,
// and only tells the truth when someone asks for it.
func TestMountDefersBuildOfMissingSource(t *testing.T) {
	app, out, _ := mount(t, Manifest{Plugins: []Plugin{echoPlugin("ghost", "/v1/ghost", "ghost.wasm")}})

	if n := out.builds(); n != 0 {
		t.Fatalf("Mount built %d plugins; a lazy mount builds none", n)
	}
	if loaded(t, app)["ghost"] {
		t.Fatal("/v1/plugins reports ghost loaded before any request reached it")
	}

	code, body := do(t, app, "POST", "/v1/ghost/echo", `{"a":1}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("first request to a broken plugin = %d, want 503: %s", code, body)
	}
	// The 503 has to name the plugin AND the reason, or an operator learns
	// nothing from it.
	for _, want := range []string{"ghost", "ghost.wasm"} {
		if !strings.Contains(body, want) {
			t.Fatalf("503 body %q does not mention %q", body, want)
		}
	}
	if loaded(t, app)["ghost"] {
		t.Fatal("a FAILED build must not count as loaded")
	}
	if n := out.builds(); n != 0 {
		t.Fatalf("builds = %d after a failed build, want 0", n)
	}
}

// A failed build is retried, so a plugin whose source lands after boot starts
// working on the next request — no restart. This is the documented choice, so
// it gets a test.
func TestFailedBuildIsRetriedNotRemembered(t *testing.T) {
	app, out, dir := mount(t, Manifest{Plugins: []Plugin{echoPlugin("late", "/v1/late", "late.wasm")}})

	if code, _ := do(t, app, "POST", "/v1/late/echo", `{"a":1}`); code != http.StatusServiceUnavailable {
		t.Fatalf("request before the source exists = %d, want 503", code)
	}
	if code, _ := do(t, app, "POST", "/v1/late/echo", `{"a":1}`); code != http.StatusServiceUnavailable {
		t.Fatalf("second request before the source exists = %d, want 503", code)
	}
	if n := out.failures(); n != 2 {
		t.Fatalf("failed build attempts = %d after 2 requests, want 2 (the failure is not cached)", n)
	}

	dropSource(t, dir, "late.wasm")

	code, body := do(t, app, "POST", "/v1/late/echo", `{"name":"ada"}`)
	if code != http.StatusOK {
		t.Fatalf("request after the source arrived = %d, want 200: %s", code, body)
	}
	if !strings.Contains(body, `"ada"`) {
		t.Fatalf("echo body = %s, want the request echoed back", body)
	}
	if n := out.builds(); n != 1 {
		t.Fatalf("builds = %d, want 1", n)
	}
	if !loaded(t, app)["late"] {
		t.Fatal("/v1/plugins still reports late unloaded after it served a request")
	}
}

// Zero builds after Mount, exactly one build after a burst of simultaneous
// first requests — and every request in the burst is served by it.
func TestConcurrentFirstRequestsBuildOnce(t *testing.T) {
	p := echoPlugin("echo", "/v1/echo", "echo.wasm")
	// A pool this size takes tens of milliseconds to build, while the burst
	// below lands within about one. That margin is what makes the requests
	// genuinely simultaneous: with a pool of 1 they would queue up behind a
	// build that had already finished, and the test would pass on a build that
	// is merely fast rather than singular. The margin is also scale-invariant —
	// a slower machine slows the arrivals and the build alike.
	p.Pool = 64
	app, out, dir := mount(t, Manifest{Plugins: []Plugin{p}})
	dropSource(t, dir, "echo.wasm")

	if n := out.builds(); n != 0 {
		t.Fatalf("builds = %d after Mount, want 0", n)
	}
	// Warm fiber's route tree on an unrelated path, so the burst races on the
	// plugin build and nothing else.
	loaded(t, app)

	const n = 16
	codes := make([]int, n)
	bodies := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i], bodies[i], errs[i] = try(app, "POST", "/v1/echo/echo", `{"name":"ada"}`)
		}(i)
	}
	close(start) // every goroutine is already parked here: they go together
	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if codes[i] != http.StatusOK {
			t.Fatalf("request %d = %d, want 200: %s", i, codes[i], bodies[i])
		}
		if !strings.Contains(bodies[i], `"ada"`) {
			t.Fatalf("request %d body = %s, want the request echoed back", i, bodies[i])
		}
	}
	if got := out.builds(); got != 1 {
		t.Fatalf("builds = %d after %d concurrent first requests, want exactly 1", got, n)
	}
	if !loaded(t, app)["echo"] {
		t.Fatal("/v1/plugins reports echo unloaded after it served 16 requests")
	}
}

// Shutdown releases what was built and leaves alone what was not. The built
// plugin's pool must actually be closed — not merely forgotten.
func TestShutdownReleasesOnlyWhatWasBuilt(t *testing.T) {
	app, _, dir := mount(t, Manifest{Plugins: []Plugin{
		echoPlugin("used", "/v1/used", "echo.wasm"),
		echoPlugin("idle", "/v1/idle", "echo.wasm"),
	}})
	dropSource(t, dir, "echo.wasm")

	if code, body := do(t, app, "POST", "/v1/used/echo", `{"name":"ada"}`); code != http.StatusOK {
		t.Fatalf("POST /v1/used/echo = %d: %s", code, body)
	}

	used, idle := svcOf(t, "used"), svcOf(t, "idle")
	if used == nil {
		t.Fatal("the requested plugin built no service")
	}
	if idle != nil {
		t.Fatal("a plugin nobody requested built a service")
	}

	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// The pool it held is really gone: its interpreters no longer answer.
	if _, err := used.Pool.Invoke(context.Background(), "echo", []byte(`{}`)); err == nil {
		t.Fatal("the built plugin's pool still serves after Shutdown; it was not released")
	}
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

// Shutdown with nothing built at all is a no-op, not a crash — the ordinary
// case for a deployment whose plugins nobody called.
func TestShutdownWithNothingBuilt(t *testing.T) {
	_, _, _ = mount(t, Manifest{Plugins: []Plugin{echoPlugin("idle", "/v1/idle", "echo.wasm")}})
	if svcOf(t, "idle") != nil {
		t.Fatal("nothing was requested, so nothing should have been built")
	}
	if err := Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown with nothing built: %v", err)
	}
}

// svcOf returns the goa service a mounted plugin built, or nil if it never did.
func svcOf(t *testing.T, name string) *goa.Service {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	for _, l := range mounted {
		if l.p.Name != name {
			continue
		}
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.svc
	}
	t.Fatalf("plugin %q is not mounted", name)
	return nil
}

// Laziness is not an excuse to stop reading the manifest. A typo is a fact that
// is wrong whenever it is read, so it still kills boot.
func TestManifestTyposStillFailAtBoot(t *testing.T) {
	cases := []struct {
		name string
		p    Plugin
		want string
	}{
		{"unknown kind", Plugin{Name: "x", Kind: "quantum", Prefix: "/v1/x"}, "unknown kind"},
		{"missing target", Plugin{Name: "x", Kind: "proxy", Prefix: "/v1/x"}, "needs a target"},
		{"missing prefix", Plugin{Name: "x", Kind: "proxy", Target: "http://x.internal"}, "prefix"},
		{"relative prefix", Plugin{Name: "x", Kind: "proxy", Prefix: "v1/x", Target: "http://x.internal"}, "prefix"},
		{"missing source", Plugin{Name: "x", Kind: "wasm", Prefix: "/v1/x"}, "needs a source"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			b, err := json.Marshal(Manifest{Plugins: []Plugin{tc.p}})
			if err != nil {
				t.Fatalf("marshal manifest: %v", err)
			}
			path := filepath.Join(dir, "plugins.json")
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			t.Setenv("CLOUD_PLUGINS", path)

			app, out := newApp(t)
			err = Mount(app, cloud.Deps{Logger: luxlog.NewWriter(out), Brand: "hanzo"})
			if err == nil {
				t.Fatal("Mount accepted a broken manifest entry")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `"x"`) {
				t.Fatalf("Mount error %q must name the plugin and mention %q", err, tc.want)
			}
		})
	}
}

// roundTrip lets a test register a transport without a listener.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The proxy kind is lazy on exactly the same terms as wasm: routed at boot,
// built on first use.
func TestProxyPluginIsLazy(t *testing.T) {
	RegisterTransport("test-proxy", roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"path":"` + r.URL.Path + `"}`)),
		}, nil
	}))
	app, out, _ := mount(t, Manifest{Plugins: []Plugin{{
		Name: "prox", Kind: "proxy", Prefix: "/v1/prox",
		Target: "http://prox.internal", Via: "test-proxy",
	}}})

	if loaded(t, app)["prox"] {
		t.Fatal("/v1/plugins reports prox loaded before any request reached it")
	}
	code, body := do(t, app, "GET", "/v1/prox/thing", "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/prox/thing = %d: %s", code, body)
	}
	if !strings.Contains(body, "/v1/prox/thing") {
		t.Fatalf("proxied body = %s, want the forwarded path", body)
	}
	if !loaded(t, app)["prox"] || out.builds() != 1 {
		t.Fatalf("after one request: loaded=%v builds=%d, want true/1", loaded(t, app)["prox"], out.builds())
	}
}

// A transport registers whenever its client package is linked in, which can be
// after Mount. That makes an unknown transport a condition of the moment, not a
// manifest typo: it mounts, and says so at 503.
func TestUnregisteredTransportFailsAtRequestNotBoot(t *testing.T) {
	app, _, _ := mount(t, Manifest{Plugins: []Plugin{{
		Name: "later", Kind: "proxy", Prefix: "/v1/later",
		Target: "http://later.internal", Via: "not-registered-yet",
	}}})

	code, body := do(t, app, "GET", "/v1/later/thing", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET through an unregistered transport = %d, want 503: %s", code, body)
	}
	if !strings.Contains(body, "later") || !strings.Contains(body, "not-registered-yet") {
		t.Fatalf("503 body %q must name the plugin and the missing transport", body)
	}
}
