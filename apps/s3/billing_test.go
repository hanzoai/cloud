package s3

// Integration tests proving the per-org credit drawdown reaches the s3 data plane
// through the ONE shared cloud.ResourceMeter: an unfunded org is refused 402
// before S3 is touched, a funded op debits the CALLER org (product "s3", unit
// "op") once, a handler failure bills nothing, a free fee is un-gated, and
// unconfigured commerce is a no-op. The metering client's DEFAULT org is "hanzo",
// so every "billed acme" assertion also proves the debit targets the CALLER org
// and never the default.
//
// The preamble is tested directly against an operation stub (paid takes any typed
// handler): the S3 backend is never dialed, so success and failure of the wrapped
// operation are deterministic and the billing contract is isolated from a live
// SeaweedFS. It admits only a validated principal (X-User-Id), so requests carry
// it exactly as SanitizeIdentity would in prod.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/internal/fare"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/s3admin"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/zap-proto/zip"
)

// billServer is a minimal commerce double: a fixed balance + records the
// X-Org-Id header and body of any usage debit (commerce reads X-Org-Id only).
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

// newBilledService builds an s3 Service with S3 admin credentials present (so the
// preamble does not 503 on Configured()) and a metering client pointed at commerceURL (default
// org "hanzo"; empty ⇒ !Enabled()).
func newBilledService(t *testing.T, commerceURL string) *cloud.Service[state] {
	t.Helper()
	t.Setenv(account.KeyEnv, testCSRFKey)
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIATEST")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secrettest")
	t.Setenv("S3_ADMIN_ENDPOINT", "127.0.0.1:1")
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-token", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	deps := cloud.Deps{Metering: m, Env: "mainnet"}
	return &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "s3"),
		State: state{admin: s3admin.New()},
	}
}

// callPaid runs an operation stub for org through the real preamble. The stub
// records whether it ran and returns hErr (nil ⇒ success path), answering 204 for
// the same reason every void op here does: a nil Out.
func callPaid(t *testing.T, s *cloud.Service[state], org string, hErr error) (status int, ran *int32) {
	t.Helper()
	var calls int32
	h := fare.Paid(s, func(ctx context.Context, _ *noInput) (*struct{}, error) {
		atomic.AddInt32(&calls, 1)
		if _, err := fare.Org(ctx); err != nil {
			t.Error("an operation ran with no org — admission hands the tenant down")
		}
		if hErr != nil {
			return nil, hErr
		}
		return nil, nil
	})
	app := zip.New(zip.Config{DisableStartupMessage: true})
	// What serve.go installs for the whole binary: the request parked on the
	// context, which is where admit reads the caller it judges. No package's own
	// harness runs Serve, so without this every call here is refused for a reason
	// that has nothing to do with the preamble under test.
	app.Use(cloud.Bridge())
	zip.Post(app, "/v1/s3/op", h)
	req := httptest.NewRequest("POST", "/v1/s3/op", nil)
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org) // validated principal (tenant() gates on X-User-Id)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test paid: %v", err)
	}
	return resp.StatusCode, &calls
}

// Unfunded org → 402 insufficient_balance, the wrapped handler NEVER runs (no S3
// touched), nothing is debited.
func TestPaid_RefusesUnfundedOrg(t *testing.T) {
	bs := &billServer{available: 0}
	s := newBilledService(t, bs.start(t))

	status, ran := callPaid(t, s, "acme", nil)
	if status != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", status)
	}
	if atomic.LoadInt32(ran) != 0 {
		t.Fatalf("handler ran %d times for an unfunded org, want 0 (gate must precede S3)", atomic.LoadInt32(ran))
	}
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a refused op, want 0", bs.debits())
	}
}

