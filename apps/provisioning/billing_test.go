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
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/internal/planetest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// billServer is a minimal commerce double: it returns a fixed balance and
// records the X-Org-Id header + body of any usage debit. X-Org-Id is the
// header commerce's service-token auth actually reads (metering >= v0.1.2);
// the tenant namespace resolves from it, so a stale name would silently debit
// the default org.
type billServer struct {
	available int64

	// The usage DEBIT crosses the internal plane, not HTTP — metering.Usage.Ref is
	// `json:"-"` and could not survive a JSON body. The balance READ above is still
	// HTTP. See internal/planetest.
	peer *planetest.Commerce
}

func (b *billServer) start(t *testing.T) string {
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

func (b *billServer) debits() int32               { return b.peer.Count() }
func (b *billServer) lastDebit() (string, []byte) { return b.peer.Org(), b.peer.Body() }

// newBilledService builds a provisioning Service with a mock provisioner and a real
// metering client pointed at commerceURL (default org "hanzo").
func newBilledService(t *testing.T, commerceURL string, kinds ...string) (*cloud.Service[state], *mockProv) {
	t.Helper()
	t.Setenv("CLOUD_KMS_NODES", "")
	t.Setenv("CLOUD_KMS_PASSPHRASE", "")
	log := luxlog.New("module", "provbilltest")
	mp := &mockProv{cs: "kv://u:pw@kv.hanzo.svc:6379/0", host: "kv.hanzo.svc", port: 6379, db: "prefix:"}
	reg := map[string]Provisioner{}
	for _, k := range kinds {
		reg[k] = mp
	}
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-token", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	s := &cloud.Service[state]{
		Base: cloud.Base{Log: log, Bill: cloud.NewMeter(cloud.Deps{Metering: m, Env: "mainnet"}, "provisioning")},
		State: state{
			store: newTestStore(t),
			sec:   openSecrets("hanzo", log),
			reg:   reg,
		},
	}
	return s, mp
}

// mountCreate mounts one kind's create the way the binary mounts it: what Serve
// installs (Bridge parks the request the typed op resolves its tenant from,
// DenyEnvelope turns a returned cloud.Denied back into the money wire's own
// bytes), then the typed op. No package's harness runs Serve, so a create tested
// without DenyEnvelope would refuse in zip's shape and assert the wrong body.
func mountCreate(app *zip.App, s *cloud.Service[state], kind string) {
	app.Use(cloud.Bridge())
	app.Use(cloud.DenyEnvelope())
	zip.Post(app, "/v1/"+kind, ops{s: s}.createFor(kind), zip.WithStatus(http.StatusCreated))
}

func postCreate(t *testing.T, s *cloud.Service[state], kind, org, name string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountCreate(app, s, kind)
	req, _ := http.NewRequest("POST", "/v1/"+kind, strings.NewReader(`{"name":"`+name+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org) // validated principal (tenant() gates on X-User-Id)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	return resp
}

// Unfunded org → 402 insufficient_balance, and NOTHING is provisioned (the gate
// runs before the backend). No free provisioning.
func TestCreate_RefusesUnfundedOrg(t *testing.T) {
	bs := &billServer{available: 0}
	s, mp := newBilledService(t, bs.start(t), "vector")

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
	s, mp := newBilledService(t, bs.start(t), "vector")

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
	s, mp := newBilledService(t, bs.start(t), "vector")

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

// No local ledger means the meter ASKS commerce, and a commerce that cannot
// answer is UNKNOWN — never permission. A priced create is refused and the
// provisioner never runs, because provisioning first and discovering later that
// nobody could bill it is a resource somebody has to find.
//
// The peer here serves the debit and NOT the gate, so its socket answers and its
// op does not: an outage, which is the fact that must not be read as "nobody
// bills here". See TestMeter_UnconfiguredIsNoop for both halves.
func TestCreate_BillerOutageRefusesAndProvisionsNothing(t *testing.T) {
	planetest.Serve(t)
	s, mp := newBilledService(t, "", "vector") // empty commerce URL => the gate crosses the plane
	resp := postCreate(t, s, "vector", "acme", "orders")
	if resp.StatusCode == http.StatusCreated {
		t.Fatal("a priced create ran while the biller could not answer — that is free work")
	}
	if mp.created != 0 {
		t.Fatalf("provisioner ran %d times while the biller could not answer, want 0", mp.created)
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
