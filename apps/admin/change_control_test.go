package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/admin/commerce"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/digitalocean"
	"github.com/hanzoai/cloud/apps/admin/health"
	"github.com/hanzoai/cloud/apps/admin/iam"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// changesSomething is the CLOSED list of this board's operations that CHANGE something —
// money is granted, a ceiling moves, a switch flips, standing is given or taken, a
// machine or a volume goes away. Every one of them asks the estate's anti-forgery
// control, through core.Change / core.ChangeScoped.
//
// THIS BOARD IS DRIVEN FROM A BROWSER, and a browser authenticates it from an httpOnly
// session cookie, which is ambient: any origin's request to us carries it. Admission
// answers WHO is calling and cannot answer whether the CALL was meant, so without the
// control a page an operator merely visits reaches every line below as them — and the
// org-scoped rows are worse, because the caller they admit is an ordinary customer's
// own org admin on the open web.
//
// A CHANGE is not readable off the HTTP method: over MCP and the call plane every
// operation arrives as one POST, so the method describes the transport. It is a property
// of the operation, which is why it is written down here.
var changesSomething = map[string]string{
	"POST /v1/admin/caps":                                       "sets a spend ceiling on any org",
	"PATCH /v1/admin/caps/{id}":                                 "retunes a spend ceiling",
	"DELETE /v1/admin/caps/{id}":                                "removes a spend ceiling, which raises what an org may spend",
	"PUT /v1/admin/promos":                                      "sets the platform-wide plan discount",
	"POST /v1/admin/customers/{org}/credit":                     "grants an org credit it can spend",
	"POST /v1/admin/grants":                                     "issues a credit grant",
	"POST /v1/admin/customers/{org}/suspend":                    "cuts login and token issuance for every member of an org",
	"POST /v1/admin/customers/{org}/reactivate":                 "restores them",
	"PUT /v1/admin/flags/{key}":                                 "flips a platform switch, the paywall kill switch among them",
	"POST /v1/admin/services":                                   "declares a service and its launch mode",
	"POST /v1/admin/services/{service}/mode":                    "opens or closes a service to everyone not yet approved",
	"POST /v1/admin/waitlist/boost":                             "approves users off the waitlist",
	"POST /v1/admin/sync":                                       "starts a platform sync",
	"POST /v1/admin/finance/backfill":                           "rewrites the finance ledger for a period",
	"POST /v1/admin/infra/volumes/{id}/snapshot":                "spends storage taking a snapshot",
	"POST /v1/admin/infra/volumes/{id}/resize":                  "grows a volume, and the bill with it",
	"DELETE /v1/admin/infra/volumes/{id}":                       "destroys a volume",
	"POST /v1/admin/infra/nodes/{id}/cordon":                    "takes a node out of scheduling",
	"DELETE /v1/admin/infra/droplets/{id}":                      "destroys a machine",
	"POST /v1/admin/infra/droplets/{id}/resize":                 "resizes a machine, and the bill with it",
	"DELETE /v1/admin/infra/loadbalancers/{id}":                 "destroys a load balancer",
	"POST /v1/admin/infra/clusters/{id}/nodepools/{pool}/scale": "scales a node pool, and the bill with it",
}

// TestEveryChangeIsClassified makes forgetting structurally impossible: the ledger and
// what the board registers must SUM, so an operation added without deciding whether it
// changes anything goes red here rather than shipping uncontrolled.
func TestEveryChangeIsClassified(t *testing.T) {
	all := registeredHere(t)
	for _, addr := range all {
		method := addr[:strings.IndexByte(addr, ' ')]
		_, named := changesSomething[addr]
		if consumes(method) && !named {
			t.Errorf("%s is registered and %s changes state — say what it does in changesSomething "+
				"and give it core.Change, or state why it is a read", addr, method)
		}
		if named && !consumes(method) {
			t.Errorf("%s is listed as a change but %s cannot be one", addr, method)
		}
	}
	for addr := range changesSomething {
		if !contains(all, addr) {
			t.Errorf("changesSomething names %s, which this board does not register", addr)
		}
	}
}

