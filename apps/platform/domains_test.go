package platform

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fakeDNS is a deterministic, mutable resolver: a test "publishes" a TXT record
// by calling setTXT, exactly as a customer would add it at their DNS provider.
type fakeDNS struct {
	mu  sync.Mutex
	txt map[string][]string
}

func newFakeDNS() *fakeDNS { return &fakeDNS{txt: map[string][]string{}} }

func (f *fakeDNS) setTXT(name string, vals ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txt[name] = vals
}

func (f *fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.txt[name]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f *fakeDNS) LookupCNAME(_ context.Context, name string) (string, error) { return name, nil }

// mountDomains builds a hermetic app over an in-memory cluster (fakeK8s) + a fake
// DNS resolver so the whole add → verify → live domain flow is deterministic and
// never touches a real cluster or real DNS.
func mountDomains(t *testing.T) (*zip.App, *fakeDNS) {
	t.Helper()
	store, err := openStore(t.TempDir(), testMaster, testKMS)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dns := newFakeDNS()
	fp := newFakeProjects()
	s := &cloud.Service[state]{Log: luxlog.New("test"), Brand: "hanzo", State: state{store: store, projects: fp, k8s: fakeK8s(), sitesHost: "hanzo.app", resolver: dns}}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, s)
	testProjects.Store(app, fp)
	return app, dns
}

func decodeDomains(t *testing.T, b []byte) []domainView {
	t.Helper()
	var out []domainView
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode domains: %v (%s)", err, b)
	}
	return out
}

func decodeDomain(t *testing.T, b []byte) domainView {
	t.Helper()
	var out domainView
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode domain: %v (%s)", err, b)
	}
	return out
}

func findDomain(views []domainView, host string) (domainView, bool) {
	for _, v := range views {
		if v.Host == host {
			return v, true
		}
	}
	return domainView{}, false
}

// TestCreateAppSeedsDefaultHost proves every app is born with its canonical
// default host as an ingress host, so it has a working URL the moment it deploys.
func TestCreateAppSeedsDefaultHost(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountDomains(t)
		seedProject(t, app, "maxpower", "web")
		_, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
			"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		})
		var av appOut
		_ = json.Unmarshal(body, &av)
		want := "api.hanzo.app"
		found := false
		for _, d := range av.Domains {
			if d == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("app must be seeded with default host %q, got domains %v", want, av.Domains)
		}
	})
}

// TestValidateOrgDomainsAllowsVerifiedCustom proves the RED extension: a custom
// host is accepted ONLY when this org owns a verified claim on it; a pending
// claim, and another org's verified claim, are both refused.
func TestValidateOrgDomainsAllowsVerifiedCustom(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s := newTestStore(t)
		sv := &cloud.Service[state]{State: state{store: s, sitesHost: "hanzo.app"}}

		// The app's own name — always allowed.
		if err := validateOrgDomains(sv, ctx, "maxpower", "api", []string{"api.hanzo.app"}); err != nil {
			t.Fatalf("the app's own name must be allowed: %v", err)
		}
		// A PENDING custom claim is NOT yet allowed to render.
		_ = s.CreateDomain(ctx, Domain{Host: "yourco.com", Org: "maxpower", AppID: "app_1", AppSlug: "api", Status: "pending", Token: "t", CreatedAt: 1})
		if err := validateOrgDomains(sv, ctx, "maxpower", "api", []string{"yourco.com"}); err == nil {
			t.Fatal("a pending custom domain must be refused until verified")
		}
		// Once verified, this org may render it.
		_, _ = s.MarkDomainVerified(ctx, "maxpower", "app_1", "yourco.com", 2)
		if err := validateOrgDomains(sv, ctx, "maxpower", "api", []string{"yourco.com"}); err != nil {
			t.Fatalf("a verified custom domain must be allowed: %v", err)
		}
		// A DIFFERENT org may NOT render maxpower's verified domain.
		if err := validateOrgDomains(sv, ctx, "acme", "api", []string{"yourco.com"}); err == nil {
			t.Fatal("another org must never render a domain it does not own")
		}
	})
}

