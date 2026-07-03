package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hanzoai/cloud/audit"
)

// ── rich stateful fakes for the customer-management surfaces ──────────────────

// cockpitFakes bundles a stateful IAM + commerce fake and the mounted `do` helper,
// exposing the recorded state (forbidden flips, deposits) tests assert on.
type cockpitFakes struct {
	iam      *httptest.Server
	commerce *httptest.Server
	svc      *svc
	do       func(method, path string, hdr map[string]string, body string) (*http.Response, []byte)

	mu          sync.Mutex
	forbidden   map[string]bool  // "owner/name" -> forbidden (mutated by update-user)
	updateCalls []string         // ids passed to update-user
	deposits    []depositCapture // deposits commerce received
	balances    map[string]int64 // org -> availableCents (mutated by deposit)
}

type depositCapture struct {
	org    string
	user   string
	amount int64
}

// adminHdr is a validated global-admin identity (what SanitizeIdentity mints for
// owner==AdminOrg) plus a replayable credential.
func adminHdr() map[string]string {
	return map[string]string{
		"X-User-IsAdmin": "true", "X-Org-Id": "admin", "X-User-Id": "admin/z", "X-User-Email": "z@hanzo.ai",
		"Authorization": "Bearer operator-jwt", "Cookie": "iam_access_token=operator-jwt",
	}
}

