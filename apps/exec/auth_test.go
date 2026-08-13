package exec

// auth_test.go — the two ways in that a red team walked through, each pinned by the
// request that walked through it.
//
// Both were the same defect wearing two faces: authorization inferred from the
// SPELLING of a request rather than being a property of the request. One read the
// tenant off a header a client controls; the other decided whether to check a
// credential by matching a lowercase prefix list against a path fiber had already
// matched case-insensitively. Neither is a rule anyone forgot to apply — both are
// rules applied to the wrong thing.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// raw fires a request with full control of headers — no key, a wrong key, a forged
// tenant. Every exploit below is one of those three.
func raw(t *testing.T, app *zip.App, method, path string, body string, hdr map[string]string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	rq := httptest.NewRequest(method, "http://api.hanzo.ai"+path, r)
	for k, v := range hdr {
		rq.Header.Set(k, v)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestForgedOrgHeaderCannotChooseTheTenant is the cross-tenant break.
//
// callCtx preferred cloud.Who(ctx).Org, which is zip.CallerOf, which reads the
// X-Org-Id REQUEST HEADER — and SanitizeIdentity deliberately RESTORES that header
// for a request with no validated bearer (middleware_identity.go:455). So
// `X-Org-Id: victim-corp` on a service-key request made storeFor open victim-corp's
// sandbox store: the run executed there, and /v1/files and /v1/download read its
// artifacts back out.
//
// A header names nothing now. The tenant is principal.OrgFrom — the org a VALIDATED
// principal resolved to — or, for the untenanted service key, this deployment's own
// brand org. There is no third source.
func TestForgedOrgHeaderCannotChooseTheTenant(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "pwned", "", 0, nil }
	app := mount(t)

	var billed []string
	p.OnLease = func(org string) { billed = append(billed, org) }

	body, _ := json.Marshal(CodeRun{Lang: "py", Code: "print(1)"})
	resp := raw(t, app, http.MethodPost, "/v1/exec", string(body), map[string]string{
		"X-API-Key":    "k",
		"Content-Type": "application/json",
		"X-Org-Id":     "victim-corp",
		// No X-User-Id: there is no validated principal, which is exactly the
		// request shape SanitizeIdentity restores the client's own org onto.
	})
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 200 (the service key is valid)", resp.StatusCode, b)
	}
	for _, org := range billed {
		if org == "victim-corp" {
			t.Fatalf("the sandbox was leased for %q — a header chose the tenant, so the run "+
				"executed in another org's store and its artifacts are readable from it", org)
		}
	}
	if len(billed) == 0 || billed[0] != "hanzo" {
		t.Fatalf("leased for %v, want the deployment's own brand org — an untenanted service "+
			"key acts for the deployment and for nobody else", billed)
	}
}

// TestPathCaseCannotSkipTheCredential is the unauthenticated-execution break.
//
// fiber routes case-insensitively (cloud.RoutePath exists in this repo for exactly
// that), so /V1/EXEC matched the route and then missed a lowercase prefix list. The
// consequences were not subtle: no key at all ran code, a wrong key read another
// session's bytes, and with CODE_EXEC_API_KEY UNSET — the documented fail-closed
// 503 — it still ran code.
func TestPathCaseCannotSkipTheCredential(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "pwned", "", 0, nil }
	app := mount(t)

	// Every spelling the router accepts, and every credential state that must be
	// refused on it.
	for _, path := range []string{"/v1/exec", "/V1/EXEC", "/V1/Exec", "/v1/EXEC/"} {
		for _, tc := range []struct {
			why  string
			hdr  map[string]string
			want int
		}{
			{"no key at all", map[string]string{"Content-Type": "application/json"}, http.StatusUnauthorized},
			{"a wrong key", map[string]string{"X-API-Key": "nope", "Content-Type": "application/json"}, http.StatusUnauthorized},
		} {
			resp := raw(t, app, http.MethodPost, path, `{"lang":"py","code":"print(1)"}`, tc.hdr)
			if resp.StatusCode != tc.want {
				b, _ := io.ReadAll(resp.Body)
				t.Errorf("POST %s with %s = %d, want %d (body %s)", path, tc.why, resp.StatusCode, tc.want, b)
			}
		}
	}
	if n := p.Ran(); n != 0 {
		t.Fatalf("%d program(s) executed for requests that presented no valid credential", n)
	}

	// The file surface, case-flipped: this is how the artifacts came out.
	for _, path := range []string{"/V1/FILES/m_0000a", "/V1/DOWNLOAD/m_0000a/secret.csv"} {
		resp := raw(t, app, http.MethodGet, path, "", map[string]string{"X-API-Key": "wrong"})
		if resp.StatusCode != http.StatusUnauthorized {
			b, _ := io.ReadAll(resp.Body)
			t.Errorf("GET %s with a wrong key = %d, want 401 (body %s)", path, resp.StatusCode, b)
		}
	}
}

