package tracker

// actor_test.go pins which identity the tracker sudoes as, through the real
// wire — the same way every other test in this package proves a tenancy rule.
//
// Same rule as the coding path, and the same latent failure it fixed: this forge
// names its users by their EMAIL (oauth2_client.USERNAME=email), so sudoing as
// the IAM username addresses the wrong account for anyone whose username is not
// their address's local part, and answers 403 "no forge identity" for a person
// who has one.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// asIdentity is doWireAs with the identity headers stated separately, so a test
// can make the username and the email DISAGREE — which is the whole question.
func asIdentity(t *testing.T, app *zip.App, path, org, id, name, email string) (int, []byte) {
	t.Helper()
	var r io.Reader
	rq := httptest.NewRequest("GET", path, r)
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", id)
	if name != "" {
		rq.Header.Set(authz.HeaderUserName, name)
	}
	if email != "" {
		rq.Header.Set(authz.HeaderUserEmail, email)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// THE EMAIL DECIDES. The forge knows `z` and has never heard of `zoo-admin`, so
// a board that renders is the proof the right actor was sudoed.
func TestForgeActor_IsDerivedFromTheEmailNotTheUsername(t *testing.T) {
	f := newForge(t)
	f.visible["z"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(1, "real work", "open", "todo"))
	app := mountForge(t, f)

	code, raw := asIdentity(t, app, "/v1/tracker/projects", "hanzo", "u_1", "zoo-admin", "z@hanzo.ai")
	if code != 200 {
		t.Fatalf("the forge was asked as the wrong actor: %d %s", code, raw)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("not an array: %s", raw)
	}
	if len(rows) == 0 {
		t.Fatal("the board is empty; the actor the forge saw was not `z`")
	}
}

// With no address the username still works — an API-key principal carries one
// and no email, and it is a different spelling of the same person.
func TestForgeActor_FallsBackToTheUsername(t *testing.T) {
	f := newForge(t)
	f.visible["a"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(1, "real work", "open", "todo"))
	app := mountForge(t, f)

	code, raw := asIdentity(t, app, "/v1/tracker/projects", "hanzo", "u_1", "a", "")
	if code != 200 {
		t.Fatalf("the username fallback did not resolve: %d %s", code, raw)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("[]")) {
		t.Fatal("the board is empty; the fallback actor was not `a`")
	}
}
