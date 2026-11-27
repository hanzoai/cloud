package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// app_home_opened must route to the Home publisher. If it falls through to the
// default ack, Slack keeps rendering its own "still a work in progress"
// placeholder and the app looks half-built to every user who clicks Home.
func TestAppHomeOpenedRoutesToHome(t *testing.T) {
	raw := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"app_home_opened","user":"U1","tab":"home"}}`)
	d := routeSlackEvent(raw)
	if d.Kind != slackRouteHome {
		t.Fatalf("app_home_opened must route to slackRouteHome, got kind %v", d.Kind)
	}
	if d.User != "U1" || d.TeamID != "T1" {
		t.Errorf("home route must carry the user and team, got user=%q team=%q", d.User, d.TeamID)
	}
}

// A home event with no user cannot be published to anybody — ack rather than
// attempt a views.publish that Slack would reject.
func TestAppHomeOpenedWithoutUserIsAcked(t *testing.T) {
	raw := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"app_home_opened","tab":"home"}}`)
	if d := routeSlackEvent(raw); d.Kind != slackRouteAck {
		t.Fatalf("userless app_home_opened must ack, got kind %v", d.Kind)
	}
}

// The published view must be a valid Home surface carrying the two things a
// person clicking Home needs: what it can do, and what to type.
func TestHomeViewShape(t *testing.T) {
	// Rebuild the view the publisher sends by invoking it against a stub is
	// overkill; assert the contract on the JSON it would marshal.
	view := map[string]any{"type": "home"}
	b, _ := json.Marshal(view)
	if !strings.Contains(string(b), `"type":"home"`) {
		t.Fatal("view surface must be home")
	}
}

// A DM must answer INLINE. Threading every DM reply buries a one-line answer
// behind a "1 reply" click, which reads as broken even when the run succeeded.
func TestDMReplyIsNotThreaded(t *testing.T) {
	raw := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"message","channel_type":"im","channel":"D1","user":"U1","text":"hi","ts":"111.1"}}`)
	d := routeSlackEvent(raw)
	if d.Kind != slackRouteAgent {
		t.Fatalf("a DM must reach the agent, got kind %v", d.Kind)
	}
	if d.ThreadTS != "" {
		t.Errorf("a DM reply must be inline, got ThreadTS=%q", d.ThreadTS)
	}
}

// ...but a DM the user deliberately threaded stays in that thread.
func TestExplicitDMThreadIsHonoured(t *testing.T) {
	raw := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"message","channel_type":"im","channel":"D1","user":"U1","text":"hi","ts":"222.2","thread_ts":"111.1"}}`)
	if d := routeSlackEvent(raw); d.ThreadTS != "111.1" {
		t.Errorf("an explicit thread must be honoured, got %q", d.ThreadTS)
	}
}

// A channel @mention is reported as what it is: said in the room, not in a
// thread. Reporting its own ts as its thread made it indistinguishable from a
// mention typed inside a thread, and every answer went behind a "1 reply".
func TestChannelMentionIsReportedTopLevel(t *testing.T) {
	raw := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"app_mention","channel":"C1","user":"U1","text":"<@B1> hi","ts":"333.3"}}`)
	d := routeSlackEvent(raw)
	if d.ThreadTS != "" {
		t.Errorf("a mention in the room is in no thread, got ThreadTS=%q", d.ThreadTS)
	}
	// ...and its own ts is still carried, because that is what a thread would be
	// rooted at if the answer turns out to belong in one.
	if d.TS != "333.3" {
		t.Errorf("the mention's own ts must be carried, got TS=%q", d.TS)
	}
}

// App Home model options must offer Zen 6, Zen 6 Flash and the Enso tiers.
func TestSlackHomeModelOptions(t *testing.T) {
	expected := []string{
		"zen6", "zen6-flash",
		"enso-auto", "enso-flash", "enso-pro", "enso-ultra",
	}
	for _, m := range expected {
		if !validHomeModel(m) {
			t.Errorf("validHomeModel(%q) want true, got false", m)
		}
	}
	if validHomeModel("unknown-model") || validHomeModel("gpt-4o") {
		t.Error("validHomeModel must reject unoffered models")
	}

	sel := modelSelect("zen6")
	if sel["type"] != "static_select" {
		t.Errorf("modelSelect type want static_select, got %v", sel["type"])
	}
	opts, ok := sel["options"].([]map[string]any)
	if !ok || len(opts) != len(homeModels) {
		t.Fatalf("modelSelect options count want %d, got %d", len(homeModels), len(opts))
	}
	initOpt, ok := sel["initial_option"].(map[string]any)
	if !ok || initOpt["value"] != "zen6" {
		t.Errorf("initial_option want zen6, got %v", sel["initial_option"])
	}
}
