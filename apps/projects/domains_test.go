package projects

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/internal/fqdn"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// caller is one identity as the trust boundary presents it: the effective org plus
// the two admin bits SanitizeIdentity mints from validated claims. Both are stripped
// on ingress and re-injected only for a verified principal, so setting them here is
// modelling a token IAM signed, not forging a header — the forgery path is closed
// upstream and is middleware_identity_test.go's subject, not this file's.
type caller struct {
	org        string
	superAdmin bool // X-User-IsAdmin — platform sudo (member of the reserved admin org)
	orgAdmin   bool // X-User-IsOrgAdmin — admin OF the effective org
}

// domainsApp installs the REAL domain ops behind the REAL bridge, at their real
// paths, and returns a bind driver.
//
// It registers the ops it drives rather than calling routes(), because routes()
// composes only on the Router production gives it. It declares its middleware with
// Group(prefix, mw) and registers the typed ops on the App with FULL paths; on a
// bare *zip.App — which is what a test holds — that makes a node at the prefix with
// no routes beneath it, and zip refuses to compose a program whose middleware could
// never run. cloud.Listen mounts on a *scope instead, whose Use and Group install at
// the root and gate by request path (scope.go), so the same registration composes
// there. scope is unexported, so a test either reaches for it or registers what it
// drives.
//
// What is under test is unchanged either way: cloud.Bridge parking the request,
// siteOf resolving the tenant from it, bindDomains deciding. That is the production
// chain, at the production paths.
func domainApp(t *testing.T, s *cloud.Service[state]) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: s.Log})
	compose(app)
	app.Use(cloud.DenyEnvelope())
	r := cloud.ZipApp(app)
	o := ops{s: s}
	zip.Post(r, "/v1/projects/:slug/domains", o.bindDomains)
	zip.Get(r, "/v1/projects/:slug/domains", o.listDomains)
	zip.Post(r, "/v1/projects/:slug/domains/:host/verify", o.verifyDomain)
	zip.Delete(r, "/v1/projects/:slug/domains/:host", o.releaseDomain, zip.WithStatus(http.StatusNoContent))
	return app
}

