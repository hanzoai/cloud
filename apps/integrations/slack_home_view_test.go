package integrations

import (
	"encoding/json"
	"strings"
	"testing"
)

func viewJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("view must marshal: %v", err)
	}
	return string(b)
}

// A linked user gets the controls, with THEIR OWN choice pre-selected. Without
// initial_option Slack renders the menu blank and a saved choice looks lost.
func TestHomeShowsSelectedModel(t *testing.T) {
	v := homeView(nil, "acme", "U1", userLink{Subject: "acme/z", Model: "enso-ultra"}, true)
	js := viewJSON(t, v)
	if !strings.Contains(js, `"initial_option"`) {
		t.Error("the saved choice must be pre-selected, or it looks lost")
	}
	if !strings.Contains(js, `"enso-ultra"`) {
		t.Error("the selected model must appear in the view")
	}
	for _, want := range []string{homeActionModel, homeActionRouting} {
		if !strings.Contains(js, want) {
			t.Errorf("the view must carry the %q control", want)
		}
	}
}

// No stored choice must still render a usable menu, defaulted to the auto SKU.
func TestHomeDefaultsToEnsoAuto(t *testing.T) {
	js := viewJSON(t, homeView(nil, "acme", "U1", userLink{Subject: "acme/z"}, true))
	if !strings.Contains(js, `"enso"`) {
		t.Error("the default menu must offer enso auto")
	}
}

// An UNLINKED user is shown how to connect and NOT given settings — a model
// chosen by an identity we cannot resolve is a control that does nothing.
func TestHomeUnlinkedShowsConnectNotSettings(t *testing.T) {
	js := viewJSON(t, homeView(nil, "acme", "U1", userLink{}, false))
	if strings.Contains(js, homeActionModel) {
		t.Error("an unlinked user must not be offered a model control")
	}
	if !strings.Contains(strings.ToLower(js), "connect") {
		t.Error("an unlinked user must be told how to connect")
	}
}

// Interactivity and events share one signed URL and differ only by encoding.
func TestInteractionBodyIsDetected(t *testing.T) {
	if !slackInteractionBody([]byte(`payload=%7B%22type%22%3A%22block_actions%22%7D`)) {
		t.Error("a form-encoded payload= body is an interaction")
	}
	if slackInteractionBody([]byte(`{"type":"event_callback"}`)) {
		t.Error("a JSON events envelope is NOT an interaction")
	}
}

// The payload names the setting and the user; it must never name the tenant.
func TestInteractionParsesUserAndAction(t *testing.T) {
	raw := []byte(`payload=%7B%22type%22%3A%22block_actions%22%2C%22team%22%3A%7B%22id%22%3A%22T1%22%7D%2C%22user%22%3A%7B%22id%22%3A%22U1%22%7D%2C%22actions%22%3A%5B%7B%22action_id%22%3A%22hanzo_model%22%2C%22selected_option%22%3A%7B%22value%22%3A%22enso-flash%22%7D%7D%5D%7D`)
	in, ok := parseSlackInteraction(raw)
	if !ok {
		t.Fatal("a block_actions payload must parse")
	}
	if in.User.ID != "U1" || in.Team.ID != "T1" {
		t.Errorf("user/team must come from the signed payload, got %q/%q", in.User.ID, in.Team.ID)
	}
	if len(in.Actions) != 1 || in.Actions[0].SelectedOption.Value != "enso-flash" {
		t.Errorf("the chosen value must survive parsing, got %+v", in.Actions)
	}
}

// Only a value WE offered may be stored: the menu came from us, so anything
// else is a stale client or a forged payload choosing what this org pays for.
func TestOnlyOfferedModelsAreAccepted(t *testing.T) {
	for _, ok := range []string{"enso", "enso-flash", "enso-ultra"} {
		if !validHomeModel(ok) {
			t.Errorf("%q is on the menu and must be accepted", ok)
		}
	}
	for _, bad := range []string{"gpt-4", "", "best", "enso; drop", "ENSO"} {
		if validHomeModel(bad) {
			t.Errorf("%q was never offered and must be refused", bad)
		}
	}
}
