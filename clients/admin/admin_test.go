package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/commerce"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/digitalocean"
	"github.com/hanzoai/cloud/clients/admin/health"
	"github.com/hanzoai/cloud/clients/admin/iam"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// mount builds a zip app with admin mounted against the given upstream bases,
// and returns a `do` helper that issues test requests through the whole app.
func mount(t *testing.T, iamURL, commerceURL, healthURL string) func(method, path string, hdr map[string]string) (*http.Response, []byte) {
	do, _, _ := mountService(t, iamURL, commerceURL, healthURL)
	return do
}

// mountService is mount but also returns the underlying cloud.Service[state] (so finance tests can swap
// in a fake DigitalOcean client, and the cockpit tests can attach an audit store)
// AND the raw fiber app (so tests that need a request BODY can drive it directly —
// the returned `do` sends a nil body). The handlers read s.* live at request time,
// so an override before issuing a request takes effect.
func mountService(t *testing.T, iamURL, commerceURL, healthURL string) (func(method, path string, hdr map[string]string) (*http.Response, []byte), *cloud.Service[core.State], *fiber.App) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	s := &cloud.Service[core.State]{State: core.State{
		IAM:      iam.New(iamURL),
		Commerce: commerce.New(commerceURL, "test-token"),
		Health:   health.New(healthURL),
		DO:       digitalocean.New(""), // no token → honest not-configured unless a test overrides s.State.DO
		AdminOrg: "admin",
		// The harness enables ONE white-label tenant — "maxpower" (the org orgAdminHdr
		// belongs to) — so the scoped-panel tests exercise the ADMITTED WL tier. The
		// gate now requires WL enablement for any non-super caller, so the deny tests use
		// a DIFFERENT org (not in this set) to prove a non-enabled org-admin is refused,
		// and the fail-closed default (empty set ⇒ deny) is covered by a dedicated unit
		// test on State.IsWhiteLabelTenant. A test that needs the fleet-only default
		// clears s.State.WLTenants after mount.
		WLTenants: map[string]bool{"maxpower": true},
	}}
	// Mirror the REAL Mount EXACTLY by registering the same routes() the subsystem uses
	// (org-scoped panels behind GuardScoped, the platform control plane behind Guard,
	// each domain owning its own routes), so the harness stays authoritative for the
	// two-tier gate + every surface.
	routes(app, s)
	fa := app.Fiber()

	return func(method, path string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}, s, fa
}

type adminRoute struct{ method, path string }

// scopedAdminRoutes are the ORG-SCOPED panels (guardScoped): a SuperAdmin OR a validated
// org admin is admitted, and the handler scopes the data. A caller with NO validated
// principal (anonymous, or an org header but no X-User-Id) is still refused.
var scopedAdminRoutes = []adminRoute{
	{"GET", "/v1/admin/me"},
	{"GET", "/v1/admin/overview"},
	{"GET", "/v1/admin/orgs"},
	{"GET", "/v1/admin/users"},
	{"GET", "/v1/admin/usage"},
	{"GET", "/v1/admin/analytics"},
	{"GET", "/v1/admin/bases"},
}

// platformAdminRoutes are SuperAdmin ONLY (s.guard) — the cross-tenant platform reads +
// the launch/release/flags/access control plane. A non-super caller is ALWAYS 403.
var platformAdminRoutes = []adminRoute{
	{"GET", "/v1/admin/roles"},
	{"GET", "/v1/admin/applications"},
	{"GET", "/v1/admin/audit"},
	{"GET", "/v1/admin/audit/verify"},
	{"GET", "/v1/admin/products"},
	{"GET", "/v1/admin/finance"},
	{"POST", "/v1/admin/sync"},
	{"GET", "/v1/admin/customers"},
	{"GET", "/v1/admin/customers/acme"},
	{"POST", "/v1/admin/customers/acme/credit"},
	{"POST", "/v1/admin/customers/acme/suspend"},
	{"POST", "/v1/admin/customers/acme/reactivate"},
	{"GET", "/v1/admin/revenue"},
	{"GET", "/v1/admin/flags"},
	{"GET", "/v1/admin/waitlist"},
	{"POST", "/v1/admin/waitlist/boost"},
}

// adminRoutes is the full surface (both tiers) — the fail-closed gate test denies an
// unauthenticated caller on EVERY one.
var adminRoutes = append(append([]adminRoute{}, scopedAdminRoutes...), platformAdminRoutes...)

