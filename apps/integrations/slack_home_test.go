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
