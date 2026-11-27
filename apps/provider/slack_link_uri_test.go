package provider

import (
	"net/url"
	"testing"

	"github.com/hanzoai/cloud/manifest"
)

// The hanzo.id leg of the Slack link redirects to the address the hanzo-slack
// client registers, and the edge routes that address onto the callback this
// package serves. IAM refuses any other spelling with "invalid redirect_uri",
// which strands the person after the Slack sign-in with no link made.
func TestLinkCallbackIsTheRegisteredAddress(t *testing.T) {
	s := linkService(&emptyKMS{})
	const registered = "https://api.hanzo.ai/v1/integrations/slack/link/callback"
	if got := slackLinkCallbackURI(s); got != registered {
		t.Fatalf("leg 3 redirect = %q, want the registered %q", got, registered)
	}
	auth, err := url.Parse(slackOIDCAuthorizeURL(s, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if got := auth.Query().Get("redirect_uri"); got != registered {
		t.Fatalf("the authorize request carries redirect_uri %q, want %q", got, registered)
	}
	if got := manifest.Normalize("/v1/integrations/slack/link/callback"); got != "/v1/provider/slack/link/callback" {
		t.Fatalf("the registered address routes to %q, not the provider callback", got)
	}
}
