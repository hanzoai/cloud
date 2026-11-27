package provider

// slack_quiet_test.go — what the Slack adapter itself says about a message: only
// that it could not be passed on.
//
// The adapter used to set Slack's "is thinking…" status on the ask's own thread
// while channels answered wherever the turn decided — in the room, for a short
// question asked in the room — so one question showed as a status in a thread
// AND a message in the room, and the status was never cleared because nothing
// ever posted where it hung. The turn's one message is the whole of what the
// room sees now.
//
// No transport app runs in this package's tests, which is the one case where the
// adapter DOES speak: a message nothing took is answered with that fact, in the
// room it was asked in, rather than with silence.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestASlackMessageIsNeverAnsweredWithAStatus(t *testing.T) {
	slackConfiguredEnv(t)
	type call struct{ method, channel, text string }
	var mu sync.Mutex
	var called []call
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth.v2.access", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "access_token": "xoxb-acme", "scope": "chat:write", "bot_user_id": "BACME",
			"team": map[string]string{"id": "TACME", "name": "TACME"},
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Channel string `json:"channel"`
			Text    string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		called = append(called, call{strings.TrimPrefix(r.URL.Path, "/"), body.Channel, body.Text})
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1800000000.000001"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	orig := slackWebAPIBase
	slackWebAPIBase = srv.URL
	t.Cleanup(func() { slackWebAPIBase = orig })

	app := newApp(t, newSigningKMS(t, "quiet-secret"))
	if cb := connectSlack(t, app, "acme", "acmecode"); cb.Code != http.StatusFound {
		t.Fatalf("connect: %d (%s)", cb.Code, cb.Body)
	}
	body := `{"type":"event_callback","team_id":"TACME","event_id":"EvQuiet1","event":{"type":"message","channel_type":"channel","user":"Uacme","text":"<@BACME> can you update 'Ship It' on hanzo.ai to 'Get Started' in hero?","channel":"C1","ts":"1700000001.000100"}}`
	if res := slackPost(t, app, "/v1/provider/slack/events", "quiet-secret", "application/json", body); res.Code != http.StatusOK {
		t.Fatalf("events ack want 200, got %d (%s)", res.Code, res.Body)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		seen := append([]call(nil), called...)
		mu.Unlock()
		for _, c := range seen {
			if c.method == "assistant.threads.setStatus" {
				t.Fatalf("the adapter set a status of its own; the turn's one message is all the room may see (%+v)", seen)
			}
		}
		if len(seen) == 1 && seen[0].method == "chat.postMessage" && seen[0].channel == "C1" && seen[0].text == mrkdwn(ingestLost) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a message nothing took was not answered with that fact, once, in its room: %+v", seen)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