// TestHTTPDomainLifecycle drives the whole customer journey over HTTP: default
// present, another name under the apex refused, add a custom host (pending +
// challenge), verify it (DNS proof → verified + rendered), then remove it.
func TestHTTPDomainLifecycle(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, dns := mountDomains(t)
		seedProject(t, app, "maxpower", "web")
		do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
			"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		})
		const base = "/v1/platform/projects/web/apps/api/domains"

		// List — the default host is present, primary, verified.
		code, body := do(t, app, http.MethodGet, base, "maxpower", nil)
		if code != http.StatusOK {
			t.Fatalf("list domains want 200, got %d", code)
		}
		def, ok := findDomain(decodeDomains(t, body), "api.hanzo.app")
		if !ok || !def.Primary || def.Kind != "default" {
			t.Fatalf("default host must be listed as primary default, got %+v", def)
		}

		// Another name under the apex is another app's, never this one's to attach.
		if code, body = do(t, app, http.MethodPost, base, "maxpower", map[string]any{"host": "www.hanzo.app"}); code != http.StatusForbidden {
			t.Fatalf("attaching another name under the apex want 403, got %d (%s)", code, body)
		}

		// Add a BYO custom host → pending, with the exact DNS records to publish.
		code, body = do(t, app, http.MethodPost, base, "maxpower", map[string]any{"host": "app.yourco.com"})
		if code != http.StatusCreated {
			t.Fatalf("add custom domain want 201, got %d (%s)", code, body)
		}
		cust := decodeDomain(t, body)
		if cust.Kind != "custom" || cust.Verified || cust.Status != "pending" {
			t.Fatalf("custom host must be pending+unverified, got %+v", cust)
		}
		var txtName, token, cnameTarget string
		for _, r := range cust.Records {
			switch r.Type {
			case "TXT":
				txtName, token = r.Name, r.Value
			case "CNAME":
				cnameTarget = r.Value
			}
		}
		if txtName != "_hanzo-challenge.app.yourco.com" || token == "" {
			t.Fatalf("challenge TXT record wrong: name=%q token=%q", txtName, token)
		}
		if cnameTarget != "api.hanzo.app" {
			t.Fatalf("CNAME target must be the app default host, got %q", cnameTarget)
		}

		// Verify BEFORE publishing the record → honest still-pending (not an error).
		code, body = do(t, app, http.MethodPost, base+"/app.yourco.com/verify", "maxpower", nil)
		if code != http.StatusOK {
			t.Fatalf("verify (no record) want 200, got %d", code)
		}
		if v := decodeDomain(t, body); v.Verified || v.Status != "pending" {
			t.Fatalf("verify without DNS must stay pending, got %+v", v)
		}

		// Publish the TXT record, then verify → verified + rendered into the ingress.
		dns.setTXT(txtName, token)
		code, body = do(t, app, http.MethodPost, base+"/app.yourco.com/verify", "maxpower", nil)
		if code != http.StatusOK {
			t.Fatalf("verify (with record) want 200, got %d (%s)", code, body)
		}
		if v := decodeDomain(t, body); !v.Verified {
			t.Fatalf("verify with correct TXT must succeed, got %+v", v)
		}
		// The verified host is now an active ingress host on the app.
		_, body = do(t, app, http.MethodGet, "/v1/platform/projects/web/apps/api", "maxpower", nil)
		var av appOut
		_ = json.Unmarshal(body, &av)
		if !slices.Contains(av.Domains, "app.yourco.com") {
			t.Fatalf("verified custom host must be an active ingress host, got %v", av.Domains)
		}

		// Remove it → released, and no longer listed.
		if code, _ := do(t, app, http.MethodDelete, base+"/app.yourco.com", "maxpower", nil); code != http.StatusNoContent {
			t.Fatalf("remove custom domain want 204, got %d", code)
		}
		_, body = do(t, app, http.MethodGet, base, "maxpower", nil)
		if _, ok := findDomain(decodeDomains(t, body), "app.yourco.com"); ok {
			t.Fatal("removed custom domain must not be listed")
		}

		// The default host can never be removed.
		if code, _ := do(t, app, http.MethodDelete, base+"/api.hanzo.app", "maxpower", nil); code != http.StatusBadRequest {
			t.Fatalf("removing the default host want 400, got %d", code)
		}
	})
}

