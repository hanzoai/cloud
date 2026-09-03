package planetest

// ledger.go — a whole money double, both halves.
//
// A metered surface cannot be tested with only one of them. The gate reads a
// BALANCE over HTTP and the debit crosses the PLANE, so a test holding just the
// recorder proves a charge landed while saying nothing about whether an unfunded
// caller was stopped — which is the half that matters more, because it is the one
// that spends a vendor's money. Assembling the pair was being copied per package.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/metering"
)

// Ledger is the plane peer that records debits plus the balance every
// Meter.Authorize reads before allowing one.
type Ledger struct {
	*Commerce
	// Available is the balance the gate reads, in cents. Zero refuses.
	Available int64
	// URL is the commerce base a metering.Client is built against.
	URL string
}

// Money binds both halves. available is the starting balance, in cents.
func Money(t *testing.T, available int64) *Ledger {
	t.Helper()
	l := &Ledger{Commerce: Serve(t), Available: available}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"available": l.Available})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	l.URL = srv.URL
	return l
}

// Client is the metering client a subsystem bills through, pointed at this
// ledger. A caller wraps it in whatever its Mount takes — cloud.Deps{Metering: …}
// for an app that builds its own Base, cloud.NewMeter for one that keeps a
// package-global meter.
//
// It stops at the CLIENT on purpose. Package cloud's own tests import this
// package, so a helper here returning a cloud.Deps or a cloud.Meter would
// close an import cycle. The money doubles are what is worth sharing; the one line
// that wraps them is not worth a cycle.
//
// The client's default org is "hanzo", deliberately, and it is NOT any test's
// caller: an assertion that the caller's OWN org was debited therefore also proves
// the charge did not quietly land on the client default. Every test that names an
// org gets the cross-tenant property for free.
func (l *Ledger) Client(t *testing.T) *metering.Client {
	t.Helper()
	m, err := metering.New(metering.Config{BaseURL: l.URL, Org: "hanzo"})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	return m
}

// Charged reports the one debit recorded: who was billed, how much, and the unit
// it was billed as. ok is false when nothing was recorded, so a test can assert
// "billed nobody" without reaching into the recorder.
func (l *Ledger) Charged() (org string, cents int64, model string, ok bool) {
	d, ok := l.Last()
	if !ok {
		return "", 0, "", false
	}
	return l.Org(), Cents(d.In.Amount), d.In.Usage.Model, true
}