// TestEveryChangeAsksTheControl drives each change TWICE against the same app: once as a
// browser carrying only an ambient session cookie and no echoed token, which must be
// refused BY THE CONTROL; and once as a caller who PRESENTED a credential, which must
// not be — a Bearer caller cannot be forged into and must pay nothing for this.
//
// The pair is what makes each row mean something. A refusal on its own would pass
// against an operation that refuses everything, including one that does not exist: every
// other gate here answers 403 too, and "admin required" is what an unattested caller
// gets whether or not the control ever ran.
func TestEveryChangeAsksTheControl(t *testing.T) {
	app := seamApp(t)
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

// TestTheControlCoversEverySeam drives one cap change by NAME, at a path that is not its
// own, which is how MCP and the call plane address it — no route middleware is reached
// there, so only a control inside the operation answers.
//
// WITH NO BODY AT ALL, which both seams take as happily as a full one: an empty body
// decodes to the zero input rather than failing. That is the shape a cross-origin page
// can send under a CORS-simple content type with no preflight to consult, so it is the
// shape worth measuring — the control has to answer before the input matters, and it
// does, because it reads the request's credentials rather than the operation's In.
func TestTheControlCoversEverySeam(t *testing.T) {
	app := seamApp(t)
	ambient := map[string]string{"Cookie": "session=v"}

	for _, id := range []string{"adminCreateCap", "adminUpdateCap", "adminDeleteCap", "adminSetPromo"} {
		if got := post(t, app, zip.CallPath+id, "", ambient); !strings.Contains(got, "CSRF") {
			t.Errorf("call plane %s with an ambient cookie answered %q — the control did not run", id, clip(got))
		}
	}

	for _, name := range []string{"adminCreateCap", "adminDeleteCap"} {
		frame := `{"kind":"request","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
		if got := post(t, app, "/mcp", frame, ambient); !strings.Contains(got, "CSRF") {
			t.Errorf("MCP tools/call %s with an ambient cookie answered %q — the control did not run", name, clip(got))
		}
	}
}

// TestAValidTokenReachesPastTheControl is the positive control, and without it the two
// tests above would pass against a board that refused everything.
//
// The token is minted by the REAL issuer — apps/account's GET /v1/account/csrf, mounted
// here beside the board — so this also measures the property the boot verdicts exist to
// guarantee: the process that mints and the process that verifies hold ONE key, and a
// token minted at one address verifies at the other. The caller then gets past the
// control and meets the NEXT gate, which is the board's own admission.
func TestAValidTokenReachesPastTheControl(t *testing.T) {
	app := seamApp(t)
	tok := mintToken(t, app)
	got := drive(t, app, http.MethodPost, "/v1/admin/caps", map[string]string{
		"Cookie": "session=v", "X-CSRF-Token": tok,
	})
	if strings.Contains(got, "CSRF") {
		t.Fatalf("a token minted at /v1/account/csrf was refused at an admin change: %q", clip(got))
	}
	if !strings.Contains(got, "admin required") {
		t.Fatalf("past the control the caller should meet the board's own admission, got %q", clip(got))
	}
}

// ── the seam app ────────────────────────────────────────────────────────────────

// seamApp mounts the board beside the account surface that mints the token, which is
// what a browser talks to: one origin, two capabilities.
func seamApp(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv(account.KeyEnv, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	routes(app, &cloud.Service[core.State]{State: core.State{
		IAM:       iam.New(""),
		Commerce:  commerce.New("", "test-token"),
		Health:    health.New(""),
		DO:        digitalocean.New(""),
		WLTenants: map[string]bool{"maxpower": true},
	}})
	if err := account.Use(app, cloud.Deps{Brand: "hanzo"}); err != nil {
		t.Fatalf("account: %v", err)
	}
	return app
}

func mintToken(t *testing.T, app *zip.App) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/account/csrf", nil)
	req.Header.Set("X-User-Id", "u-1")
	req.Header.Set("X-Org-Id", "acme")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET /v1/account/csrf: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	var r struct {
		Token string `json:"csrfToken"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Token == "" {
		t.Fatalf("no token minted: %s", b)
	}
	return r.Token
}

// ── helpers ─────────────────────────────────────────────────────────────────────

// consumes says whether a method changes anything. It is the transport's own rule and is
// used ONLY to cross-check the ledger against what is registered — never to decide an
// operation's act, which the ledger states.
func consumes(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func registeredHere(t *testing.T) []string {
	t.Helper()
	doc, err := openapi.Spec(seamApp(t), openapi.Info{Title: "admin", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	var out []string
	for path, byMethod := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/admin") {
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

// split turns "DELETE /v1/admin/caps/{id}" into a method and a concrete path,
// substituting a value for every declared parameter so the router matches the route
// rather than answering 404.
func split(addr string) (method, path string) {
	i := strings.IndexByte(addr, ' ')
	method, path = addr[:i], addr[i+1:]
	for {
		open := strings.IndexByte(path, '{')
		if open < 0 {
			return method, path
		}
		shut := strings.IndexByte(path[open:], '}')
		path = path[:open] + "x" + path[open+shut+1:]
	}
}

func drive(t *testing.T, app *zip.App, method, path string, headers map[string]string) string {
	t.Helper()
	return post(t, app, path, "", headers, method)
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
