package billing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/money"
	"github.com/hanzoai/cloud/types"
)

// countingCommerce answers every GPU charge 201 and COUNTS them. The count is the
// whole point: a money WRITE that is replayed must reach the wallet once, and a fake
// that only remembers the last call cannot tell one debit from two.
type countingCommerce struct {
	mu      sync.Mutex
	charges int
	cards   string // the portal payment-methods body (a card on file by default)
}

func (f *countingCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/gpu-charge", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		f.mu.Lock()
		f.charges++
		n := f.charges
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"transactionId":"txn_` + string(rune('0'+n)) + `","status":"ok"}`))
	})
	mux.HandleFunc("/v1/billing/portal/payment-methods", func(w http.ResponseWriter, _ *http.Request) {
		body := f.cards
		if body == "" {
			body = `[{"id":"pm_1","brand":"visa","last4":"4242","isDefault":true}]`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *countingCommerce) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.charges
}

// publishLedger publishes a REAL per-org finance ledger (the wallet every other debit
// in the fleet lands in) for the duration of the test, funded with cents for org.
func publishLedger(t *testing.T, org string, cents int64) finance.Client {
	t.Helper()
	t.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // 32 zero bytes, dev-only
	fin := finance.New(t.TempDir())
	if cents > 0 {
		if _, err := fin.Deposit(context.Background(), types.DepositInput{
			Org: org, Subject: org, Amount: money.FromCents(cents), Currency: "usd",
			Notes: "test float", Ref: "seed:" + org,
		}); err != nil {
			t.Fatalf("seed deposit: %v", err)
		}
	}
	finance.Publish(fin)
	t.Cleanup(func() { finance.Publish(nil) })
	return fin
}

func walletCents(t *testing.T, fin finance.Client, org string) int64 {
	t.Helper()
	bal, err := fin.Balance(context.Background(), org, org, "usd", false)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return bal.Cents()
}

// TestGPUCharge_ReplayDebitsOnce is the money defect: POST /v1/billing/gpu-charge
// carries a caller-supplied `requestId` that deduplicated NOTHING, so a client retry,
// a proxy replay or a double-clicked launch button charged the customer twice.
//
// Two identical posts — same requestId, same amount — must move the wallet ONCE, and
// the replay must answer the original transaction rather than minting a second one.
func TestGPUCharge_ReplayDebitsOnce(t *testing.T) {
	f := &countingCommerce{}
	fin := publishLedger(t, "maxpower", 500_00)
	app := mountApp(t, f.server(t).URL, "svc-token")

	const body = `{"amountCents":24000,"currency":"usd","requestId":"launch-abc","tag":"gpu-h100"}`
	code, first := callBody(t, app, http.MethodPost, "/v1/billing/gpu-charge", "maxpower/dave", "maxpower", body)
	if code != http.StatusCreated {
		t.Fatalf("first charge: want 201, got %d (%s)", code, first)
	}
	code, second := callBody(t, app, http.MethodPost, "/v1/billing/gpu-charge", "maxpower/dave", "maxpower", body)
	if code != http.StatusCreated {
		t.Fatalf("replay: want 201, got %d (%s)", code, second)
	}

	if got := walletCents(t, fin, "maxpower"); got != 500_00-24000 {
		t.Errorf("wallet debited twice: want %d cents left, got %d", 500_00-24000, got)
	}
	// Co-resident the LEDGER is the writer, so the upstream money write is not made at
	// all — a second writer is a second way for the same charge to land twice.
	if n := f.count(); n != 0 {
		t.Errorf("upstream money write called %d times — co-resident the ledger is the one writer", n)
	}
	// The replay answers the SAME transaction, so a retrying client cannot conclude it
	// bought two GPUs.
	var a, b map[string]any
	_ = json.Unmarshal(first, &a)
	_ = json.Unmarshal(second, &b)
	if a["transactionId"] == nil || a["transactionId"] != b["transactionId"] {
		t.Errorf("replay must answer the original transaction: first=%v second=%v", a["transactionId"], b["transactionId"])
	}
}

// TestGPUCharge_DistinctRequestsBothCharge is the other half: idempotency must not
// become a lock. Two DIFFERENT launches (different requestId) are two real debits.
func TestGPUCharge_DistinctRequestsBothCharge(t *testing.T) {
	f := &countingCommerce{}
	fin := publishLedger(t, "maxpower", 500_00)
	app := mountApp(t, f.server(t).URL, "svc-token")

	for _, id := range []string{"launch-1", "launch-2"} {
		code, body := callBody(t, app, http.MethodPost, "/v1/billing/gpu-charge", "maxpower/dave", "maxpower",
			`{"amountCents":10000,"currency":"usd","requestId":"`+id+`","tag":"gpu-h100"}`)
		if code != http.StatusCreated {
			t.Fatalf("charge %s: want 201, got %d (%s)", id, code, body)
		}
	}
	if got := walletCents(t, fin, "maxpower"); got != 500_00-20000 {
		t.Errorf("two distinct launches must both debit: want %d cents left, got %d", 500_00-20000, got)
	}
}

// TestGPUCharge_GatesFailClosed — idempotency must not have cost the two money rules.
// No card is 402 card_required; prepaid that cannot cover the charge is 402
// insufficient_prepaid; and in neither case does the wallet move.
func TestGPUCharge_GatesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, cards, body, code string
		funded                  int64
	}{
		{"no card on file", `[]`, `{"amountCents":100,"requestId":"r1"}`, "card_required", 500_00},
		{"prepaid cannot cover it", "", `{"amountCents":60000,"requestId":"r2"}`, "insufficient_prepaid", 500_00},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &countingCommerce{cards: tc.cards}
			fin := publishLedger(t, "maxpower", tc.funded)
			app := mountApp(t, f.server(t).URL, "svc-token")

			code, body := callBody(t, app, http.MethodPost, "/v1/billing/gpu-charge", "maxpower/dave", "maxpower", tc.body)
			if code != http.StatusPaymentRequired {
				t.Fatalf("want 402, got %d (%s)", code, body)
			}
			var got struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("decode refusal: %v (%s)", err, body)
			}
			if got.Error.Code != tc.code {
				t.Errorf("refusal code: want %q, got %q (%s)", tc.code, got.Error.Code, body)
			}
			if left := walletCents(t, fin, "maxpower"); left != tc.funded {
				t.Errorf("a refused charge must move no money: want %d, got %d", tc.funded, left)
			}
		})
	}
}

// TestGPUCharge_CardGateUnreadableRefuses — "no card" and "could not ask" must not look
// alike on a gate that guards money. An upstream that cannot answer is 502, never a
// charge that proceeds.
func TestGPUCharge_CardGateUnreadableRefuses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	fin := publishLedger(t, "maxpower", 500_00)
	app := mountApp(t, srv.URL, "svc-token")

	code, body := callBody(t, app, http.MethodPost, "/v1/billing/gpu-charge", "maxpower/dave", "maxpower",
		`{"amountCents":100,"requestId":"r"}`)
	if code != http.StatusBadGateway {
		t.Fatalf("unreadable card gate: want 502, got %d (%s)", code, body)
	}
	if left := walletCents(t, fin, "maxpower"); left != 500_00 {
		t.Errorf("a charge refused at the gate must move no money: got %d", left)
	}
}

// TestGPUCharge_SplitDeployProxiesWithTheKey — with no ledger in this process there is
// nothing here to key on, so the charge forwards exactly as it always did, carrying the
// caller's key to the process that does the write.
func TestGPUCharge_SplitDeployProxiesWithTheKey(t *testing.T) {
	var gotKey, gotPath string
	var charges int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath, charges = r.Header.Get("X-Idempotency-Key"), r.URL.Path, charges+1
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"transactionId":"txn_upstream","status":"ok"}`)
	}))
	t.Cleanup(srv.Close)
	// No finance.Publish: this process has no ledger, which is the split deploy.
	app := mountApp(t, srv.URL, "svc-token")

	code, body := callBody(t, app, http.MethodPost, "/v1/billing/gpu-charge", "maxpower/dave", "maxpower",
		`{"amountCents":24000,"requestId":"launch-abc"}`)
	if code != http.StatusCreated || !strings.Contains(string(body), "txn_upstream") {
		t.Fatalf("split deploy must forward verbatim: got %d (%s)", code, body)
	}
	if gotPath != "/v1/billing/gpu-charge" || charges != 1 {
		t.Errorf("upstream call: path=%q charges=%d", gotPath, charges)
	}
	if gotKey != "launch-abc" {
		t.Errorf("the caller's key must reach the process that writes: got %q", gotKey)
	}
}

// TestGPUCharge_ExactMoney pins that the debit is the amount asked for, to the cent,
// through money.Amount — never a float rounding of it.
func TestGPUCharge_ExactMoney(t *testing.T) {
	f := &countingCommerce{}
	fin := publishLedger(t, "maxpower", 100_00)
	app := mountApp(t, f.server(t).URL, "svc-token")

	code, body := callBody(t, app, http.MethodPost, "/v1/billing/gpu-charge", "maxpower/dave", "maxpower",
		`{"amountCents":1,"currency":"usd","requestId":"penny"}`)
	if code != http.StatusCreated {
		t.Fatalf("charge: want 201, got %d (%s)", code, body)
	}
	if got := walletCents(t, fin, "maxpower"); got != 100_00-1 {
		t.Errorf("a one-cent charge must debit exactly one cent: want %d, got %d", 100_00-1, got)
	}
	_ = money.FromCents(1) // the debit type the handler must use
}