// newCockpitFakes builds the stateful fleet: orgs acme + globex (owned by admin),
// with users, balances, subscriptions, and a dated usage ledger — all relative to
// `now` so the analytics windows are stable whenever the test runs.
func newCockpitFakes(t *testing.T) *cockpitFakes {
	t.Helper()
	now := time.Now().UTC()
	f := &cockpitFakes{
		forbidden: map[string]bool{},
		balances:  map[string]int64{"acme": 20000, "globex": 5000},
	}
	spend := map[string]int64{"acme": 1500, "globex": 300}

	// Signup + usage dates relative to now so analytics windows include them.
	acmeCreated := now.AddDate(0, 0, -45).Format(time.RFC3339)
	globexCreated := now.AddDate(0, 0, -20).Format(time.RFC3339)
	usage := map[string][]txn{
		"acme": {
			{ID: "t1", Type: "withdraw", Amount: 100, Currency: "usd", CreatedAt: now.AddDate(0, 0, -40).Format(time.RFC3339)},
			{ID: "t2", Type: "withdraw", Amount: 200, Currency: "usd", CreatedAt: now.AddDate(0, 0, -5).Format(time.RFC3339)},
			{ID: "t3", Type: "deposit", Amount: 20000, Currency: "usd", CreatedAt: now.AddDate(0, 0, -46).Format(time.RFC3339)},
		},
		"globex": {
			{ID: "t4", Type: "withdraw", Amount: 400, Currency: "usd", CreatedAt: now.AddDate(0, 0, -3).Format(time.RFC3339)},
		},
	}
	// users per org (owner/name): forbidden read live from f.forbidden.
	type u struct{ owner, name, email, key string; admin bool }
	users := map[string][]u{
		"acme":   {{"acme", "anna", "anna@acme.test", "hk-anna-secret", true}, {"acme", "bob", "bob@acme.test", "", false}},
		"globex": {{"globex", "gwen", "gwen@globex.test", "hk-gwen-secret", true}},
	}

	f.iam = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		q := r.URL.Query()
		switch {
		case strings.HasSuffix(r.URL.Path, "/get-organizations"):
			fmt.Fprintf(w, `{"status":"ok","msg":"","data":[
				{"owner":"admin","name":"acme","displayName":"Acme Inc","createdTime":%q},
				{"owner":"admin","name":"globex","displayName":"Globex","createdTime":%q}
			],"data2":2}`, acmeCreated, globexCreated)
		case strings.HasSuffix(r.URL.Path, "/get-users"):
			owner := q.Get("owner")
			rows := []string{}
			for _, us := range users[owner] {
				f.mu.Lock()
				forb := f.forbidden[us.owner+"/"+us.name]
				f.mu.Unlock()
				created := acmeCreated
				if owner == "globex" {
					created = globexCreated
				}
				rows = append(rows, fmt.Sprintf(`{"owner":%q,"name":%q,"email":%q,"isAdmin":%v,"isForbidden":%v,"accessKey":%q,"createdTime":%q,"lastSigninTime":%q}`,
					us.owner, us.name, us.email, us.admin, forb, us.key, created, now.AddDate(0, 0, -2).Format(time.RFC3339)))
			}
			fmt.Fprintf(w, `{"status":"ok","msg":"","data":[%s],"data2":%d}`, strings.Join(rows, ","), len(rows))
		case strings.HasSuffix(r.URL.Path, "/get-user"):
			id := q.Get("id")
			parts := strings.SplitN(id, "/", 2)
			owner := ""
			if len(parts) == 2 {
				owner = parts[0]
			}
			for _, us := range users[owner] {
				if us.owner+"/"+us.name == id {
					f.mu.Lock()
					forb := f.forbidden[id]
					f.mu.Unlock()
					// Full object incl. fields update-user must preserve.
					fmt.Fprintf(w, `{"status":"ok","msg":"","data":{"owner":%q,"name":%q,"email":%q,"isAdmin":%v,"isForbidden":%v,"accessKey":%q,"displayName":"X","phone":"","type":"normal-user"}}`,
						us.owner, us.name, us.email, us.admin, forb, us.key)
					return
				}
			}
			w.WriteHeader(404)
			io.WriteString(w, `{"status":"error","msg":"not found"}`)
		case strings.HasSuffix(r.URL.Path, "/update-user"):
			id := q.Get("id")
			body, _ := io.ReadAll(r.Body)
			var obj map[string]any
			_ = json.Unmarshal(body, &obj)
			forb, _ := obj["isForbidden"].(bool)
			f.mu.Lock()
			f.forbidden[id] = forb
			f.updateCalls = append(f.updateCalls, id)
			f.mu.Unlock()
			io.WriteString(w, `{"status":"ok","msg":"","data":"Affected"}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"status":"error","msg":"not found"}`)
		}
	}))

	f.commerce = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		org := r.Header.Get("X-Org-Id")
		q := r.URL.Query()
		user := q.Get("user")
		f.mu.Lock()
		bal := f.balances[org]
		f.mu.Unlock()
		sp := int64(0)
		if org != "" && user == org {
			sp = spend[org]
		} else {
			bal = 0 // wrong subject/namespace → empty wallet (the live contract)
		}
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/deposit"):
			var req struct {
				User     string `json:"user"`
				Amount   int64  `json:"amount"`
				Currency string `json:"currency"`
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &req)
			f.mu.Lock()
			f.balances[org] += req.Amount
			f.deposits = append(f.deposits, depositCapture{org: org, user: req.User, amount: req.Amount})
			f.mu.Unlock()
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"transactionId":"dep-%d","user":%q,"amount":%d,"currency":%q,"type":"deposit"}`, req.Amount, req.User, req.Amount, req.Currency)
		case strings.HasSuffix(r.URL.Path, "/usage-rollup"):
			fmt.Fprintf(w, `{"consumedCents":%d,"overageCents":0,"balance":{"balanceCents":%d,"availableCents":%d}}`, sp, bal, bal)
		case strings.HasSuffix(r.URL.Path, "/balance"):
			fmt.Fprintf(w, `{"user":%q,"currency":"usd","available":%d,"balance":%d}`, user, bal, bal)
		case strings.HasSuffix(r.URL.Path, "/subscriptions"):
			if org == "acme" && user == "acme" {
				io.WriteString(w, `{"subscriptions":[{"status":"active","plan":{"name":"Pro","price":5000,"currency":"usd","interval":"month"}}]}`)
			} else {
				io.WriteString(w, `{"subscriptions":[]}`)
			}
		case strings.HasSuffix(r.URL.Path, "/transactions"):
			// Commerce serves the ledger WRAPPED as { count, transactions:[...] }
			// (the live contract) — the fake mirrors it so the decode is guarded
			// against the real shape, not a bare array a mock would let pass.
			rows := usage[org]
			b, _ := json.Marshal(map[string]any{"count": len(rows), "transactions": rows})
			w.Write(b)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"status":"error","msg":"not found"}`)
		}
	}))

	_, s, fa := mountSvc(t, f.iam.URL, f.commerce.URL, "")
	f.svc = s
	f.do = func(method, path string, hdr map[string]string, body string) (*http.Response, []byte) {
		t.Helper()
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rdr)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		bb, _ := io.ReadAll(resp.Body)
		return resp, bb
	}
	t.Cleanup(func() { f.iam.Close(); f.commerce.Close() })
	return f
}

