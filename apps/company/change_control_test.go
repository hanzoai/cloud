package company

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// changesSomething is the CLOSED list of this surface's operations that CHANGE
// something — a formation is opened or advanced, founders and structure are set, KYC
// is started or decided, documents are generated and signed, equity is imported or
// issued, and the formation tariff is CHARGED to the caller's own org. Every one of
// them asks the estate's anti-forgery control (apps/account) in its own preamble.
//
// A browser authenticates this surface from an httpOnly session cookie, which is
// ambient: any origin's request to us carries it. So without the control, a page an
// operator merely visits reaches every line below as them — /payment charges the
// org the formation fee on first shot, /skip moves the machine past a stage, and
// /esign/complete records a signature that was never given.
//
// A CHANGE is not readable off the HTTP method: over MCP and the call plane every
// operation arrives as one POST, so the method describes the transport. It is a
// property of the operation, which is why it is written down here.
var changesSomething = map[string]string{
	"POST /v1/company":                  "opens a formation for the caller's org",
	"PUT /v1/company/structure":         "sets the entity structure the tariff is quoted from",
	"POST /v1/company/founders":         "sets who the founders are",
	"POST /v1/company/kyc":              "starts identity verification on the founders",
	"POST /v1/company/kyc/refresh":      "re-reads a verification verdict",
	"POST /v1/company/kyc/decision":     "records the verdict",
	"POST /v1/company/payment":          "charges the caller's org the formation tariff",
	"POST /v1/company/documents":        "renders the formation documents and files them with the state",
	"POST /v1/company/esign":            "sends the documents for founder signature",
	"POST /v1/company/esign/complete":   "records that they were signed",
	"POST /v1/company/genesis":          "anchors the company record",
	"POST /v1/company/advance":          "moves the formation to the next stage",
	"POST /v1/company/skip":             "moves it past a stage without doing that stage",
	"POST /v1/company/import/documents": "ingests documents into the org's data room",
	"POST /v1/company/import/captable":  "imports an equity table",
	"POST /v1/company/fundraise/round":  "records a priced round",
	"POST /v1/company/fundraise/safe":   "records a SAFE",
	"POST /v1/company/fundraise/deck":   "ingests deck bytes into the org's data room",
}

// quotes are the two POSTs on this surface that change NOTHING. Both are pure
// arithmetic over the body: they read no tenant, open no store and write nothing, so
// they are POSTs for their body alone and there is no act to control. They are named
// here rather than left out, because "changes nothing" is a claim about an operation
// and the ledger is where this package states such claims.
var quotes = map[string]string{
	"POST /v1/company/tariff": "quotes a price from a structure and a jurisdiction (fees.go, pure)",
	"POST /v1/company/ein":    "builds an EIN application plan from a responsible party and a NAICS code (ein.go, pure)",
}

// TestEveryChangeIsClassified makes forgetting structurally impossible: the two
// ledgers must SUM to what this package registers, so an operation added without
// deciding whether it changes anything goes red here rather than shipping
// uncontrolled.
func TestEveryChangeIsClassified(t *testing.T) {
	all := registeredHere(t)
	for _, addr := range all {
		method := addr[:strings.IndexByte(addr, ' ')]
		_, named := changesSomething[addr]
		_, quoted := quotes[addr]
		if consumes(method) && !named && !quoted {
			t.Errorf("%s is registered and %s changes state — say what it does in changesSomething "+
				"and give it the control, or say in quotes why it changes nothing", addr, method)
		}
		if named && quoted {
			t.Errorf("%s is in both ledgers; it is one or the other", addr)
		}
		if (named || quoted) && !consumes(method) {
			t.Errorf("%s is listed but %s cannot change anything", addr, method)
		}
	}
	for addr := range changesSomething {
		if !contains(all, addr) {
			t.Errorf("changesSomething names %s, which this package does not register", addr)
		}
	}
	for addr := range quotes {
		if !contains(all, addr) {
			t.Errorf("quotes names %s, which this package does not register", addr)
		}
	}
}