// TestGate_DeniesEveryRoute proves the non-negotiable: EVERY /v1/admin/* route is
// SuperAdmin only, fail-closed. An anonymous caller and a tenant-admin (whose
// identity carries an org but NOT the sanitizer-minted X-User-IsAdmin) are BOTH
// denied 403 on every route — no upstream is even reached. admin mirrors the
// gateway's admin-guard: SanitizeIdentity sets X-User-IsAdmin only for a
// validated principal whose owner == AdminOrg, so a forged header never survives
// ingress and the c.IsAdmin() read here is authoritative.
func TestGate_DeniesEveryRoute(t *testing.T) {
	// Upstreams point nowhere reachable; the gate must reject BEFORE any call.
	do := mount(t, "http://127.0.0.1:0", "http://127.0.0.1:0", "http://127.0.0.1:0")

	// NO validated principal ⇒ denied on EVERY route (platform + scoped). guardScoped
	// requires a sanitized X-User-Id, which an anonymous caller lacks — and a client
	// that merely forges X-Org-Id (the documented Phase-1 residual) still has no
	// X-User-Id, so it is refused here and can never reach a scoped read.
	noPrincipal := []struct {
		name string
		hdr  map[string]string
	}{
		{"anonymous", nil},
		{"forged X-Org-Id, no validated user", map[string]string{"X-Org-Id": "victim"}},
	}
	for _, tc := range noPrincipal {
		for _, r := range adminRoutes {
			resp, body := do(r.method, r.path, tc.hdr)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s [%s]: got %d, want 403 (body=%s)", r.method, r.path, tc.name, resp.StatusCode, body)
			}
		}
	}

	// A VALIDATED org admin (X-User-Id + pinned X-Org-Id + the sanitizer-minted
	// X-User-IsOrgAdmin, but NO GLOBAL X-User-IsAdmin) is denied on every PLATFORM route
	// (super-only). The org-scoped routes admit them but hard-scope the data — proven in
	// scope_test.go. (A validated NON-admin member, lacking the org-admin bit, is refused
	// on the scoped panels too — TestScope_MemberWithoutOrgAdminDenied.)
	orgAdmin := map[string]string{"X-Org-Id": "acme", "X-User-Id": "acme/bob", "X-User-Email": "bob@acme.test", "X-User-IsOrgAdmin": "true"}
	for _, r := range platformAdminRoutes {
		resp, body := do(r.method, r.path, orgAdmin)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s [org-admin on platform route]: got %d, want 403 (body=%s)", r.method, r.path, resp.StatusCode, body)
		}
	}
}

