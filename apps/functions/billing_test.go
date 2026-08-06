package functions

// Integration tests proving the per-org credit-drawdown gate is wired into the
// REAL invoke path via the ONE shared cloud.ResourceMeter: an unfunded org is
// refused 402 before any sandbox compute runs, a funded org runs and its OWN org
// ledger is debited (product "functions", unit "invoke"), a sandbox transport
// unreachable sandbox bills nothing (no billable compute), a free fee is un-gated, and an
// unconfigured commerce is a no-op. The metering client's DEFAULT org is "hanzo",
// so every "billed acme" assertion also proves the debit targets the CALLER org,
// never the default — multitenancy end-to-end through the handler.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	planeops "github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// billServer is a minimal commerce double. The BALANCE read is still HTTP — that is
// what the gate makes — and the usage DEBIT arrives on the internal plane, which is
// where metering moved it ("the peer is a socket away; ask it").
type billServer struct {
	available int64

	mu       sync.Mutex
	usageOrg string
	usageIn  *planeops.RecordIn
	usages   int32
}

// record is the DEBIT as it actually arrives now: a typed plane op, not an HTTP
// POST. The org is the caller's plane identity, which is the fact these tests care
// about most — that the CALLER's org is debited and never the client's default.
func (b *billServer) record(org string, in *planeops.RecordIn) {
	atomic.AddInt32(&b.usages, 1)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.usageOrg, b.usageIn = org, in
}

func (b *billServer) start(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": b.available})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func (b *billServer) debits() int32 { return atomic.LoadInt32(&b.usages) }
func (b *billServer) lastDebit() (string, *planeops.RecordIn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usageOrg, b.usageIn
}

// sandbox is a sandboxes PEER double, on the real plane: it answers the five ops
// apps/sandbox publishes, so an invoke reaches it exactly the way it reaches the
// real one — cloud.Ask resolves the app, zip dispatches the op.
//
// It counts the RUNS, not the calls, which is the fact every test below turns on:
// "did compute happen". A lease that is never run in is not compute, and the gate
// tests are about compute never happening.
type sandbox struct {
	mu    sync.Mutex
	calls int32
	pods  map[string]bool
	bill  *billServer
}

func (s *sandbox) serve(t *testing.T, bill *billServer) {
	t.Helper()
	s.bill = bill
	dir, err := os.MkdirTemp("", "fnpeer")
	if err != nil {
		t.Fatalf("run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", dir)
	planeops.Unbind()
	t.Cleanup(planeops.Unbind)
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	s.pods = map[string]bool{}

	p := cloud.Plane()
	zip.Post[planeops.LeaseIn, planeops.Leased](p, "/sandbox/lease",
		func(ctx context.Context, in *planeops.LeaseIn) (*planeops.Leased, error) {
			if cloud.Who(ctx).Org == "" {
				return nil, zip.ErrForbidden("org required")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			id := in.ID
			if id == "" || !s.pods[id] {
				id = fmt.Sprintf("m_%d", len(s.pods))
				s.pods[id] = true
			}
			return &planeops.Leased{ID: id, Class: "exec", Status: "running", Workdir: "/mnt/data"}, nil
		}, zip.WithOperationID(planeops.SandboxLease))
	zip.Post[planeops.RunIn, planeops.Ran](p, "/sandbox/run",
		func(ctx context.Context, in *planeops.RunIn) (*planeops.Ran, error) {
			// Only the PROGRAM line is compute; the artifact sweep that follows it is
			// bookkeeping and counting it would double every assertion below.
			if len(in.Argv) >= 3 && strings.HasPrefix(in.Argv[2], ": > ") {
				atomic.AddInt32(&s.calls, 1)
				return &planeops.Ran{Stdout: "ok"}, nil
			}
			return &planeops.Ran{}, nil
		}, zip.WithOperationID(planeops.SandboxRun))
	zip.Post[planeops.WriteIn, planeops.Wrote](p, "/sandbox/write",
		func(ctx context.Context, in *planeops.WriteIn) (*planeops.Wrote, error) {
			return &planeops.Wrote{Path: "/mnt/data/" + in.Path, Bytes: len(in.Data)}, nil
		}, zip.WithOperationID(planeops.SandboxWrite))
	zip.Post[planeops.PathIn, planeops.Blob](p, "/sandbox/read",
		func(ctx context.Context, in *planeops.PathIn) (*planeops.Blob, error) {
			return &planeops.Blob{Path: "/mnt/data", Dir: true}, nil
		}, zip.WithOperationID(planeops.SandboxRead))
	zip.Post[planeops.EndIn, struct{}](p, "/sandbox/end",
		func(ctx context.Context, in *planeops.EndIn) (*struct{}, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.pods, in.ID)
			return &struct{}{}, nil
		}, zip.WithOperationID(planeops.SandboxEnd))

	// The DEBIT crosses the plane too. metering stopped POSTing to commerce's
	// /v1/billing/usage — "the peer is a socket away; ask it" (apps/metering) — so an
	// HTTP double alone can no longer observe a debit, and the billing assertions
	// below were failing against one before this peer existed. The op answers here
	// and reports to the same billServer, so `debits()` still counts what was written.
	zip.Post[planeops.RecordIn, planeops.Recorded](p, "/finance/record",
		func(ctx context.Context, in *planeops.RecordIn) (*planeops.Recorded, error) {
			s.bill.record(cloud.Who(ctx).Org, in)
			return &planeops.Recorded{}, nil
		}, zip.WithOperationID(planeops.FinanceRecord))

	for _, name := range []string{"sandboxes", "commerce"} {
		stop, serr := cloud.ServePlane(name, luxlog.NewNoOpLogger())
		if serr != nil {
			t.Fatalf("serve %s plane: %v", name, serr)
		}
		t.Cleanup(func() { _ = stop() })
	}
}

func (s *sandbox) ran() int32 { return atomic.LoadInt32(&s.calls) }

// live reports the sandboxes this peer still holds. A function invoke must leave
// NONE: its lease ends with the call, unlike a chat session's.
func (s *sandbox) live() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pods)
}

