package integrations

// The Marketplace entry point used to 302 to Slack carrying no state, and the
// callback's first act is verify(state). So every click from Slack's directory
// approved the scopes and then landed on
// /integrations?error=slack&reason=invalid+state — a dead end reached AFTER the
// person had already said yes.
//
// This pins where it goes instead, and that it never sends anyone to Slack
// without the state the callback requires.

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInstallSendsPeopleToTheConsoleNotToSlack(t *testing.T) {
	loc := installLocation(t)

	if strings.Contains(loc, "slack.com") {
		t.Fatalf("install redirected to Slack with no state — the callback will "+
			"reject it as invalid state after the person has already approved: %s", loc)
	}
	if !strings.Contains(loc, "/integrations") {
		t.Fatalf("install should land on the console connect surface, got %s", loc)
	}
	// Labelled, so the console can say why someone arrived and offer Slack rather
	// than dropping them on a bare list.
	if !strings.Contains(loc, "connect=slack") {
		t.Errorf("install redirect should name the provider it came for: %s", loc)
	}
}

// installLocation drives the handler and returns its Location header.
func installLocation(t *testing.T) string {
	t.Helper()
	app := newApp(t, newKMS(t))
	req := httptest.NewRequest("GET", "/v1/integration/slack/install", nil)
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.StatusCode < 300 || res.StatusCode > 399 {
		t.Fatalf("install status = %d, want a redirect", res.StatusCode)
	}
	return res.Header.Get("Location")
}
