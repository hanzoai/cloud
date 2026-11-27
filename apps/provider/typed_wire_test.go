package provider

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
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
// FOUR FAMILIES, and the split is on what the wire IS rather than on the vendor:
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
//   - A FILE BODY (1). The response IS a file's content, served under the type
//     read from the bytes themselves. A typed Out always marshals JSON, so typing
//     it would base64 a document into an envelope and make every reader decode one
//     to look at it.
var untypedByDesign = map[string]string{
	// ── browser legs: 302 or an HTML page with a __Host- cookie ──
	"GET /v1/provider/slack/install":          "the Add-to-Slack entry point; answers a 302 into Slack's own consent flow.",
	"GET /v1/provider/slack/link":             "a browser leg: 302 to sign-in, never JSON.",
	"GET /v1/provider/slack/link/slack":       "a browser leg: 302 into Slack's OAuth, never JSON.",
	"GET /v1/provider/slack/link/callback":    "a browser leg: an HTML confirmation that sets a __Host- cookie.",
	"GET /v1/provider/discord/link":           "a browser leg: 302 to sign-in, never JSON.",
	"GET /v1/provider/discord/link/discord":   "a browser leg: 302 into Discord's OAuth, never JSON.",
	"GET /v1/provider/discord/link/callback":  "a browser leg: an HTML confirmation that sets a __Host- cookie.",
	"GET /v1/provider/teams/link":             "a browser leg: 302 to sign-in, never JSON.",
	"GET /v1/provider/teams/link/aad":         "a browser leg: 302 into Entra ID, never JSON.",
	"GET /v1/provider/teams/link/callback":    "a browser leg: an HTML confirmation that sets a __Host- cookie.",
	"GET /v1/provider/telegram/link":          "a browser leg: 302 to sign-in, never JSON.",
	"GET /v1/provider/telegram/link/auth":     "a browser leg: 302 into Telegram's login widget, never JSON.",
	"GET /v1/provider/telegram/link/callback": "a browser leg: an HTML confirmation that sets a __Host- cookie.",

	// ── inbound webhooks: the platform's protocol over the RAW request ──
	"POST /v1/provider/slack/events":         "Slack's HMAC covers the RAW received bytes, which a re-encoded In is not.",
	"POST /v1/provider/slack/commands":       "Slack's slash-command wire: form-encoded, HMAC over the raw bytes.",
	"POST /v1/provider/github/webhook":       "GitHub's HMAC covers the RAW received bytes.",
	"POST /v1/provider/forge/webhook":        "the forge's HMAC covers the RAW received bytes.",
	"POST /v1/provider/linear/webhook":       "Linear's HMAC covers the RAW received bytes.",
	"POST /v1/provider/discord/interactions": "Discord's Ed25519 signature covers the RAW received bytes.",
	"POST /v1/provider/teams/events": "header-authed, and answers an EMPTY 200 to a body it cannot parse; zip " +
		"unmarshals before the handler, so a typed In turns that 200 into a 400 and retry-storms the platform.",
	"POST /v1/provider/telegram/webhook": "header-authed, same empty-200-on-unparseable contract as Teams.",
	"GET /v1/provider/whatsapp/webhook": "Meta's subscription challenge: the token is compared, then its " +
		"`hub.challenge` is echoed as bare text — a typed Out would wrap it in JSON and the subscription would never verify.",
	"POST /v1/provider/whatsapp/webhook": "Meta's HMAC covers the RAW received bytes, and it answers 200 to " +
		"anything it cannot act on — a status callback carries no message, and a non-2xx is retried with backoff " +
		"until the subscription is disabled.",
	"POST /v1/provider/openrouter/webhook": "the credential is read from the destination's own Headers map BEFORE the " +
		"body is touched, so a caller with no key never buys a decode — an order zip's pre-handler unmarshal inverts.",

	// ── a person's GitHub return ──
	"GET /v1/provider/github/user/callback": "a browser leg: GitHub returns the person here, and it answers a 302 to " +
		"the console carrying the parked grant (or the labeled failure). zip.WithStatus takes 2xx only, and the " +
		"answer is a Location, never a JSON body.",
	// ── a person's Google return ──
	"GET /v1/provider/google/user/callback": "a browser leg: Google returns the person here, and it answers a 302 to " +
		"the page carrying the parked grant (or the labeled failure). zip.WithStatus takes 2xx only, and the " +
		"answer is a Location, never a JSON body.",
	// ── the generic OAuth callback ──
	"GET /v1/provider/{provider}/callback": "ONE route serving every provider's OAuth callback (RedirectPath is asserted " +
		"equal for all of them in Mount), state-authed and answering 302, or the chooser's HTML page.",
	"POST /v1/provider/{provider}/choose": "a browser leg: the chooser page's form-encoded post, authed by the signed " +
		"token the page carries rather than a session, answering 302 to the install page or to the page that asked.",
	"GET /v1/provider/twitter/callback": "the same X callback under the other redirect the X app has registered. " +
		"State is signed for provider x. It answers a 302, the same browser leg as the generic callback.",

	// ── the file body: the response IS the bytes ──
	"GET /v1/provider/slack/file": "the response IS the file's bytes, served under the type magic reads out of them " +
		"(image or PDF inline, everything else inert as octet-stream); a typed Out would base64 a document into JSON " +
		"and make every reader decode one to look at it.",
	"GET /v1/provider/{provider}/logo": "the response IS an image's bytes, fetched by an <img> that carries no bearer; " +
		"a typed Out would base64 a picture into JSON, and a principal gate would refuse every page that shows one.",
}

// TestEveryRouteIsTypedOrNamed requires the two ledgers to SUM to the served
// surface, so a route added raw goes red without anyone remembering this file —
// which is exactly what did not happen while the reasons lived only in comments.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("CLOUD_DOMAIN", "api.hanzo.ai")
	if err := Use(app, cloud.Deps{MasterKey: testMaster}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })

	doc, err := spec.Of(app, spec.Info{Title: "integrations", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := spec.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/provider") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/provider") {
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
