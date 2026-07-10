package provisioning

// Integration tests proving the per-org billing gate is wired into the REAL
// create path: an unfunded org is refused 402 before any backend is touched, a
// funded org is provisioned and its OWN org ledger is debited, and a free kind
// (fee 0) is un-gated. The metering client's DEFAULT org is "hanzo", so every
// "billed acme" assertion also proves the debit targets the CALLER org, never
// the default — the multitenancy property end-to-end through the handler.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/commerce/metering"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
)

// billServer is a minimal commerce double: it returns a fixed balance and
// records the X-Org-Id header + body of any usage debit. X-Org-Id is the
// header commerce's service-token auth actually reads (metering >= v0.1.2);
// the tenant namespace resolves from it, so a stale name would silently debit
// the default org.
type billServer struct {
	available int64

	mu        sync.Mutex
	usageOrg  string
	usageBody []byte
	usages    int32
}

func (b *billServer) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": b.available})
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&b.usages, 1)
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.usageOrg, b.usageBody = r.Header.Get("X-Org-Id"), body
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"transactionId":"tx_1","type":"usage"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (b *billServer) debits() int32 { return atomic.LoadInt32(&b.usages) }
func (b *billServer) lastDebit() (string, []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usageOrg, b.usageBody
}

// newBilledSvc builds a provisioning Service with a mock provisioner and a real
// metering client pointed at commerceURL (default org "hanzo").
func newBilledSvc(t *testing.T, commerceURL string, kinds ...string) (*cloud.Service[state], *mockProv) {
	t.Helper()
	t.Setenv("CLOUD_KMS_NODES", "")
	t.Setenv("CLOUD_KMS_PASSPHRASE", "")
	log := luxlog.New("module", "provbilltest")
	mp := &mockProv{cs: "redis://u:pw@kv.hanzo.svc:6379/0", host: "kv.hanzo.svc", port: 6379, db: "prefix:"}
	reg := map[string]Provisioner{}
	for _, k := range kinds {
		reg[k] = mp
	}
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-token", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	s := &cloud.Service[state]{
		Base: cloud.Base{Log: log, Bill: cloud.NewResourceMeter(cloud.Deps{Logger: log, Metering: m, Env: "mainnet"}, "provisioning")},
		State: state{
			store: newTestStore(t),
			sec:   openSecrets("hanzo", log),
			reg:   reg,
		},
	}
	return s, mp
}

func postCreate(t *testing.T, s *cloud.Service[state], kind, org, name string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Post("/v1/"+kind, create(s, kind))
	req, _ := http.NewRequest("POST", "/v1/"+kind, strings.NewReader(`{"name":"`+name+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org) // validated principal (tenant() gates on X-User-Id)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	return resp
}

// Unfunded org → 402 insufficient_balance, and NOTHING is provisioned (the gate
// runs before the backend). No free provisioning.
func TestCreate_RefusesUnfundedOrg(t *testing.T) {
	bs := &billServer{available: 0}
	s, mp := newBilledSvc(t, bs.start(t), "vector")

	resp := postCreate(t, s, "vector", "acme", "orders")
	if resp.StatusCode != http.StatusPaymentRequired {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 402", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"code":"insufficient_balance"`) {
		t.Fatalf("body %s missing insufficient_balance code", body)
	}
	if mp.created != 0 {
		t.Fatalf("provisioner ran %d times for an unfunded org, want 0 (gate must precede the backend)", mp.created)
	}
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a refused request, want 0", bs.debits())
	}
}

// Funded org → 201, resource provisioned, and the CALLER org (acme, not the
// client default hanzo) is debited the provision fee.
func TestCreate_AllowsAndDebitsCallerOrg(t *testing.T) {
	bs := &billServer{available: 100000}
	s, mp := newBilledSvc(t, bs.start(t), "vector")

	resp := postCreate(t, s, "vector", "acme", "orders")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	if mp.created != 1 {
		t.Fatalf("provisioner ran %d times, want 1", mp.created)
	}
	if !waitForDebit(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("debits = %d, want 1 (a successful provision must bill)", bs.debits())
	}
	org, body := bs.lastDebit()
	if org != "acme" {
		t.Fatalf("debited org %q, want caller %q (never the default 'hanzo')", org, "acme")
	}
	var u struct {
		User   string `json:"user"`
		Amount int64  `json:"amount"`
	}
	_ = json.Unmarshal(body, &u)
	if u.User != "acme" {
		t.Fatalf("debit user = %q, want caller org %q", u.User, "acme")
	}
	if u.Amount != cloud.DefaultResourceFeeCents {
		t.Fatalf("debit amount = %d, want default fee %d", u.Amount, cloud.DefaultResourceFeeCents)
	}
}

// A free kind (fee 0) is un-gated: even at zero balance the resource is created
// and nothing is debited.
func TestCreate_FreeKindUngated(t *testing.T) {
	t.Setenv("CLOUD_PROVISION_FEE_CENTS_VECTOR", "0")
	bs := &billServer{available: 0}
	s, mp := newBilledSvc(t, bs.start(t), "vector")

	resp := postCreate(t, s, "vector", "acme", "orders")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201 (free kind is un-gated)", resp.StatusCode, body)
	}
	if mp.created != 1 {
		t.Fatalf("provisioner ran %d times, want 1", mp.created)
	}
	// Give any (incorrect) async debit a chance to land, then assert none did.
	time.Sleep(50 * time.Millisecond)
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a free kind, want 0", bs.debits())
	}
}

// Billing unconfigured (no commerce URL) → the gate is a no-op: provisioning
// works and nothing is billed (an unconfigured deployment is never blocked).
func TestCreate_BillingUnconfiguredNoop(t *testing.T) {
	s, mp := newBilledSvc(t, "", "vector") // empty commerce URL => !Enabled()
	resp := postCreate(t, s, "vector", "acme", "orders")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	if mp.created != 1 {
		t.Fatalf("provisioner ran %d times, want 1", mp.created)
	}
}

func waitForDebit(cond func() bool) bool {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}
