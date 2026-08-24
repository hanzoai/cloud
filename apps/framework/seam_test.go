package framework

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// byName drives an operation the way MCP and the call plane address it — by its
// own id, at a path under /mcp that is not /v1/framework/*. Nothing installed on
// this subsystem's group runs there, so only what the operation reads for itself
// answers.
func byName(t *testing.T, app *zip.App, id, args, org, user string, admin bool) string {
	t.Helper()
	frame := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + id +
		`","arguments":` + args + `}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(frame)))
	req.Header.Set("Content-Type", "application/json")
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if admin {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("tools/call %s: %v", id, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestTheCallerIsWholeOnEveryWayIn.
//
// An operation here is reached by six seams and only one of them is a route, so
// the caller it acts as has to be read from the request rather than from anything
// a route put in place. All three facts or none: the org alone is not a person,
// and the engine keys role grants by USER — an operation running with an org and
// no user would claim the org's one-shot first-manager row for the empty string,
// which nobody holds and nobody can revoke.
//
// PAIRED, always. The refusal row is driven with an org and NO user; the row
// beside it is the same call with a whole principal and must get through to the
// engine, or the refusal would pass just as well against an operation that
// refuses everything.
func TestTheCallerIsWholeOnEveryWayIn(t *testing.T) {
	app := mountApp(t)

	// listDocTypes reads; assignRole is the write that seeds an owner. Both take
	// the same caller and both are reachable by name.
	halfIdentity := byName(t, app, "get_framework_doctypes", `{}`, "acme", "", false)
	if !strings.Contains(halfIdentity, "valid principal") && !strings.Contains(halfIdentity, "orbidden") {
		t.Errorf("an org with no user reached the engine over MCP: %s", halfIdentity)
	}
	whole := byName(t, app, "get_framework_doctypes", `{}`, "acme", "u_acme", false)
	if strings.Contains(whole, "valid principal") {
		t.Errorf("a whole principal was refused over MCP (%s) — the row above measured nothing", whole)
	}

	// And the SuperAdmin bit survives the trip: over MCP it is a header like the
	// rest, so an operation that reads the request reads it too.
	if !strings.Contains(byName(t, app, "get_framework_doctypes", `{}`, "acme", "u_acme", true), "result") {
		t.Errorf("a SuperAdmin was refused over MCP")
	}
}

// TestTheOrgSeedGoesToAPerson. The engine claims an unowned org for its first
// caller. Over MCP that caller must be a whole principal, so the claim lands on
// somebody who can later grant and revoke — and an org reached with no user must
// still be claimable afterwards.
func TestTheOrgSeedGoesToAPerson(t *testing.T) {
	app := mountApp(t)

	// A write, by name, with no user id. It must not claim the org.
	out := byName(t, app, "post_framework_doctypes", `{"name":"Task","module":"Projects",`+
		`"fields":[{"fieldname":"subject","fieldtype":"Data","reqd":true}]}`, "acme", "", false)
	if !strings.Contains(out, "valid principal") && !strings.Contains(out, "orbidden") {
		t.Fatalf("a nameless caller reached the engine's write path over MCP: %s", out)
	}

	// The org is still unclaimed: a real person creating a DocType is seeded
	// System Manager and succeeds. Over the ROUTE, which is the ordinary path.
	code, body := call(t, app, http.MethodPost, "/v1/framework/doctypes", "acme", "u_real", false, taskDocType())
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("the org must still be claimable by a person: %d %s", code, body)
	}

	// And that person really holds the role — read it back rather than inferring
	// it from the status.
	code, body = call(t, app, http.MethodGet, "/v1/framework/roles", "acme", "u_real", false, nil)
	if code != http.StatusOK {
		t.Fatalf("roles: %d %s", code, body)
	}
	var got struct {
		Roles []struct{ User, Role string } `json:"data"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("roles body %s: %v", body, err)
	}
	var holders []string
	for _, r := range got.Roles {
		holders = append(holders, r.User+"="+r.Role)
		if strings.TrimSpace(r.User) == "" {
			t.Errorf("a role is held by nobody: %+v", got.Roles)
		}
	}
	if len(holders) == 0 {
		t.Fatalf("the first caller was not seeded, so nothing was measured: %s", body)
	}
}

// TestAnOrgArrivesWithAPerson pins what caller() leans on. It reads the user off
// principal.Org's verdict instead of re-deciding it, so this states the verdict:
// an org claim with no validated user is no org at all. If that ever stopped
// holding, a caller here would carry an org and no name and the engine would seed
// this org's one permanent claim against nobody.
func TestAnOrgArrivesWithAPerson(t *testing.T) {
	app := mountApp(t)
	// Same request twice, differing only in whether a user was validated.
	code, body := call(t, app, http.MethodGet, "/v1/framework/doctypes", "acme", "", false, nil)
	if code == http.StatusOK {
		t.Errorf("an org claim with no validated user was accepted: %d %s", code, body)
	}
	if code, body = call(t, app, http.MethodGet, "/v1/framework/doctypes", "acme", "u_acme", false, nil); code != http.StatusOK {
		t.Errorf("a validated principal was refused: %d %s — the row above measured nothing", code, body)
	}
}