// TestGate_AllowsSuperAdmin proves the flip side: a validated SuperAdmin
// (X-User-IsAdmin=true, minted only for owner==AdminOrg) is admitted — the gate
// is not vacuously closed. Reaches /v1/admin/me, which needs no upstream.
func TestGate_AllowsSuperAdmin(t *testing.T) {
	do := mount(t, "http://127.0.0.1:0", "http://127.0.0.1:0", "http://127.0.0.1:0")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin", "X-User-Id": "admin/z", "X-User-Email": "z@hanzo.ai"}
	resp, body := do("GET", "/v1/admin/me", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SuperAdmin GET /v1/admin/me: got %d, want 200 (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Status string  `json:"status"`
		Data   adminMe `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode me envelope: %v", err)
	}
	if env.Status != "ok" {
		t.Fatalf("me status = %q, want ok", env.Status)
	}
	if env.Data.Owner != "admin" || env.Data.Email != "z@hanzo.ai" || !env.Data.IsSuperAdmin {
		t.Errorf("me identity wrong: %+v", env.Data)
	}
	// SuperAdmin canonicalization: the isSuperAdmin key MUST be present and true
	// for a platform SuperAdmin.
	if !env.Data.IsSuperAdmin {
		t.Errorf("me: isSuperAdmin must be true for a SuperAdmin: %+v", env.Data)
	}
}

// fakeIAM stands in for the IAM management surface. It records whether the
// caller's credential was replayed and returns /v1 envelopes.
type fakeIAM struct {
	server  *httptest.Server
	gotAuth string
	gotCook string
}

func newFakeIAM() *fakeIAM {
	f := &fakeIAM{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gotAuth = r.Header.Get("Authorization")
		f.gotCook = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/get-organizations"):
			io.WriteString(w, `{"status":"ok","msg":"","data":[
				{"owner":"admin","name":"hanzo","displayName":"Hanzo","createdTime":"2020-01-01T00:00:00Z"},
				{"owner":"admin","name":"acme","displayName":"Acme Inc","createdTime":"2021-02-02T00:00:00Z"}
			],"data2":2}`)
		case strings.HasSuffix(r.URL.Path, "/get-users"):
			// A single-page count probe (pageSize=1) still reports data2 total.
			io.WriteString(w, `{"status":"ok","msg":"","data":[
				{"owner":"hanzo","name":"alice","email":"alice@hanzo.ai","displayName":"Alice","tag":"staff","createdTime":"2020-03-01T00:00:00Z","lastSigninTime":"2026-06-01T00:00:00Z","isAdmin":true,"isForbidden":false}
			],"data2":7}`)
		case strings.HasSuffix(r.URL.Path, "/get-roles"):
			io.WriteString(w, `{"status":"ok","msg":"","data":[{"owner":"admin","name":"ops","displayName":"Ops"}],"data2":1}`)
		case strings.HasSuffix(r.URL.Path, "/get-applications"):
			io.WriteString(w, `{"status":"ok","msg":"","data":[{"owner":"admin","name":"hanzo-cloud","clientId":"cid"}],"data2":1}`)
		case strings.HasSuffix(r.URL.Path, "/get-records"):
			io.WriteString(w, `{"status":"ok","msg":"","data":[{"createdTime":"2026-06-29T00:00:00Z","organization":"hanzo","user":"alice","clientIp":"1.2.3.4","method":"POST","action":"login","requestUri":"/v1/iam/login"}],"data2":1}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"status":"error","msg":"not found"}`)
		}
	}))
	return f
}

// fakeCommerce mimics the LIVE commerce billing contract (commerce >=1.46.8, the
// 2026-07 per-org durability rework): the per-org wallet is resolved from the
// TRUSTED X-Org-Id header (set only with the service-token bearer) and keyed under
// the BARE org slug as the `user` subject. A wrong header (X-IAM-Org-Id) or a wrong
// subject ("org/org") resolves to an EMPTY wallet — so this fake is a regression
// guard for the reconciliation bug that made every admin money panel read $0 while
// real balances existed (lux $10,000, maxpower $20,498). Verified against live
// commerce /v1/billing/{balance,usage-rollup}.
type fakeCommerce struct {
	server          *httptest.Server
	balances        map[string]int64 // org slug -> availableCents (credits)
	spend           map[string]int64 // org slug -> consumedCents (month-to-date)
	sawIAMOrgHeader bool             // true if the stale X-IAM-Org-Id header was ever sent
}

func newFakeCommerce() *fakeCommerce {
	f := &fakeCommerce{
		balances: map[string]int64{"acme": 5000, "hanzo": 5000},
		spend:    map[string]int64{"acme": 1500, "hanzo": 1500},
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-IAM-Org-Id") != "" {
			f.sawIAMOrgHeader = true
		}
		// Live commerce trusts ONLY X-Org-Id (with the service-token bearer) for the
		// org namespace and keys the wallet under the bare org slug. Anything else
		// (missing X-Org-Id, or user != org) resolves to an empty wallet.
		org := r.Header.Get("X-Org-Id")
		user := r.URL.Query().Get("user")
		bal, spend := int64(0), int64(0)
		if org != "" && user == org {
			bal, spend = f.balances[org], f.spend[org]
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/usage-rollup"):
			fmt.Fprintf(w, `{"consumedCents":%d,"overageCents":0,"balance":{"balanceCents":%d,"availableCents":%d}}`, spend, bal, bal)
		case strings.HasSuffix(r.URL.Path, "/balance"):
			fmt.Fprintf(w, `{"user":%q,"currency":"usd","balance":%d,"holds":0,"available":%d}`, user, bal, bal)
		case strings.HasSuffix(r.URL.Path, "/subscriptions"):
			io.WriteString(w, `{"subscriptions":[]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	return f
}

// TestCommerce_ReconcilesWithXOrgIdBareSlug pins the exact live-commerce contract
// the admin money aggregation depends on: the org selector is the TRUSTED X-Org-Id
// header and the wallet subject is the BARE org slug (user=<org>) — NOT
// X-IAM-Org-Id and NOT "org/org". This is the regression guard for the $0-fleet-
// revenue bug (commerce.go had X-IAM-Org-Id; admin.go orgSubject had "org/org", so
// every real balance read $0). /v1/admin/orgs must surface acme's real $50.00.
func TestCommerce_ReconcilesWithXOrgIdBareSlug(t *testing.T) {
	// The billing subject is the bare org slug for BOTH the X-Org-Id header and the
	// `user` param — commerce.Client bakes that in (one subject, no "org/org"). This
	// test proves it end to end: /v1/admin/orgs must surface acme's real $50.00.
	iam := newFakeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.server.Close()

	do := mount(t, iam.server.URL, commerce.server.URL, "")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}
	resp, body := do("GET", "/v1/admin/orgs", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("orgs: got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data []orgRow `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// acme (sorted first) must show its REAL money, proving the header + subject key.
	var acme *orgRow
	for i := range env.Data {
		if env.Data[i].Org == "acme" {
			acme = &env.Data[i]
		}
	}
	if acme == nil {
		t.Fatalf("acme org missing from %+v", env.Data)
	}
	if acme.CreditsCents != 5000 || acme.SpendCents != 1500 {
		t.Errorf("acme money = credits %d / spend %d, want 5000/1500 — the money did NOT reconcile (stale X-IAM-Org-Id or org/org subject reads $0)", acme.CreditsCents, acme.SpendCents)
	}
	// The stale header must NEVER be sent.
	if commerce.sawIAMOrgHeader {
		t.Error("admin sent the stale X-IAM-Org-Id header — commerce reads X-Org-Id only")
	}
}

// TestOrgs_RealAggregation drives /v1/admin/orgs against fake IAM + commerce and
// verifies the envelope, the field mapping, the per-org user count (from IAM
// data2), the money (from commerce), and that the caller's credential is
// replayed to IAM (admin never forges a service credential for the fan-out).
func TestOrgs_RealAggregation(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.server.Close()

	do := mount(t, iam.server.URL, commerce.server.URL, "")
	admin := map[string]string{
		"X-User-IsAdmin": "true", "X-Org-Id": "admin",
		"Authorization": "Bearer operator-jwt", "Cookie": "iam_access_token=operator-jwt",
	}
	resp, body := do("GET", "/v1/admin/orgs", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("orgs: got %d, want 200 (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Status string   `json:"status"`
		Data   []orgRow `json:"data"`
		Data2  int      `json:"data2"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Status != "ok" || env.Data2 != 2 || len(env.Data) != 2 {
		t.Fatalf("orgs envelope wrong: status=%q data2=%d rows=%d", env.Status, env.Data2, len(env.Data))
	}
	// Rows are sorted by org name: acme, hanzo.
	acme := env.Data[0]
	if acme.Org != "acme" || acme.Display != "Acme Inc" {
		t.Errorf("org row[0] = %+v, want acme/Acme Inc", acme)
	}
	if acme.Users != 7 {
		t.Errorf("org acme users = %d, want 7 (IAM data2)", acme.Users)
	}
	if acme.SpendCents != 1500 || acme.CreditsCents != 5000 {
		t.Errorf("org acme money = spend %d credits %d, want 1500/5000", acme.SpendCents, acme.CreditsCents)
	}
	// The operator's own credential MUST have been replayed to IAM.
	if iam.gotAuth != "Bearer operator-jwt" {
		t.Errorf("IAM did not receive the caller's Authorization: got %q", iam.gotAuth)
	}
	if !strings.Contains(iam.gotCook, "operator-jwt") {
		t.Errorf("IAM did not receive the caller's Cookie: got %q", iam.gotCook)
	}
}

// TestUsers_MapsIAMToOperatorUser verifies the cross-org directory mapping,
// including the derived isSuperAdmin (owner == adminOrg) and the data2 total.
func TestUsers_MapsIAMToOperatorUser(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	do := mount(t, iam.server.URL, "", "")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}

	resp, body := do("GET", "/v1/admin/users?org=hanzo", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("users: got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data  []operatorUser `json:"data"`
		Data2 int            `json:"data2"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data2 != 7 || len(env.Data) != 1 {
		t.Fatalf("users total=%d rows=%d, want 7/1", env.Data2, len(env.Data))
	}
	u := env.Data[0]
	if u.Name != "alice" || u.Email != "alice@hanzo.ai" || !u.IsAdmin || u.LastSignin == "" {
		t.Errorf("user mapping wrong: %+v", u)
	}
	// owner "hanzo" != adminOrg "admin" → not a SuperAdmin.
	if u.IsSuperAdmin {
		t.Errorf("user owner=hanzo must not be flagged SuperAdmin")
	}
}

// TestRolesAndApplications_PassthroughShape verifies the verbatim IAM passthrough
// keeps the exact wire fields (clientId on Application, etc.) the operator decodes.
func TestRolesAndApplications_PassthroughShape(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	do := mount(t, iam.server.URL, "", "")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}

	_, appsBody := do("GET", "/v1/admin/applications", admin)
	var appsEnv struct {
		Data []struct {
			Name     string `json:"name"`
			ClientId string `json:"clientId"`
		} `json:"data"`
		Data2 int `json:"data2"`
	}
	if err := json.Unmarshal(appsBody, &appsEnv); err != nil {
		t.Fatalf("apps decode: %v", err)
	}
	if len(appsEnv.Data) != 1 || appsEnv.Data[0].ClientId != "cid" {
		t.Errorf("applications passthrough lost clientId: %+v", appsEnv.Data)
	}

	_, rolesBody := do("GET", "/v1/admin/roles", admin)
	if !strings.Contains(string(rolesBody), `"ops"`) {
		t.Errorf("roles passthrough missing role name: %s", rolesBody)
	}
}

// TestAudit_MapsRecords verifies the audit directory returns the IAM Record wire
// shape the operator's AuditRow decodes.
func TestAudit_MapsRecords(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	do := mount(t, iam.server.URL, "", "")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}

	resp, body := do("GET", "/v1/admin/audit", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("audit: got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data []struct {
			CreatedTime  string `json:"createdTime"`
			Organization string `json:"organization"`
			RequestUri   string `json:"requestUri"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) != 1 || env.Data[0].Organization != "hanzo" || env.Data[0].RequestUri != "/v1/iam/login" {
		t.Errorf("audit record shape wrong: %+v", env.Data)
	}
}

// TestOverview_RealTilesAndSources verifies the Platform Overview: real org/user
// counts + money from the upstreams, and a per-source freshness row that reports
// the honest state of each feed (iam ok, commerce ok, o11y not-configured here).
func TestOverview_RealTilesAndSources(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.server.Close()

	do := mount(t, iam.server.URL, commerce.server.URL, "") // no o11y health → source not-ok
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}

	resp, body := do("GET", "/v1/admin/overview", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview: got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data overviewData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	d := env.Data
	if d.Orgs != 2 {
		t.Errorf("overview orgs = %d, want 2", d.Orgs)
	}
	// 2 orgs × 7 users each (both count probes return data2=7).
	if d.Users != 14 {
		t.Errorf("overview users = %d, want 14", d.Users)
	}
	// 2 orgs × 1500 consumed cents.
	if d.SpendCents30d != 3000 {
		t.Errorf("overview spend = %d, want 3000", d.SpendCents30d)
	}
	if d.CreditsCents != 10000 {
		t.Errorf("overview credits = %d, want 10000", d.CreditsCents)
	}
	if d.LastSync == "" {
		t.Error("overview lastSync must be set")
	}
	// Source freshness: iam ok, commerce ok, o11y not-ok (unconfigured).
	src := map[string]core.SourceStatus{}
	for _, s := range d.Sources {
		src[s.Name] = s
	}
	if !src["iam"].OK || src["iam"].Rows != 2 {
		t.Errorf("iam source = %+v, want ok/2 rows", src["iam"])
	}
	if !src["commerce"].OK {
		t.Errorf("commerce source = %+v, want ok", src["commerce"])
	}
	if src["o11y"].OK || src["o11y"].Error == "" {
		t.Errorf("o11y source must be not-ok with an error when unconfigured: %+v", src["o11y"])
	}
}

// TestOverview_CommercePartialOnPerOrgError proves the decomplected freshness rule: the
// commerce source is DEGRADED when ANY per-org money read fails — the fleet total is then
// an undercount and must NOT read healthy. Commerce succeeds for hanzo but 500s for acme;
// the overview folds acme's failure into a not-ok commerce source (the SAME partial
// pattern revenue/finance use) instead of the old single-probe that masked it.
func TestOverview_CommercePartialOnPerOrgError(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	commerce := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Org-Id") == "acme" {
			w.WriteHeader(500) // commerce down for THIS org only
			io.WriteString(w, `{"status":"error","msg":"commerce down for acme"}`)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/usage-rollup"):
			io.WriteString(w, `{"consumedCents":1500,"overageCents":0}`)
		case strings.HasSuffix(r.URL.Path, "/balance"):
			io.WriteString(w, `{"available":5000,"balance":5000}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer commerce.Close()

	do := mount(t, iam.server.URL, commerce.URL, "")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}
	resp, body := do("GET", "/v1/admin/overview", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview: %d (%s)", resp.StatusCode, body)
	}
	var env struct {
		Data overviewData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	src := map[string]core.SourceStatus{}
	for _, s := range env.Data.Sources {
		src[s.Name] = s
	}
	c, ok := src["commerce"]
	if !ok {
		t.Fatal("overview must report a commerce source")
	}
	if c.OK {
		t.Errorf("commerce source must be DEGRADED when a per-org read failed (undercount masked as healthy), got %+v", c)
	}
	if c.Error == "" {
		t.Errorf("degraded commerce source must carry an error: %+v", c)
	}
	// The healthy org still contributes — an honest PARTIAL total, never a hard panel fail.
	if env.Data.SpendCents30d != 1500 {
		t.Errorf("spend = %d, want 1500 (only hanzo read; acme failed)", env.Data.SpendCents30d)
	}
}

// TestUsage_RealTotalsHonestEmptySeries proves the usage roll-up returns the REAL
// fleet spend from commerce but an HONEST empty series/byProduct — the timeseries
// feed lives in insights/datastore, and admin must never fabricate a trend.
func TestUsage_RealTotalsHonestEmptySeries(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.server.Close()

	do := mount(t, iam.server.URL, commerce.server.URL, "")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}

	resp, body := do("GET", "/v1/admin/usage", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("usage: got %d (body=%s)", resp.StatusCode, body)
	}
	var env struct {
		Data usageData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Totals.SpendCents != 3000 { // 2 orgs × 1500
		t.Errorf("usage total spend = %d, want 3000", env.Data.Totals.SpendCents)
	}
	// Honest empty — NOT nil (the JSON must be [], which the operator renders as
	// an empty chart), and NEVER a fabricated point.
	if env.Data.Series == nil || len(env.Data.Series) != 0 {
		t.Errorf("usage series must be an empty array (no fabricated trend), got %v", env.Data.Series)
	}
	if env.Data.ByProduct == nil || len(env.Data.ByProduct) != 0 {
		t.Errorf("usage byProduct must be an empty array, got %v", env.Data.ByProduct)
	}
}

// TestProductsAndSync_HonestShapes verifies products returns the real empty
// registry (no fabricated workloads) and sync acknowledges with {started:true}.
func TestProductsAndSync_HonestShapes(t *testing.T) {
	do := mount(t, "", "", "")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}

	_, pBody := do("GET", "/v1/admin/products", admin)
	var pEnv struct {
		Data  []productRow `json:"data"`
		Data2 int          `json:"data2"`
	}
	if err := json.Unmarshal(pBody, &pEnv); err != nil {
		t.Fatalf("products decode: %v", err)
	}
	if pEnv.Data == nil || len(pEnv.Data) != 0 || pEnv.Data2 != 0 {
		t.Errorf("products must be an empty registry (no fabricated rows): %+v", pEnv)
	}

	_, sBody := do("POST", "/v1/admin/sync", admin)
	var sEnv struct {
		Status string          `json:"status"`
		Data   map[string]bool `json:"data"`
	}
	if err := json.Unmarshal(sBody, &sEnv); err != nil {
		t.Fatalf("sync decode: %v", err)
	}
	if sEnv.Status != "ok" || !sEnv.Data["started"] {
		t.Errorf("sync must ack {started:true}: %+v", sEnv)
	}
}

// TestIAMError_SurfacedNotFabricated proves a failing upstream yields a real
// error envelope (status:error), NOT a stubbed/zero success — the operator shows
// the error state, honoring the api.ts "nothing here fabricates data" contract.
func TestIAMError_SurfacedNotFabricated(t *testing.T) {
	// IAM that always 500s.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, `{"status":"error","msg":"iam boom"}`)
	}))
	defer bad.Close()

	do := mount(t, bad.URL, "", "")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}

	_, body := do("GET", "/v1/admin/orgs", admin)
	var env struct {
		Status string `json:"status"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Status != "error" || env.Msg == "" {
		t.Errorf("failing IAM must surface an error envelope, got %+v", env)
	}
}

// TestMount_NilGuards keeps the Mount contract honest (nil app / nil logger).
func TestMount_NilGuards(t *testing.T) {
	if err := Mount(nil, cloud.Deps{Logger: luxlog.New("test")}); err == nil {
		t.Error("Mount(nil app) must error")
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{}); err == nil {
		t.Error("Mount(nil logger) must error")
	}
}
