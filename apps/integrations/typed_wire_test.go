package integrations

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// untypedByDesign is the CLOSED list of connector operations that are NOT typed
// ops, each with the wire fact that keeps it raw.
//
// These reasons were WRITTEN AT THE REGISTRATIONS and are now checkable, which is
// the only change this file makes. A reason in a comment cannot notice that it
// stopped being true, and this package has been "complete at 19 refused" in prose
// while serving 21 — the two that drifted in were classified by nobody, because
// prose does not count.
//
// THREE FAMILIES, and the split is on what the wire IS rather than on the vendor:
//
//   - A BROWSER LEG (12). Each answers a 302 to a sign-in, or a short HTML
//     confirmation that sets a __Host- cookie. A typed op always marshals JSON
//     and cannot set the status a redirect needs.
//   - AN INBOUND WEBHOOK (9). Each speaks its platform's own protocol over the
//     RAW request. Five are signed over the exact received bytes (Slack and
//     GitHub HMAC, Discord Ed25519), which a re-encoded In is not. Two are
//     header-authed and answer an EMPTY 200 to a body they cannot parse — zip
//     unmarshals before the handler, so typing them would turn that 200 into a
//     400 and retry-storm the platform. The remaining two answer their platform's
//     own envelope.
//   - THE GENERIC OAUTH CALLBACK (1). One route serving every provider's
//     callback, state-authed and answering 302.
var untypedByDesign = map[string]string{
	// ── browser legs: 302 or an HTML page with a __Host- cookie ──
	"GET /v1/integrations/slack/install":          "the Add-to-Slack entry point; answers a 302 into Slack's own consent flow.",
	"GET /v1/integrations/slack/link":             "a browser leg: 302 to sign-in, never JSON.",
	"GET /v1/integrations/slack/link/slack":       "a browser leg: 302 into Slack's OAuth, never JSON.",
	"GET /v1/integrations/slack/link/callback":    "a browser leg: an HTML confirmation that sets a __Host- cookie.",
	"GET /v1/integrations/discord/link":           "a browser leg: 302 to sign-in, never JSON.",
	"GET /v1/integrations/discord/link/discord":   "a browser leg: 302 into Discord's OAuth, never JSON.",
	"GET /v1/integrations/discord/link/callback":  "a browser leg: an HTML confirmation that sets a __Host- cookie.",
	"GET /v1/integrations/teams/link":             "a browser leg: 302 to sign-in, never JSON.",
	"GET /v1/integrations/teams/link/aad":         "a browser leg: 302 into Entra ID, never JSON.",
	"GET /v1/integrations/teams/link/callback":    "a browser leg: an HTML confirmation that sets a __Host- cookie.",
	"GET /v1/integrations/telegram/link":          "a browser leg: 302 to sign-in, never JSON.",
	"GET /v1/integrations/telegram/link/auth":     "a browser leg: 302 into Telegram's login widget, never JSON.",
	"GET /v1/integrations/telegram/link/callback": "a browser leg: an HTML confirmation that sets a __Host- cookie.",

	// ── inbound webhooks: the platform's protocol over the RAW request ──
	"POST /v1/integrations/slack/events":         "Slack's HMAC covers the RAW received bytes, which a re-encoded In is not.",
	"POST /v1/integrations/slack/commands":       "Slack's slash-command wire: form-encoded, HMAC over the raw bytes.",
	"POST /v1/integrations/github/webhook":       "GitHub's HMAC covers the RAW received bytes.",
	"POST /v1/integrations/linear/webhook":       "Linear's HMAC covers the RAW received bytes.",
	"POST /v1/integrations/discord/interactions": "Discord's Ed25519 signature covers the RAW received bytes.",
	"POST /v1/integrations/teams/events": "header-authed, and answers an EMPTY 200 to a body it cannot parse; zip " +
		"unmarshals before the handler, so a typed In turns that 200 into a 400 and retry-storms the platform.",
	"POST /v1/integrations/telegram/webhook": "header-authed, same empty-200-on-unparseable contract as Teams.",
	"GET /v1/integrations/whatsapp/webhook": "Meta's subscription challenge: the token is compared, then its " +
		"`hub.challenge` is echoed as bare text — a typed Out would wrap it in JSON and the subscription would never verify.",
	"POST /v1/integrations/whatsapp/webhook": "Meta's HMAC covers the RAW received bytes, and it answers 200 to " +
		"anything it cannot act on — a status callback carries no message, and a non-2xx is retried with backoff " +
		"until the subscription is disabled.",
	"POST /v1/integrations/openrouter/webhook": "the credential is read from the destination's own Headers map BEFORE the " +
		"body is touched, so a caller with no key never buys a decode — an order zip's pre-handler unmarshal inverts.",

	// ── the generic OAuth callback ──
	"GET /v1/integrations/{provider}/callback": "ONE route serving every provider's OAuth callback (RedirectPath is asserted " +
		"equal for all of them in Mount), state-authed and answering 302.",
}

// TestEveryRouteIsTypedOrNamed requires the two ledgers to SUM to the served
// surface, so a route added raw goes red without anyone remembering this file —
// which is exactly what did not happen while the reasons lived only in comments.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("CLOUD_DOMAIN", "api.hanzo.ai")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })

	doc, err := openapi.Spec(app, openapi.Info{Title: "integrations", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/integrations") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/integrations") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if typed[key] {
			continue
		}
		if _, named := untypedByDesign[key]; !named {
			untyped = append(untyped, key)
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it, or name it in untypedByDesign with the WIRE FACT that keeps "+
			"it raw — re-read against the pinned zip, never inherited from an older pass.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this surface no longer serves", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("the two ledgers must sum to the served surface: typed %d + named %d = %d, served %d",
			len(typed), len(untypedByDesign), got, want)
	}
}
