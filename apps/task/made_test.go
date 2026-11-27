package task

// made_test.go — the board of an org whose code lives in the workspace the
// platform made for it on the forge: read and written AS the person, through the
// account bound to their IAM subject, in the namespace the org's record names.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// madeBoards stands webby-ai's workspace up as webby-ai-2, with josh enrolled
// there and the board `site` holding one card.
func madeBoards(t *testing.T) (*stubForge, func(method, path string, body any) (int, []byte)) {
	t.Helper()
	f := newForge(t)
	f.bound["u_josh"] = "josh"
	f.visible["josh"] = []string{"webby-ai-2"}
	f.repo("webby-ai-2", "site", issue(1, "first card", "open", "todo"))
	f.repo("hanzoai", "estate", issue(2, "hanzo private work", "open", "todo"))
	was := boardWorkspace
	boardWorkspace = func(_ context.Context, org string) (forge.Workspace, error) {
		if org == "webby-ai" {
			return forge.Workspace{Owner: "webby-ai-2", Made: true}, nil
		}
		return was(context.Background(), org)
	}
	t.Cleanup(func() { boardWorkspace = was })
	app := mountMinted(t, f, principal.Person)
	return f, func(method, path string, body any) (int, []byte) {
		return asUser(t, app, method, path, "webby-ai", "josh", body)
	}
}

// mountMinted is mountForge behind a stand-in for the identity boundary that
// attests every request's X-User-Id as a principal of kind.
func mountMinted(t *testing.T, f *stubForge, kind principal.Kind) *zip.App {
	t.Helper()
	identity = map[string]client.Email{}
	serveIdentity(t)
	t.Setenv("CLOUD_FORGE_HOST", f.URL)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(zip.H(func(c *zip.Ctx) error {
		if u := c.Header("X-User-Id"); u != "" {
			principal.Mint(c, principal.Principal{Org: c.Header("X-Org-Id"), User: u, Subject: u, Kind: kind, Stable: true, Holder: c.Header("X-Org-Id") + "/" + u})
		}
		return c.Next()
	}))
	compose(app)
	sharedKey(t)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{KMS: kmsStub{token: f.token}}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// AN API KEY HOLDS NO SEAT: its board in a code workspace is refused, and the
// forge is not asked who it is.
func TestMadeWorkspace_AKeyHoldsNoSeat(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f := newForge(t)
		f.bound["u_josh"] = "josh"
		f.visible["josh"] = []string{"webby-ai-2"}
		was := boardWorkspace
		boardWorkspace = func(context.Context, string) (forge.Workspace, error) {
			return forge.Workspace{Owner: "webby-ai-2", Made: true}, nil
		}
		t.Cleanup(func() { boardWorkspace = was })
		app := mountMinted(t, f, principal.Key)
		code, raw := asUser(t, app, http.MethodGet, "/v1/task/projects", "webby-ai", "josh", nil)
		if code != http.StatusForbidden || !strings.Contains(string(raw), "person") {
			t.Fatalf("projects = %d %s", code, raw)
		}
	})
}

// THE BOARDS ARE THE WORKSPACE'S, read as the enrolled person: their seat is
// what the forge's ACL reads, and nothing of the estate's is reachable.
func TestMadeWorkspace_BoardsAreReadAsTheEnrolledPerson(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, as := madeBoards(t)
		code, raw := as(http.MethodGet, "/v1/task/projects", nil)
		if code != http.StatusOK {
			t.Fatalf("projects = %d %s", code, raw)
		}
		if !strings.Contains(string(raw), `"key":"site"`) || strings.Contains(string(raw), "estate") {
			t.Fatalf("projects = %s", raw)
		}
		code, raw = as(http.MethodGet, "/v1/task/board", nil)
		if code != http.StatusOK || !strings.Contains(string(raw), "first card") || strings.Contains(string(raw), "hanzo private work") {
			t.Fatalf("board = %d %s", code, raw)
		}
	})
}

