package todo

// actor_test.go pins WHO the board acts as on the forge.
//
// This surface WRITES — it opens issues and moves cards — so acting as the wrong
// person is not a wrong view, it is somebody else's words under their name.
//
// It used to sudo as the IAM USERNAME. A forge login and an IAM username are
// different namespaces: a login is the local part of a confirmed address, a
// username is separately chosen. On the shared signup org those collide BY
// CHOICE — pick the username `z` and the forge answers as whoever holds z@… —
// so the resolution is now the same proved one the coding path uses
// (forge.Client.Caller): subject → confirmed address → the login the forge
// agrees is theirs.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// asSubject issues a request with the subject and the IAM username stated
// SEPARATELY, which asUser cannot do — it derives one from the other. The T1
// attack is precisely a caller whose username is not their own login, so the two
// have to be able to disagree.
func asSubject(t *testing.T, app *zip.App, method, path, org, subject, username string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	rq.Header.Set("X-Org-Id", org)
	rq.Header.Set("X-User-Id", subject)
	rq.Header.Set(authz.HeaderUserName, username)
	resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// THE COLLISION, exactly as T1 describes it, on the write path.
//
// mallory CHOOSES the IAM username `z`, which is the forge login a colleague
// holds. Sudoing as the username handed her that colleague's identity. Her own
// confirmed address is z@elsewhere.example, and the forge says `z` is
// z@hanzo.ai — so the resolution that replaced it has nothing to act as.
func TestBoardActor_RefusesAUsernameThatIsSomebodyElsesLogin(t *testing.T) {
	f := newForge(t)
	f.visible["z"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(1, "real work", "open", "todo"))
	app := mountForge(t, f)
	identity["u_mallory"] = client.Email{Address: "z@elsewhere.example", Verified: true}

	code, raw := asSubject(t, app, http.MethodGet, "/v1/todo/projects",
		"hanzo", "u_mallory", "z", nil)
	if code != http.StatusForbidden {
		t.Fatalf("a caller whose USERNAME is somebody else's login read their board: %d %s", code, raw)
	}

	// And the WRITE endpoint refuses the same way — this is the one that puts
	// words in a colleague's mouth.
	code, raw = asSubject(t, app, http.MethodPost, "/v1/todo/projects/api/issues",
		"hanzo", "u_mallory", "z", map[string]any{"title": "filed as somebody else"})
	if code != http.StatusForbidden {
		t.Fatalf("an issue was opened as somebody else: %d %s", code, raw)
	}
	for _, w := range f.writtenBy() {
		if w == "z" {
			t.Fatal("the forge was written to as z by a caller who is not z")
		}
	}
}

// AN UNCONFIRMED ADDRESS IS REFUSED even when it would match — an address a
// person merely typed is not evidence they hold it.
func TestBoardActor_RefusesAnUnverifiedAddress(t *testing.T) {
	f := newForge(t)
	f.visible["z"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(1, "real work", "open", "todo"))
	app := mountForge(t, f)
	identity["u_imposter"] = client.Email{Address: "z@hanzo.ai", Verified: false}

	code, raw := asUser(t, app, http.MethodGet, "/v1/todo/projects", "hanzo", "imposter", nil)
	if code != http.StatusForbidden {
		t.Fatalf("an unconfirmed address was accepted as an identity: %d %s", code, raw)
	}
}

// THE OWNER STILL READS THEIR BOARD. A control that refuses everybody is an
// outage, not a control.
func TestBoardActor_TheOwnerResolvesAndReads(t *testing.T) {
	f := newForge(t)
	f.visible["z"] = []string{"hanzoai"}
	f.repo("hanzoai", "api", issue(1, "real work", "open", "todo"))
	app := mountForge(t, f)
	identity["u_z"] = client.Email{Address: "z@hanzo.ai", Verified: true}

	code, raw := asUser(t, app, http.MethodGet, "/v1/todo/projects", "hanzo", "z", nil)
	if code != http.StatusOK {
		t.Fatalf("the owner was refused their own board: %d %s", code, raw)
	}
	var boards []map[string]any
	if err := json.Unmarshal(raw, &boards); err != nil {
		t.Fatalf("not an array: %s", raw)
	}
	if len(boards) == 0 {
		t.Fatal("the board is empty; the actor the forge saw was not z")
	}
}

// A principal the identity store does not know is refused — never a client
// carrying the bare machine identity, which can see every org on the forge.
func TestBoardActor_RefusesAnUnknownPrincipal(t *testing.T) {
	f := newForge(t)
	f.visible[""] = []string{"hanzoai"} // if it sudoed as nobody, this would answer
	f.repo("hanzoai", "api", issue(1, "secret", "open"))
	app := mountForge(t, f)
	identity["u_ghost"] = client.Email{} // known route, no address

	code, raw := asUser(t, app, http.MethodGet, "/v1/todo/projects", "hanzo", "ghost", nil)
	if code != http.StatusForbidden {
		t.Fatalf("a principal with no address read a board: %d %s", code, raw)
	}
}
