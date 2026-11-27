// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package host

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/otlptranslator"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// collectUsage installs a provider this test can read, resets the instrument Once
// (production has one composition root; a test has many), and returns a collector.
//
// Every assertion below drives the REAL Meter — rm.Authorize and rm.Record — and
// never the observe* helpers, because the property under test is that the money
// plane's call sites emit, not that a counter can count.
func collectUsage(t *testing.T) func() map[string]map[string]float64 {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	usageOnce = sync.Once{}
	usageDebits, usageGate, usageCharged, usageTokens = nil, nil, nil, nil
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		usageOnce = sync.Once{}
		usageDebits, usageGate, usageCharged, usageTokens = nil, nil, nil, nil
	})

	// Points are keyed by their attributes rendered as k=v,k=v in attribute order,
	// so an assertion names the series it means rather than an index.
	return func() map[string]map[string]float64 {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("collect: %v", err)
		}
		out := map[string]map[string]float64{}
		for _, s := range rm.ScopeMetrics {
			for _, m := range s.Metrics {
				pts := map[string]float64{}
				switch d := m.Data.(type) {
				case metricdata.Sum[int64]:
					for _, p := range d.DataPoints {
						pts[p.Attributes.Encoded(attribute.DefaultEncoder())] = float64(p.Value)
					}
				case metricdata.Sum[float64]:
					for _, p := range d.DataPoints {
						pts[p.Attributes.Encoded(attribute.DefaultEncoder())] = p.Value
					}
				default:
					continue
				}
				out[m.Name] = pts
			}
		}
		return out
	}
}

// A debit nobody can see is a debit nobody can alert on. The ledger is exact and
// private — answering "is anything still being billed" out of it means reading
// every org's books — so the operator's question is answered by a measurement or
// it is not answered at all.
//
// It also proves the two things the ledger deliberately does NOT hold: the exact
// sub-cent charge, and the QUANTITY that charge priced. The books carry dollars
// and are right to carry nothing else; without this series "how many tokens did
// that org burn" has no answer anywhere in the process.
func TestADebitIsVisibleToAnAlert(t *testing.T) {
	read := collectUsage(t)
	fc := &recCommerce{balanceAvailable: 500000}
	rm := meterFor(t, fc.server(t).URL, "mainnet")

	rm.Record(account.PayerOf("", "acme"), "chat", meter.Usage{
		Model:            "gpt-5",
		AmountMicros:     1234, // $0.001234 — sub-cent on purpose
		PromptTokens:     120,
		CompletionTokens: 30,
	})
	// A second act, priced in CENTS — the other of the two amount sources. Both are
	// asserted from ONE series so the two cannot drift in scale: Money().Micros()
	// pads a cent-scaled decimal out to micros, and a version of it that returned
	// the raw coefficient instead would put this charge in at 0.00025 rather than
	// 2.50 and nothing would notice.
	rm.Record(account.PayerOf("", "acme"), "chat", meter.Usage{
		Model:       "gpt-5",
		AmountCents: 250,
	})
	// The ledger answers before the meter observes the answer, so wait on the
	// measurement itself, not on the ledger having been called.
	posted := func() bool { return read()["hanzo_usage_debits_total"]["outcome=posted"] == 2 }
	if !waitFor(func() bool { return fc.usages() == 2 && posted() }, 10*time.Second) {
		t.Fatalf("usage records = %d, debits = %v, want 2 posted", fc.usages(), read()["hanzo_usage_debits_total"])
	}

	got := read()
	debits := got["hanzo_usage_debits_total"]
	if debits == nil {
		t.Fatal("hanzo_usage_debits_total was never recorded — a billing-stopped alert " +
			"would have nothing to read, which is how a silent stop stays silent")
	}
	if n := debits["outcome=posted"]; n != 2 {
		t.Fatalf("debits{posted} = %v, want 2 (in %v)", n, debits)
	}
	// ZERO IS A MEASUREMENT: the outcomes that did not happen must still EXIST, or
	// "no debit failed in 30m" is indistinguishable from "the counter was never built".
	for _, o := range []string{"outcome=failed", "outcome=unbilled"} {
		if _, ok := debits[o]; !ok {
			t.Fatalf("%s series absent — unseeded, so silence cannot be read as zero (in %v)", o, debits)
		}
	}

	who := "org=acme,product=provisioning"
	if usd := got["hanzo_usage_charged_usd_total"][who]; usd != 2.501234 {
		t.Fatalf("charged = %v USD, want 2.501234 ($0.001234 + $2.50) — a sub-cent charge "+
			"rounded away is exactly the amount the rollup exists to show, and a cent amount "+
			"read at the wrong scale is off by ten thousand (in %v)",
			usd, got["hanzo_usage_charged_usd_total"])
	}
	toks := got["hanzo_usage_tokens_total"]
	if n := toks["direction=prompt,"+who]; n != 120 {
		t.Fatalf("prompt tokens = %v, want 120 (in %v)", n, toks)
	}
	if n := toks["direction=completion,"+who]; n != 30 {
		t.Fatalf("completion tokens = %v, want 30 (in %v)", n, toks)
	}
}

