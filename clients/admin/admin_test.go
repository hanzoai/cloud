package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

// mount builds a zip app with admin mounted against the given upstream bases,
// and returns a `do` helper that issues test requests through the whole app.
func mount(t *testing.T, iamURL, commerceURL, healthURL string) func(method, path string, hdr map[string]string) (*http.Response, []byte) {
	do, _ := mountSvc(t, iamURL, commerceURL, healthURL)
	return do
}

// mountSvc is mount but also returns the underlying svc so finance tests can
// swap in a fake DigitalOcean client (s.do) before issuing a request. The
// handlers read s.do live at request time, so an override here takes effect.
func mountSvc(t *testing.T, iamURL, commerceURL, healthURL string) (func(method, path string, hdr map[string]string) (*http.Response, []byte), *svc) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	s := &svc{
		iam:      newIAMClient(iamURL),
		commerce: newCommerceClient(commerceURL, "test-token"),
		health:   newHealthClient(healthURL),
		do:       newDOClient(""), // no token → honest not-configured unless a test overrides s.do
		adminOrg: "admin",
	}
	app.Get("/v1/admin/me", s.guard(s.me))
	app.Get("/v1/admin/overview", s.guard(s.overview))
	app.Get("/v1/admin/orgs", s.guard(s.orgs))
	app.Get("/v1/admin/users", s.guard(s.users))
	app.Get("/v1/admin/roles", s.guard(s.roles))
	app.Get("/v1/admin/applications", s.guard(s.applications))
	app.Get("/v1/admin/audit", s.guard(s.audit))
	app.Get("/v1/admin/audit/verify", s.guard(s.auditVerify))
	app.Get("/v1/admin/usage", s.guard(s.usage))
	app.Get("/v1/admin/products", s.guard(s.products))
	app.Get("/v1/admin/finance", s.guard(s.finance))
	app.Post("/v1/admin/sync", s.guard(s.sync))
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
	}, s
}

// adminRoutes is every mounted /v1/admin route + its method — the full god-mode
// surface the gate must fail-close on for a non-global-admin.
var adminRoutes = []struct{ method, path string }{
	{"GET", "/v1/admin/me"},
	{"GET", "/v1/admin/overview"},
	{"GET", "/v1/admin/orgs"},
	{"GET", "/v1/admin/users"},
	{"GET", "/v1/admin/roles"},
	{"GET", "/v1/admin/applications"},
	{"GET", "/v1/admin/audit"},
	{"GET", "/v1/admin/audit/verify"},
	{"GET", "/v1/admin/usage"},
	{"GET", "/v1/admin/products"},
	{"GET", "/v1/admin/finance"},
	{"POST", "/v1/admin/sync"},
}

// TestGate_DeniesEveryRoute proves the non-negotiable: EVERY /v1/admin/* route is
// global-admin only, fail-closed. An anonymous caller and a tenant-admin (whose
// identity carries an org but NOT the sanitizer-minted X-User-IsAdmin) are BOTH
// denied 403 on every route — no upstream is even reached. admin mirrors the
// gateway's admin-guard: SanitizeIdentity sets X-User-IsAdmin only for a
// validated principal whose owner == AdminOrg, so a forged header never survives
// ingress and the c.IsAdmin() read here is authoritative.
func TestGate_DeniesEveryRoute(t *testing.T) {
	// Upstreams point nowhere reachable; the gate must reject BEFORE any call.
	do := mount(t, "http://127.0.0.1:0", "http://127.0.0.1:0", "http://127.0.0.1:0")

	cases := []struct {
		name string
		hdr  map[string]string
	}{
		{"anonymous", nil},
		{"tenant-admin (owner set, not global-admin)", map[string]string{"X-Org-Id": "acme"}},
		{"tenant-user with email but no admin", map[string]string{"X-Org-Id": "acme", "X-User-Id": "acme/bob", "X-User-Email": "bob@acme.test"}},
	}
	for _, tc := range cases {
		for _, r := range adminRoutes {
			resp, body := do(r.method, r.path, tc.hdr)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s %s [%s]: got %d, want 403 (body=%s)", r.method, r.path, tc.name, resp.StatusCode, body)
			}
		}
	}
}

// TestGate_AllowsGlobalAdmin proves the flip side: a validated global admin
// (X-User-IsAdmin=true, minted only for owner==AdminOrg) is admitted — the gate
// is not vacuously closed. Reaches /v1/admin/me, which needs no upstream.
func TestGate_AllowsGlobalAdmin(t *testing.T) {
	do := mount(t, "http://127.0.0.1:0", "http://127.0.0.1:0", "http://127.0.0.1:0")
	admin := map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "admin", "X-User-Id": "admin/z", "X-User-Email": "z@hanzo.ai"}
	resp, body := do("GET", "/v1/admin/me", admin)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("global-admin GET /v1/admin/me: got %d, want 200 (body=%s)", resp.StatusCode, body)
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
	if env.Data.Owner != "admin" || env.Data.Email != "z@hanzo.ai" || !env.Data.IsGlobalAdmin {
		t.Errorf("me identity wrong: %+v", env.Data)
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

// newFakeCommerce serves the billing reads with fixed cents so the money
// aggregation is deterministic.
func newFakeCommerce() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/usage-rollup"):
			io.WriteString(w, `{"consumedCents":1500,"overageCents":0,"balance":{"balanceCents":5000,"availableCents":5000}}`)
		case strings.HasSuffix(r.URL.Path, "/balance"):
			io.WriteString(w, `{"user":"x","currency":"usd","balance":5000,"holds":0,"available":5000}`)
		default:
			w.WriteHeader(404)
		}
	}))
}

// TestOrgs_RealAggregation drives /v1/admin/orgs against fake IAM + commerce and
// verifies the envelope, the field mapping, the per-org user count (from IAM
// data2), the money (from commerce), and that the caller's credential is
// replayed to IAM (admin never forges a service credential for the fan-out).
func TestOrgs_RealAggregation(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.Close()

	do := mount(t, iam.server.URL, commerce.URL, "")
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
// including the derived isGlobalAdmin (owner == adminOrg) and the data2 total.
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
	// owner "hanzo" != adminOrg "admin" → not a global admin.
	if u.IsGlobalAdmin {
		t.Errorf("user owner=hanzo must not be flagged global admin")
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
	defer commerce.Close()

	do := mount(t, iam.server.URL, commerce.URL, "") // no o11y health → source not-ok
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
	src := map[string]sourceStatus{}
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

// TestUsage_RealTotalsHonestEmptySeries proves the usage roll-up returns the REAL
// fleet spend from commerce but an HONEST empty series/byProduct — the timeseries
// feed lives in insights/datastore, and admin must never fabricate a trend.
func TestUsage_RealTotalsHonestEmptySeries(t *testing.T) {
	iam := newFakeIAM()
	defer iam.server.Close()
	commerce := newFakeCommerce()
	defer commerce.Close()

	do := mount(t, iam.server.URL, commerce.URL, "")
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
