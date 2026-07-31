package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/sites"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestOperatorOrgsFromEnv: the operator set is keyed by the SAME value the vouch
// is looked up by — the VERBATIM validated IAM owner (principal.Org), trimmed and
// nothing else. Only whitespace around a comma-separated entry is dropped.
//
// This used to fold each entry through the old sanitizeOrg (lowercase +
// non-alnum→'-' + truncate-32) while domains.go looked the tenant up verbatim, so
// the two halves of one decision disagreed: configuring "Acme" wrote the key
// "acme", handing a DIFFERENT tenant — the one whose real IAM owner is "acme" —
// platform-operator vouch, which SKIPS the DNS-01 ownership proof entirely; and
// the genuine operator "Acme" silently lost its own. "team.a" → "team-a" is the
// same class. Fold on one side of a comparison is never a normalization, it is a
// collision, so both sides are now the same verbatim value.
func TestOperatorOrgsFromEnv(t *testing.T) {
	t.Setenv("CLOUD_PLATFORM_OPERATOR_ORGS", "")
	got := operatorOrgsFromEnv("hanzo")
	if !got["hanzo"] || len(got) != 1 {
		t.Fatalf("default operator orgs = %v, want {hanzo}", got)
	}
	t.Setenv("CLOUD_PLATFORM_OPERATOR_ORGS", "hanzo, yadota ,Acme,team.a")
	got = operatorOrgsFromEnv("ignored-when-env-set")
	for _, o := range []string{"hanzo", "yadota", "Acme", "team.a"} {
		if !got[o] {
			t.Errorf("operator org %q missing from %v — entries are verbatim", o, got)
		}
	}
	// The folded spellings are NOT operators: they name other tenants.
	for _, o := range []string{"acme", "team-a"} {
		if got[o] {
			t.Errorf("folded spelling %q vouched from %v — a case/punctuation fold hands "+
				"a different tenant the operator's DNS-proof bypass", o, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("operator orgs = %v, want exactly 4 verbatim entries", got)
	}
	// The brand default is verbatim too — no fold on the fallback path either.
	t.Setenv("CLOUD_PLATFORM_OPERATOR_ORGS", "")
	if got = operatorOrgsFromEnv("Acme"); !got["Acme"] || got["acme"] || len(got) != 1 {
		t.Fatalf("brand-default operator orgs = %v, want exactly {Acme}", got)
	}
}

// TestOperatorVouchIsVerbatimEndToEnd is the cross-tenant privilege-bleed
// regression, driven through the REAL route. `vouched` is what skips the DNS-01
// ownership proof (BindHost, live immediately) and bypasses the "host we operate"
// refusal — so a tenant that merely case-folds onto an operator's name must NOT
// get it.
//
// CLOUD_PLATFORM_OPERATOR_ORGS="Acme" names ONE operator. Tenant "acme" is a
// different IAM owner and must self-serve: its bind is a PENDING claim carrying a
// DNS challenge, and a host we operate is refused outright. The operator "Acme"
// keeps its vouch: bound live, no challenge.
func TestOperatorVouchIsVerbatimEndToEnd(t *testing.T) {
	t.Setenv("CLOUD_PLATFORM_OPERATOR_ORGS", "Acme")
	ctx := context.Background()
	log := luxlog.New("test")
	store := newTestStore(t)
	svc := &cloud.Service[state]{
		Base: cloud.Base{Log: log},
		State: state{
			apex: "hanzo.app", store: store, cf: sites.NewPurger(log),
			operatorOrgs: operatorOrgsFromEnv("ignored-when-env-set"),
		},
	}
	app := zip.New(zip.Config{Logger: log})
	routes(app, svc)

	// Two DISTINCT tenants whose owners differ only in case, each with its own
	// project. Slugs are lowercase because slugParam lowercases the path segment;
	// the ORG is the axis under test, and it is never folded.
	if err := store.CreateProject(ctx, mkProject("Acme", "operator-site", "Operator")); err != nil {
		t.Fatalf("create operator project: %v", err)
	}
	if err := store.CreateProject(ctx, mkProject("acme", "tenant-site", "Tenant")); err != nil {
		t.Fatalf("create tenant project: %v", err)
	}
	bind := func(org, slug, host string) (int, projectsDomain) {
		t.Helper()
		body, _ := json.Marshal(projectsDomainsBind{Domains: []string{host}})
		req := httptest.NewRequest(http.MethodPost, "/v1/projects/"+slug+"/domains", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("bind %q for %q: %v", host, org, err)
		}
		defer func() { _ = resp.Body.Close() }()
		var out struct {
			Bound []projectsDomain `json:"bound"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if len(out.Bound) == 0 {
			return resp.StatusCode, projectsDomain{}
		}
		return resp.StatusCode, out.Bound[0]
	}

	// The lookalike tenant is NOT the operator: it self-serves through DNS-01.
	code, v := bind("acme", "tenant-site", "lookalike.example")
	if code != http.StatusOK {
		t.Fatalf("lookalike bind = %d, want 200 (a pending claim)", code)
	}
	if v.Verified || v.Status != "pending" || len(v.Records) == 0 {
		t.Fatalf("tenant %q was VOUCHED as platform operator: %+v — a case fold onto "+
			"operator \"Acme\" skipped the DNS-01 ownership proof", "acme", v)
	}
	// …and it cannot claim a host WE operate at all; only a vouched org may.
	if code, _ = bind("acme", "tenant-site", "api.hanzo.app"); code != http.StatusForbidden {
		t.Fatalf("lookalike claim of our own host = %d, want 403", code)
	}
	// The real operator keeps its vouch: bound live, no challenge owed.
	code, v = bind("Acme", "operator-site", "operator.example")
	if code != http.StatusOK || !v.Verified || v.Status != "live" {
		t.Fatalf("operator %q lost its vouch: code=%d %+v", "Acme", code, v)
	}
}

// Hostname syntax is internal/fqdn's contract now, and fqdn_test.go pins every
// case this file used to assert (plus the trailing root dot and the 253-byte
// bound, neither of which this path used to handle). Re-asserting them here would
// be a second copy of the same expectation — exactly the duplication that let the
// two paths drift in the first place.

// TestListHostsForProject: a site's own subdomain plus bound custom domains are
// all reported, scoped to (org, slug); another org's binding is never leaked.
func TestListHostsForProject(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("yadota", "yadota", "Yadota")); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Bind the subdomain slug + two custom domains to (yadota, yadota).
	for _, h := range []string{"yadota", "yadota.tech", "www.yadota.tech"} {
		if err := s.BindHost(ctx, h, "yadota", "yadota", 100); err != nil {
			t.Fatalf("bind %q: %v", h, err)
		}
	}
	// A different org's binding must not appear in yadota's list.
	if err := s.CreateProject(ctx, mkProject("acme", "acme", "Acme")); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.BindHost(ctx, "acme.example", "acme", "acme", 100); err != nil {
		t.Fatalf("bind acme: %v", err)
	}

	hosts, err := s.ListHostsForProject(ctx, "yadota", "yadota")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hosts) != 3 {
		t.Fatalf("want 3 hosts, got %d (%v)", len(hosts), hosts)
	}
	set := map[string]bool{}
	for _, h := range hosts {
		set[h] = true
	}
	for _, want := range []string{"yadota", "yadota.tech", "www.yadota.tech"} {
		if !set[want] {
			t.Errorf("missing bound host %q in %v", want, hosts)
		}
	}
	if set["acme.example"] {
		t.Fatal("cross-org host leaked into project domain list")
	}

	// A custom domain already bound to another site is refused (first-come).
	if err := s.BindHost(ctx, "yadota.tech", "acme", "acme", 200); err == nil {
		t.Fatal("expected errHostTaken binding another org's custom domain")
	}
}

// TestResolveHostCustomDomain: a bound custom domain resolves to its project (the
// exact join the site edge uses for custom-domain serving), and reflects the
// project's live status.
func TestResolveHostCustomDomain(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := mkProject("yadota", "yadota", "Yadota")
	p.Status = "live"
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.BindHost(ctx, "yadota.tech", "yadota", "yadota", 100); err != nil {
		t.Fatalf("bind: %v", err)
	}
	got, err := s.ResolveHost(ctx, "yadota.tech")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Org != "yadota" || got.Slug != "yadota" || got.Status != "live" {
		t.Fatalf("resolved %+v, want org=yadota slug=yadota status=live", got)
	}
}

// TestSelfHostsAreNotClaimable proves the claim gate refuses every host WE run,
// not just the published-site apex. `api.hanzo.ai` — the production API host —
// used to pass this gate: the serve gate excluded it so it could never serve, but
// the claim row is first-come and global, so the claim permanently DENIED the host
// to its real owner (verified against production: a customer claimed it and the
// hanzo org was then refused 409 on its own hostname).
func TestSelfHostsAreNotClaimable(t *testing.T) {
	// One source, set exactly as sites.New does at startup.
	sites.SetSelfDomains([]string{"hanzo.app", "hanzo.ai"})
	t.Cleanup(func() { sites.SetSelfDomains(nil) })

	s := &cloud.Service[state]{State: state{apex: "hanzo.app"}}
	for _, h := range []string{"hanzo.ai", "api.hanzo.ai", "console.hanzo.ai", "hanzo.app", "anything.hanzo.app"} {
		if !ours(s, h) {
			t.Errorf("%q is a host we operate but the claim gate would allow it", h)
		}
	}
	// A genuine customer domain is unaffected — this gate must never widen past ours.
	for _, h := range []string{"shop.yadota.tech", "yadota.tech", "hanzo.ai.evil.test"} {
		if ours(s, h) {
			t.Errorf("%q is a customer domain but the claim gate refused it", h)
		}
	}
}

// TestUnbindHostReleasesOnlyOurOwn proves a released host becomes claimable again
// and that the release is tenant-scoped. Until releaseDomain existed there was no
// writer that could drop a row at all — setDomains only adds, and delete-project
// unbinds the bare slug alone — so a mistyped or retired domain was held forever.
func TestUnbindHostReleasesOnlyOurOwn(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, org := range []string{"yadota", "acme"} {
		if err := s.CreateProject(ctx, mkProject(org, org, org)); err != nil {
			t.Fatalf("create %s: %v", org, err)
		}
	}
	if err := s.BindHost(ctx, "yadota.tech", "yadota", "yadota", 100); err != nil {
		t.Fatalf("bind: %v", err)
	}
	// A non-owner "release" is a scoped no-op — it must not drop someone else's row.
	if err := s.UnbindHost(ctx, "yadota.tech", "acme", "acme"); err != nil {
		t.Fatalf("noop release: %v", err)
	}
	if _, err := s.ResolveHost(ctx, "yadota.tech"); err != nil {
		t.Fatal("a non-owner release dropped the owner's host")
	}
	// The owner releases it, and only then may another org claim it.
	if err := s.BindHost(ctx, "yadota.tech", "acme", "acme", 200); !errors.Is(err, errHostTaken) {
		t.Fatalf("still-held host rebind = %v, want errHostTaken", err)
	}
	if err := s.UnbindHost(ctx, "yadota.tech", "yadota", "yadota"); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if err := s.BindHost(ctx, "yadota.tech", "acme", "acme", 300); err != nil {
		t.Fatalf("released host must be claimable again, got %v", err)
	}
}
