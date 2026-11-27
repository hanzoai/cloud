package ledger_test

import (
	"net/http"

	"github.com/hanzoai/cloud/sdk/meter"
)

// Example shows the ONE way a non-LLM product opts into pay-for-everything:
// build the client on the commerce address and wrap the handler. Every request is
// then balance-gated (fail-closed, with no switch that opens it) and metered to
// commerce.
func Example() {
	mtr, err := meter.New(meter.Config{BaseURL: "http://commerce.hanzo.svc.cluster.local:8001"})
	if err != nil {
		panic(err)
	}

	// Price by outcome: charge a flat 5 cents per successful search request.
	priceSearch := func(r *http.Request, status int, in meter.AuthInput) int64 {
		if status >= 200 && status < 300 {
			return 5
		}
		return 0
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[]}`))
	})

	// One wrap meters the whole surface.
	handler := mtr.Middleware(meter.MiddlewareConfig{
		Provider: "search",
		Price:    priceSearch,
		Skip:     func(r *http.Request) bool { return r.URL.Path == "/healthz" },
	})(mux)

	_ = handler // http.ListenAndServe(":8080", handler)
}

// Example_imperative shows direct use without middleware — for products that
// price per unit of work discovered during the request (e.g. functions billed
// by execution time), gating up front and recording the measured cost after.
func Example_imperative() {
	mtr, _ := meter.New(meter.Config{BaseURL: "http://commerce.hanzo.svc.cluster.local:8001"})

	handle := func(w http.ResponseWriter, r *http.Request) {
		in := meter.IdentityFromGatewayHeaders(r)

		// Pre-request gate (fail-closed).
		if err := mtr.Authorize(r.Context(), in); err != nil {
			if err == meter.ErrInsufficientBalance {
				http.Error(w, "insufficient balance", http.StatusPaymentRequired)
			} else {
				http.Error(w, "billing unavailable", http.StatusServiceUnavailable)
			}
			return
		}

		// ... do the work, measuring cost ...
		costCents := int64(12)

		w.WriteHeader(http.StatusOK)

		// Post-request record (best-effort).
		_, _ = mtr.Record(r.Context(), meter.Usage{
			User:        in.User,
			Org:         in.Org,
			AmountCents: costCents,
			Provider:    "functions",
			RequestID:   r.Header.Get("X-Request-Id"),
			Status:      "success",
		})
	}
	_ = handle
}