// newBilledService builds a functions service with a store, the sandbox-backed exec
// client, and a metering client pointed at commerceURL (default org "hanzo"; empty ⇒
// !Enabled()).
func newBilledService(t *testing.T, commerceURL string) *cloud.Service[state] {
	t.Helper()
	log := luxlog.New("module", "fnbilltest")
	m, err := metering.New(metering.Config{BaseURL: commerceURL, Token: "svc-token", Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return &cloud.Service[state]{
		Base: cloud.NewBase(cloud.Deps{Logger: log, Metering: m, Env: "mainnet"}, "functions"),
		State: state{
			stores: cloud.NewOrgStore(cloud.Base{DataDir: t.TempDir()}, "functions", openStore),
			exec:   newExecClient(),
		},
	}
}

// seedFn inserts a ready function directly into the org's per-org store — the
// SAME file the invoke handler resolves through the shared cache, so it sees it.
func seedFn(t *testing.T, s *cloud.Service[state], org, name string) {
	t.Helper()
	store, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("seed fn store: %v", err)
	}
	if _, err := store.Upsert(context.Background(), Function{
		Org: org, Name: name, Runtime: "python", Code: "print(1)", TimeoutSec: 30, MemoryLimit: "256Mi", Status: "ready",
	}); err != nil {
		t.Fatalf("seed fn: %v", err)
	}
}

// fireInvoke fires POST /v1/functions/:name/invoke for org through the real handler.
func fireInvoke(t *testing.T, s *cloud.Service[state], org, name string) *http.Response {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Post("/v1/functions/:name/invoke", cloud.Handle(s, invoke))
	req := httptest.NewRequest("POST", "/v1/functions/"+name+"/invoke", bytes.NewReader([]byte(`{"input":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // validated principal (org() gates on it)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test invoke: %v", err)
	}
	return resp
}

// Unfunded org → 402 insufficient_balance, sandbox NEVER called, nothing debited.
func TestInvoke_RefusesUnfundedOrg(t *testing.T) {
	sb := &sandbox{}
	bs := &billServer{available: 0}
	sb.serve(t, bs)
	s := newBilledService(t, bs.start(t))
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusPaymentRequired {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 402", resp.StatusCode, body)
	}
	if sb.ran() != 0 {
		t.Fatalf("sandbox ran %d times for an unfunded org, want 0 (gate must precede compute)", sb.ran())
	}
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a refused invoke, want 0", bs.debits())
	}
}

// Funded org → 200, sandbox runs, and the CALLER org (acme, not the client
// default hanzo) is debited once with product "functions" / unit "invoke".
func TestInvoke_AllowsAndDebitsCallerOrg(t *testing.T) {
	sb := &sandbox{}
	bs := &billServer{available: 100000}
	sb.serve(t, bs)
	s := newBilledService(t, bs.start(t))
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 200", resp.StatusCode, body)
	}
	if sb.ran() != 1 {
		t.Fatalf("sandbox ran %d times, want 1", sb.ran())
	}
	if !waitFor(func() bool { return bs.debits() == 1 }) {
		t.Fatalf("debits = %d, want 1 (a successful invoke must bill)", bs.debits())
	}
	org, in := bs.lastDebit()
	if org != "acme" {
		t.Fatalf("debited org %q, want caller %q (never the default 'hanzo')", org, "acme")
	}
	if in == nil {
		t.Fatal("a debit was counted but nothing was recorded")
	}
	if in.Subject != "acme" {
		t.Fatalf("debit subject = %q, want caller org %q", in.Subject, "acme")
	}
	amt, err := in.Amount.Parse()
	if err != nil {
		t.Fatalf("debit amount %+v: %v", in.Amount, err)
	}
	// The plane carries the EXACT decimal, so the fee is read back in minor units
	// rather than compared as a folded cent count.
	if got := amt.MinorString(); got != strconv.FormatInt(cloud.DefaultResourceFeeCents, 10) {
		t.Fatalf("debit amount = %s minor units, want default fee %d",
			got, cloud.DefaultResourceFeeCents)
	}
	if in.Usage.Provider != "functions" {
		t.Fatalf("debit provider = %q, want %q", in.Usage.Provider, "functions")
	}
	if in.Usage.Model != "invoke" {
		t.Fatalf("debit model = %q, want %q", in.Usage.Model, "invoke")
	}
}

// A sandbox that cannot be reached is authorized but runs NO billable compute →
// nothing is debited (no free usage, and no charge for work that never happened).
func TestInvoke_UnreachableSandboxNotBilled(t *testing.T) {
	bs := &billServer{available: 100000}
	// NO sandboxes peer is served, so the call cannot reach one: plane.Ask answers
	// ErrNoPeer and run() reports a deployment that cannot execute code.
	s := newBilledService(t, bs.start(t))
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 503 (no executor deployed here)", resp.StatusCode, body)
	}
	time.Sleep(50 * time.Millisecond) // give any (incorrect) async debit a chance
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a sandbox transport failure, want 0", bs.debits())
	}
}

// Free fee (0) is un-gated: even at zero balance the invoke runs and nothing is
// debited.
func TestInvoke_FreeFeeUngated(t *testing.T) {
	t.Setenv("CLOUD_FUNCTION_FEE_CENTS", "0")
	sb := &sandbox{}
	bs := &billServer{available: 0}
	sb.serve(t, bs)
	s := newBilledService(t, bs.start(t))
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 200 (free fee is un-gated)", resp.StatusCode, body)
	}
	if sb.ran() != 1 {
		t.Fatalf("sandbox ran %d times, want 1", sb.ran())
	}
	time.Sleep(50 * time.Millisecond)
	if bs.debits() != 0 {
		t.Fatalf("debits = %d for a free fee, want 0", bs.debits())
	}
}

// No commerce URL no longer means "nothing bills". Once apps are their own
// binaries the ledger has ONE writer and it lives with commerce, so a meter
// without a local URL ASKS it — and a biller it cannot reach is UNKNOWN, never
// allowed. Allowing here is what turned every priced act free the moment an app
// was split out, silently, so the priced invoke is REFUSED and the sandbox never
// runs. See TestResourceMeter_UnconfiguredIsNoop, which pins the same rule at
// the gate itself.
func TestInvoke_UnreachableBillerRefusesAndRunsNothing(t *testing.T) {
	sb := &sandbox{}
	sb.serve(t, &billServer{})
	s := newBilledService(t, "") // empty commerce URL ⇒ !Enabled()
	seedFn(t, s, "acme", "resize")

	resp := fireInvoke(t, s, "acme", "resize")
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a priced invoke ran with no reachable biller — that is free work")
	}
	if sb.ran() != 0 {
		t.Fatalf("sandbox ran %d times with no biller reachable, want 0", sb.ran())
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