// TestHTTPCustomDomainGlobalUniqueness proves one host, one owner: a second org
// (and a second app in the same org) cannot claim a host already claimed.
func TestHTTPCustomDomainGlobalUniqueness(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountDomains(t)
		// maxpower app A claims yourco.com.
		seedProject(t, app, "maxpower", "web")
		do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
			"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		})
		if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/domains", "maxpower", map[string]any{"host": "yourco.com"}); code != http.StatusCreated {
			t.Fatalf("first claim want 201, got %d", code)
		}
		// A SECOND org cannot claim the same host.
		seedProject(t, app, "acme", "web")
		do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "acme", map[string]any{
			"name": "site", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		})
		if code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/site/domains", "acme", map[string]any{"host": "yourco.com"}); code != http.StatusConflict {
			t.Fatalf("cross-org claim of a taken host want 409, got %d (%s)", code, body)
		}
		// A SECOND app in the SAME org cannot claim it either.
		do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
			"name": "web2", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		})
		if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/web2/domains", "maxpower", map[string]any{"host": "yourco.com"}); code != http.StatusConflict {
			t.Fatalf("same-org second-app claim want 409, got %d", code)
		}
	})
}

// TestHTTPDomainApexRefused proves a host under the platform apex can never be
// BYO-claimed (RED — no cross-tenant grab of another org's *.hanzo.app host
// through the custom path), whatever its depth.
func TestHTTPDomainApexRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountDomains(t)
		seedProject(t, app, "maxpower", "web")
		do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
			"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
		})
		for _, host := range []string{"site.hanzo.app", "api.acme.hanzo.app"} {
			if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/domains", "maxpower", map[string]any{"host": host}); code != http.StatusForbidden {
				t.Fatalf("claiming %s want 403, got %d", host, code)
			}
		}
	})
}

// TestServiceCRRendersCustomIngress proves the render path: a verified custom host
// in DomainsJSON becomes an operator ingress host with cert-manager TLS, and an
// app answering only at its own name asks for no certificate (the apex wildcard
// covers it). Either way its route outranks the apex's wildcard.
func TestServiceCRRendersCustomIngress(t *testing.T) {
	ingress := func(domains string) (hosts []string, tls bool, issuer, priority string) {
		a := Application{Slug: "api", Port: 8080, Replicas: 1, DomainsJSON: domains}
		cr := serviceCR("tenant-maxpower", "maxpower", "web", a, "ghcr.io/hanzoai/nginx:1", "hanzo.app")
		hosts, _, _ = unstructured.NestedStringSlice(cr.Object, "spec", "ingress", "hosts")
		tls, _, _ = unstructured.NestedBool(cr.Object, "spec", "ingress", "tls")
		issuer, _, _ = unstructured.NestedString(cr.Object, "spec", "ingress", "clusterIssuer")
		priority, _, _ = unstructured.NestedString(cr.Object, "spec", "ingress", "annotations", "ingress.kubernetes.io/router.priority")
		return
	}
	hosts, tls, issuer, priority := ingress(`["api.hanzo.app","app.yourco.com"]`)
	if !slices.Contains(hosts, "app.yourco.com") || !slices.Contains(hosts, "api.hanzo.app") {
		t.Fatalf("ingress hosts must include both the default and the verified custom host, got %v", hosts)
	}
	if !tls || issuer != "letsencrypt-prod" || priority != appRoutePriority {
		t.Fatalf("a custom host must carry cert-manager TLS and the app rank, got tls=%v issuer=%q priority=%q", tls, issuer, priority)
	}
	if _, tls, issuer, priority := ingress(`["api.hanzo.app"]`); tls || issuer != "" || priority != appRoutePriority {
		t.Fatalf("an app at its own name rides the wildcard certificate, got tls=%v issuer=%q priority=%q", tls, issuer, priority)
	}
	if _, tls, _, _ := ingress(`["api.x.hanzo.app"]`); !tls {
		t.Fatal("a two-level host is not covered by the wildcard and must ask for its own certificate")
	}
	// No hosts → no ingress block at all.
	if ingressSpec(nil, "hanzo.app") != nil {
		t.Fatal("ingressSpec(nil) must be nil (no ingress)")
	}
}
