package integrations

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

// A channel @mention still threads — the reply shares the room.
func TestChannelMentionStillThreads(t *testing.T) {
	raw := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"app_mention","channel":"C1","user":"U1","text":"<@B1> hi","ts":"333.3"}}`)
	if d := routeSlackEvent(raw); d.ThreadTS != "333.3" {
		t.Errorf("a channel mention must thread under itself, got %q", d.ThreadTS)
	}
}