// TestCustomers_ListRealFleet proves the fleet customer list is real: every field
// (owner email, plan, balance, spend, MRR, status, user count) comes from the live
// IAM + commerce upstreams.
func TestCustomers_ListRealFleet(t *testing.T) {
	f := newCockpitFakes(t)
	resp, body := f.do("GET", "/v1/admin/customers", adminHdr(), "")
	if resp.StatusCode != 200 {
		t.Fatalf("customers: %d (%s)", resp.StatusCode, body)
	}
	var env struct {
		Data  []customerRow `json:"data"`
		Data2 int           `json:"data2"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data2 != 2 || len(env.Data) != 2 {
		t.Fatalf("want 2 customers, got %d (%+v)", len(env.Data), env.Data)
	}
	acme := env.Data[0] // sorted: acme, globex
	if acme.Org != "acme" || acme.OwnerEmail != "anna@acme.test" || acme.Plan != "Pro" {
		t.Errorf("acme identity wrong: %+v", acme)
	}
	if acme.BalanceCents != 20000 || acme.SpendCents != 1500 || acme.MRRCents != 5000 {
		t.Errorf("acme money wrong: bal=%d spend=%d mrr=%d", acme.BalanceCents, acme.SpendCents, acme.MRRCents)
	}
	if acme.Users != 2 || acme.Status != "active" {
		t.Errorf("acme users/status wrong: users=%d status=%s", acme.Users, acme.Status)
	}
}

// TestCustomerDetail_RealAndNoSecretLeak proves the detail is real AND that the
// hk- access key VALUE never appears in the response (presence only).
func TestCustomerDetail_RealAndNoSecretLeak(t *testing.T) {
	f := newCockpitFakes(t)
	resp, body := f.do("GET", "/v1/admin/customers/acme", adminHdr(), "")
	if resp.StatusCode != 200 {
		t.Fatalf("detail: %d (%s)", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "hk-anna-secret") {
		t.Fatalf("SECRET LEAK: the access key value appears in the customer detail response")
	}
	var env struct {
		Data customerDetail `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	d := env.Data
	if d.Org != "acme" || d.Plan != "Pro" || d.BalanceCents != 20000 || d.MRRCents != 5000 {
		t.Errorf("detail money/plan wrong: %+v", d)
	}
	// anna has a key, bob does not → apiKeys count = 1.
	if d.APIKeys != 1 {
		t.Errorf("apiKeys = %d, want 1", d.APIKeys)
	}
	if len(d.Users) != 2 {
		t.Fatalf("want 2 users, got %d", len(d.Users))
	}
	// The users carry hasApiKey (presence) but NO key value field exists in the type.
	var anna *customerUser
	for i := range d.Users {
		if d.Users[i].Name == "anna" {
			anna = &d.Users[i]
		}
	}
	if anna == nil || !anna.HasAPIKey || !anna.IsAdmin {
		t.Errorf("anna mapping wrong: %+v", anna)
	}
	if len(d.Transactions) == 0 {
		t.Error("detail must include the real ledger transactions")
	}
}

// TestGrantCredit_DepositLandsAndAudited proves grant credit is a REAL commerce
// deposit (right org/subject) reflected in the balance AND recorded to the
// tamper-evident audit trail with a before/after.
func TestGrantCredit_DepositLandsAndAudited(t *testing.T) {
	f := newCockpitFakes(t)
	rec, err := audit.Open(":memory:", nil)
	if err != nil {
		t.Fatalf("audit open: %v", err)
	}
	defer rec.Close()
	f.svc.auditStore = rec

	resp, body := f.do("POST", "/v1/admin/customers/acme/credit", adminHdr(), `{"amountCents":5000,"reason":"support comp"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("credit: %d (%s)", resp.StatusCode, body)
	}
	var env struct {
		Status string `json:"status"`
		Data   struct {
			GrantedCents  int64  `json:"grantedCents"`
			BalanceCents  int64  `json:"balanceCents"`
			TransactionID string `json:"transactionId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Status != "ok" || env.Data.GrantedCents != 5000 {
		t.Fatalf("grant envelope wrong: %+v", env)
	}
	// The balance reflects the grant (20000 + 5000).
	if env.Data.BalanceCents != 25000 {
		t.Errorf("balance after grant = %d, want 25000", env.Data.BalanceCents)
	}
	// Commerce received a deposit for the RIGHT org + subject (X-Org-Id=acme, user=acme).
	f.mu.Lock()
	deps := append([]depositCapture(nil), f.deposits...)
	f.mu.Unlock()
	if len(deps) != 1 || deps[0].org != "acme" || deps[0].user != "acme" || deps[0].amount != 5000 {
		t.Fatalf("deposit not landed on the right subject: %+v", deps)
	}
	// The action is on the tamper-evident trail with a before/after balance.
	rows, total, err := rec.Query(context.Background(), audit.Filter{Action: "admin.customer.credit"})
	if err != nil || total < 1 || len(rows) < 1 {
		t.Fatalf("credit not audited: total=%d err=%v", total, err)
	}
	r := rows[0]
	if r.Actor.Org != "admin" || r.Outcome.Result != "success" || r.Resource.ID != "acme" {
		t.Errorf("audit record wrong: %+v", r)
	}
	if !strings.Contains(string(r.Before), "balanceCents") || !strings.Contains(string(r.After), "grantedCents") {
		t.Errorf("audit before/after missing: before=%s after=%s", r.Before, r.After)
	}
}

// TestGrantCredit_Validation proves the guardrails (positive amount, cap, real org).
func TestGrantCredit_Validation(t *testing.T) {
	f := newCockpitFakes(t)
	cases := []struct {
		name, org, body string
		wantStatus      int
		wantErr         bool
	}{
		{"zero amount", "acme", `{"amountCents":0}`, 200, true},
		{"negative", "acme", `{"amountCents":-100}`, 200, true},
		{"over cap", "acme", `{"amountCents":999999999}`, 200, true},
		{"unknown org", "nope", `{"amountCents":100}`, 404, true},
	}
	for _, tc := range cases {
		resp, body := f.do("POST", "/v1/admin/customers/"+tc.org+"/credit", adminHdr(), tc.body)
		if resp.StatusCode != tc.wantStatus {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, resp.StatusCode, tc.wantStatus, body)
		}
		if tc.wantErr && !strings.Contains(string(body), `"error"`) {
			t.Errorf("%s: expected error envelope, got %s", tc.name, body)
		}
	}
	// No deposit should have landed for any invalid grant.
	f.mu.Lock()
	n := len(f.deposits)
	f.mu.Unlock()
	if n != 0 {
		t.Errorf("invalid grants must NOT deposit, but %d landed", n)
	}
}

