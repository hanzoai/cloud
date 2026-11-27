package provider

// The Marketplace entry point used to 302 to Slack carrying no state, and the
// callback's first act is verify(state). So every click from Slack's directory
// approved the scopes and then landed on an invalid-state failure — a dead end
// reached AFTER the person had already said yes.
//
// This pins where it goes instead, and that it never sends anyone to Slack
// without the state the callback requires.

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fiber "github.com/zap-proto/fiber/v3"
)

func TestInstallSendsPeopleToTheConsoleNotToSlack(t *testing.T) {
	loc := installLocation(t)

	if strings.Contains(loc, "slack.com") {
		t.Fatalf("install redirected to Slack with no state — the callback will "+
			"reject it as invalid state after the person has already approved: %s", loc)
	}
	// The console's connectors page, opened on the Slack card: /connectors/:provider
	// is the deep link the console serves, and it scrolls to the card the person
	// came for. /integrations is no console page at all — every click from Slack's
	// directory landed on not-found.
	if !strings.HasSuffix(loc, "/connectors/slack") {
		t.Fatalf("install should open the console on the Slack card, got %s", loc)
	}
}

// TestTheCallbackReturnsToTheConnectorsPage pins where a finished or failed
// install lands: the console page that reads connected=/error= and says what
// happened. The console serves it at /connectors; a return anywhere else is a
// not-found page after the person already approved.
func TestTheCallbackReturnsToTheConnectorsPage(t *testing.T) {
	for _, loc := range []string{
		returnURL("https://console.hanzo.ai", "", "connected", "slack", "account", "Acme"),
		returnURL("https://console.hanzo.ai", "", "error", "github", "reason", "x"),
	} {
		if !strings.HasPrefix(loc, "https://console.hanzo.ai/connectors?") {
			t.Errorf("callback return = %s, want the console's /connectors page", loc)
		}
	}
}

// installLocation drives the handler and returns its Location header.
func installLocation(t *testing.T) string {
	t.Helper()
	app := newApp(t, newKMS(t))
	req := httptest.NewRequest("GET", "/v1/provider/slack/install", nil)
	res, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if res.StatusCode < 300 || res.StatusCode > 399 {
		t.Fatalf("install status = %d, want a redirect", res.StatusCode)
	}
	return res.Header.Get("Location")
}
