// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// alertsApp builds the receiver exactly as mountAlerts registers it, so the
// tests exercise the real route table and not the handlers in isolation.
func alertsApp(t *testing.T) *zip.App {
	t.Helper()
	recent = alertRing{}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	mountAlerts(app)
	return app
}

// post/get wrap the package's shared `do` helper (scope_test.go) so there is
// one request path in these tests.
func post(t *testing.T, app *zip.App, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	code, b := do(t, app, req)
	return code, string(b)
}

func get(t *testing.T, app *zip.App, path string) (int, string) {
	t.Helper()
	code, b := do(t, app, httptest.NewRequest(http.MethodGet, path, nil))
	return code, string(b)
}

// The exact payload Alertmanager v0.27 posts, so the receipt is proven against
// the wire format rather than a hand-tuned fixture.
const pagePayload = `{
  "receiver": "lux-pager",
  "status": "firing",
  "alerts": [
    {"status":"firing",
     "labels":{"alertname":"LuxNetworkDown","severity":"critical","page":"true","network":"mainnet","instance":"luxd-0","brand":"lux"},
     "annotations":{"summary":"mainnet C-Chain has no reachable validator","description":"0/5 up"}}
  ],
  "commonLabels": {"brand":"lux","network":"mainnet","alertname":"LuxNetworkDown"},
  "commonAnnotations": {"summary":"mainnet C-Chain has no reachable validator"}
}`

func TestReceiptLineMatchesTheReplacedReceiver(t *testing.T) {
	var p webhook
	if err := json.Unmarshal([]byte(pagePayload), &p); err != nil {
		t.Fatal(err)
	}
	got := receipt("/v1/o11y/alerts/page", &p, p.Alerts[0])
	want := "PAGE-DELIVERED path=/v1/o11y/alerts/page receiver=lux-pager status=firing " +
		"alert=LuxNetworkDown severity=critical page=true network=mainnet instance=luxd-0 " +
		":: mainnet C-Chain has no reachable validator"
	if got != want {
		t.Fatalf("receipt mismatch\n got: %s\nwant: %s", got, want)
	}
}

// Every field has a defined stand-in when the payload omits it. These are the
// defaults the previous receiver used and operators' greps depend on them.
func TestReceiptDefaultsForAnEmptyPayload(t *testing.T) {
	var p webhook
	got := receipt("/v1/o11y/alerts/default", &p, alerts(&p)[0])
	want := "PAGE-DELIVERED path=/v1/o11y/alerts/default receiver=? status=? alert=? " +
		"severity=? page=- network=- instance=- :: "
	if got != want {
		t.Fatalf("defaults mismatch\n got: %q\nwant: %q", got, want)
	}
}

// No "alerts" key still yields one receipt, built from the common labels — a
// delivery that recorded nothing would be the one failure this cannot have.
func TestPayloadWithoutAlertsStillProducesOneReceipt(t *testing.T) {
	p := webhook{
		Receiver:          "lux-watchdog",
		Status:            "firing",
		CommonLabels:      map[string]string{"alertname": "Watchdog", "severity": "none"},
		CommonAnnotations: map[string]string{"summary": "dead-man switch"},
	}
	got := alerts(&p)
	if len(got) != 1 {
		t.Fatalf("want 1 synthetic alert, got %d", len(got))
	}
	line := receipt("/v1/o11y/alerts/watchdog", &p, got[0])
	if !strings.Contains(line, "alert=Watchdog") || !strings.Contains(line, ":: dead-man switch") {
		t.Fatalf("synthetic receipt lost the common labels: %s", line)
	}
	if !strings.Contains(line, "status=firing") {
		t.Fatalf("synthetic receipt must fall back to the payload status: %s", line)
	}
}

func TestPostAlwaysAnswersOKAndReplayShowsIt(t *testing.T) {
	a := alertsApp(t)

	code, body := get(t, a, "/v1/o11y/alerts/last")
	if code != 200 || body != "(none)" {
		t.Fatalf("empty replay: got %d %q, want 200 %q", code, body, "(none)")
	}

	for _, r := range []string{"default", "watchdog", "page", "slack"} {
		code, body := post(t, a, "/v1/o11y/alerts/"+r, pagePayload)
		if code != 200 || body != "ok" {
			t.Fatalf("POST /%s: got %d %q, want 200 %q", r, code, body, "ok")
		}
	}

	code, body = get(t, a, "/v1/o11y/alerts/last")
	if code != 200 {
		t.Fatalf("replay: got %d", code)
	}
	lines := strings.Split(body, "\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 receipts, got %d:\n%s", len(lines), body)
	}
	for i, r := range []string{"default", "watchdog", "page", "slack"} {
		if !strings.HasPrefix(lines[i], "PAGE-DELIVERED path=/v1/o11y/alerts/"+r+" ") {
			t.Fatalf("line %d is not the %s receipt: %s", i, r, lines[i])
		}
	}
}

// A body Alertmanager could not have sent still proves the hop happened, so it
// is recorded rather than rejected: a 4xx here would make Alertmanager retry
// and the receipt would misreport how many pages were delivered.
func TestUnparseableBodyIsStillARecordedDelivery(t *testing.T) {
	a := alertsApp(t)
	code, body := post(t, a, "/v1/o11y/alerts/page", "not json at all")
	if code != 200 || body != "ok" {
		t.Fatalf("got %d %q, want 200 %q", code, body, "ok")
	}
	_, replayed := get(t, a, "/v1/o11y/alerts/last")
	if !strings.HasPrefix(replayed, "PAGE-DELIVERED path=/v1/o11y/alerts/page receiver=? status=?") {
		t.Fatalf("unparseable delivery not recorded: %q", replayed)
	}
}

func TestRingIsBoundedAndKeepsTheNewest(t *testing.T) {
	a := alertsApp(t)
	for i := 0; i < recentMax+50; i++ {
		body := fmt.Sprintf(`{"receiver":"r%d","alerts":[{"labels":{"alertname":"A%d"}}]}`, i, i)
		if code, _ := post(t, a, "/v1/o11y/alerts/page", body); code != 200 {
			t.Fatalf("post %d: %d", i, code)
		}
	}
	_, replayed := get(t, a, "/v1/o11y/alerts/last")
	lines := strings.Split(replayed, "\n")
	if len(lines) != recentMax {
		t.Fatalf("ring unbounded: %d lines, want %d", len(lines), recentMax)
	}
	if !strings.Contains(lines[0], "alert=A50") {
		t.Fatalf("oldest survivor should be A50, got: %s", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], fmt.Sprintf("alert=A%d", recentMax+49)) {
		t.Fatalf("newest lost: %s", lines[len(lines)-1])
	}
}
