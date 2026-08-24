package wallet

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
// something — an account or a wallet is minted, key material is rotated, and a
// caller-supplied digest is SIGNED with the org's custody key and the org is DEBITED
// for the round. Every one of them asks the estate's anti-forgery control
// (apps/account) in its own preamble.
//
// A browser authenticates this surface from an httpOnly session cookie, which is
// ambient: any origin's request to us carries it. Signing is the sharp one — a page an
// operator merely visits could name any 32 bytes and have the org's key sign them,
// paying for the threshold round out of the org's balance — and a rotation is
// permanent: the old address stops being the wallet.
//
// A CHANGE is not readable off the HTTP method: over MCP and the call plane every
// operation arrives as one POST, so the method describes the transport. It is a
// property of the operation, which is why it is written down here.
var changesSomething = map[string]string{
	"POST /v1/wallet/accounts":         "opens a custody account for the caller's org",
	"POST /v1/wallet":                  "mints a wallet and its key material",
	"POST /v1/wallet/:id/keys":         "rotates the wallet's key, and its address with it",
	"POST /v1/wallet/:id/sign":         "signs a caller-supplied digest with the org's key and debits the org",
	"POST /v1/wallet/:id/transactions": "signs a Safe transaction hash on the ring and debits the org",
}

// TestEveryChangeIsClassified makes forgetting structurally impossible: the ledger and
// what this package registers on the EDGE must sum, so an operation added without
// deciding whether it changes anything goes red here rather than shipping
// uncontrolled. The plane surface is not counted: it listens on the app's canonical
// socket, carries no browser cookie, and is not a seam a page can reach.
func TestEveryChangeIsClassified(t *testing.T) {
	_, app := newService(t, map[Kind]Custody{}, KindKMS)
	var all []string
	for _, r := range app.Routes() {
		if !strings.HasPrefix(r.Pattern, "/v1/wallet") {
			continue
		}
		addr := strings.ToUpper(r.Method) + " " + r.Pattern
		all = append(all, addr)
		if _, named := changesSomething[addr]; consumes(r.Method) && !named {
			t.Errorf("%s is registered and %s changes state — say what it does in changesSomething "+
				"and give it the control, or state why it is a read", addr, r.Method)
		}
	}
	for addr := range changesSomething {
		found := false
		for _, s := range all {
			found = found || s == addr
		}
		if !found {
			t.Errorf("changesSomething names %s, which this package does not register", addr)
		}
	}
}

// TestEveryChangeAsksTheControl drives each change TWICE against the same app: once as
// a browser carrying only an ambient session cookie and no echoed token, which must be
// refused BY THE CONTROL; and once as a caller who PRESENTED a credential, which must
// not be — a Bearer caller cannot be forged into and must pay nothing for this.
//
// The pair is what makes each row mean something. A refusal on its own would pass
// against an operation that refuses everything: "wallet not found" is what an
// unattested caller gets whether or not the control ever ran.
func TestEveryChangeAsksTheControl(t *testing.T) {
	_, app := newService(t, map[Kind]Custody{}, KindKMS)
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

// TestTheControlCoversEverySeam drives the signature by NAME, at a path that is not its
// own, which is how MCP and the call plane address it — no route middleware is reached
// there, so only a control inside the operation answers.
//
// WITH NO BODY AT ALL, which both seams take as happily as a full one: an empty body
// decodes to the zero input rather than failing. That is the shape a cross-origin page
// can send under a CORS-simple content type with no preflight to consult, so it is the
// shape worth measuring — the control has to answer before the input matters, and it
// does, because it reads the request's credentials rather than the operation's In.
func TestTheControlCoversEverySeam(t *testing.T) {
	_, app := newService(t, map[Kind]Custody{}, KindKMS)
	ambient := map[string]string{"Cookie": "session=v"}

	for _, id := range []string{"post_wallet_by_id_sign", "post_wallet_by_id_keys", "post_wallet"} {
		if got := post(t, app, zip.CallPath+id, "", ambient); !strings.Contains(got, "CSRF") {
			t.Errorf("call plane %s with an ambient cookie answered %q — the control did not run", id, clip(got))
		}
	}
	for _, name := range []string{"post_wallet_by_id_sign", "post_wallet_by_id_keys"} {
		frame := `{"kind":"request","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{}}}`
		if got := post(t, app, "/mcp", frame, ambient); !strings.Contains(got, "CSRF") {
			t.Errorf("MCP tools/call %s with an ambient cookie answered %q — the control did not run", name, clip(got))
		}
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────────

// consumes says whether a method changes anything. It is the transport's own rule and
// is used ONLY to cross-check the ledger against what is registered — never to decide
// an operation's act, which the ledger states.
func consumes(m string) bool {
	switch strings.ToUpper(m) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func split(addr string) (method, path string) {
	i := strings.IndexByte(addr, ' ')
	return addr[:i], strings.ReplaceAll(addr[i+1:], ":id", "wal_x")
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