// TestSuspendReactivate_ForbidsUsersAndAudits proves suspend flips IAM isForbidden
// on every org user (the real access lever) and is audited, and reactivate reverses
// it — the customer's status reflects the change on a re-list.
func TestSuspendReactivate_ForbidsUsersAndAudits(t *testing.T) {
	f := newCockpitFakes(t)
	rec, _ := audit.Open(":memory:", nil)
	defer rec.Close()
	f.svc.auditStore = rec

	// Suspend acme.
	resp, body := f.do("POST", "/v1/admin/customers/acme/suspend", adminHdr(), "")
	if resp.StatusCode != 200 {
		t.Fatalf("suspend: %d (%s)", resp.StatusCode, body)
	}
	// Both acme users were update-user'd to forbidden.
	f.mu.Lock()
	if !f.forbidden["acme/anna"] || !f.forbidden["acme/bob"] {
		t.Errorf("suspend did not forbid both users: %+v", f.forbidden)
	}
	f.mu.Unlock()

	// A re-list shows acme suspended (all users forbidden).
	_, lb := f.do("GET", "/v1/admin/customers", adminHdr(), "")
	var env struct{ Data []customerRow `json:"data"` }
	_ = json.Unmarshal(lb, &env)
	for _, c := range env.Data {
		if c.Org == "acme" && c.Status != "suspended" {
			t.Errorf("acme status = %q after suspend, want suspended", c.Status)
		}
	}
	// Audited.
	if _, total, _ := rec.Query(context.Background(), audit.Filter{Action: "admin.customer.suspend"}); total < 1 {
		t.Errorf("suspend not audited")
	}

	// Reactivate reverses it.
	if _, rb := f.do("POST", "/v1/admin/customers/acme/reactivate", adminHdr(), ""); !strings.Contains(string(rb), `"suspended":false`) {
		t.Errorf("reactivate response wrong: %s", rb)
	}
	f.mu.Lock()
	if f.forbidden["acme/anna"] || f.forbidden["acme/bob"] {
		t.Errorf("reactivate did not clear forbidden: %+v", f.forbidden)
	}
	f.mu.Unlock()
}

