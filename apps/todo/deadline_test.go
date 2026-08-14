package todo

// deadline_test.go is the regression test for the bug this file's neighbours
// were changed to fix.
//
// The shipped symptom: todo.hanzo.ai sat on loading skeletons forever with an
// EMPTY BROWSER CONSOLE — no error, because the XHR never completed. The forge
// takes 10-22 seconds to answer /orgs/{org}/repos for the 64-repo `hanzo` org
// (it computes permissions and statistics per repository), the board list is
// that call, and nothing on the path had a deadline: the forge client's own
// timeout bounds ONE request while a read here makes many.
//
// The lesson generalises past the todo, which is why these tests are blunt
// about it: a slow dependency with no deadline does not surface as an error, it
// surfaces as a UI that looks like it is still working. The only defence is that
// EVERY read has a deadline and that exceeding it is a visible, named failure —
// never an empty list, which renders as "you have no work" and is indistinguish-
// able from a correct answer.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// wedgedForge accepts the connection and never answers — the shape of a slow or
// stuck upstream, which is different from one that refuses. A refusal was always
// handled; this is the case that hung.
func wedgedForge(t *testing.T) *httptest.Server {
	t.Helper()
	done := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	// Cleanups run last-in-first-out, so `done` closes BEFORE Close waits on the
	// handlers. Without that ordering the server's own Close blocks: a cache
	// refresh outlives the request that started it — that is what makes it a
	// refresh — so a handler can still be parked here after the test has ended.
	t.Cleanup(s.Close)
	t.Cleanup(func() { close(done) })
	return s
}

// mountAt mounts the todo against an arbitrary forge URL with a SHORT budget,
// so the assertion is about the deadline existing rather than about waiting for
// the production one.
func mountAt(t *testing.T, forgeURL string, b time.Duration) *zip.App {
	t.Helper()
	old := budget
	budget = b
	t.Cleanup(func() { budget = old })

	identity = map[string]plane.Email{}
	serveIdentity(t)
	t.Setenv("CLOUD_FORGE_HOST", forgeURL)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{
		DataDir: t.TempDir(),
		KMS:     kmsStub{token: "forge-machine-token"},
	}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// THE regression test. A forge that never answers must become a 504 within the
// budget. Before the fix this request never returned at all.
func TestForgeDeadline_AWedgedForgeIs504AndNotAHang(t *testing.T) {
	app := mountAt(t, wedgedForge(t).URL, 300*time.Millisecond)

	for _, path := range []string{"/v1/todo/projects", "/v1/todo/projects/api/issues/1"} {
		t.Run(path, func(t *testing.T) {
			start := time.Now()
			code, body := asUser(t, app, http.MethodGet, path, "hanzo", "alice", nil)
			took := time.Since(start)

			if code != http.StatusGatewayTimeout {
				t.Fatalf("a wedged forge answered %d (%s), want 504", code, body)
			}
			// Generous, because the point is "bounded", not "fast". Before the
			// fix this did not return.
			if took > 10*time.Second {
				t.Fatalf("the budget did not bound the request: took %s", took)
			}
		})
	}
}

// The failure must be an ERROR, never an empty board. A 200 with `[]` is the
// silent failure: it renders as "you have no work" and nobody investigates it.
func TestForgeDeadline_ATimeoutIsNeverAnEmptyBoard(t *testing.T) {
	app := mountAt(t, wedgedForge(t).URL, 300*time.Millisecond)

	code, body := asUser(t, app, http.MethodGet, "/v1/todo/projects", "hanzo", "alice", nil)
	if code == http.StatusOK {
		t.Fatalf("a timeout was rendered as a successful empty board: %s", body)
	}
	// And the reason reaches the client, so the page can say WHY rather than
	// showing a spinner or a bare "something went wrong".
	if len(body) == 0 {
		t.Fatal("a 504 with no body gives the page nothing to render")
	}
}

// Every forge-backed route carries the budget, including the writes. A deadline
// on the reads alone would leave "move this card" able to hang the same way.
func TestForgeDeadline_TheWritesAreBoundedToo(t *testing.T) {
	app := mountAt(t, wedgedForge(t).URL, 300*time.Millisecond)

	start := time.Now()
	code, _ := asUser(t, app, http.MethodPost, "/v1/todo/projects/api/issues",
		"hanzo", "alice", map[string]any{"title": "a card filed at a wedged forge"})
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("an unbounded write: took %s", took)
	}
	if code != http.StatusGatewayTimeout {
		t.Fatalf("a wedged forge answered %d on a write, want 504", code)
	}
}
