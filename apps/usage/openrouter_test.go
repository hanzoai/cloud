// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package usage

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// delivery is one Broadcast POST as OpenRouter sends it: OTLP/JSON carrying a
// generation span and the parent span it wraps that in. The attribute names are
// OpenRouter's own — gen_ai.* for the model, tokens and cost, openrouter.* for the
// identity the Identity category adds. gen_ai.usage.output_tokens is deliberately a
// bare NUMBER while its siblings are strings: OTLP's canonical int64 is a string and
// senders emit both, so one payload proves both decode.
const delivery = `{"resourceSpans":[{
 "resource":{"attributes":[{"key":"service.name","value":{"stringValue":"openrouter"}}]},
 "scopeSpans":[{"spans":[
  {"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"00f067aa0ba902b7","name":"chat",
   "startTimeUnixNano":"1767225600000000000","endTimeUnixNano":"1767225601000000000",
   "status":{"code":1},
   "attributes":[
    {"key":"gen_ai.request.model","value":{"stringValue":"anthropic/claude-sonnet-4.5"}},
    {"key":"gen_ai.response.model","value":{"stringValue":"anthropic/claude-sonnet-4.5"}},
    {"key":"gen_ai.usage.input_tokens","value":{"intValue":"1200"}},
    {"key":"gen_ai.usage.output_tokens","value":{"intValue":340}},
    {"key":"gen_ai.usage.total_tokens","value":{"intValue":"1540"}},
    {"key":"gen_ai.usage.input_tokens.cached","value":{"intValue":"1024"}},
    {"key":"gen_ai.usage.total_cost","value":{"doubleValue":0.013542}},
    {"key":"openrouter.api_key_name","value":{"stringValue":"agents-prod"}},
    {"key":"openrouter.provider_name","value":{"stringValue":"Anthropic"}},
    {"key":"user.id","value":{"stringValue":"z@hanzo.ai"}}
   ]},
  {"traceId":"4bf92f3577b34da6a3ce929d0e0e4736","spanId":"1a2b3c4d5e6f7081",
   "name":"Order Processing","startTimeUnixNano":"1767225599000000000",
   "attributes":[{"key":"trace.name","value":{"stringValue":"Order Processing"}}]}
 ]}]}]}`

func decodeDelivery(t *testing.T) trace {
	t.Helper()
	var tr trace
	if err := json.Unmarshal([]byte(delivery), &tr); err != nil {
		t.Fatalf("decode delivery: %v", err)
	}
	return tr
}

// TestDeliveryBecomesAUsageRow is the whole point of the door: a real Broadcast
// payload has to arrive in hanzo.cloud_usage as a row the money lenses already read
// — provider `openrouter` so GROUP BY provider finds it, the cost in the nano-USD
// money of record AND in the cents rendering that must agree with it, and the KEY
// THAT SPENT IT in `account`, which is the fact nothing else in the estate can
// supply. The whole row is compared, in openrouterInsert's column order, so a
// mapping that moves a value into the wrong column fails here rather than writing a
// plausible-looking lie into the ledger.
func TestDeliveryBecomesAUsageRow(t *testing.T) {
	tr := decodeDelivery(t)
	spans := tr.ResourceSpans[0].ScopeSpans[0].Spans
	if len(spans) != 2 {
		t.Fatalf("decoded %d spans, want 2", len(spans))
	}
	// The clock only answers for a span that names no start; this one does, so a
	// distinctive `now` proves the span's own instant is what lands.
	now := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	row, ok := usageRow("hanzo", spans[0], now)
	if !ok {
		t.Fatal("the generation span was not metered")
	}
	// $0.013542 == 13_542_000 nano-USD == 1.3542 cents, which rounds to 1.
	want := []any{
		"00f067aa0ba902b7", time.Unix(0, 1767225600000000000).UTC(),
		"hanzo", "z@hanzo.ai", "hanzo",
		"anthropic/claude-sonnet-4.5", "openrouter", "00f067aa0ba902b7",
		uint32(1200), uint32(340), uint32(1540), uint32(1024),
		uint64(1), int64(13_542_000), int64(13_542_000), "usd",
		"success", "",
		"openrouter/agents-prod",
	}
	if !reflect.DeepEqual(row, want) {
		t.Fatalf("row mismatch\n got %#v\nwant %#v", row, want)
	}
	// The column list and the bound arguments are ONE fact written twice; a row that
	// is a value short of its statement is a runtime failure at the warehouse, which
	// is the worst place to find it.
	if n := strings.Count(openrouterInsert, "?"); n != len(want) {
		t.Fatalf("openrouterInsert binds %d values, the row has %d", n, len(want))
	}
	// The parent span names no model, carries no cost, and must not become a row.
	if _, ok := usageRow("hanzo", spans[1], now); ok {
		t.Error("a trace parent was metered as a generation")
	}
}

// TestUnkeyedDeliveryStoresNothing is the door's admission contract. Broadcast signs
// nothing — its only authentication is the Headers map it sends verbatim — so the
// credential IS the proof, and a delivery that cannot present a key we minted has to
// be refused before anything reaches the ledger.
//
// The two outcomes are DISTINGUISHABLE, which is what makes this a test of admission
// rather than of a status code: a refused delivery answers 401 and never touches the
// warehouse, while an ADMITTED one gets as far as the write and answers 503 in a
// process with no warehouse behind it. A door that stopped checking the credential
// would answer 503 to the forgery, and that is the failure this catches.
func TestUnkeyedDeliveryStoresNothing(t *testing.T) {
	app := mountApp(t)
	// The seam every keyed door resolves through, stubbed so the test needs no IAM:
	// one key is real, everything else names no org — including a key shaped exactly
	// like a real one.
	orig := orgForKey
	orgForKey = func(_ context.Context, key string) (string, bool) {
		return "hanzo", key == "pk-real"
	}
	t.Cleanup(func() { orgForKey = orig })

	for _, tc := range []struct {
		name string
		auth string
		want int
	}{
		{"no credential at all", "", http.StatusUnauthorized},
		{"a forged key", "Bearer pk-forged", http.StatusUnauthorized},
		{"a bare secret in the header", "hunter2", http.StatusUnauthorized},
		{"the minted key", "Bearer pk-real", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := post(t, app, delivery, tc.auth)
			if code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", code, tc.want, body)
			}
		})
	}
}

// TestEmptyDeliveryIsAccepted keeps Test Connection green: OpenRouter saves a
// destination only if it answers 2xx to an empty payload, so a door that 400s one
// can never be configured at all.
func TestEmptyDeliveryIsAccepted(t *testing.T) {
	app := mountApp(t)
	orig := orgForKey
	orgForKey = func(context.Context, string) (string, bool) { return "hanzo", true }
	t.Cleanup(func() { orgForKey = orig })

	code, body := post(t, app, "", "Bearer pk-real")
	if code != http.StatusOK {
		t.Fatalf("empty payload = %d, want 200 (%s)", code, body)
	}
	var r receipt
	if err := json.Unmarshal(body, &r); err != nil || r.Stored != 0 {
		t.Fatalf("receipt = %s, want stored 0", body)
	}
}

// post drives one Broadcast delivery at the door.
func post(t *testing.T, app *zip.App, body, auth string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, openrouterPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test POST %s: %v", openrouterPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
