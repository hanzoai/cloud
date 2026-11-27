package host

// THE PIN FOR THE DOOR PROBLEM.
//
// A typed operation is reachable four ways and the doors do not look alike to
// middleware: over REST the request path IS the operation, over MCP it is the
// agent address, over the call plane it is /.well-known/zip/op/<name>, over the
// graph it is /v1/graphql, and an in-process invoke has no request at all. So a
// control installed with app.Use and written against the request path judges the
// DOOR on three of those and never runs on the fourth.
//
// Three separate controls were written that way and each had to be found by
// hand: the money gate (toll.go), the tamper-evident trail (note.go), the rate
// ceiling and the abuse sensor's spread signal. The fourth will be written the
// same way unless something asks the question at the moment it is added — which
// is what this does. It does not judge a middleware; it requires that Somebody
// answered, once, in writing, whether the thing it reads is a property of the
// REQUEST or of the OPERATION.
//
// Adding a middleware to the composition root fails this test until its name
// appears below with that answer. That is the whole mechanism: the test is a
// place to have the thought, not a checker that can have it for you.

import (
	"os"
	"strings"
	"testing"
)

// reviewed is every middleware and rule the composition root installs, with the
// reason it is safe over a door. Three shapes of answer are legitimate:
//
//	request  — what it reads is a property of the request, not the operation.
//	deferred — it reads the operation, at the op-invoke seam or by deferring to it.
//	inert    — it decides nothing from the address.
var reviewed = map[string]string{
	// ── request ──────────────────────────────────────────────────────────────
	"middleware.Recover":           "request: catches a panic, reads nothing.",
	"middleware.RequestID":         "request: mints an id.",
	"middleware.ProductionHeaders": "request: sets response headers.",
	"TracingMiddleware":            "request: one span per request, whatever it reached.",
	"EdgeRateLimit":                "request: per-address, pre-auth. Reads no path.",
	"EdgeCORS":                     "request: origin and method; the path only to find the IAM sign-in routes, which are raw handlers with no second door.",
	"EdgeOrigin":                   "request: strips an admitted Origin before a non-IAM child reads it; IAM's own routes keep it and the edge's CORS answer is restored on the way out.",
	"EdgeCache":                    "request: the Cache-Control and ETag of an HTTP GET or HEAD; a door is a POST and passes untouched.",
	"SanitizeIdentity":             "request: validates the credential the request carried. Its grant is asked of the address; on a door it stands down and Permit decides on the operation (intent.go).",
	"Bridge":                       "request: puts the request on the context.",
	"AccountFromPrincipal":         "request: resolves the payer from the validated principal.",
	"DenyEnvelope":                 "inert: renders a refusal another layer already made.",
	"inflight.Count":               "inert: counts the handlers running so the drain waits for them; reads neither the request nor the operation.",

	// ── address, and that is what they are for ───────────────────────────────
	"markdown.Negotiation":               "request: content negotiation by prefix. A door serves no markdown.",
	"object.Middleware":                  "request: answers the signed links of the object store on local disk under /v1/object/, authorized by their own signature; no other path is touched, and no door reaches it.",
	"sites.New":                          "request: routes HOSTS, not operations — it owns the root path space.",
	"preview.Middleware":                 "request: routes a preview HOST onto the sandbox app; no other host is touched.",
	"manifest.ProviderWebhookMiddleware": "request: rewrites PayPal's registered POST /v1/webhook/paypal onto commerce's sessionless /v1/commerce/webhooks/paypal; MCP, call plane and graph doors are untouched.",
	"shardRtr.Middleware":                "request: picks a shard from the tenant, not the address.",
	"Stage":                              "request: names which plugin's chain is running.",

	// ── the three that had to be fixed ───────────────────────────────────────
	"AuditTrail":     "deferred: names the operation Rule noted when the matched route is not its own (note.go).",
	"ScopeRateLimit": "deferred: stands down on a door and Ceiling decides on the operation (middleware_ratelimit.go).",
	"AbuseGate":      "deferred: the door adds no spread; Reach adds the operation at the seam (note.go).",

	// ── already asked about the operation ────────────────────────────────────
	"BillingGate": "deferred: says it took the request, and Toll stands down for one it did (toll.go).",
	"Contain":     "deferred: refuses a support session by address; on a door Confine, in the rule, asks the operation (impersonation.go).",
	"SpendGate":   "deferred: Toll re-asks standing() about op.Method and op.Path.",
	"rule":        "deferred: IS the op-invoke seam — Note, Reach, Permit, Confine, Intent, Ceiling, Toll.",
}

