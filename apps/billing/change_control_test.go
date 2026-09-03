package billing

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// changesSomething is the CLOSED list of this package's operations that CHANGE
// something — money moves, a ceiling is raised, an instrument is vaulted or
// dropped, a plan starts or ends. Every one of them asks the estate's anti-CSRF
// control (apps/account) in its own preamble.
//
// IN THE OPERATION, because a route is one of the seams that reach it. zip records
// the route's handler and the op as two fields of one entry and wraps only the
// handler, while MCP, the call plane, the graph and the CLI call the op directly,
// with the depth-0 identity middleware having already authenticated whoever is
// calling. A browser authenticates this surface from an httpOnly session COOKIE,
// which any origin's request to us carries too — so without the control in the op,
// a page the CORS policy admits reaches every line below as the person reading it.
//
// A CHANGE is not readable off the HTTP method: over MCP and the call plane every
// operation arrives as one POST, so the method describes the transport. It is a
// property of the operation, which is why it is written down here.
var changesSomething = map[string]string{
	"POST /v1/billing/topup/token":                   "charges a card token and credits the caller's wallet",
	"POST /v1/billing/topup":                         "charges a card on file and credits the caller's wallet",
	"DELETE /v1/billing/alerts/{id}":                 "removes a spend ceiling, which raises what the org may spend",
	"POST /v1/billing/alerts":                        "sets a spend ceiling",
	"PATCH /v1/billing/alerts/{id}":                  "retunes a spend ceiling",
	"POST /v1/billing/recharge/run-all":              "sweep-charges every tenant's saved card",
	"DELETE /v1/billing/methods/{id}":                "drops a saved instrument at the processor",
	"DELETE /v1/billing/portal/methods/{id}":         "the same act at the hosted-checkout address",
	"POST /v1/billing/mode":                          "switches between sandbox and real money",
	"POST /v1/billing/crypto/deposit":                "mints and records a deposit intent and its address",
	"POST /v1/billing/subscriptions/{id}/cancel":     "ends a plan",
	"POST /v1/billing/subscriptions/{id}/reactivate": "restores a plan, so billing resumes",
	"POST /v1/billing/invoices":                      "raises an invoice",
	"POST /v1/billing/invoices/{id}/issue":           "issues an invoice and mints its number",
	"POST /v1/billing/invoices/{id}/void":            "cancels an invoice",
	"POST /v1/billing/invoices/{id}/collect":         "moves money: credits, then balance, then the card",
	"POST /v1/billing/methods":                       "vaults an instrument at the processor and records it",
	"POST /v1/billing/portal/methods":                "the same act at the hosted-checkout address",
	"POST /v1/billing/subscribe/card":                "vaults a card, charges the first period and opens a subscription",
	"PUT /v1/billing/recharge":                       "arms the sweep that charges a saved card off-session, and sets what it charges",
}

// TestEveryChangeIsClassified makes forgetting structurally impossible: the two
// ledgers must SUM to what this package registers, so an operation added without
// deciding whether it changes anything goes red here rather than shipping
// uncontrolled. It is the same shape as TestEveryRouteIsTypedOrNamed and for the
// same reason — a control every author has to remember is one an author will
// eventually be written without.
func TestEveryChangeIsClassified(t *testing.T) {
	for _, addr := range registeredHere(t) {
		method := addr[:strings.IndexByte(addr, ' ')]
		_, named := changesSomething[addr]
		if consumes(method) && !named {
			t.Errorf("%s is registered and %s changes state — say what it does in changesSomething "+
				"and give it the control, or state why it is a read", addr, method)
		}
		if named && !consumes(method) {
			t.Errorf("%s is listed as a change but %s cannot be one", addr, method)
		}
	}
	for addr := range changesSomething {
		if !contains(registeredHere(t), addr) {
			t.Errorf("changesSomething names %s, which this package does not register", addr)
		}
	}
}