// AN ISSUE IS FILED, COMMENTED ON AND CLOSED AS THE PERSON, in the workspace's
// namespace.
func TestMadeWorkspace_WritesAreThePersons(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f, as := madeBoards(t)
		if code, raw := as(http.MethodPost, "/v1/task/projects/site/issues", map[string]any{"title": "second card", "description": "why"}); code != http.StatusCreated {
			t.Fatalf("create = %d %s", code, raw)
		}
		if code, raw := as(http.MethodPatch, "/v1/task/projects/site/issues/1", map[string]any{"comment": "on it", "status": "done"}); code != http.StatusOK {
			t.Fatalf("patch = %d %s", code, raw)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.writes) == 0 {
			t.Fatal("nothing was written")
		}
		paths := []string{}
		for _, w := range f.writes {
			if w.actor != "josh" || !strings.HasPrefix(w.path, "/repos/webby-ai-2/site/issues") {
				t.Fatalf("write %s %s as %q", w.method, w.path, w.actor)
			}
			paths = append(paths, w.method+" "+w.path)
		}
		got := strings.Join(paths, ",")
		for _, want := range []string{"POST /repos/webby-ai-2/site/issues", "PATCH /repos/webby-ai-2/site/issues/1", "POST /repos/webby-ai-2/site/issues/1/comments"} {
			if !strings.Contains(got, want) {
				t.Fatalf("writes %s lack %s", got, want)
			}
		}
	})
}

// AN ISSUE'S CONVERSATION IS READ AS THE PERSON.
func TestMadeWorkspace_CommentsAreRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f, as := madeBoards(t)
		f.comments["webby-ai-2/site#1"] = []map[string]any{
			{"id": 5, "body": "looks good", "user": map[string]any{"login": "seat-ann", "full_name": "ann"}, "created_at": "2026-10-04T10:00:00Z"},
			{"id": 6, "body": "and the footer", "user": map[string]any{"login": "seat-bo"}, "created_at": "2026-10-04T11:00:00Z"},
		}
		code, raw := as(http.MethodGet, "/v1/task/projects/site/issues/1/comments", nil)
		if code != http.StatusOK {
			t.Fatalf("comments = %d %s", code, raw)
		}
		var got commentList
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Data) != 2 || got.Data[0].Body != "looks good" || got.Data[0].Author != "ann" || got.Data[1].Author != "seat-bo" || got.Home != "" {
			t.Fatalf("comments = %+v", got)
		}
	})
}

// A PERSON WITH NO SEAT YET IS TOLD HOW TO TAKE ONE, and nothing is read.
func TestMadeWorkspace_NoSeatYetSaysHowToTakeOne(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		f, as := madeBoards(t)
		delete(f.bound, "u_josh")
		code, raw := as(http.MethodGet, "/v1/task/projects", nil)
		if code != http.StatusForbidden || !strings.Contains(string(raw), "open Codebase") {
			t.Fatalf("projects = %d %s", code, raw)
		}
	})
}

// AN ORG WHOSE WORKSPACE IS NOT MADE YET IS TOLD SO; one whose record cannot
// be read is a failure, not an empty board.
func TestMadeWorkspace_NoWorkspaceYet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, as := madeBoards(t)
		boardWorkspace = func(context.Context, string) (forge.Workspace, error) { return forge.Workspace{}, nil }
		if code, raw := as(http.MethodGet, "/v1/task/projects", nil); code != http.StatusForbidden || !strings.Contains(string(raw), "no namespace on the forge") {
			t.Fatalf("no workspace = %d %s", code, raw)
		}
		boardWorkspace = func(context.Context, string) (forge.Workspace, error) {
			return forge.Workspace{}, errors.New("store down")
		}
		if code, raw := as(http.MethodGet, "/v1/task/projects", nil); code != http.StatusBadGateway {
			t.Fatalf("unreadable workspace = %d %s", code, raw)
		}
	})
}
