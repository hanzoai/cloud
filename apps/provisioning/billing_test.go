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

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
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
	t.Setenv("CLOUD_ENV", "mainnet")
	t.Helper()
	t.Setenv("CLOUD_KMS_NODES", "")
	t.Setenv("CLOUD_KMS_PASSPHRASE", "")
	log := luxlog.New("module", "provbilltest")
	mp := &mockProv{cs: "kv://u:pw@kv.hanzo.svc:6379/0", host: "kv.hanzo.svc", port: 6379, db: "prefix:"}
	reg := map[string]Provisioner{}
	for _, k := range kinds {
		reg[k] = mp
	}
	m, err := meter.New(meter.Config{BaseURL: commerceURL})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	s := &cloud.Service[state]{
		Log: log, Bill: cloud.NewMeter(cloud.Deps{Metering: m}, "provisioning"),
		State: state{
			store: newTestStore(t),
			sec:   newSecrets(nil, log),
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
	app.Post("/v1/"+kind, ops{s: s}.createFor(kind), zip.WithStatus(http.StatusCreated))
}

func postCreate(t *testing.T, s *cloud.Service[state], kind, org, name string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	mountCreate(app, s, kind)
	req, _ := http.NewRequest("POST", "/v1/"+kind, strings.NewReader(`{"name":"`+name+`"}`))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)       // validated principal (tenant() gates on X-User-Id)
		req.Header.Set("X-User-IsOrgAdmin", "true") // an admin of the org: provisioning is theirs (administers)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	return resp
}

// Unfunded org → 402 insufficient_balance, and NOTHING is provisioned (the gate
// runs before the backend). No free provisioning.
func TestCreate_RefusesUnfundedOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// Funded org → 201, resource provisioned, and the CALLER org (acme, not the
// client default hanzo) is debited the provision fee.
func TestCreate_AllowsAndDebitsCallerOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// A free kind (fee 0) is un-gated: even at zero balance the resource is created.
// It is a served call, so it is counted, at $0, and charges nothing.
func TestCreate_FreeKindUngated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
		// Every served call is counted (finance.record), a free one at $0 that moves no
		// money; give any (incorrect) priced debit a chance to land, then assert none did.
		if !planetest.Wait(func() bool { return bs.debits() > 0 }) {
			t.Fatal("a free create was not counted: every served call is")
		}
		time.Sleep(50 * time.Millisecond)
		if n := bs.peer.Charged(); n != 0 {
			t.Fatalf("%d of %d crossings charged a free kind, want none", n, bs.debits())
		}
	})
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		planetest.Serve(t)
		s, mp := newBilledService(t, "", "vector") // empty commerce URL => the gate crosses the plane
		resp := postCreate(t, s, "vector", "acme", "orders")
		if resp.StatusCode == http.StatusCreated {
			t.Fatal("a priced create ran while the biller could not answer — that is free work")
		}
		if mp.created != 0 {
			t.Fatalf("provisioner ran %d times while the biller could not answer, want 0", mp.created)
		}
	})
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

// An unfunded org's create refusal is the gate's own 402, byte for byte, with the
// kind's x402 terms on PAYMENT-REQUIRED beside it. A paid create is checked before
// anything is written and settled once the resource is live, and the fee is not
// taken from the balance again.
func TestCreate_FeeOverX402(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		bs := &billServer{}
		s, mp := newBilledService(t, bs.start(t), "vector")
		send := func(name, payment string) (int, string, http.Header) {
			app := zip.New(zip.Config{DisableStartupMessage: true})
			mountCreate(app, s, "vector")
			req, _ := http.NewRequest("POST", "/v1/vector", strings.NewReader(`{"name":"`+name+`"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Org-Id", "acme")
			req.Header.Set("X-User-Id", "u-acme")
			req.Header.Set("X-User-IsOrgAdmin", "true")
			if payment != "" {
				req.Header.Set(client.HeaderPaymentSignature, payment)
			}
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(b), resp.Header
		}

		code0, body0, hdr0 := send("orders", "")
		if code0 != http.StatusPaymentRequired || hdr0.Get(client.HeaderPaymentRequired) != "" {
			t.Fatalf("with no rail = %d %s, want a bare 402", code0, body0)
		}

		want, _ := client.Provision("vector")
		var steps []cloud.FeeStep
		cloud.SetHooks(cloud.Hooks{X402Fee: func(c *zip.Ctx, resource string, step cloud.FeeStep) (cloud.FeeAnswer, error) {
			if resource != want.Resource() {
				t.Errorf("the create asked the rail about %q, want %q", resource, want.Resource())
			}
			steps = append(steps, step)
			switch {
			case step == cloud.FeeTerms:
				c.SetHeader(client.HeaderPaymentRequired, "terms")
				return cloud.FeeAnswer{}, nil
			case c.Header(client.HeaderPaymentSignature) == "":
				return cloud.FeeAnswer{}, nil
			}
			return cloud.FeeAnswer{OK: true}, nil
		}})
		t.Cleanup(func() { cloud.SetHooks(cloud.Hooks{}) })

		code, body, hdr := send("orders", "")
		if code != code0 || body != body0 || hdr.Get(client.HeaderPaymentRequired) != "terms" {
			t.Fatalf("with a rail = %d %s (challenge %q), want the same refusal plus the terms", code, body, hdr.Get(client.HeaderPaymentRequired))
		}
		if mp.created != 0 {
			t.Fatalf("a refused create provisioned %d", mp.created)
		}

		bs.available = 100000
		steps = nil
		code, body, _ = send("orders", "signed")
		if code != http.StatusCreated || mp.created != 1 {
			t.Fatalf("a paid create = %d %s (provisioned %d), want 201", code, body, mp.created)
		}
		if len(steps) != 2 || steps[0] != cloud.FeeCheck || steps[1] != cloud.FeeSettle {
			t.Fatalf("steps %v, want a check then a settle", steps)
		}
		if !waitForDebit(func() bool { return bs.debits() == 1 }) {
			t.Fatalf("the paid create was not counted")
		}
		if n := bs.peer.Charged(); n != 0 {
			t.Fatalf("charged = %d, want 0: the payment was the fee", n)
		}

		// A create that fails is not billed: its payment is never settled.
		steps = nil
		if code, body, _ := send("orders", "signed"); code != http.StatusConflict {
			t.Fatalf("a duplicate create = %d %s, want 409", code, body)
		}
		if len(steps) != 1 || steps[0] != cloud.FeeCheck {
			t.Fatalf("steps %v for a failed create, want the check alone", steps)
		}
	})
}
