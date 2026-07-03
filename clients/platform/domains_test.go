package platform

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

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
	store, err := openStore(filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	dns := newFakeDNS()
	s := &svc{store: store, k8s: fakeK8s(), log: luxlog.New("test"), brand: "hanzo", sitesHost: "hanzo.app", resolver: dns}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	s.routes(app)
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
	app, _ := mountDomains(t)
	do(t, app, http.MethodPost, "/v1/platform/projects", "maxpower", map[string]any{"name": "web"})
	_, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
	})
	var av appView
	_ = json.Unmarshal(body, &av)
	want := "api.maxpower.hanzo.app"
	found := false
	for _, d := range av.Domains {
		if d == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("app must be seeded with default host %q, got domains %v", want, av.Domains)
	}
}

// TestValidateOrgDomainsAllowsVerifiedCustom proves the RED extension: a
// non-subtree host is accepted ONLY when this org owns a verified claim on it; a
// pending claim, and another org's verified claim, are both refused.
func TestValidateOrgDomainsAllowsVerifiedCustom(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	svc := &svc{store: s, sitesHost: "hanzo.app"}

	// Own subtree — always allowed.
	if err := svc.validateOrgDomains(ctx, "maxpower", []string{"api.maxpower.hanzo.app"}); err != nil {
		t.Fatalf("own subtree must be allowed: %v", err)
	}
	// A PENDING custom claim is NOT yet allowed to render.
	_ = s.CreateDomain(ctx, Domain{Host: "yourco.com", Org: "maxpower", AppID: "app_1", AppSlug: "api", Status: "pending", Token: "t", CreatedAt: 1})
	if err := svc.validateOrgDomains(ctx, "maxpower", []string{"yourco.com"}); err == nil {
		t.Fatal("a pending custom domain must be refused until verified")
	}
	// Once verified, this org may render it.
	_, _ = s.MarkDomainVerified(ctx, "maxpower", "app_1", "yourco.com", 2)
	if err := svc.validateOrgDomains(ctx, "maxpower", []string{"yourco.com"}); err != nil {
		t.Fatalf("a verified custom domain must be allowed: %v", err)
	}
	// A DIFFERENT org may NOT render maxpower's verified domain.
	if err := svc.validateOrgDomains(ctx, "acme", []string{"yourco.com"}); err == nil {
		t.Fatal("another org must never render a domain it does not own")
	}
}

// TestHTTPDomainLifecycle drives the whole customer journey over HTTP: default
// present, add a subtree host (active), add a custom host (pending + challenge),
// verify it (DNS proof → verified + rendered), then remove it.
func TestHTTPDomainLifecycle(t *testing.T) {
	app, dns := mountDomains(t)
	do(t, app, http.MethodPost, "/v1/platform/projects", "maxpower", map[string]any{"name": "web"})
	do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
	})
	const base = "/v1/platform/projects/web/apps/api/domains"

	// List — the default host is present, primary, verified.
	code, body := do(t, app, http.MethodGet, base, "maxpower", nil)
	if code != http.StatusOK {
		t.Fatalf("list domains want 200, got %d", code)
	}
	def, ok := findDomain(decodeDomains(t, body), "api.maxpower.hanzo.app")
	if !ok || !def.Primary || def.Kind != "default" {
		t.Fatalf("default host must be listed as primary default, got %+v", def)
	}

	// Add an org-subtree host → active immediately (no verification).
	code, body = do(t, app, http.MethodPost, base, "maxpower", map[string]any{"host": "www.maxpower.hanzo.app"})
	if code != http.StatusCreated {
		t.Fatalf("add subtree domain want 201, got %d (%s)", code, body)
	}
	if v := decodeDomain(t, body); v.Kind != "subtree" || !v.Verified {
		t.Fatalf("subtree host must be verified/active, got %+v", v)
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
	if cnameTarget != "api.maxpower.hanzo.app" {
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
	var av appView
	_ = json.Unmarshal(body, &av)
	if !contains(av.Domains, "app.yourco.com") {
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
	if code, _ := do(t, app, http.MethodDelete, base+"/api.maxpower.hanzo.app", "maxpower", nil); code != http.StatusBadRequest {
		t.Fatalf("removing the default host want 400, got %d", code)
	}
}

// TestHTTPCustomDomainGlobalUniqueness proves one host, one owner: a second org
// (and a second app in the same org) cannot claim a host already claimed.
func TestHTTPCustomDomainGlobalUniqueness(t *testing.T) {
	app, _ := mountDomains(t)
	// maxpower app A claims yourco.com.
	do(t, app, http.MethodPost, "/v1/platform/projects", "maxpower", map[string]any{"name": "web"})
	do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
	})
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/domains", "maxpower", map[string]any{"host": "yourco.com"}); code != http.StatusCreated {
		t.Fatalf("first claim want 201, got %d", code)
	}
	// A SECOND org cannot claim the same host.
	do(t, app, http.MethodPost, "/v1/platform/projects", "acme", map[string]any{"name": "web"})
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
}

// TestHTTPDomainApexRefused proves a host under the platform apex that is NOT the
// caller's own subtree can never be BYO-claimed (RED — no cross-tenant grab of
// another org's *.hanzo.app host through the custom path).
func TestHTTPDomainApexRefused(t *testing.T) {
	app, _ := mountDomains(t)
	do(t, app, http.MethodPost, "/v1/platform/projects", "maxpower", map[string]any{"name": "web"})
	do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "maxpower", map[string]any{
		"name": "api", "source": "image", "image": map[string]any{"repository": "ghcr.io/hanzoai/nginx", "tag": "1"},
	})
	if code, _ := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/domains", "maxpower", map[string]any{"host": "api.acme.hanzo.app"}); code != http.StatusForbidden {
		t.Fatalf("claiming another org's subtree host want 403, got %d", code)
	}
}

// TestServiceCRRendersCustomIngress proves the render path: a verified custom host
// in DomainsJSON becomes an operator ingress host with cert-manager TLS.
func TestServiceCRRendersCustomIngress(t *testing.T) {
	a := Application{Slug: "api", Port: 8080, Replicas: 1, DomainsJSON: `["api.maxpower.hanzo.app","app.yourco.com"]`}
	cr := serviceCR("tenant-maxpower", "maxpower", "web", a, "ghcr.io/hanzoai/nginx:1")
	hosts, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "ingress", "hosts")
	if !contains(hosts, "app.yourco.com") || !contains(hosts, "api.maxpower.hanzo.app") {
		t.Fatalf("ingress hosts must include both the default and the verified custom host, got %v", hosts)
	}
	tls, _, _ := unstructured.NestedBool(cr.Object, "spec", "ingress", "tls")
	issuer, _, _ := unstructured.NestedString(cr.Object, "spec", "ingress", "clusterIssuer")
	if !tls || issuer != "letsencrypt-prod" {
		t.Fatalf("ingress must carry cert-manager TLS, got tls=%v issuer=%q", tls, issuer)
	}
	// No hosts → no ingress block at all.
	if ingressSpec(nil) != nil {
		t.Fatal("ingressSpec(nil) must be nil (no ingress)")
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
