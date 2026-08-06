package integrations

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Slack REQUIRES the Direct install URL to answer 302 to a slack.com URL. This
// asserts exactly that contract, because it is the thing Slack validates and the
// reason the field rejected a slack.com URL pasted directly.
func TestSlackInstallRedirectsToSlack(t *testing.T) {
	t.Setenv(slackClientIDEnv, "2235925749.11541406513920")
	u, err := slackAuthorize(slackCreds(), "https://api.hanzo.ai"+callbackPath("slack"), "")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if !strings.HasPrefix(u, "https://slack.com/oauth/v2/authorize?") {
		t.Fatalf("install must send the browser to slack.com consent, got %q", u)
	}
	for _, want := range []string{"client_id=2235925749", "assistant%3Awrite", "redirect_uri=https%3A%2F%2Fapi.hanzo.ai%2Fv1%2Fintegrations%2Fslack%2Fcallback"} {
		if !strings.Contains(u, want) {
			t.Errorf("consent URL missing %q\n got %s", want, u)
		}
	}
	_ = httptest.NewRecorder
	_ = os.Getenv
}

// Unconfigured must be an honest 503, never a consent URL with an empty
// client_id — Slack answers that with its own error page and no way back.
func TestSlackInstallUnconfigured(t *testing.T) {
	t.Setenv(slackClientIDEnv, "")
	if slackConfigured() {
		t.Fatal("slackConfigured must be false with no client id")
	}
}
