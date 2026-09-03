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

	"github.com/hanzoai/cloud/apps/principal"
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
// sandbox store: the run executed there, and /v1/exec/files and /v1/exec/download read its
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
		"Content-Type": "application/json",
		"X-Org-Id":     "victim-corp",
		// No X-User-Id: there is no validated principal, which is exactly the
		// request shape SanitizeIdentity restores the client's own org onto.
	})
	if resp.StatusCode == http.StatusOK {
		t.Fatal("a bare org header ran code — a header chose the tenant")
	}
	if len(billed) != 0 {
		t.Fatalf("leased for %v — nothing should be leased for a caller with no principal", billed)
	}
}

// TestPathCaseCannotSkipTheCredential is the unauthenticated-execution break.
//
// fiber routes case-insensitively (cloud.RoutePath exists in this repo for exactly
// that), so /V1/EXEC matched the route and then missed a lowercase prefix list of
// paths the credential middleware covered. No key at all ran code.
//
// The list is gone along with the key it guarded. Admission is now a property of
// the request — a validated principal or nothing — so there is no spelling of the
// path that reaches a different answer, and no list left to fall out of step.
func TestPathCaseCannotSkipTheCredential(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "pwned", "", 0, nil }
	app := mount(t)

	for _, path := range []string{"/v1/exec", "/V1/EXEC"} {
		resp := raw(t, app, http.MethodPost, path, `{"lang":"py","code":"print(1)"}`,
			map[string]string{"Content-Type": "application/json"})
		if resp.StatusCode == http.StatusOK {
			t.Errorf("POST %s with no principal = 200", path)
		}
	}
	if n := p.Ran(); n != 0 {
		t.Fatalf("%d program(s) executed for a caller with no principal", n)
	}
}

// TestTheOtherEndpointsFailClosed is the finding no path list could ever have covered.
//
// A typed op is ALSO an MCP tool and an op-plane op. MCP's tools/call invokes the
// op directly (zip typed.go:474, registeredOp.direct) — no route, so no route
// middleware, so no credential check ran and `tools/call name=post_exec` with no
// key executed code.
//
// This calls the handler the way those entry points do: straight, with a bare
// context. It carries no validated principal, so it is refused — which is the
// property, rather than a second gate that has to be kept in step.
func TestTheOtherEndpointsFailClosed(t *testing.T) {
	p := servePeer(t)
	p.Run = func(string, []string) (string, string, int, map[string][]byte) { return "pwned", "", 0, nil }
	mount(t)

	if _, err := run(t.Context(), &CodeRun{Lang: "py", Code: "print(1)"}); err == nil {
		t.Fatal("the typed op ran from a context that passed no credential check — this is " +
			"the MCP tools/call and op-plane entry point, and it invokes the handler directly")
	}
	if n := p.Ran(); n != 0 {
		t.Fatalf("%d program(s) executed through an entry point with no credential check", n)
	}

	// And it is not refusing everything: a context naming the acting org works.
	// That is the ONLY way in now — an entry point states the caller, or there is
	// no caller.
	if _, err := run(principal.WithActing(t.Context(), "hanzo"), &CodeRun{Lang: "py", Code: "print(1)"}); err != nil {
		t.Fatalf("a context with an acting org was refused: %v — the gate is now a wall around nothing", err)
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
