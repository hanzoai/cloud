package ingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "ingress.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func testSvc(t *testing.T) *cloud.Service[state] {
	t.Helper()
	return &cloud.Service[state]{
		Base:  cloud.NewBase(cloud.Deps{Logger: luxlog.NewNoOpLogger()}, "ingress"),
		State: state{store: testStore(t), engine: newEngine(luxlog.NewNoOpLogger())},
	}
}

// applyOne is a test shortcut: compile a single route/service(/middlewares) into
// the engine.
func applyOne(e *Engine, r Route, svcs []Service, mws []Middleware) (int, int) {
	sm := map[string]Service{}
	for _, s := range svcs {
		sm[s.ID] = s
	}
	mm := map[string]Middleware{}
	for _, m := range mws {
		mm[m.ID] = m
	}
	tls := map[string]struct{}{}
	if r.TLS {
		tls[normalizeHost(r.Host)] = struct{}{}
	}
	return e.apply([]Route{r}, sm, mm, tls)
}

// ── store ─────────────────────────────────────────────────────────────────────

func TestStoreCRUDAndTenantIsolation(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	if err := st.Put(ctx, "orgA", KindService, "s1", `{"id":"s1"}`, "", 1); err != nil {
		t.Fatalf("put: %v", err)
	}
	doc, found, err := st.Get(ctx, "orgA", KindService, "s1")
	if err != nil || !found || doc != `{"id":"s1"}` {
		t.Fatalf("get: doc=%q found=%v err=%v", doc, found, err)
	}
	// Another org cannot see orgA's object.
	if _, found, _ := st.Get(ctx, "orgB", KindService, "s1"); found {
		t.Fatal("tenant isolation breach: orgB read orgA object")
	}
	if _, err := st.Delete(ctx, "orgA", KindService, "s1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found, _ := st.Get(ctx, "orgA", KindService, "s1"); found {
		t.Fatal("object still present after delete")
	}
}

func TestStoreHostUniquenessAcrossOrgs(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	if err := st.Put(ctx, "orgA", KindRoute, "r1", `{"id":"r1"}`, "app.test", 1); err != nil {
		t.Fatalf("orgA put: %v", err)
	}
	// A different org claiming the same host must be rejected.
	if err := st.Put(ctx, "orgB", KindRoute, "r2", `{"id":"r2"}`, "app.test", 2); err != ErrHostTaken {
		t.Fatalf("expected ErrHostTaken, got %v", err)
	}
	// The SAME route updating itself (same org+id) keeping its host is fine.
	if err := st.Put(ctx, "orgA", KindRoute, "r1", `{"id":"r1","x":1}`, "app.test", 3); err != nil {
		t.Fatalf("self-update: %v", err)
	}
}

// ── engine routing / proxy ────────────────────────────────────────────────────

func TestEngineRoutesByHostToBackend(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hit:" + r.URL.Path))
	}))
	defer backend.Close()

	e := newEngine(luxlog.NewNoOpLogger())
	svc := Service{ID: "s1", Backends: []Backend{{URL: backend.URL}}}
	live, skipped := applyOne(e, Route{ID: "r1", Host: "app.test", Service: "s1"}, []Service{svc}, nil)
	if live != 1 || skipped != 0 {
		t.Fatalf("apply: live=%d skipped=%d", live, skipped)
	}

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "http://app.test/hello", nil))
	if rec.Code != 200 || rec.Body.String() != "hit:/hello" {
		t.Fatalf("proxy: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Unknown host → 404.
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "http://nope.test/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown host: code=%d", rec.Code)
	}
}

func TestEngineSkipsRouteWithUnknownService(t *testing.T) {
	e := newEngine(luxlog.NewNoOpLogger())
	live, skipped := applyOne(e, Route{ID: "r1", Host: "app.test", Service: "missing"}, nil, nil)
	if live != 0 || skipped != 1 {
		t.Fatalf("expected route skipped: live=%d skipped=%d", live, skipped)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "http://app.test/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for skipped route, got %d", rec.Code)
	}
}

// ── middleware ────────────────────────────────────────────────────────────────

func TestStripPrefixMiddleware(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	defer backend.Close()

	e := newEngine(luxlog.NewNoOpLogger())
	svc := Service{ID: "s1", Backends: []Backend{{URL: backend.URL}}}
	mw := Middleware{ID: "strip", Type: MWStripPrefix, Config: map[string]string{"prefixes": "/api"}}
	route := Route{ID: "r1", Host: "app.test", Service: "s1", Middlewares: []string{"strip"}}
	if live, _ := applyOne(e, route, []Service{svc}, []Middleware{mw}); live != 1 {
		t.Fatalf("apply live=%d", live)
	}

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "http://app.test/api/users?q=1", nil))
	if rec.Body.String() != "/users" {
		t.Fatalf("stripPrefix: backend saw %q, want /users", rec.Body.String())
	}
}