// identify puts one caller's minted identity on a request. Both bits are stripped
// on ingress and re-injected only for a verified principal, so setting them here
// models a token IAM signed, not a forgery — that path is closed upstream and is
// middleware_identity_test.go's subject.
func identify(req *http.Request, c caller) {
	req.Header.Set("X-Org-Id", c.org)
	req.Header.Set("X-User-Id", "u-"+c.org) // a validated principal (org() gates on it)
	if c.superAdmin {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	if c.orgAdmin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
}

func domainsApp(t *testing.T, s *cloud.Service[state]) func(c caller, slug, host string) (int, projectsDomain) {
	t.Helper()
	app := domainApp(t, s)

	return func(c caller, slug, host string) (int, projectsDomain) {
		t.Helper()
		body, _ := json.Marshal(projectsDomainsBind{Domains: []string{host}})
		req := httptest.NewRequest(http.MethodPost, "/v1/projects/"+slug+"/domains", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		identify(req, c)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("bind %q for %+v: %v", host, c, err)
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
}

// TestVouchIsSuperAdminOnly is the privilege-escalation regression on the ROLE
// axis, driven through the REAL handler. The vouch skips the DNS-01 ownership proof
// (BindHost, live immediately) and bypasses the "host we operate" refusal, so it is
// PLATFORM authority and only the platform predicate carries it.
//
// Three identities in the deployment's OWN brand org, one question each:
//
//	plain member  → NOT vouched (pending claim + 403 on ours)
//	org ADMIN     → NOT vouched (pending claim + 403 on ours)
//	SuperAdmin    → vouched (operator onboarding)
//
// The org-admin row is the tightening. The gate used to admit `operatorOrgs[org] &&
// IsOrgAdmin(c)` — the deployment names an org, IAM says you administer it — and
// the default set is the brand org in EVERY deployment. But `isAdmin` is
// SELF-SERVICE: an org's own admin sets it on a member of THEIR org, so every
// `hanzo` admin could enrol any `hanzo` member into the gate, and the far side of
// the gate would be populated by an authority the platform does not administer.
// A vouched bind takes the name first-come and global and routes at once, so that
// is `login.example-bank.com` served from our edge to any custom-domain customer
// already pointed at it, and denied to its real owner for good.
//
// SuperAdmin ⟺ `owner == "admin"` is the ONE platform predicate; admitting a second,
// weaker spelling of platform authority IS the escalation, whatever conjunction
// dresses it up.
func TestVouchIsSuperAdminOnly(t *testing.T) {
	ctx := context.Background()
	log := luxlog.New("test")
	store := newTestStore(t)
	svc := &cloud.Service[state]{
		Base:  cloud.Base{Log: log},
		State: state{apex: "hanzo.app", store: store, cf: sites.NewCloudflareEdge(log)},
	}
	bind := domainsApp(t, svc)
	for _, org := range []string{"hanzo", "acme"} {
		if err := store.CreateProject(ctx, mkProject(org, "site", org)); err != nil {
			t.Fatalf("create %s project: %v", org, err)
		}
	}

	// Neither identity in the brand org may skip the proof: not the plain member
	// every staff account and every invitee has, and NOT the org admin — which any
	// existing brand-org admin can hand to any brand-org member.
	for _, tc := range []struct {
		name string
		c    caller
		host string
	}{
		{"plain member of the brand org", caller{org: "hanzo"}, "login.example-bank.com"},
		{"ADMIN of the brand org", caller{org: "hanzo", orgAdmin: true}, "admin.example-bank.com"},
	} {
		// It self-serves like any other tenant: a PENDING claim carrying the challenge.
		code, v := bind(tc.c, "site", tc.host)
		if code != http.StatusOK {
			t.Fatalf("%s bind = %d, want 200 (a pending claim)", tc.name, code)
		}
		if v.Verified || v.Status != "pending" || len(v.Records) == 0 {
			t.Fatalf("%s was VOUCHED: %+v — this bind skipped the DNS-01 ownership proof "+
				"on a host the caller does not own, and the org-admin bit that reaches it "+
				"is set by the org's own admins, not by the platform", tc.name, v)
		}
		// …and it cannot take a host WE operate at all; only a vouched caller may.
		if code, _ = bind(tc.c, "site", "evil.hanzo.app"); code != http.StatusForbidden {
			t.Fatalf("%s claim of our own host = %d, want 403 — ours() must apply to every "+
				"caller the platform predicate does not admit", tc.name, code)
		}
	}

	// OPERATOR ONBOARDING STILL WORKS. A real SuperAdmin vouches — and in ANY org,
	// because platform sudo is cross-tenant by construction: this is the operator
	// switched into a customer's org to bind the domain it manages DNS for.
	code, v := bind(caller{org: "acme", superAdmin: true}, "site", "customer.example")
	if code != http.StatusOK || !v.Verified || v.Status != "live" {
		t.Fatalf("SuperAdmin lost the vouch: code=%d %+v — operator onboarding is "+
			"disabled, which is a regression and not a fix", code, v)
	}
}

// TestVouchDoesNotTurnOnTheOrg is the cross-tenant privilege-bleed regression on the
// ORG axis, driven through the REAL route: the vouch reads the caller's PLATFORM
// bit and nothing about which tenant the request operates in.
//
// It used to turn on the org, against a configured set of operator names, and both
// halves of that comparison had to be the same value or the mismatch WAS a
// cross-tenant grant: the set was folded (lowercase + non-alnum→'-' + truncate-32)
// while the lookup stayed verbatim, so configuring "Acme" wrote the key "acme" and
// handed the DNS-proof bypass to whichever different tenant really owns "acme",
// while the genuine "Acme" silently lost it ("team.a" → "team-a" likewise). No
// comparison, no fold, no collision — but only if the org truly leaves the
// predicate, so that is what this pins.
//
// Four org names spanning the classes that used to matter — the brand org, its case
// fold, an unrelated tenant, and the reserved `admin` org's own NAME — each with an
// org ADMIN and a SuperAdmin. The org-admin never vouches and the SuperAdmin always
// does, in every one.
func TestVouchDoesNotTurnOnTheOrg(t *testing.T) {
	ctx := context.Background()
	log := luxlog.New("test")
	store := newTestStore(t)
	svc := &cloud.Service[state]{
		Base:  cloud.Base{Log: log},
		State: state{apex: "hanzo.app", store: store, cf: sites.NewCloudflareEdge(log)},
	}
	bind := domainsApp(t, svc)

	// Slugs are lowercase because slugParam lowercases the path segment; the ORG is
	// the axis under test, and it is never folded.
	for i, org := range []string{"hanzo", "Hanzo", "acme", "admin"} {
		if err := store.CreateProject(ctx, mkProject(org, "site"+string(rune('a'+i)), org)); err != nil {
			t.Fatalf("create %q project: %v", org, err)
		}
	}
	for i, org := range []string{"hanzo", "Hanzo", "acme", "admin"} {
		slug := "site" + string(rune('a'+i))

		// An ADMIN of this org self-serves through DNS-01, whatever the org is called.
		// "admin" is the reserved org's NAME, and naming it in X-Org-Id is not being in
		// it: the platform bit is minted upstream from the validated owner, never read
		// off the org the request carries.
		code, v := bind(caller{org: org, orgAdmin: true}, slug, "admin."+slug+".example")
		if code != http.StatusOK {
			t.Fatalf("org %q admin bind = %d, want 200 (a pending claim)", org, code)
		}
		if v.Verified || v.Status != "pending" || len(v.Records) == 0 {
			t.Fatalf("org %q was VOUCHED on its NAME: %+v — the org must not reach the "+
				"platform predicate, or a fold or a lookalike name is a proof bypass", org, v)
		}
		// …and it cannot claim a host WE operate at all; only a vouched caller may.
		if code, _ = bind(caller{org: org, orgAdmin: true}, slug, "api.hanzo.app"); code != http.StatusForbidden {
			t.Fatalf("org %q admin claim of our own host = %d, want 403", org, code)
		}

		// A SuperAdmin vouches in EVERY org — cross-tenant is what platform sudo is,
		// and it is the onboarding path for a customer's domain.
		code, v = bind(caller{org: org, superAdmin: true}, slug, "super."+slug+".example")
		if code != http.StatusOK || !v.Verified || v.Status != "live" {
			t.Fatalf("SuperAdmin lost the vouch in org %q: code=%d %+v", org, code, v)
		}
	}
}

// TestOursNeverEntersHostTable is the storage backstop behind the claim gate: a
// name the platform holds can never PHYSICALLY enter site_hosts, whatever the
// caller — so a vouch that skips the gate still cannot write one.
//
// It could. bindHost asked sites.IsReserved with the full hostname while that set
// holds bare LABELS, so every FQDN matched nothing: `login.hanzo.ai` sailed past
// the only guard behind ours() and took a first-come row on our own auth apex.
// Now it asks sites.Ours, which splits on shape.
//
// The other half matters as much: the label policy must NEVER reach a customer's
// own hostname. `www.` and `login.` are reserved LABELS on our apex and are also
// the two most common custom domains a customer brings, so a backstop that keyed
// on "first label is reserved" would refuse the ordinary case.
func TestOursNeverEntersHostTable(t *testing.T) {
	sites.SetSelfDomains([]string{"hanzo.app", "hanzo.ai"})
	t.Cleanup(func() { sites.SetSelfDomains(nil) })
	ctx := context.Background()
	log := luxlog.New("test")
	store := newTestStore(t)
	svc := &cloud.Service[state]{
		Base:  cloud.Base{Log: log},
		State: state{apex: "hanzo.app", store: store, cf: sites.NewCloudflareEdge(log)},
	}
	bind := domainsApp(t, svc)
	if err := store.CreateProject(ctx, mkProject("hanzo", "site", "Site")); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A SuperAdmin — the most privileged caller there is, and the one whose vouch
	// skips ours() entirely. The table still refuses our own names.
	super := caller{org: "hanzo", superAdmin: true}
	for _, h := range []string{"login.hanzo.ai", "api.hanzo.ai", "hanzo.ai", "anything.hanzo.app"} {
		if code, _ := bind(super, "site", h); code != http.StatusBadRequest {
			t.Errorf("bind %q = %d, want 400 — a host we run reached site_hosts", h, code)
		}
		if _, err := store.ResolveHost(ctx, h); err == nil {
			t.Errorf("%q holds a row in site_hosts", h)
		}
	}
	// A REAL customer domain is unaffected, including the labels our own apex
	// reserves. This is the regression the shape split exists to avoid.
	for _, h := range []string{"www.example.com", "login.example-bank.com", "api.yadota.tech"} {
		if code, v := bind(super, "site", h); code != http.StatusOK || !v.Verified {
			t.Errorf("customer domain %q = %d %+v, want 200 live — the LABEL policy must "+
				"never apply to a hostname a customer owns", h, code, v)
		}
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

// fakeDNS answers the ownership challenge from a fixed table, so verification is
// deterministic and never touches the network.
type fakeDNS map[string][]string

func (f fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if txt, ok := f[name]; ok {
		return txt, nil
	}
	return nil, errors.New("no such host")
}

// TestReleaseOnlyAddressesNamesBindCouldHaveMade is the subdomain-takeover
// regression. Bind and release must agree on what a hostname IS, and they did not:
// bind required fqdn.Valid, release required only non-empty.
//
// site_hosts also holds each project's BARE SLUG — the structural row deploy.go
// binds so `<slug>.<apex>` serves — and a bare label is not Valid, so release
// accepted a row bind could never re-create. The domains panel renders it like any
// other claim, as a live `https://<slug>`, so a tenant deleting the odd-looking
// entry drops its OWN subdomain irrecoverably: resolution falls back to
// ResolveUniqueLiveSlug, which refuses once two live projects share the slug, and
// the next such tenant to deploy takes the freed row and the subdomain for good.
func TestReleaseOnlyAddressesNamesBindCouldHaveMade(t *testing.T) {
	ctx := context.Background()
	log := luxlog.New("test")
	store := newTestStore(t)
	svc := &cloud.Service[state]{
		Base:  cloud.Base{Log: log},
		State: state{apex: "hanzo.app", store: store, cf: sites.NewCloudflareEdge(log)},
	}
	app := domainApp(t, svc)
	if err := store.CreateProject(ctx, mkProject("acme", "acme", "Acme")); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The structural row onPublish binds: the bare slug, serving acme.hanzo.app.
	if err := store.BindHost(ctx, "acme", "acme", "acme", 100); err != nil {
		t.Fatalf("bind slug host: %v", err)
	}

	release := func(host string) int {
		t.Helper()
		// Escaped, so a hostile value is what the ROUTE decodes rather than what the
		// test harness refuses to build a URL from.
		req := httptest.NewRequest(http.MethodDelete, "/v1/projects/acme/domains/"+url.PathEscape(host), nil)
		identify(req, caller{org: "acme", orgAdmin: true})
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("release %q: %v", host, err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	// A bare label is not a hostname this surface can address — bind would refuse
	// it, so release must too.
	if code := release("acme"); code != http.StatusBadRequest {
		t.Errorf("release of the bare slug = %d, want 400", code)
	}
	if _, err := store.ResolveHost(ctx, "acme"); err != nil {
		t.Fatal("the site's own subdomain row was deleted through the domains API — " +
			"the domains panel offers this row with a delete control, and nothing in " +
			"that API can put it back")
	}
	// Neither is anything else Valid refuses.
	for _, h := range []string{"", "  ", "not a host", "http://acme.example", "acme.example:8080"} {
		if code := release(h); code == http.StatusNoContent {
			t.Errorf("release accepted %q — release and bind must take the same shape", h)
		}
	}
	// A REAL custom domain still releases, which is the call's whole job.
	if err := store.BindHost(ctx, "acme.example", "acme", "acme", 100); err != nil {
		t.Fatalf("bind custom: %v", err)
	}
	if code := release("acme.example"); code != http.StatusNoContent {
		t.Fatalf("release of a real custom domain = %d, want 204", code)
	}
	if _, err := store.ResolveHost(ctx, "acme.example"); err == nil {
		t.Error("a released custom domain still holds its row")
	}
}

// TestVerifyDomainPromotesOnlyOnProof drives the verify HANDLER, which had no test
// at all: Store.VerifyHost was covered and fqdn.Verify was covered, but the handler
// that joins them — the already-verified early return, the token read from the ROW
// rather than the request, and the promotion itself — was not. Nothing pinned that
// this surface actually requires the proof.
func TestVerifyDomainPromotesOnlyOnProof(t *testing.T) {
	ctx := context.Background()
	log := luxlog.New("test")
	store := newTestStore(t)
	dns := fakeDNS{}
	svc := &cloud.Service[state]{
		Base:  cloud.Base{Log: log},
		State: state{apex: "hanzo.app", store: store, cf: sites.NewCloudflareEdge(log), resolver: dns},
	}
	app := domainApp(t, svc)
	if err := store.CreateProject(ctx, mkProject("acme", "acme", "Acme")); err != nil {
		t.Fatalf("create: %v", err)
	}

	who := caller{org: "acme", orgAdmin: true}
	verify := func(host string) (int, projectsDomain) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/projects/acme/domains/"+host+"/verify", nil)
		identify(req, who)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("verify %q: %v", host, err)
		}
		defer func() { _ = resp.Body.Close() }()
		var v projectsDomain
		_ = json.NewDecoder(resp.Body).Decode(&v)
		return resp.StatusCode, v
	}

	// An org admin binds a customer domain: pending, with a challenge to publish.
	bind := domainsApp(t, svc)
	code, v := bind(who, "acme", "shop.acme.example")
	if code != http.StatusOK || v.Verified {
		t.Fatalf("bind = %d %+v, want a pending claim", code, v)
	}
	claim, err := store.HostClaimFor(ctx, "shop.acme.example", "acme", "acme")
	if err != nil {
		t.Fatalf("read claim: %v", err)
	}

	// No record published yet: an honest still-pending, never a promotion.
	if code, v := verify("shop.acme.example"); code != http.StatusOK || v.Verified {
		t.Fatalf("verify with no DNS = %d %+v, want 200 pending", code, v)
	}
	// A record with the WRONG token proves nothing.
	dns[fqdn.Challenge("shop.acme.example")] = []string{"not-the-token"}
	if _, v := verify("shop.acme.example"); v.Verified {
		t.Fatal("a TXT record with the wrong token promoted the host")
	}
	// The token is read from the ROW, so publishing the real one promotes.
	dns[fqdn.Challenge("shop.acme.example")] = []string{claim.Token}
	if code, v := verify("shop.acme.example"); code != http.StatusOK || !v.Verified || v.Status != "live" {
		t.Fatalf("verify with the published token = %d %+v, want live", code, v)
	}
	// Idempotent, and the re-check does not need DNS any more.
	delete(dns, fqdn.Challenge("shop.acme.example"))
	if _, v := verify("shop.acme.example"); !v.Verified {
		t.Fatal("an already-verified host was walked back to pending")
	}
	// A host this site never claimed is a 404, not a promotion — and a name the
	// surface cannot address is refused before any lookup happens.
	if code, _ := verify("never.claimed.example"); code != http.StatusNotFound {
		t.Errorf("verify of an unclaimed host = %d, want 404", code)
	}
	if code, _ := verify("acme"); code != http.StatusBadRequest {
		t.Errorf("verify of a bare label = %d, want 400", code)
	}
}