// TestEveryChangeAsksTheControl drives each change TWICE against the same app: once as
// a browser carrying only an ambient session cookie and no echoed token, which must be
// refused BY THE CONTROL; and once as a caller who PRESENTED a credential, which must
// not be — a Bearer caller cannot be forged into and must pay nothing for this.
//
// The pair is what makes each row mean something. A refusal on its own would pass
// against an operation that refuses everything, including one that does not exist:
// every gate on this surface answers 403 or 404, and "no formation for this org" is
// what an unattested caller gets whether or not the control ever ran.
func TestEveryChangeAsksTheControl(t *testing.T) {
	app, _, _ := mountFake(t)
	for addr, what := range changesSomething {
		t.Run(addr, func(t *testing.T) {
			method, path := split(addr)
			forged := drive(t, app, method, path, map[string]string{"Cookie": "session=v"})
			if !strings.Contains(forged, "CSRF") {
				t.Errorf("an ambient cross-origin request answered %q — this operation %s and "+
					"must ask the control", clip(forged), what)
			}
			presented := drive(t, app, method, path, map[string]string{
				"Cookie": "session=v", "Authorization": "Bearer sk-test",
			})
			if strings.Contains(presented, "CSRF") {
				t.Errorf("a caller who presented a credential was refused by the control (%q) — "+
					"they cannot be forged into, and the row above measured nothing", clip(presented))
			}
		})
	}
}

// TestTheControlCoversEverySeam drives the charge by NAME, at a path that is not its
// own, which is how MCP and the call plane address it — no route middleware is reached
// there, so only a control inside the operation answers.
//
// WITH NO BODY AT ALL, which both seams take as happily as a full one: an empty body
// decodes to the zero input rather than failing. That is the shape a cross-origin page
// can send under a CORS-simple content type with no preflight to consult, and it is
// the whole of what /v1/company/payment needs — it takes no body, so the forged
// request and the real one are the same bytes.
func TestTheControlCoversEverySeam(t *testing.T) {
	app, _, _ := mountFake(t)
	ambient := map[string]string{"Cookie": "session=v"}

	for _, id := range []string{"post_company_payment", "post_company_skip", "post_company_esign_complete"} {
		if got := post(t, app, zip.CallPath+id, "", ambient); !strings.Contains(got, "CSRF") {
			t.Errorf("call plane %s with an ambient cookie answered %q — the control did not run", id, clip(got))
		}
	}
	for _, name := range []string{"post_company_payment", "post_company_skip"} {
		frame := `{"kind":"request","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
		if got := post(t, app, "/mcp", frame, ambient); !strings.Contains(got, "CSRF") {
			t.Errorf("MCP tools/call %s with an ambient cookie answered %q — the control did not run", name, clip(got))
		}
	}
}

// TestAQuoteCostsAnAPIClientNothing is the other half of the classification: the two
// pure POSTs are reachable with only a cookie, because there is no act to control and
// refusing one would be a control that protects nothing.
func TestAQuoteCostsAnAPIClientNothing(t *testing.T) {
	app, _, _ := mountFake(t)
	for addr := range quotes {
		method, path := split(addr)
		got := drive(t, app, method, path, map[string]string{"Cookie": "session=v"})
		if strings.Contains(got, "CSRF") {
			t.Errorf("%s asks the control, but it is classified as changing nothing: %q", addr, clip(got))
		}
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────────

// consumes says whether a method changes anything. It is the transport's own rule and
// is used ONLY to cross-check the ledgers against what is registered — never to decide
// an operation's act, which the ledgers state.
func consumes(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func registeredHere(t *testing.T) []string {
	t.Helper()
	app, _, _ := mountFake(t)
	var out []string
	for _, r := range app.Routes() {
		if !strings.HasPrefix(r.Pattern, "/v1/company") {
			continue
		}
		out = append(out, strings.ToUpper(r.Method)+" "+r.Pattern)
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

func split(addr string) (method, path string) {
	i := strings.IndexByte(addr, ' ')
	return addr[:i], addr[i+1:]
}

func drive(t *testing.T, app *zip.App, method, path string, headers map[string]string) string {
	t.Helper()
	return post(t, app, path, "{}", headers, method)
}

func post(t *testing.T, app *zip.App, path, body string, headers map[string]string, method ...string) string {
	t.Helper()
	verb := http.MethodPost
	if len(method) > 0 {
		verb = method[0]
	}
	req := httptest.NewRequest(verb, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	// A VALIDATED principal, so the ambient path means something: the identity boundary
	// sets these only from a credential it verified.
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
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