// installed reads the composition root and returns what it mounts, in order:
// App, Listen, and Witness, which Listen and the o11y main both call.
func installed(t *testing.T) []string {
	t.Helper()
	var names []string
	for _, file := range []string{"internal/host/app.go", "internal/host/serve.go", "sdk/identify.go", "sdk/witness.go"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for line := range strings.SplitSeq(string(b), "\n") {
			line = strings.TrimSpace(line)
			for _, call := range []string{"app.Use(", "app.Authorize(", "app.Peer().Authorize("} {
				i := strings.Index(line, call)
				if i < 0 || strings.HasPrefix(line, "//") {
					continue
				}
				rest := line[i+len(call):]
				// zip.H only adapts a plain handler func to a component, so the
				// mount is the func it wraps: zip.H(inflight.Count) reads as
				// inflight.Count, and every wrapped func needs its own answer.
				rest = strings.TrimPrefix(rest, "zip.H(")
				// The constructor's name is what precedes its own arguments. A
				// chained mount (sites.New(...).Middleware()) reads as its head,
				// which is stable and is what a reader would call it.
				if j := strings.IndexAny(rest, "("); j >= 0 {
					rest = rest[:j]
				}
				rest = strings.TrimPrefix(strings.TrimRight(strings.TrimSpace(rest), ")"), "cloud.")
				if rest != "" {
					names = append(names, rest)
				}
			}
		}
	}
	return names
}

// EVERY MOUNT HAS AN ANSWER. A middleware added to the composition root fails
// here until Somebody says which of the two things it reads.
func TestPin_EveryMountAnsweredTheDoorQuestion(t *testing.T) {
	names := installed(t)
	if len(names) < 15 {
		t.Fatalf("read only %d mounts from the composition root (%v) — the extractor "+
			"stopped matching and this pin is no longer reading anything", len(names), names)
	}
	for _, n := range names {
		if _, ok := reviewed[n]; !ok {
			t.Errorf("%s is installed at the composition root and has no answer in reviewed.\n"+
				"    Over MCP, the call plane and the graph, c.Path() names the DOOR and not the\n"+
				"    operation, and an in-process invoke has no request at all. Say which it reads:\n"+
				"      request  — a property of the request; safe as written.\n"+
				"      deferred — a property of the operation; decide at the rule (intent.go), not here.", n)
		}
	}
}

// AND NO ANSWER OUTLIVES ITS MOUNT. An entry for something no longer installed
// is a reader's false comfort: it reads as a control that was considered and is
// still there.
func TestPin_NoAnswerOutlivesItsMount(t *testing.T) {
	live := map[string]bool{}
	for _, n := range installed(t) {
		live[n] = true
	}
	for n := range reviewed {
		if !live[n] {
			t.Errorf("reviewed names %s, which the composition root no longer installs", n)
		}
	}
}

// AND THE ONE ORDER THAT IS LOAD-BEARING. Bridge copies the request's org and
// its validated flag onto the context as it passes, and every typed reader
// downstream answers from that context. Installed AHEAD of the identity
// boundary it copies what the CLIENT sent — so an operation resolves the tenant
// a caller named for itself, with no credential, and nothing anywhere errors.
//
// It is the quietest failure in the file: the boundary still runs, still strips,
// still mints an empty attestation, and every one of its own tests still passes,
// because they read what the boundary wrote rather than what an operation
// resolves. identity_doors_test.go reads what the operation resolves, and it was
// this order that made it report a forged tenant on all three doors.
func TestPin_TheBoundaryPrecedesTheBridge(t *testing.T) {
	names := installed(t)
	at := func(want string) int {
		for i, n := range names {
			if n == want {
				return i
			}
		}
		return -1
	}
	boundary, bridge := at("SanitizeIdentity"), at("Bridge")
	if boundary < 0 || bridge < 0 {
		t.Fatalf("SanitizeIdentity at %d and Bridge at %d — one of them is no longer "+
			"mounted under that name, and this pin is reading nothing", boundary, bridge)
	}
	if boundary > bridge {
		t.Fatalf("Bridge is installed at %d, ahead of the identity boundary at %d. It "+
			"copies org and validated onto the context as it passes, so every typed "+
			"reader downstream would answer from what the CLIENT sent.", bridge, boundary)
	}
}