// TestRevenue_RealAggregate proves the fleet revenue board: totals, paying-customer
// count, ARPU, and the per-customer table are real commerce aggregates.
func TestRevenue_RealAggregate(t *testing.T) {
	f := newCockpitFakes(t)
	resp, body := f.do("GET", "/v1/admin/revenue", adminHdr(), "")
	if resp.StatusCode != 200 {
		t.Fatalf("revenue: %d (%s)", resp.StatusCode, body)
	}
	var env struct {
		Data revenueData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	d := env.Data
	if d.TotalBalancesCents != 25000 { // acme 20000 + globex 5000
		t.Errorf("total balances = %d, want 25000", d.TotalBalancesCents)
	}
	if d.TotalSpendCents != 1800 { // 1500 + 300
		t.Errorf("total spend = %d, want 1800", d.TotalSpendCents)
	}
	if d.MRRCents != 5000 {
		t.Errorf("MRR = %d, want 5000", d.MRRCents)
	}
	if d.PayingCustomers != 2 {
		t.Errorf("paying customers = %d, want 2", d.PayingCustomers)
	}
	if d.ARPUCents != 900 { // 1800 / 2
		t.Errorf("ARPU = %d, want 900", d.ARPUCents)
	}
	if len(d.PerCustomer) != 2 || d.PerCustomer[0].Org != "acme" { // sorted by spend desc
		t.Errorf("per-customer table wrong: %+v", d.PerCustomer)
	}
	if len(d.SpendTrend) == 0 {
		t.Error("revenue must include a real spend trend")
	}
}

// TestAnalytics_HandlerRealWiring proves the analytics handler wires IAM signups +
// commerce ledger into a REAL cohort/active/growth board, and flags computed=true.
func TestAnalytics_HandlerRealWiring(t *testing.T) {
	f := newCockpitFakes(t)
	resp, body := f.do("GET", "/v1/admin/analytics?range=all", adminHdr(), "")
	if resp.StatusCode != 200 {
		t.Fatalf("analytics: %d (%s)", resp.StatusCode, body)
	}
	var env struct {
		Data analyticsData `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	d := env.Data
	if d.TotalCustomers != 2 {
		t.Errorf("total customers = %d, want 2", d.TotalCustomers)
	}
	// Real ledger present → retention/active/usage computed, growth always.
	if !d.Computed["growth"] || !d.Computed["retention"] || !d.Computed["active"] {
		t.Errorf("computed flags must be true with a real ledger: %+v", d.Computed)
	}
	// Both customers had recent usage → MAU covers them.
	if d.MAU < 1 {
		t.Errorf("MAU = %d, want >=1 (recent usage)", d.MAU)
	}
	// Retention grid has cohorts from the two signups.
	if len(d.Retention.Cohorts) == 0 {
		t.Error("retention grid must have cohorts from real signups")
	}
	// Top customer by usage present (acme 300c > globex 400c? globex 400 wins).
	if len(d.TopCustomers) == 0 {
		t.Error("top customers must be populated from real usage")
	}
}
