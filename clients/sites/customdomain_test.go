package sites

import (
	"net/http/httptest"
	"testing"

	luxlog "github.com/luxfi/log"
)

// selfServer is a test Server whose self domains are hanzo.ai + hanzo.app — the
// exclusion set that keeps our own API/console hosts off the custom-domain path.
func selfServer() *Server {
	return New(Config{
		Apex:        "hanzo.app",
		Reserved:    []string{"app", "api", "admin"},
		SelfDomains: []string{"hanzo.ai", "hanzo.app"},
	}, luxlog.New("test"))
}

func TestCustomCandidate(t *testing.T) {
	s := selfServer()
	custom := []string{"yadota.tech", "www.yadota.tech", "example.com", "deep.sub.example.org"}
	for _, h := range custom {
		if !s.customCandidate(h) {
			t.Errorf("customCandidate(%q) = false, want true", h)
		}
	}
	notCustom := []string{"", "localhost", "hanzo.ai", "api.hanzo.ai", "console.hanzo.ai", "hanzo.app", "maxpower.hanzo.app"}
	for _, h := range notCustom {
		if s.customCandidate(h) {
			t.Errorf("customCandidate(%q) = true, want false", h)
		}
	}
}

// TestMiddlewareCustomDomainServed: a bound, LIVE custom domain enters the
// site-serving path (not passthrough) and is resolved by its FULL host. Storage
// is unconfigured in the test, so serving stops at 503 / an honest page — which
// still proves the ROUTING: the request never reached the sentinel and the
// resolver was asked for the full host, not a path- or header-derived key.
func TestMiddlewareCustomDomainServed(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{Org: "yadota", Slug: "yadota", Bucket: "hanzo-sites", Prefix: "yadota/yadota", Status: "live"}}
	SetResolver(fr)
	defer SetResolver(nil)
	app := newTestApp(selfServer())

	req := httptest.NewRequest("GET", "http://yadota.tech/", nil)
	req.Header.Set("X-Org-Id", "attacker-org") // must be ignored
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.Header.Get("X-Sentinel") == "hit" {
		t.Fatal("bound custom domain leaked into the normal API pipeline")
	}
	if resp.Header.Get("X-Hanzo-Site") != "yadota" {
		t.Errorf("X-Hanzo-Site = %q, want yadota", resp.Header.Get("X-Hanzo-Site"))
	}
	got := fr.slugs()
	if len(got) != 1 || got[0] != "yadota.tech" {
		t.Fatalf("resolver called with %v, want exactly [yadota.tech] (full host only)", got)
	}
}

// TestMiddlewareCustomDomainUnboundPassthrough: a host routed to this edge with NO
// binding must NOT 404 the API — it Continues to the normal pipeline.
func TestMiddlewareCustomDomainUnboundPassthrough(t *testing.T) {
	fr := &fakeResolver{found: false}
	SetResolver(fr)
	defer SetResolver(nil)
	app := newTestApp(selfServer())
	req := httptest.NewRequest("GET", "http://unbound.tech/", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.Header.Get("X-Sentinel") != "hit" {
		t.Fatalf("unbound custom host did not pass through (status %d)", resp.StatusCode)
	}
}

// TestMiddlewareSelfHostNeverResolved: a self host (api.hanzo.ai …) is never a
// custom-domain candidate — the resolver is never consulted and the request
// passes straight through. This is BOTH the anti-shadowing guarantee AND the
// proof that the high-traffic API path pays no per-request binding lookup.
func TestMiddlewareSelfHostNeverResolved(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{Org: "x", Slug: "x", Status: "live"}}
	SetResolver(fr)
	defer SetResolver(nil)
	app := newTestApp(selfServer())
	for _, host := range []string{"api.hanzo.ai", "console.hanzo.ai", "cloud.hanzo.ai", "hanzo.ai"} {
		req := httptest.NewRequest("GET", "http://"+host+"/v1/anything", nil)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("test %s: %v", host, err)
		}
		if resp.Header.Get("X-Sentinel") != "hit" {
			t.Errorf("self host %q was not passed through", host)
		}
	}
	if n := len(fr.slugs()); n != 0 {
		t.Fatalf("resolver consulted %d times for self hosts, want 0 (%v)", n, fr.slugs())
	}
}

// TestMiddlewareCustomDomainNotLivePassthrough: a bound but non-live site (draft/
// building) on a custom domain must NOT serve — it Continues (not yet public).
func TestMiddlewareCustomDomainNotLivePassthrough(t *testing.T) {
	fr := &fakeResolver{found: true, site: Site{Org: "yadota", Slug: "yadota", Status: "building"}}
	SetResolver(fr)
	defer SetResolver(nil)
	app := newTestApp(selfServer())
	req := httptest.NewRequest("GET", "http://yadota.tech/", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if resp.Header.Get("X-Sentinel") != "hit" {
		t.Fatalf("non-live custom domain did not pass through (status %d)", resp.StatusCode)
	}
}
