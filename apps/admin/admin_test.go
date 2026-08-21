package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/commerce"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/digitalocean"
	"github.com/hanzoai/cloud/apps/admin/health"
	"github.com/hanzoai/cloud/apps/admin/iam"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// TestMain pins ONE plane runtime directory for this whole binary, before any test runs.
//
// It has to happen here and not in a stand-in. The plane WRITES ZIP_RUNTIME_DIR itself on
// its first call and an externally-set one always wins, so whichever test dials first
// decides where every later socket is looked for — and since the fleet money read became
// a plane call, that is now most of them. A stand-in that set the directory when it
// started would be setting it after that race was already lost: the platform stand-in
// began listening in a temp dir while the test looked for it under /run/hanzo.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pl")
	if err != nil {
		fmt.Fprintln(os.Stderr, "plane runtime dir:", err)
		os.Exit(1)
	}
	os.Setenv(zip.RuntimeDirEnv, dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

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
	compose(app)
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
	// `self` is the replica id Mount threads from Deps.Self; the harness pins it so the
	// /plugins board's Host is asserted against a known value rather than a hostname.
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
	{"GET", "/v1/admin/infra"},
	{"POST", "/v1/admin/infra/volumes/v1/snapshot"},
	{"DELETE", "/v1/admin/infra/volumes/v1"},
	{"POST", "/v1/admin/infra/nodes/1/cordon"},
	// /v1/admin/plugins is NOT here: clients/plugin owns that address, and its own
	// TestGate covers the same route for anonymous, tenant-admin and forged-header
	// callers. The coverage moved with the route rather than being dropped.
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

// TestUsers_DefaultsPagination locks the "0 of 222" fix in the terms the user list
// takes: a row LIMIT and an OFFSET, defaulted when the operator directory omits
// them, with IAM's real unpaged total reaching the client rather than the page
// length. It also pins the TRANSLATION — a page number is the console's spelling
// and never travels — so a client asking for page 1 does not send an offset and a
// client asking for page 3 sends the offset that page starts at.
func TestUsers_DefaultsPagination(t *testing.T) {
	var gotLimit, gotOffset string
	iamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/iam/users" {
			gotLimit = r.URL.Query().Get("limit")
			gotOffset = r.URL.Query().Get("offset")
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"users":[
				{"owner":"hanzo","name":"alice","email":"alice@hanzo.ai","displayName":"Alice"}
			],"total":222}`)
			return
		}
		w.WriteHeader(404)
		io.WriteString(w, `{"status":404,"error":"not found"}`)
	}))
	defer iamSrv.Close()

	do := mount(t, iamSrv.URL, "http://127.0.0.1:0", "http://127.0.0.1:0")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin", "X-User-Id": "admin/z", "X-User-Email": "z@hanzo.ai"}
	resp, body := do("GET", "/v1/admin/users?org=hanzo", admin) // NOTE: no ?pageSize — the bug path.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/admin/users: got %d (body=%s)", resp.StatusCode, body)
	}
	if gotLimit != "200" {
		t.Fatalf("users list must default limit=200 when the client omits it, got %q", gotLimit)
	}
	if gotOffset != "" {
		t.Fatalf("the first page starts at the beginning, so no offset travels; got %q", gotOffset)
	}
	var env struct {
		Data  []operatorUser `json:"data"`
		Total int            `json:"total"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode users envelope: %v (body=%s)", err, body)
	}
	if env.Total != 222 || len(env.Data) == 0 {
		t.Fatalf("users must surface the REAL directory (got %d rows, total %d), not 0-of-222", len(env.Data), env.Total)
	}

	if resp, body := do("GET", "/v1/admin/users?org=hanzo&p=3&pageSize=50", admin); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/admin/users paged: got %d (body=%s)", resp.StatusCode, body)
	}
	if gotLimit != "50" || gotOffset != "100" {
		t.Fatalf("page 3 at 50 rows = limit 50 offset 100, got limit=%q offset=%q", gotLimit, gotOffset)
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
		case r.URL.Path == "/v1/iam/organizations":
			io.WriteString(w, `{"organizations":[
				{"owner":"admin","name":"hanzo","displayName":"Hanzo","createdTime":"2020-01-01T00:00:00Z"},
				{"owner":"admin","name":"acme","displayName":"Acme Inc","createdTime":"2021-02-02T00:00:00Z"}
			],"count":2}`)
		case r.URL.Path == "/v1/iam/users":
			// The roster is OWNER-SCOPED — that is the tenancy boundary IAM enforces —
			// so the directory asks each org and this answers each with its own rows and
			// its own total. Three belong to hanzo and four to acme, which is the only
			// shape that can tell a real per-org count from a fleet total handed to
			// every row. An unscoped read is refused here exactly as IAM refuses it.
			switch r.URL.Query().Get("owner") {
			case "hanzo":
				io.WriteString(w, `{"users":[
					{"owner":"hanzo","name":"alice","email":"alice@hanzo.ai","displayName":"Alice","tag":"staff","createdTime":"2020-03-01T00:00:00Z","lastSigninTime":"2026-06-01T00:00:00Z","isAdmin":true,"isForbidden":false},
					{"owner":"hanzo","name":"bob","email":"bob@hanzo.ai","displayName":"Bob"},
					{"owner":"hanzo","name":"cara","email":"cara@hanzo.ai","displayName":"Cara"}
				],"total":3}`)
			case "acme":
				io.WriteString(w, `{"users":[
					{"owner":"acme","name":"dan","email":"dan@acme.com","displayName":"Dan"},
					{"owner":"acme","name":"eve","email":"eve@acme.com","displayName":"Eve"},
					{"owner":"acme","name":"finn","email":"finn@acme.com","displayName":"Finn"},
					{"owner":"acme","name":"gus","email":"gus@acme.com","displayName":"Gus"}
				],"total":4}`)
			default:
				w.WriteHeader(400)
				io.WriteString(w, `{"status":400,"error":"owner is required"}`)
			}
		case r.URL.Path == "/v1/iam/roles":
			io.WriteString(w, `{"roles":[{"owner":"admin","name":"ops","displayName":"Ops"}],"total":1}`)
		case r.URL.Path == "/v1/iam/applications":
			io.WriteString(w, `{"applications":[{"owner":"admin","name":"hanzo-cloud","clientId":"cid"}]}`)
		case r.URL.Path == "/v1/iam/audit-logs":
			io.WriteString(w, `{"auditLogs":[{"createdTime":"2026-06-29T00:00:00Z","organization":"hanzo","user":"alice","clientIp":"1.2.3.4","method":"POST","action":"login","requestUri":"/v1/iam/login"}],"total":1}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"status":404,"error":"not found"}`)
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
// commerce /v1/billing/{balance,usage/rollup}.
type fakeCommerce struct {
	server          *httptest.Server
	balances        map[string]int64 // org slug -> availableCents (credits)
	spend           map[string]int64 // org slug -> consumedCents (month-to-date)
	sawIAMOrgHeader bool             // true if the stale X-IAM-Org-Id header was ever sent
	mu              sync.Mutex
	n               int // requests served, so a test can assert it was NOT asked
}

// calls reports how many requests this stand-in has served.
func (f *fakeCommerce) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func newFakeCommerce() *fakeCommerce {
	f := &fakeCommerce{
		balances: map[string]int64{"acme": 5000, "hanzo": 5000},
		spend:    map[string]int64{"acme": 1500, "hanzo": 1500},
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		f.n++
		f.mu.Unlock()
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
		case strings.HasSuffix(r.URL.Path, "/usage/rollup"):
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

// fleetMoney is the money peer the fan-out tests bill against: acme and hanzo each hold
// $50.00 with $15.00 consumed. It also RECORDS which tenant each call named, which is the
// property that replaced the old header contract — see TestMoney_NamesTheTenant.
func fleetMoney(seen *[]string) func(string) (int64, int64, error) {
	var mu sync.Mutex
	return func(org string) (int64, int64, error) {
		mu.Lock()
		defer mu.Unlock()
		*seen = append(*seen, org)
		switch org {
		case "acme", "hanzo":
			return 1500, 5000, nil
		}
		return 0, 0, nil
	}
}

// TestMoney_NamesTheTenant pins the tenancy rule the fleet money read depends on: the org
// travels ON THE CALL, so the ledger answers for the tenant that was named and one tenant
// can never be shown another's books. It replaces a test that pinned the same property in
// HTTP header terms (X-Org-Id, and the wallet subject as the bare slug) — the read is a
// plane call now, and there is no header to get wrong. /v1/admin/orgs must still surface
// acme's real $50.00.
func TestMoney_NamesTheTenant(t *testing.T) {
	var seen []string
	serveCommerce(t, fleetMoney(&seen))
	iam := newFakeIAM()
	defer iam.server.Close()

	do := mount(t, iam.server.URL, "", "")
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
	if acme.CreditsCents != 5000 {
		t.Errorf("acme credits = %d, want 5000 — the money did not reconcile", acme.CreditsCents)
	}
	// Every org in the window was asked for BY NAME. An unnamed call would read the
	// caller's own books and report them as the tenant's.
	if len(seen) != 2 || !slices.Contains(seen, "acme") || !slices.Contains(seen, "hanzo") {
		t.Errorf("money was asked for %v, want each tenant named once", seen)
	}
}

// TestOrgs_RealAggregation drives /v1/admin/orgs against fake IAM + commerce and
// verifies the envelope, the field mapping, the per-org user count (from IAM
// total), the money (from commerce), and that the caller's credential is
// replayed to IAM (admin never forges a service credential for the fan-out).
func TestOrgs_RealAggregation(t *testing.T) {
	var seen []string
	serveCommerce(t, fleetMoney(&seen))
	iam := newFakeIAM()
	defer iam.server.Close()

	do := mount(t, iam.server.URL, "", "")
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
		Total  int      `json:"total"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Status != "ok" || env.Total != 2 || len(env.Data) != 2 {
		t.Fatalf("orgs envelope wrong: status=%q total=%d rows=%d", env.Status, env.Total, len(env.Data))
	}
	// Rows are sorted by org name: acme, hanzo.
	acme := env.Data[0]
	if acme.Org != "acme" || acme.Display != "Acme Inc" {
		t.Errorf("org row[0] = %+v, want acme/Acme Inc", acme)
	}
	if acme.Users != 4 {
		t.Errorf("org acme users = %d, want 4 (acme's OWN members, not the fleet total)", acme.Users)
	}
	// Credits are the wallet, read from the money plane. Spend and tokens are the AI
	// LEDGER — a different plane with a different owner — so with no warehouse wired
	// they are a true zero here, and the money read cannot make them look otherwise.
	if acme.CreditsCents != 5000 {
		t.Errorf("org acme credits = %d, want 5000", acme.CreditsCents)
	}
	if acme.SpendCents != 0 || acme.Tokens != 0 {
		t.Errorf("org acme usage = spend %d tokens %d, want 0/0 with no warehouse", acme.SpendCents, acme.Tokens)
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
// including the derived isSuperAdmin (owner == adminOrg) and the full total.
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
		Total int            `json:"total"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The read named hanzo, so it is hanzo's THREE — not the seven the fleet holds.
	// A directory that answered 7 to a request naming one org would be showing the
	// operator another tenant's people under that tenant's heading.
	if env.Total != 3 || len(env.Data) != 3 {
		t.Fatalf("users total=%d rows=%d, want 3/3 for org=hanzo", env.Total, len(env.Data))
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
		Total int `json:"total"`
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
	var seen []string
	serveCommerce(t, fleetMoney(&seen))
	iam := newFakeIAM()
	defer iam.server.Close()

	do := mount(t, iam.server.URL, "", "") // no o11y health → source not-ok
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
	// Seven members across the two orgs — three in hanzo, four in acme. Summing a real
	// per-org count is the tile; it is not orgs x the fleet total.
	if d.Users != 7 {
		t.Errorf("overview users = %d, want 7 (3 hanzo + 4 acme)", d.Users)
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
	// The AI tiles read the usage ledger, and an unwired warehouse is a source that is
	// DOWN — not a fleet that served nothing. A zero tile beside a healthy source is the
	// exact shape that let $0.00 and 0 tokens look like the truth for a month.
	if src["usage"].OK || src["usage"].Error == "" {
		t.Errorf("usage source must be not-ok with an error when no warehouse is wired: %+v", src["usage"])
	}
	if d.SpendCents30d != 0 || d.Tokens30d != 0 {
		t.Errorf("no warehouse must read as zero AI spend/tokens, got %d/%d", d.SpendCents30d, d.Tokens30d)
	}
}

// TestOverview_CommercePartialOnPerOrgError proves the decomplected freshness rule: the
// commerce source is DEGRADED when ANY per-org money read fails — the fleet total is then
// an undercount and must NOT read healthy. Commerce succeeds for hanzo but 500s for acme;
// the overview folds acme's failure into a not-ok commerce source (the SAME partial
// pattern revenue/finance use) instead of the old single-probe that masked it.
func TestOverview_CommercePartialOnPerOrgError(t *testing.T) {
	serveCommerce(t, func(org string) (int64, int64, error) {
		if org == "acme" {
			return 0, 0, fmt.Errorf("ledger down for acme") // this tenant only
		}
		return 1500, 5000, nil
	})
	iam := newFakeIAM()
	defer iam.server.Close()

	do := mount(t, iam.server.URL, "", "")
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
	if env.Data.CreditsCents != 5000 {
		t.Errorf("credits = %d, want 5000 (only hanzo read; acme failed)", env.Data.CreditsCents)
	}
}

// TestUsage_ReadsTheLedgerNotCommerce proves the usage board asks the AI ledger. With no
// warehouse wired it reports zeros and EMPTY arrays — never nil, which the console would
// read as an absent field, and never a fabricated trend. The point of the test is the
// second assertion: commerce is not consulted at all, because "what was served" was never
// commerce's question. It used to fan out one HTTP read per org to answer it, against a
// route that does not exist.
func TestUsage_ReadsTheLedgerNotCommerce(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.server.Close()

	do := mount(t, iam.server.URL, commerce.server.URL, "")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}

	before := commerce.calls()
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
	if n := commerce.calls() - before; n != 0 {
		t.Errorf("usage made %d commerce calls; the ledger owns this question", n)
	}
	if env.Data.Totals != (usageTotals{}) {
		t.Errorf("no warehouse must read as zeros, got %+v", env.Data.Totals)
	}
	// Honest empty — NOT nil (the JSON must be [], which the operator renders as
	// an empty chart), and NEVER a fabricated point.
	if env.Data.Series == nil || len(env.Data.Series) != 0 {
		t.Errorf("usage series must be an empty array (no fabricated trend), got %v", env.Data.Series)
	}
	if env.Data.ByModel == nil || len(env.Data.ByModel) != 0 {
		t.Errorf("usage byModel must be an empty array, got %v", env.Data.ByModel)
	}
}

// TestProductsAndSync_HonestShapes verifies products returns the real empty
// registry (no fabricated workloads) and sync acknowledges with {started:true}.
func TestProductsAndSync_HonestShapes(t *testing.T) {
	servePlatformEmpty(t)
	do := mount(t, "", "", "")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin"}

	_, pBody := do("GET", "/v1/admin/products", admin)
	var pEnv struct {
		Data  []productRow `json:"data"`
		Total int          `json:"total"`
	}
	if err := json.Unmarshal(pBody, &pEnv); err != nil {
		t.Fatalf("products decode: %v", err)
	}
	if pEnv.Data == nil || len(pEnv.Data) != 0 || pEnv.Total != 0 {
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
	if err := Mount(nil, cloud.Deps{}); err == nil {
		t.Error("Mount(nil app) must error")
	}
}

// serveCommerce stands up the commerce app answering plane.FinanceSpend, which is how
// the fleet boards ask for money now. `by` decides each org's answer, so a test can make
// ONE tenant fail and assert the fold reports a partial rather than an undercount that
// reads healthy. Without a peer at all the router says ErrNoPeer, which is a different
// and equally real fact — see TestOverview_HonestSources.
func serveCommerce(t *testing.T, by func(org string) (int64, int64, error)) {
	t.Helper()
	// The plane's own runtime directory, resolved the way cloud resolves it, so this
	// process's plane app IS the peer a money read reaches by name. Same three lines
	// the cockpit's stand-in uses — the arrangement production has.
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", planeRunDir(t))
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	zip.Post[plane.SpendIn, plane.Spend](cloud.Plane(), "/finance/spend",
		func(ctx context.Context, _ *plane.SpendIn) (*plane.Spend, error) {
			org := cloud.Who(ctx).Org
			if org == "" {
				return nil, zip.ErrForbidden("spend: no org on the call")
			}
			spend, balance, err := by(org)
			if err != nil {
				return nil, err
			}
			return &plane.Spend{
				Consumed: plane.Amount(money.FromCents(spend).Unwrap()),
				Balance:  plane.Amount(money.FromCents(balance).Unwrap()),
			}, nil
		}, zip.WithOperationID(plane.FinanceSpend))

	// Subscriptions, on the same stand-in: the plan read left HTTP for the plane,
	// and two apps answering "commerce" would shadow each other. One active $50/mo
	// sub per org — the figure the HTTP fixture served.
	zip.Post[plane.SubsIn, plane.Subs](cloud.Plane(), "/finance/subs",
		func(ctx context.Context, _ *plane.SubsIn) (*plane.Subs, error) {
			if cloud.Who(ctx).Org == "" {
				return nil, zip.ErrForbidden("subs: no org on the call")
			}
			return &plane.Subs{Rows: []plane.Sub{
				{Status: "active", MRRCents: 5000, PlanName: "Pro"},
			}}, nil
		}, zip.WithOperationID(plane.FinanceSubs))

	// The vendor COGS god-view, on the same stand-in: DO compute $3,000 + OpenAI
	// $500 = $3,500, the multi-vendor figure the boards fold. Two lines, because a
	// single line cannot show that the fold sums them rather than reporting the
	// first — which is the bug a one-vendor fixture would hide.
	zip.Post[plane.CostsIn, plane.Costs](cloud.Plane(), "/finance/costs",
		func(ctx context.Context, in *plane.CostsIn) (*plane.Costs, error) {
			if cloud.Who(ctx).Org == "" {
				return nil, zip.ErrForbidden("costs: no org on the call")
			}
			period := ""
			if in != nil {
				period = in.Period
			}
			return &plane.Costs{
				Period:   period,
				Currency: "usd",
				Vendors: []plane.VendorCost{
					{Vendor: "digitalocean", Service: "compute", AmountCents: 300_000,
						Period: period, Source: "actual", Currency: "usd"},
					{Vendor: "openai", Service: "llm-inference", AmountCents: 50_000,
						Period: period, Source: "actual", Currency: "usd"},
				},
				TotalCents: 350_000,
			}, nil
		}, zip.WithOperationID(plane.FinanceCosts))

	// Bind the canonical socket for the name, so a read that asks for "commerce"
	// reaches this process's plane. Without it the router answers ErrNoPeer, which
	// the boards read — correctly — as "this deployment runs no commerce".
	stop, err := cloud.ServePlane("commerce", luxlog.NewNoOpLogger())
	if err != nil {
		t.Fatalf("serve commerce plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
}

// planeRunDir is a directory short enough to bind a socket in. t.TempDir cannot be
// used: a unix socket address is 104 bytes and t.TempDir spends most of them on the
// test's own name, so a descriptive test name fails to bind.
func planeRunDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pl")
	if err != nil {
		t.Fatalf("plane runtime dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// servePlatformEmpty stands up the platform app answering an EMPTY fleet.
//
// The board reads the operator's view over the internal plane, so without a
// platform to ask, /v1/admin/products reports that it could not reach one —
// which is right, and is a different fact from "the estate is empty". This test
// is about the second: an observer with nothing to report must render an empty
// registry and never fabricate a row.
func servePlatformEmpty(t *testing.T) {
	t.Helper()
	app := zip.New(zip.Config{AppName: "platform"})
	compose(app)
	zip.Post[struct{}, plane.Fleet](app, "/platform/fleet",
		func(context.Context, *struct{}) (*plane.Fleet, error) {
			return &plane.Fleet{}, nil
		}, zip.WithOperationID(plane.PlatformFleet))
	go func() { _ = app.Listen(zip.SocketPath("platform")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for range 200 {
		if c, derr := net.Dial("unix", zip.SocketPath("platform")); derr == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("platform stand-in never began listening at %s", zip.SocketPath("platform"))
}
