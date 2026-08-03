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

package o11y

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// The regression this whole file exists for.
//
// Live, before the fix:
//
//	POST /v1/o11y/alerts/hanzo-slack  →  200 ok
//	[o11y] PAGE-DELIVERED … alert=NodeMemoryCommittedCritical
//	[o11y] PAGE-SLACK-FAILED … err=integrations: slack not connected for org
//
// Three green lights and a silent pager. An egress that cannot carry the alert
// must not answer as though it did.
func TestUndeliverableAlertAnswers503(t *testing.T) {
	a := alertsApp(t)
	swapEgress(t, egress{name: "slack", send: func(context.Context, string) error {
		return errors.New("integrations: slack not connected for org")
	}})

	code, body := post(t, a, "/v1/o11y/alerts/hanzo-slack", pagePayload)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("an undelivered alert answered %d %q — Alertmanager will record "+
			"Notify success and never retry", code, body)
	}
	// The body has to name the failure: whoever reads the Alertmanager log next
	// must not need a second lookup to learn which way out broke.
	if !strings.Contains(body, "slack not connected") {
		t.Fatalf("503 body does not name the cause: %q", body)
	}
}

// No egress configured at all is the loudest case, not the quietest. The
// previous code returned early and silently when CLOUD_ALERTS_SLACK_CHANNEL was
// unset — a deployment could page nobody, forever, and answer 200 to every
// notification.
func TestNoEgressConfiguredIsAFailureNotANoOp(t *testing.T) {
	a := alertsApp(t)
	swapEgress(t) // nothing configured

	code, body := post(t, a, "/v1/o11y/alerts/hanzo-pager", pagePayload)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no egress configured answered %d %q, want 503", code, body)
	}
	if !strings.Contains(body, alertsWebhookEnv) || !strings.Contains(body, alertsSlackChannelEnv) {
		t.Fatalf("the 503 must say what to configure, got %q", body)
	}
}

// The fallback carries it when Slack cannot — and the Slack failure is STILL
// recorded. Degraded is not healthy: an egress that breaks silently behind a
// working one is the same class of bug one layer in.
func TestFallbackCarriesItAndTheSlackFailureIsStillRecorded(t *testing.T) {
	a := alertsApp(t)
	var carried atomic.Int32
	swapEgress(t,
		egress{name: "slack", send: func(context.Context, string) error {
			return errors.New("integrations: slack not connected for org")
		}},
		egress{name: "webhook", send: func(context.Context, string) error {
			carried.Add(1)
			return nil
		}},
	)

	code, body := post(t, a, "/v1/o11y/alerts/hanzo-slack", pagePayload)
	if code != http.StatusOK || body != "ok" {
		t.Fatalf("the fallback delivered it; got %d %q, want 200 ok", code, body)
	}
	if carried.Load() != 1 {
		t.Fatalf("fallback egress was not used (%d sends)", carried.Load())
	}

	_, replayed := get(t, a, "/v1/o11y/alerts/last")
	if !strings.Contains(replayed, "ALERT-EGRESS-FAILED egress=slack") {
		t.Fatalf("a broken egress hid behind a working one:\n%s", replayed)
	}
	if !strings.Contains(replayed, "ALERT-DELIVERED via=webhook") {
		t.Fatalf("delivery record does not name the egress that carried it:\n%s", replayed)
	}
}

// The chain stops at the first success. A page delivered twice is a page
// people learn to ignore.
func TestChainStopsAtTheFirstSuccess(t *testing.T) {
	a := alertsApp(t)
	var second atomic.Int32
	swapEgress(t,
		egress{name: "slack", send: func(context.Context, string) error { return nil }},
		egress{name: "webhook", send: func(context.Context, string) error {
			second.Add(1)
			return nil
		}},
	)
	if code, _ := post(t, a, "/v1/o11y/alerts/hanzo-slack", pagePayload); code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
	if second.Load() != 0 {
		t.Fatalf("both egresses fired; the page was delivered twice")
	}
}

// Delivery must be SYNCHRONOUS. The old code sent in a detached goroutine, so
// the response was written before the send was attempted — which made 200
// structurally incapable of describing the send. This pins the ordering: the
// handler cannot answer until the egress has been asked.
func TestDeliveryIsSynchronous(t *testing.T) {
	a := alertsApp(t)
	var attempted atomic.Bool
	swapEgress(t, egress{name: "slow", send: func(context.Context, string) error {
		attempted.Store(true)
		return nil
	}})

	code, _ := post(t, a, "/v1/o11y/alerts/hanzo-pager", pagePayload)
	// No sleep, no poll: if the send were detached this read would race and the
	// answer would already have been written.
	if !attempted.Load() {
		t.Fatalf("the response (%d) was written before the egress was asked", code)
	}
}