// TestEveryChangeAsksTheControl drives each change TWICE against the same app:
// once as a browser carrying only an ambient session cookie and no echoed token,
// which must be refused; and once as a caller who PRESENTED a credential, which
// must not be — a Bearer caller cannot be CSRF'd and must pay nothing for this.
//
// The pair is what makes each row mean something. A refusal on its own would pass
// against an operation that refuses everything, including one that does not exist.
func TestEveryChangeAsksTheControl(t *testing.T) {
	app := mountApp(t, "")
	for addr, what := range changesSomething {
		t.Run(addr, func(t *testing.T) {
			method, path := split(addr)
			forged := drive(t, app, method, path, map[string]string{
				"Cookie": "session=v", // ambient: any origin's request to us carries it
			})
			if !strings.Contains(forged, "CSRF") {
				t.Errorf("an ambient cross-origin request answered %q — this operation %s and "+
					"must ask the control", clip(forged), what)
			}
			presented := drive(t, app, method, path, map[string]string{
				"Cookie":        "session=v",
				"Authorization": "Bearer sk-test",
			})
			if strings.Contains(presented, "CSRF") {
				t.Errorf("a caller who presented a credential was refused by the anti-CSRF control "+
					"(%q) — they cannot be CSRF'd, and the row above measured nothing", clip(presented))
			}
		})
	}
}

// TestTheControlCoversTheCallPlane drives one money operation by NAME, at a path
// that is not its own, which is how MCP and the call plane address it. No route
// middleware is reached there, so only a control inside the operation answers.
//
// WITH NO BODY AT ALL, which the call plane takes as happily as a full one — an
// empty body decodes to the zero input rather than failing. That is the shape a
// cross-origin page can send under a CORS-simple content type with no preflight
// to consult, so it is the shape worth measuring: the control has to answer
// before the input matters, and it does, because it reads the request's
// credentials rather than the operation's In.
func TestTheControlCoversTheCallPlane(t *testing.T) {
	app := mountApp(t, "")
	for _, id := range []string{"post_billing_topup", "post_billing_topup_token"} {
		body := ""
		forged := drivePlane(t, app, zip.CallPath+id, body, map[string]string{"Cookie": "session=v"})
		if !strings.Contains(forged, "CSRF") {
			t.Errorf("call plane %s with an ambient cookie answered %q — the control did not run",
				id, clip(forged))
		}
		presented := drivePlane(t, app, zip.CallPath+id, body, map[string]string{
			"Cookie": "session=v", "Authorization": "Bearer sk-test",
		})
		if strings.Contains(presented, "CSRF") {
			t.Errorf("call plane %s refused a presented credential (%q) — the row above measured nothing",
				id, clip(presented))
		}
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

// consumes says whether a method changes anything. It is the transport's own
// rule and is used ONLY to cross-check the list above against what is
// registered — never to decide an operation's act, which the list states.
func consumes(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func registeredHere(t *testing.T) []string {
	t.Helper()
	app := mountApp(t, "")
	doc, err := openapi.Spec(app, openapi.Info{Title: "billing", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	var out []string
	for path, byMethod := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/billing") {
			continue
		}
		for m := range byMethod {
			out = append(out, strings.ToUpper(m)+" "+path)
		}
	}
	return out
}

func contains(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}

// split turns "POST /v1/billing/invoices/{id}/issue" into a method and a
// concrete path, substituting a value for every declared parameter so the
// router matches the route rather than answering 404.
func split(addr string) (method, path string) {
	i := strings.IndexByte(addr, ' ')
	method, path = addr[:i], addr[i+1:]
	for {
		open := strings.IndexByte(path, '{')
		if open < 0 {
			return method, path
		}
		close := strings.IndexByte(path[open:], '}')
		path = path[:open] + "x" + path[open+close+1:]
	}
}

func drive(t *testing.T, app *zip.App, method, path string, headers map[string]string) string {
	t.Helper()
	return drivePlane(t, app, path, `{"amountCents":1}`, headers, method)
}

func drivePlane(t *testing.T, app *zip.App, path, body string, headers map[string]string, method ...string) string {
	t.Helper()
	verb := http.MethodPost
	if len(method) > 0 {
		verb = method[0]
	}
	req := httptest.NewRequest(verb, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	// A VALIDATED principal, so the ambient path means something: the identity
	// boundary sets these only from a credential it verified.
	req.Header.Set("X-User-Id", "u-1")
	req.Header.Set("X-Org-Id", "acme")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", verb, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func clip(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
