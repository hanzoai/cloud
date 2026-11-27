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
	"net/http"
	"strings"
	"testing"
	"time"
)

// The ruler's always-firing watchdog, as it arrives: severity none, watchdog true.
const watchdogPayload = `{
  "receiver": "watchdog-sink",
  "status": "firing",
  "alerts": [
    {"status":"firing",
     "labels":{"alertname":"HanzoMonitoringWatchdog","severity":"none","watchdog":"true","brand":"hanzo"},
     "annotations":{"summary":"Hanzo alerting pipeline is alive"}}
  ]
}`

// Both kinds in one batch, as a receiver every rule broadcasts to receives them.
const mixedPayload = `{
  "receiver": "hanzo-critical",
  "status": "firing",
  "alerts": [
    {"status":"firing",
     "labels":{"alertname":"HanzoMonitoringWatchdog","severity":"none","watchdog":"true"},
     "annotations":{"summary":"Hanzo alerting pipeline is alive"}},
    {"status":"firing",
     "labels":{"alertname":"HanzoServiceDown","severity":"critical","service":"help"},
     "annotations":{"summary":"help has failed its health probe for 5m"}}
  ]
}`

// sent counts what reached a human, and keeps the text.
func sent(t *testing.T) *[]string {
	t.Helper()
	texts := &[]string{}
	swapEgress(t, egress{name: "test", send: func(_ context.Context, text string) error {
		*texts = append(*texts, text)
		return nil
	}})
	return texts
}

// beatAt sets when the last heartbeat landed, restored after the test.
func beatAt(t *testing.T, at time.Time) {
	t.Helper()
	lastBeat.Lock()
	prev := lastBeat.at
	lastBeat.at = at
	lastBeat.Unlock()
	t.Cleanup(func() {
		lastBeat.Lock()
		lastBeat.at = prev
		lastBeat.Unlock()
	})
}

// The watchdog fires forever by design. Mailed to a person every five minutes it
// is noise that teaches them to ignore the pager; it is a heartbeat, and goes to
// nobody.
func TestTheWatchdogIsAHeartbeatAndPagesNobody(t *testing.T) {
	a := alertsApp(t)
	texts := sent(t)
	beatAt(t, time.Now().Add(-time.Hour))

	code, body := post(t, a, "/v1/o11y/alerts/hanzo-slack", watchdogPayload)
	if code != http.StatusOK || body != "ok" {
		t.Fatalf("watchdog: got %d %q, want 200 ok", code, body)
	}
	if len(*texts) != 0 {
		t.Fatalf("the watchdog reached a human: %q", *texts)
	}
	_, ring := get(t, a, "/v1/o11y/alerts/last")
	if !strings.HasPrefix(ring, "ALERT-HEARTBEAT path=/v1/o11y/alerts/hanzo-slack receiver=watchdog-sink status=firing alert=HanzoMonitoringWatchdog at=") {
		t.Fatalf("the ring holds no heartbeat:\n%s", ring)
	}
	if strings.Contains(ring, "ALERT-RECEIVED") || strings.Contains(ring, "ALERT-DELIVERED") {
		t.Fatalf("a heartbeat was recorded as a page:\n%s", ring)
	}
	if code, body := get(t, a, "/v1/o11y/alerts/heartbeat"); code != http.StatusOK {
		t.Fatalf("heartbeat after a beat: %d %q, want 200", code, body)
	}
}

// One batch can carry the watchdog beside a real page. The page goes out; the
// watchdog does not ride along with it.
func TestAMixedBatchPagesOnlyTheRealAlerts(t *testing.T) {
	a := alertsApp(t)
	texts := sent(t)

	code, body := post(t, a, "/v1/o11y/alerts/hanzo-slack", mixedPayload)
	if code != http.StatusOK {
		t.Fatalf("mixed: got %d %q", code, body)
	}
	if len(*texts) != 1 {
		t.Fatalf("pages sent = %d, want 1", len(*texts))
	}
	page := (*texts)[0]
	if !strings.Contains(page, "HanzoServiceDown") || strings.Contains(page, "HanzoMonitoringWatchdog") {
		t.Fatalf("the page must carry the real alert and not the watchdog:\n%s", page)
	}
	if !strings.Contains(page, "1 alert(s)") {
		t.Fatalf("the page counts the watchdog:\n%s", page)
	}
}

// The switch: silence past the window is a 503 something outside can page on,
// and a resolved watchdog — the heartbeat stopping — does not reset the clock.
func TestTheHeartbeatGoesStaleWithoutBeats(t *testing.T) {
	a := alertsApp(t)
	sent(t)
	beatAt(t, time.Now().Add(-heartbeatWindow-time.Minute))

	code, body := get(t, a, "/v1/o11y/alerts/heartbeat")
	if code != http.StatusServiceUnavailable || !strings.Contains(body, "no heartbeat") {
		t.Fatalf("stale: got %d %q, want 503 naming the silence", code, body)
	}

	resolved := strings.ReplaceAll(watchdogPayload, `"firing"`, `"resolved"`)
	if code, _ := post(t, a, "/v1/o11y/alerts/hanzo-slack", resolved); code != http.StatusOK {
		t.Fatalf("resolved watchdog answered %d", code)
	}
	if code, _ := get(t, a, "/v1/o11y/alerts/heartbeat"); code != http.StatusServiceUnavailable {
		t.Fatalf("a resolved watchdog refreshed the heartbeat (got %d)", code)
	}

	post(t, a, "/v1/o11y/alerts/hanzo-slack", watchdogPayload)
	code, body = get(t, a, "/v1/o11y/alerts/heartbeat")
	var pulse Heartbeat
	if code != http.StatusOK || json.Unmarshal([]byte(body), &pulse) != nil || pulse.AgeSeconds > 5 || pulse.LimitSeconds != int64(heartbeatWindow/time.Second) {
		t.Fatalf("after a beat: got %d %q, want 200 with a fresh age and the 900s limit", code, body)
	}
}
