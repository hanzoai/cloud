package ml

// Integration tests proving the per-org billing gate is wired into the compute
// create path (/v1/train/jobs etc.): an unfunded org cannot run free GPU compute
// (402 before any k8s object is created), and a funded org is charged on its OWN
// org ledger. The metering client's default org is "hanzo", so "billed acme"
// also proves the caller org — not the default — is charged.

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
	"github.com/hanzoai/commerce/metering"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

type billDouble struct {
	available int64
	mu        sync.Mutex
	usageOrg  string
	usageBody []byte
	usages    int32
}

func (b *billDouble) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": b.available})
	})
	mux.HandleFunc("/v1/billing/usage", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&b.usages, 1)
		body, _ := io.ReadAll(r.Body)
		b.mu.Lock()
		b.usageOrg, b.usageBody = r.Header.Get("X-IAM-Org-Id"), body
		b.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"transactionId":"tx_1","type":"usage"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (b *billDouble) debits() int32 { return atomic.LoadInt32(&b.usages) }
func (b *billDouble) lastDebit() (string, []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usageOrg, b.usageBody
}

func newBilledMLSvc(t *testing.T, commerceURL string) *svc {
	t.Helper()
	log := luxlog.New("module", "mlbilltest")
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-token", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return &svc{
		log:  log,
		hc:   &http.Client{},
		dyn:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		bill: cloud.NewResourceMeter(cloud.Deps{Logger: log, Metering: m, Env: "mainnet"}, "compute"),
	}
}

func postTrainJob(t *testing.T, s *svc, org string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Post("/v1/train/jobs", s.create(jobKind))
	body := `{"name":"job1","spec":{"runtime":"torch"}}`
	req, _ := http.NewRequest("POST", "/v1/train/jobs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	return resp
}

// Unfunded org → 402, and NO compute is started: the gate runs before
// ensureNamespace/Create, so the fake k8s client is never touched and nothing is
// billed. This closes the free-GPU hole.
func TestComputeCreate_RefusesUnfundedOrg(t *testing.T) {
	bd := &billDouble{available: 0}
	s := newBilledMLSvc(t, bd.start(t))

	resp := postTrainJob(t, s, "acme")
	if resp.StatusCode != http.StatusPaymentRequired {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 402", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"code":"insufficient_balance"`) {
		t.Fatalf("body %s missing insufficient_balance code", body)
	}
	if bd.debits() != 0 {
		t.Fatalf("debits = %d for a refused compute request, want 0", bd.debits())
	}
}

// Funded org → 201, TrainJob created, and the CALLER org (acme, not the default
// hanzo) is debited the compute fee under provider "compute".
func TestComputeCreate_AllowsAndDebitsCallerOrg(t *testing.T) {
	bd := &billDouble{available: 100000}
	s := newBilledMLSvc(t, bd.start(t))

	resp := postTrainJob(t, s, "acme")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	if !waitForDebit(func() bool { return bd.debits() == 1 }) {
		t.Fatalf("debits = %d, want 1 (a successful compute create must bill)", bd.debits())
	}
	org, raw := bd.lastDebit()
	if org != "acme" {
		t.Fatalf("debited org %q, want caller %q (never the default 'hanzo')", org, "acme")
	}
	var u struct {
		User     string `json:"user"`
		Amount   int64  `json:"amount"`
		Provider string `json:"provider"`
	}
	_ = json.Unmarshal(raw, &u)
	if u.User != "acme" {
		t.Fatalf("debit user = %q, want caller org %q", u.User, "acme")
	}
	if u.Amount != cloud.DefaultResourceFeeCents {
		t.Fatalf("debit amount = %d, want default fee %d", u.Amount, cloud.DefaultResourceFeeCents)
	}
	if u.Provider != "compute" {
		t.Fatalf("debit provider = %q, want %q", u.Provider, "compute")
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