// A debit into a deployment with no ledger is not an error and is not a charge —
// it is the third thing, and it has to be countable on its own. A deployment that
// HAS commerce and reads `unbilled` is giving work away with a green error rate.
func TestAnUnbilledDebitIsNotAFailedOne(t *testing.T) {
	read := collectUsage(t)
	m, err := meter.New(meter.Config{}) // no BaseURL, no peer
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}
	rm := cloud.NewMeter(cloud.Deps{Metering: m}, "provisioning")

	rm.Record(account.PayerOf("", "acme"), "chat", meter.Usage{Model: "gpt-5", AmountCents: 7})

	if !waitFor(func() bool { return read()["hanzo_usage_debits_total"]["outcome=unbilled"] == 1 }, 2*time.Second) {
		t.Fatalf("debits = %v, want one unbilled", read()["hanzo_usage_debits_total"])
	}
	d := read()["hanzo_usage_debits_total"]
	if d["outcome=failed"] != 0 || d["outcome=posted"] != 0 {
		t.Fatalf("debits = %v, want ONLY unbilled — a missing ledger is neither a charge nor a fault", d)
	}
	if usd := read()["hanzo_usage_charged_usd_total"]; len(usd) != 0 {
		t.Fatalf("charged = %v, want empty — money nobody took must not appear in the rollup", usd)
	}
}

// The gate's verdicts say WHOSE problem a refusal is, and that distinction is the
// whole reason to count them separately: no-funds and cap-spent are the customer's
// to fix, unknown is the operator's. Folded into one "denied" counter, a commerce
// outage looks exactly like a fleet of broke customers.
func TestAGateVerdictNamesWhoseProblemItIs(t *testing.T) {
	capped := func(reason string) *httptest.Server {
		planetest.Serve(t).Cap(client.CapVerdict{Allow: false, Reason: reason})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/billing/balance":
				_, _ = io.WriteString(w, `{"available":100000}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	for _, tc := range []struct {
		name, want string
		gate       func(t *testing.T) *cloud.Meter
	}{
		{"funded", "verdict=allow", func(t *testing.T) *cloud.Meter {
			return meterFor(t, (&recCommerce{balanceAvailable: 5000}).server(t).URL, "mainnet")
		}},
		{"broke", "verdict=no-funds", func(t *testing.T) *cloud.Meter {
			return meterFor(t, (&recCommerce{balanceAvailable: 0}).server(t).URL, "mainnet")
		}},
		{"over cap", "verdict=cap-spent", func(t *testing.T) *cloud.Meter {
			return meterFor(t, capped("spend_cap").URL, "mainnet")
		}},
		{"commerce down", "verdict=unknown", func(t *testing.T) *cloud.Meter {
			return meterFor(t, (&recCommerce{balanceStatus: 500}).server(t).URL, "mainnet")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := collectUsage(t)
			rm := tc.gate(t)
			_ = rm.Authorize(t.Context(), account.PayerOf("", "acme"), "acme-prod", true, "sql", 100)

			g := read()["hanzo_usage_gate_total"]
			if g == nil {
				t.Fatal("hanzo_usage_gate_total was never recorded — refusals are invisible")
			}
			if n := g[tc.want]; n != 1 {
				t.Fatalf("%s = %v, want 1 (in %v)", tc.want, n, g)
			}
			// The whole vocabulary exists whether or not it fired, so a rule written
			// against any verdict reads zero rather than nothing.
			for _, v := range gateVerdicts {
				if _, ok := g["verdict="+v]; !ok {
					t.Fatalf("verdict=%s absent — unseeded (in %v)", v, g)
				}
			}
		})
	}
}

// The series have to survive the step between the SDK and the store. The manual
// reader above proves the instruments RECORD; the process writes them out through
// the Prometheus exporter into cloud's own registry, which apps/o11y drains on a
// timer, and a name the exporter rewrites is a name no alert rule matches.
//
// This is the step that has gone wrong here before — the estate ran four and a
// half months of dead span ingest under a green dashboard — so it is measured
// rather than reasoned about. The exporter is built exactly as installMeter builds
// it, including NoTranslation, which is what keeps a unit from being appended to a
// name that already carries one.
func TestTheUsageSeriesReachTheRegistryUnrenamed(t *testing.T) {
	reg := prometheus.NewRegistry()
	exp, err := otelprom.New(
		otelprom.WithRegisterer(reg),
		otelprom.WithTranslationStrategy(otlptranslator.NoTranslation),
		otelprom.WithoutScopeInfo(),
		otelprom.WithoutTargetInfo(),
	)
	if err != nil {
		t.Fatalf("exporter: %v", err)
	}
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp)))
	usageOnce = sync.Once{}
	usageDebits, usageGate, usageCharged, usageTokens = nil, nil, nil, nil
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		usageOnce = sync.Once{}
		usageDebits, usageGate, usageCharged, usageTokens = nil, nil, nil, nil
	})

	fc := &recCommerce{balanceAvailable: 500000}
	rm := meterFor(t, fc.server(t).URL, "mainnet")
	_ = rm.Authorize(t.Context(), account.PayerOf("", "acme"), "", false, "sql", 100)
	rm.Record(account.PayerOf("", "acme"), "chat", meter.Usage{
		Model: "gpt-5", AmountCents: 250, PromptTokens: 120, CompletionTokens: 30,
	})
	if !waitFor(func() bool { return fc.usages() == 1 }, 2*time.Second) {
		t.Fatalf("usage records = %d, want 1", fc.usages())
	}

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range families {
		seen[f.GetName()] = true
	}
	for _, name := range []string{
		"hanzo_usage_gate_total",
		"hanzo_usage_debits_total",
		"hanzo_usage_charged_usd_total",
		"hanzo_usage_tokens_total",
	} {
		if !seen[name] {
			got := make([]string, 0, len(seen))
			for n := range seen {
				got = append(got, n)
			}
			sort.Strings(got)
			t.Fatalf("%s is absent from the registry apps/o11y drains — an alert rule on it "+
				"would match nothing. Present: %v", name, got)
		}
	}
}
