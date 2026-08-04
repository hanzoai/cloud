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
func domainsApp(t *testing.T, s *cloud.Service[state]) func(c caller, slug, host string) (int, projectsDomain) {
	t.Helper()
	app := zip.New(zip.Config{Logger: s.Log})
	app.Use(cloud.Bridge(), cloud.DenyEnvelope())
	r := cloud.ZipApp(app)
	o := ops{s: s}
	zip.Post(r, "/v1/projects/:slug/domains", o.bindDomains)
	zip.Get(r, "/v1/projects/:slug/domains", o.listDomains)

	return func(c caller, slug, host string) (int, projectsDomain) {
		t.Helper()
		body, _ := json.Marshal(projectsDomainsBind{Domains: []string{host}})
		req := httptest.NewRequest(http.MethodPost, "/v1/projects/"+slug+"/domains", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", c.org)
		req.Header.Set("X-User-Id", "u-"+c.org) // a validated principal (org() gates on it)
		if c.superAdmin {
			req.Header.Set("X-User-IsAdmin", "true")
		}
		if c.orgAdmin {
			req.Header.Set("X-User-IsOrgAdmin", "true")
		}
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

// TestOperatorVouchNeedsAdminScope is the privilege-escalation regression, driven
// through the REAL handler. The vouch skips the DNS-01 ownership proof (BindHost,
// live immediately) and bypasses the "host we operate" refusal, so it is platform
// authority and only an ADMIN scope may carry it.
//
// It used to key on MEMBERSHIP: `operatorOrgs[org]` alone, and the set defaults to
// the deployment's brand org in EVERY deployment (brand.Default = "hanzo"). So any
// member of org hanzo — every staff account whatever its role, plus anyone a
// hanzo admin ever invited — could bind `login.example-bank.com` VERIFIED with no
// proof at all: attacker content served at any custom-domain customer whose DNS
// already points at our edge, and the name denied to its rightful owner for good,
// since a verified row is first-come and global.
//
// Four identities, one operator org, one question each:
//
//	plain member of the operator org  → NOT vouched (pending claim + 403 on ours)
//	SuperAdmin                        → vouched (operator onboarding, unchanged)
//	ADMIN of the operator org         → vouched (the set's grant, exercised by its admin)
//	ADMIN of some OTHER org           → NOT vouched (the set is not a role, and a
//	                                    role is not the set)
func TestOperatorVouchNeedsAdminScope(t *testing.T) {
	t.Setenv("CLOUD_PLATFORM_OPERATOR_ORGS", "")
	ctx := context.Background()
	log := luxlog.New("test")
	store := newTestStore(t)
	svc := &cloud.Service[state]{
		Base: cloud.Base{Log: log},
		State: state{
			apex: "hanzo.app", store: store, cf: sites.NewPurger(log),
			operatorOrgs: operatorOrgsFromEnv("hanzo"), // the default set: {hanzo}
		},
	}
	bind := domainsApp(t, svc)
	for _, org := range []string{"hanzo", "acme"} {
		if err := store.CreateProject(ctx, mkProject(org, "site", org)); err != nil {
			t.Fatalf("create %s project: %v", org, err)
		}
	}

	// A PLAIN MEMBER of the brand operator org. No admin bit of either kind — the
	// identity every staff account and every invitee has.
	member := caller{org: "hanzo"}

	// It self-serves like any other tenant: a PENDING claim carrying the challenge.
	code, v := bind(member, "site", "login.example-bank.com")
	if code != http.StatusOK {
		t.Fatalf("member bind = %d, want 200 (a pending claim)", code)
	}
	if v.Verified || v.Status != "pending" || len(v.Records) == 0 {
		t.Fatalf("a PLAIN MEMBER of the operator org was vouched: %+v — membership is not "+
			"an admin scope, and this bind skipped the DNS-01 ownership proof on a host "+
			"the caller does not own", v)
	}
	// …and it cannot take a host WE operate at all; only a vouched caller may.
	if code, _ = bind(member, "site", "evil.hanzo.app"); code != http.StatusForbidden {
		t.Fatalf("member claim of our own host = %d, want 403 — ours() must apply to "+
			"every non-vouched caller, operator-org membership included", code)
	}

	// OPERATOR ONBOARDING STILL WORKS. A real SuperAdmin vouches — and in ANY org,
	// because platform sudo is cross-tenant by construction: this is the operator
	// switched into a customer's org to bind the domain it manages DNS for.
	code, v = bind(caller{org: "acme", superAdmin: true}, "site", "customer.example")
	if code != http.StatusOK || !v.Verified || v.Status != "live" {
		t.Fatalf("SuperAdmin lost the vouch: code=%d %+v — operator onboarding is "+
			"disabled, which is a regression and not a fix", code, v)
	}

	// The set's own grant, exercised by an ADMIN of the org it names.
	code, v = bind(caller{org: "hanzo", orgAdmin: true}, "site", "operator.example")
	if code != http.StatusOK || !v.Verified || v.Status != "live" {
		t.Fatalf("admin OF the operator org lost the vouch: code=%d %+v", code, v)
	}

	// An org admin OUTSIDE the set gets nothing: the org-admin bit is self-service
	// authority within one's own tenant, never platform authority.
	code, v = bind(caller{org: "acme", orgAdmin: true}, "site", "outsider.example")
	if code != http.StatusOK {
		t.Fatalf("non-operator org-admin bind = %d, want 200 (a pending claim)", code)
	}
	if v.Verified || v.Status != "pending" {
		t.Fatalf("an org admin OUTSIDE the operator set was vouched: %+v — the IAM "+
			"isAdmin bit is org-scoped self-service, never platform authority", v)
	}
}

// TestOperatorVouchIsVerbatimEndToEnd is the cross-tenant privilege-bleed
// regression, driven through the REAL route. The vouch is what skips the DNS-01
// ownership proof (BindHost, live immediately) and bypasses the "host we operate"
// refusal — so a tenant that merely case-folds onto an operator's name must NOT
// get it, however privileged it is inside its OWN org.
//
// CLOUD_PLATFORM_OPERATOR_ORGS="Acme" names ONE operator. Tenant "acme" is a
// different IAM owner and must self-serve: its bind is a PENDING claim carrying a
// DNS challenge, and a host we operate is refused outright. The operator "Acme"
// keeps its vouch: bound live, no challenge. BOTH callers are org admins here, so
// the only axis left is the org name — which is the axis under test, and it is
// never folded.
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
	bind := domainsApp(t, svc)

	// Two DISTINCT tenants whose owners differ only in case, each with its own
	// project. Slugs are lowercase because slugParam lowercases the path segment;
	// the ORG is the axis under test, and it is never folded.
	if err := store.CreateProject(ctx, mkProject("Acme", "operator-site", "Operator")); err != nil {
		t.Fatalf("create operator project: %v", err)
	}
	if err := store.CreateProject(ctx, mkProject("acme", "tenant-site", "Tenant")); err != nil {
		t.Fatalf("create tenant project: %v", err)
	}

	// The lookalike tenant is NOT the operator: it self-serves through DNS-01.
	code, v := bind(caller{org: "acme", orgAdmin: true}, "tenant-site", "lookalike.example")
	if code != http.StatusOK {
		t.Fatalf("lookalike bind = %d, want 200 (a pending claim)", code)
	}
	if v.Verified || v.Status != "pending" || len(v.Records) == 0 {
		t.Fatalf("tenant %q was VOUCHED as platform operator: %+v — a case fold onto "+
			"operator \"Acme\" skipped the DNS-01 ownership proof", "acme", v)
	}
	// …and it cannot claim a host WE operate at all; only a vouched caller may.
	if code, _ = bind(caller{org: "acme", orgAdmin: true}, "tenant-site", "api.hanzo.app"); code != http.StatusForbidden {
		t.Fatalf("lookalike claim of our own host = %d, want 403", code)
	}
	// The real operator keeps its vouch: bound live, no challenge owed.
	code, v = bind(caller{org: "Acme", orgAdmin: true}, "operator-site", "operator.example")
	if code != http.StatusOK || !v.Verified || v.Status != "live" {
		t.Fatalf("operator %q lost its vouch: code=%d %+v", "Acme", code, v)
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
		Base: cloud.Base{Log: log},
		State: state{
			apex: "hanzo.app", store: store, cf: sites.NewPurger(log),
			operatorOrgs: map[string]bool{"hanzo": true},
		},
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