func TestRedirectSchemeMiddleware(t *testing.T) {
	e := newEngine(luxlog.NewNoOpLogger())
	svc := Service{ID: "s1", Backends: []Backend{{URL: "http://127.0.0.1:1"}}} // never reached
	mw := Middleware{ID: "https", Type: MWRedirectScheme, Config: map[string]string{"scheme": "https", "permanent": "true"}}
	route := Route{ID: "r1", Host: "app.test", Service: "s1", Middlewares: []string{"https"}}
	applyOne(e, route, []Service{svc}, []Middleware{mw})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "http://app.test/x?y=1", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("redirect code=%d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://app.test/x?y=1" {
		t.Fatalf("redirect Location=%q", loc)
	}
}

func TestHeadersMiddleware(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()

	e := newEngine(luxlog.NewNoOpLogger())
	svc := Service{ID: "s1", Backends: []Backend{{URL: backend.URL}}}
	mw := Middleware{ID: "sec", Type: MWHeaders, Config: map[string]string{"X-Frame-Options": "DENY"}}
	route := Route{ID: "r1", Host: "app.test", Service: "s1", Middlewares: []string{"sec"}}
	applyOne(e, route, []Service{svc}, []Middleware{mw})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "http://app.test/", nil))
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("headers middleware: X-Frame-Options=%q", got)
	}
}

// ── TLS host policy ───────────────────────────────────────────────────────────

func TestTLSHostPolicy(t *testing.T) {
	e := newEngine(luxlog.NewNoOpLogger())
	svc := Service{ID: "s1", Backends: []Backend{{URL: "http://127.0.0.1:1"}}}
	applyOne(e, Route{ID: "r1", Host: "secure.test", Service: "s1", TLS: true}, []Service{svc}, nil)

	if !e.TLSHost("secure.test") {
		t.Fatal("TLSHost(secure.test) = false, want true")
	}
	if !e.TLSHost("secure.test:443") { // port-stripped
		t.Fatal("TLSHost(secure.test:443) = false, want true")
	}
	if e.TLSHost("other.test") {
		t.Fatal("TLSHost(other.test) = true, want false (arbitrary SNI must not get a cert)")
	}
}

func TestReloadBuildsTLSHostSet(t *testing.T) {
	s := testSvc(t)
	ctx := context.Background()

	// A TLS route + an org-level extraHost.
	route := Route{ID: "r1", Host: "a.test", Service: "s1", TLS: true}
	if err := s.State.store.Put(ctx, "admin", KindRoute, "r1", mustJSON(route), "a.test", 1); err != nil {
		t.Fatalf("put route: %v", err)
	}
	if err := s.State.store.PutTLS(ctx, "admin", mustJSON(TLSConfig{ExtraHosts: []string{"b.test"}}), 1); err != nil {
		t.Fatalf("put tls: %v", err)
	}
	set, err := tlsHostSet(s, ctx)
	if err != nil {
		t.Fatalf("tlsHostSet: %v", err)
	}
	if _, ok := set["a.test"]; !ok {
		t.Fatal("route TLS host a.test missing from set")
	}
	if _, ok := set["b.test"]; !ok {
		t.Fatal("extraHost b.test missing from set")
	}
}

// ── validation ────────────────────────────────────────────────────────────────

func TestValidation(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
		fn   func() error
	}{
		{"good route", true, (&Route{ID: "r1", Host: "app.test", Service: "s1"}).validate},
		{"bad host", false, (&Route{ID: "r1", Host: "app test/x", Service: "s1"}).validate},
		{"empty service ref", false, (&Route{ID: "r1", Host: "app.test", Service: ""}).validate},
		{"good service", true, (&Service{ID: "s1", Backends: []Backend{{URL: "http://h:8000"}}}).validate},
		{"no backends", false, (&Service{ID: "s1"}).validate},
		{"bad backend url", false, (&Service{ID: "s1", Backends: []Backend{{URL: "notaurl"}}}).validate},
		{"good middleware", true, (&Middleware{ID: "m1", Type: MWAddPrefix, Config: map[string]string{"prefix": "/x"}}).validate},
		{"unknown middleware type", false, (&Middleware{ID: "m1", Type: "bogus"}).validate},
		{"stripPrefix missing config", false, (&Middleware{ID: "m1", Type: MWStripPrefix}).validate},
	}
	for _, c := range cases {
		err := c.fn()
		if c.ok && err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
	}
}

// ── ordering ──────────────────────────────────────────────────────────────────

func TestPathPrefixAndPriorityOrdering(t *testing.T) {
	general := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("general"))
	}))
	defer general.Close()
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("api"))
	}))
	defer apiSrv.Close()

	e := newEngine(luxlog.NewNoOpLogger())
	svcs := map[string]Service{
		"general": {ID: "general", Backends: []Backend{{URL: general.URL}}},
		"api":     {ID: "api", Backends: []Backend{{URL: apiSrv.URL}}},
	}
	routes := []Route{
		{ID: "r-general", Host: "app.test", Service: "general"},             // catch-all
		{ID: "r-api", Host: "app.test", PathPrefix: "/api", Service: "api"}, // more specific
	}
	e.apply(routes, svcs, nil, nil)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "http://app.test/api/x", nil))
	if rec.Body.String() != "api" {
		t.Fatalf("/api/x routed to %q, want api (longer prefix must win)", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest("GET", "http://app.test/home", nil))
	if rec.Body.String() != "general" {
		t.Fatalf("/home routed to %q, want general", rec.Body.String())
	}
}
