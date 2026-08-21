package ml

// Integration tests proving the per-org billing gate is wired into the compute
// create path (POST /v1/ml/models): an unfunded org cannot run free GPU compute
// (402 before any k8s object is created), and a funded org is charged on its OWN
// org ledger. The metering client's default org is "hanzo", so "billed acme"
// also proves the caller org — not the default — is charged.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/zap-proto/zip"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

type billDouble struct {
	available int64
	// The usage DEBIT crosses the internal plane, not HTTP — metering.Usage.Ref is
	// `json:"-"` and could not survive a JSON body. The balance READ above is still
	// HTTP. See internal/planetest.
	peer *planetest.Commerce
}

func (b *billDouble) start(t *testing.T) string {
	t.Helper()
	b.peer = planetest.Serve(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": b.available})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (b *billDouble) debits() int32               { return b.peer.Count() }
func (b *billDouble) lastDebit() (string, []byte) { return b.peer.Org(), b.peer.Body() }

func newBilledMLService(t *testing.T, commerceURL string) *cloud.Service[state] {
	t.Helper()
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-token", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	deps := cloud.Deps{Metering: m, Env: "mainnet"}
	return &cloud.Service[state]{
		Base: cloud.NewBase(deps, "ml"),
		State: state{
			hc:   &http.Client{},
			dyn:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
			bill: cloud.NewResourceMeter(deps, "compute"),
		},
	}
}

func postModel(t *testing.T, s *cloud.Service[state], org string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	// What Serve installs for the whole binary, narrowed to what this op needs:
	// the request on the context (Bridge) and the money wire's own envelope for a
	// refusal the op RETURNS (DenyEnvelope). No package's own harness runs Serve,
	// so a create tested without them would refuse in zip's shape and this test
	// would be asserting the wrong body.
	app.Use(cloud.Bridge())
	app.Use(cloud.DenyEnvelope())
	zip.Post(app, "/v1/ml/models", ops{s: s}.createModel, zip.WithStatus(http.StatusCreated))
	body := `{"name":"model1","spec":{"predictor":{"model":{"modelFormat":{"name":"sklearn"}}}}}`
	req, _ := http.NewRequest("POST", "/v1/ml/models", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // validated principal (tenant() gates on it)
	}
	resp, err := app.Test(req)
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
	s := newBilledMLService(t, bd.start(t))

	resp := postModel(t, s, "acme")
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

// Funded org → 201, InferenceService created, and the CALLER org (acme, not the default
// hanzo) is debited the compute fee under provider "compute".
func TestComputeCreate_AllowsAndDebitsCallerOrg(t *testing.T) {
	bd := &billDouble{available: 100000}
	s := newBilledMLService(t, bd.start(t))

	resp := postModel(t, s, "acme")
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