// Funded org → the op runs and the CALLER org (acme, not the default hanzo) is
// debited once with product "s3" / unit "op".
func TestPaid_AllowsAndDebitsCallerOrg(t *testing.T) {
	bs := &billServer{available: 100000}
	s := newBilledService(t, bs.start(t))

	status, ran := callPaid(t, s, "acme", nil)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}
	if atomic.LoadInt32(ran) != 1 {
		t.Fatalf("handler ran %d times, want 1", atomic.LoadInt32(ran))
	}
	if !waitFor(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("debits = %d, want 1 (a successful op must bill)", bs.debits())
	}
	org, body := bs.lastDebit()
	if org != "acme" {
		t.Fatalf("debited org %q, want caller %q (never the default 'hanzo')", org, "acme")
	}
	var u struct {
		User     string `json:"user"`
		Amount   int64  `json:"amount"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	_ = json.Unmarshal(body, &u)
	if u.User != "acme" {
		t.Fatalf("debit user = %q, want caller org %q", u.User, "acme")
	}
	if u.Amount != cloud.DefaultResourceFeeCents {
		t.Fatalf("debit amount = %d, want default fee %d", u.Amount, cloud.DefaultResourceFeeCents)
	}
	if u.Provider != "s3" {
		t.Fatalf("debit provider = %q, want %q", u.Provider, "s3")
	}
	if u.Model != "op" {
		t.Fatalf("debit model = %q, want %q", u.Model, "op")
	}
}

// A handler failure (e.g. the real S3 op errors) is authorized but NOT billed —
// mirrors the edge gate ("do not bill failed work").
func TestPaid_HandlerFailureNotBilled(t *testing.T) {
	bs := &billServer{available: 100000}
	s := newBilledService(t, bs.start(t))

	status, ran := callPaid(t, s, "acme", zip.Errorf(http.StatusBadGateway, "s3 down"))
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
	if atomic.LoadInt32(ran) != 1 {
		t.Fatalf("handler ran %d times, want 1", atomic.LoadInt32(ran))
	}
	time.Sleep(50 * time.Millisecond) // give any (incorrect) async debit a chance
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a failed op, want 0", bs.debits())
	}
}

// A free fee (0) is un-gated: even at zero balance the op runs and nothing is
// debited.
func TestPaid_FreeFeeUngated(t *testing.T) {
	t.Setenv("CLOUD_S3_FEE_CENTS", "0")
	bs := &billServer{available: 0}
	s := newBilledService(t, bs.start(t))

	status, ran := callPaid(t, s, "acme", nil)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (free fee is un-gated)", status)
	}
	if atomic.LoadInt32(ran) != 1 {
		t.Fatalf("handler ran %d times, want 1", atomic.LoadInt32(ran))
	}
	time.Sleep(50 * time.Millisecond)
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a free fee, want 0", bs.debits())
	}
}

// No commerce URL no longer means "nothing bills": the ledger has ONE writer and
// it lives with commerce, so a meter without a local URL ASKS it, and a biller it
// cannot reach is UNKNOWN — never allowed. The priced op is refused and the
// handler never runs. See TestResourceMeter_UnconfiguredIsNoop.
func TestPaid_UnreachableBillerRefusesAndRunsNothing(t *testing.T) {
	s := newBilledService(t, "") // empty commerce URL ⇒ !Enabled()

	status, ran := callPaid(t, s, "acme", nil)
	if status == http.StatusNoContent {
		t.Fatal("a priced op ran with no reachable biller — that is free work")
	}
	if atomic.LoadInt32(ran) != 0 {
		t.Fatalf("handler ran %d times with no biller reachable, want 0", atomic.LoadInt32(ran))
	}
}

// A request with no validated principal (no X-User-Id) is refused 403 before the
// gate — the tenant boundary precedes billing.
func TestPaid_NoPrincipalRefused(t *testing.T) {
	bs := &billServer{available: 100000}
	s := newBilledService(t, bs.start(t))

	status, ran := callPaid(t, s, "", nil)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
	if atomic.LoadInt32(ran) != 0 {
		t.Fatalf("handler ran %d times without a principal, want 0", atomic.LoadInt32(ran))
	}
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a refused (no-principal) op, want 0", bs.debits())
	}
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}
