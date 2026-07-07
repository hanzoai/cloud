package billing

import (
	"encoding/json"
	"net/http"
	"testing"
)

// multiProductLedger is a commerce GetUsage envelope carrying rows from three
// different metering surfaces (functions, s3, and an LLM/token row) so the tests
// can prove usage() attributes each to its canonical product, filters, and groups.
const multiProductLedger = `{"user":"maxpower","count":3,"usage":[` +
	`{"transactionId":"t1","amount":10,"metadata":{"provider":"functions","model":"invoke"},"createdAt":"2026-07-01T00:00:00Z"},` +
	`{"transactionId":"t2","amount":5,"metadata":{"provider":"s3","model":"op"},"createdAt":"2026-07-01T00:00:01Z"},` +
	`{"transactionId":"t3","amount":7,"metadata":{"provider":"openai","model":"gpt-4o","promptTokens":3}}` +
	`]}`

// The plain read injects a canonical product onto every row (the console reads
// metadata.product), from the SAME charged ledger — no filter given.
func TestUsageHandler_InjectsProduct(t *testing.T) {
	f := &fakeCommerce{status: 200, body: multiProductLedger}
	app := mountApp(t, f.server(t).URL, "svc-token")

	code, body := call(t, app, http.MethodGet, "/v1/billing/usage", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	var got struct {
		Usage []struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	want := []string{"functions", "s3", "inference"}
	if len(got.Usage) != 3 {
		t.Fatalf("want 3 rows, got %d (%s)", len(got.Usage), body)
	}
	for i, w := range want {
		if got.Usage[i].Metadata["product"] != w {
			t.Fatalf("row %d product: want %s, got %v", i, w, got.Usage[i].Metadata["product"])
		}
	}
}

// ?product= filters server-side through the real route (was silently ignored).
func TestUsageHandler_FilterByProduct(t *testing.T) {
	f := &fakeCommerce{status: 200, body: multiProductLedger}
	app := mountApp(t, f.server(t).URL, "svc-token")

	code, body := call(t, app, http.MethodGet, "/v1/billing/usage?product=s3", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	var got struct {
		Count int `json:"count"`
		Usage []struct {
			TransactionID string         `json:"transactionId"`
			Metadata      map[string]any `json:"metadata"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Count != 1 || len(got.Usage) != 1 || got.Usage[0].TransactionID != "t2" {
		t.Fatalf("product=s3 filter: want only t2, got %s", body)
	}
	// The subject is still pinned to the caller's own org; product is NOT a subject key.
	if f.gotQuery.Get("user") != "maxpower" {
		t.Fatalf("subject pin lost: %q", f.gotQuery.Get("user"))
	}
}

// ?groupBy=product reduces to a per-product spend rollup through the real route.
func TestUsageHandler_GroupByProduct(t *testing.T) {
	f := &fakeCommerce{status: 200, body: multiProductLedger}
	app := mountApp(t, f.server(t).URL, "svc-token")

	code, body := call(t, app, http.MethodGet, "/v1/billing/usage?groupBy=product", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	var got struct {
		GroupBy string `json:"groupBy"`
		Groups  []struct {
			Product     string `json:"product"`
			Requests    int    `json:"requests"`
			AmountCents int64  `json:"amountCents"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if got.GroupBy != "product" || len(got.Groups) != 3 {
		t.Fatalf("groupBy=product: want 3 groups, got %s", body)
	}
	by := map[string]int64{}
	for _, g := range got.Groups {
		by[g.Product] = g.AmountCents
	}
	if by["functions"] != 10 || by["s3"] != 5 || by["inference"] != 7 {
		t.Fatalf("group spend wrong: %s", body)
	}
}

// A commerce error status is passed through untouched — enrichment never runs on
// a non-200, so an upstream failure is never masked as an (empty) success.
func TestUsageHandler_UpstreamErrorPassthrough(t *testing.T) {
	f := &fakeCommerce{status: 500, body: `{"error":"boom"}`}
	app := mountApp(t, f.server(t).URL, "svc-token")

	code, body := call(t, app, http.MethodGet, "/v1/billing/usage?product=functions", "maxpower/dave", "maxpower")
	if code != 500 || string(body) != `{"error":"boom"}` {
		t.Fatalf("upstream error must pass through: code=%d body=%s", code, body)
	}
}