// TestUnsetKeyFailsClosedOnEverySpelling. An unconfigured deployment answers 503 on
// /v1/exec and answered 200 on /V1/EXEC — the documented fail-closed behaviour was
// true of one spelling of the same route.
func TestUnsetKeyFailsClosedOnEverySpelling(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "pwned", "", 0, nil }
	app := mount(t)
	t.Setenv("CODE_EXEC_API_KEY", "")

	for _, path := range []string{"/v1/exec", "/V1/EXEC"} {
		resp := raw(t, app, http.MethodPost, path, `{"lang":"py","code":"print(1)"}`,
			map[string]string{"Content-Type": "application/json", "X-API-Key": "anything"})
		if resp.StatusCode != http.StatusServiceUnavailable {
			b, _ := io.ReadAll(resp.Body)
			t.Errorf("POST %s with no key configured = %d, want 503 (body %s)", path, resp.StatusCode, b)
		}
	}
	if n := p.Ran(); n != 0 {
		t.Fatalf("%d program(s) executed on a deployment with no credential configured", n)
	}
}

// TestTheOtherDoorsFailClosed is the finding no path list could ever have covered.
//
// A typed op is ALSO an MCP tool and an op-plane op. MCP's tools/call invokes the
// op directly (zip typed.go:474, registeredOp.direct) — no route, so no route
// middleware, so no credential check ran and `tools/call name=post_exec` with no
// key executed code.
//
// This calls the handler the way those doors do: straight, with a bare context. It
// carries no validated principal and no admission marker, so it is refused — which
// is the property, rather than a third gate that has to be kept in step with the
// other two.
func TestTheOtherDoorsFailClosed(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "pwned", "", 0, nil }
	mount(t)

	if _, err := run(t.Context(), &CodeRun{Lang: "py", Code: "print(1)"}); err == nil {
		t.Fatal("the typed op ran from a context that passed no credential check — this is " +
			"the MCP tools/call and op-plane door, and it invokes the handler directly")
	}
	if n := p.Ran(); n != 0 {
		t.Fatalf("%d program(s) executed through a door with no credential check", n)
	}

	// And it is not refusing everything: a context the middleware admitted works.
	if _, err := run(admit(t.Context()), &CodeRun{Lang: "py", Code: "print(1)"}); err != nil {
		t.Fatalf("an admitted context was refused: %v — the gate is now a wall around nothing", err)
	}
}

// TestAValidatedPrincipalOwnsItsOwnSandboxes: when the chat DOES forward the end
// user's IAM bearer, the session is that org's and not the deployment's. This is the
// half the header read was pretending to implement.
func TestAValidatedPrincipalOwnsItsOwnSandboxes(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "ok", "", 0, nil }
	app := mount(t)

	var leased []string
	p.OnLease = func(org string) { leased = append(leased, org) }

	body, _ := json.Marshal(CodeRun{Lang: "py", Code: "print(1)"})
	resp := raw(t, app, http.MethodPost, "/v1/exec", string(body), map[string]string{
		"X-API-Key":    "k",
		"Content-Type": "application/json",
		// A VALIDATED principal: the user claim is what makes the org trusted
		// (principal.OrgOf — an empty user means the org that rode along is not).
		"X-User-Id": "u_alice",
		"X-Org-Id":  "acme",
	})
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 200", resp.StatusCode, b)
	}
	if len(leased) == 0 || leased[0] != "acme" {
		t.Fatalf("leased for %v, want acme — a validated principal's session is its own org's", leased)
	}
}
