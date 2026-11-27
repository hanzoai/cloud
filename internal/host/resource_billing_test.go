package host

// Tests for the shared per-org resource gate+meter (Meter). They drive
// the real metering client against a fake commerce server that RECORDS the
// X-Org-Id org header and request bodies, so the multitenancy contract is
// proven end-to-end over HTTP — no mock of the metering client itself. The fake
// is built with a metering DEFAULT org of "hanzo"; every assertion that the
// caller "acme" is billed (not "hanzo") proves the per-call org override is what
// scopes the ledger — i.e. a caller can never be billed to a default or another
// org.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/client"
	cloud "github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/sdk/meter"
)

// recCommerce is the money peer for these tests: it answers the balance READ over HTTP
// and receives the usage DEBIT over the plane, recording the org each side acted for —
// the evidence for the per-org / cross-org assertions.
type recCommerce struct {
	balanceAvailable int64 // returned as {"available":N} on GET /v1/billing/balance
	balanceStatus    int   // 0 => 200

	debits planeDebits

	mu           sync.Mutex
	balanceOrg   string // last X-Org-Id on a balance call
	balanceCalls atomic.Int32
}

// meterFor builds a Meter whose metering client has DEFAULT org "hanzo"
// (so a caller-org assertion proves the per-call override), at the given env.
func meterFor(t *testing.T, baseURL, env string) *cloud.Meter {
	t.Helper()
	m, err := meter.New(meter.Config{BaseURL: baseURL})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	t.Setenv("CLOUD_ENV", env)
	return cloud.NewMeter(cloud.Deps{Metering: m}, "provisioning")
}

func (f *recCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	f.debits.serve(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		f.balanceCalls.Add(1)
		f.mu.Lock()
		f.balanceOrg = r.Header.Get("X-Org-Id")
		f.mu.Unlock()
		status := f.balanceStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"available": f.balanceAvailable})
	})
	// NO /v1/billing/usage route: the debit crosses the plane now, and a debit that
	// still went over HTTP would 404 here rather than quietly counting.
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *recCommerce) usages() int32 { return f.debits.count() }

func (f *recCommerce) balances() int32 { return f.balanceCalls.Load() }

func (f *recCommerce) lastBalanceOrg() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.balanceOrg
}

// lastUsage is the org the debit was written to and the debit itself.
func (f *recCommerce) lastUsage() (string, client.RecordIn) {
	d, _ := f.debits.last()
	return d.Org, d.In
}