// The arrival receipt still lands for an alert that could not be delivered.
// Losing the record of the hop would trade one blind spot for another.
func TestArrivalIsRecordedEvenWhenDeliveryFails(t *testing.T) {
	a := alertsApp(t)
	swapEgress(t, egress{name: "slack", send: func(context.Context, string) error {
		return errors.New("nope")
	}})
	post(t, a, "/v1/o11y/alerts/hanzo-slack", pagePayload)

	_, replayed := get(t, a, "/v1/o11y/alerts/last")
	if !strings.Contains(replayed, "ALERT-RECEIVED") {
		t.Fatalf("arrival receipt lost:\n%s", replayed)
	}
	if !strings.Contains(replayed, "ALERT-UNDELIVERED") {
		t.Fatalf("undelivered outcome not recorded:\n%s", replayed)
	}
}

// PAGE-DELIVERED must not exist anywhere in the record. It sat exactly where an
// operator looks for proof a page landed and answered a different question; a
// grep for it should now find nothing rather than find a lie.
func TestTheMisleadingLineIsGone(t *testing.T) {
	a := alertsApp(t)
	post(t, a, "/v1/o11y/alerts/hanzo-slack", pagePayload)
	_, replayed := get(t, a, "/v1/o11y/alerts/last")
	if strings.Contains(replayed, "PAGE-DELIVERED") {
		t.Fatalf("the misleading line survived:\n%s", replayed)
	}
}

// The fallback egress posts a plain {"text": …} body, so the URL can be a Slack
// incoming webhook, an ntfy topic, or anything else the owner actually has —
// without this code learning a second format.
func TestWebhookEgressPostsTextAndFailsLoudOnNon2xx(t *testing.T) {
	var got struct {
		Text string `json:"text"`
	}
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()

	if err := webhookSend(context.Background(), srv.URL, "hello ops"); err != nil {
		t.Fatalf("webhook send: %v", err)
	}
	if got.Text != "hello ops" {
		t.Fatalf("webhook body carried %q", got.Text)
	}

	// A fallback that swallowed a failure would leave nothing underneath to
	// notice, so a non-2xx is an error.
	status.Store(http.StatusInternalServerError)
	err := webhookSend(context.Background(), srv.URL, "hello ops")
	if err == nil {
		t.Fatal("a 500 from the fallback egress was reported as delivered")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error does not carry the status: %v", err)
	}
}

// configuredEgresses reads the environment and lists ONLY what is actually
// configured. An egress that cannot be attempted must never be counted as one
// that was — that arithmetic is what makes "no egress" a 503.
func TestConfiguredEgressesReflectTheEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, channel, url string
		want               []string
	}{
		{"neither", "", "", nil},
		{"slack only", "#hanzo-ops", "", []string{"slack"}},
		{"webhook only", "", "https://example.invalid/hook", []string{"webhook"}},
		{"both, slack first", "#hanzo-ops", "https://example.invalid/hook", []string{"slack", "webhook"}},
		{"whitespace is not configuration", "  ", "  ", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(alertsSlackChannelEnv, tc.channel)
			t.Setenv(alertsWebhookEnv, tc.url)
			var got []string
			for _, e := range configuredEgresses() {
				got = append(got, e.name)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// A malformed body is still an arrival, and it still has to be DELIVERED or
// reported as undelivered. The old contract answered 200 to everything; the new
// one keeps "do not 4xx a bad payload" (Alertmanager would retry it forever)
// without inheriting "always claim success".
func TestUnparseableBodyIsRecordedButStillNeedsAnEgress(t *testing.T) {
	a := alertsApp(t)
	swapEgress(t) // none configured
	code, _ := post(t, a, "/v1/o11y/alerts/page", "not json at all")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 — the body is unparseable, the egress is missing", code)
	}
	_, replayed := get(t, a, "/v1/o11y/alerts/last")
	if !strings.HasPrefix(replayed, "ALERT-RECEIVED path=/v1/o11y/alerts/page receiver=?") {
		t.Fatalf("unparseable arrival not recorded: %q", replayed)
	}
}
