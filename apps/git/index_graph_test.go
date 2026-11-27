// Copyright © 2026 Hanzo AI. MIT License.

package git

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
)

// AN INDEXED COMMIT IS FILED INTO THE GRAPH AS ITSELF: the tree it holds, the
// instant it was pushed and the address it lives at, for the org the repo
// belongs to. The code plane is a stand-in declared under the real app name
// and op ids, so the call resolves exactly as it does against the live plane.
func TestIndexFilesTheCommitIntoTheGraph(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// A short run dir: a unix socket path is capped near 104 bytes.
		dir, err := os.MkdirTemp("", "gitg")
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("ZIP_RUNTIME_DIR", dir)
		plane.Unbind()
		cloud.ResetPlane()
		t.Cleanup(func() {
			cloud.ResetPlane()
			plane.Unbind()
			_ = os.RemoveAll(dir)
		})
		app := mountApp(t)

		type call struct {
			tree client.Tree
			org  string
		}
		var mu sync.Mutex
		var graphed []call
		cloud.Plane().Post("/code/graph", func(ctx context.Context, in *client.Tree) (*client.Restated, error) {
			mu.Lock()
			defer mu.Unlock()
			graphed = append(graphed, call{*in, cloud.Who(ctx).Org})
			return &client.Restated{}, nil
		}, zip.WithOperationID(client.CodeGraph))
		cloud.Plane().Post("/code/index", func(context.Context, *client.IndexIn) (*client.Indexed, error) {
			return &client.Indexed{}, nil
		}, zip.WithOperationID(client.CodeIndex))
		stop, err := cloud.ServePlane("code", luxlog.New("test"))
		if err != nil {
			t.Fatalf("serve code plane: %v", err)
		}
		t.Cleanup(func() { _ = stop() })

		if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "app"}); code != http.StatusCreated {
			t.Fatalf("create repo: %d %s", code, b)
		}
		code, body := do(t, app, http.MethodPost, "/v1/git/repos/app/push", "acme", map[string]any{
			"branch": "main", "message": "initial",
			"files": []map[string]any{{"path": "store/store.go", "content": "package store\n"}},
		})
		if code != http.StatusOK {
			t.Fatalf("push: %d %s", code, body)
		}
		var pushed pushResp
		if err := json.Unmarshal(body, &pushed); err != nil {
			t.Fatal(err)
		}

		// Indexed twice, as a retry or a redelivery does: the commit is filed as the
		// same tree at the same instant — the push's — from the same source both
		// times, which is what lets the graph file nothing the second time.
		s := mounted.Load()
		when := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
		for range 2 {
			if err := readAndIndex(context.Background(), s, indexInput{Org: "acme", Repo: "app", Commit: pushed.Commit, Pushed: when}); err != nil {
				t.Fatalf("readAndIndex: %v", err)
			}
		}

		// The source is an address this host answers, and it shows the commit.
		const host = "https://api.hanzo.test"
		page := "/v1/git/repos/app/commits?ref=" + pushed.Commit
		source := host + page
		if code, b := do(t, app, http.MethodGet, page, "acme", nil); code != http.StatusOK || !strings.Contains(string(b), pushed.Commit[:7]) {
			t.Fatalf("the source page %s answered %d without the commit", page, code)
		}
		at := when.Format(time.RFC3339)
		mu.Lock()
		defer mu.Unlock()
		var mine []call
		for _, c := range graphed {
			// The push's own reactor may have filed it too, at the push.
			if c.tree.Source == source && c.tree.At == at {
				mine = append(mine, c)
			}
		}
		if len(mine) != 2 {
			t.Fatalf("%d restatements name the commit %s, want 2: %+v", len(mine), source, graphed)
		}
		c := mine[0]
		for _, again := range mine[1:] {
			if !reflect.DeepEqual(again, c) {
				t.Errorf("two indexes of one commit filed different restatements:\n%+v\n%+v", c, again)
			}
		}
		if c.org != "acme" || c.tree.Repo != "app" || c.tree.At != at {
			t.Errorf("restated %+v for org %q, want repo app at %s for acme", c.tree, c.org, at)
		}
		if len(c.tree.Files) != 1 || c.tree.Files[0].Path != "store/store.go" {
			t.Errorf("restated files %+v, want the pushed tree", c.tree.Files)
		}
	})
}

// A TREE CUT SHORT BY THE READ CAPS IS NOT RESTATED: it is not the whole of what
// the commit states, and restating it would end everything past the cut. With no
// code plane here, a restatement that was attempted fails and one that was not
// does not.
func TestACutTreeIsNotRestated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir, err := os.MkdirTemp("", "gitc")
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("ZIP_RUNTIME_DIR", dir)
		plane.Unbind()
		t.Cleanup(func() {
			plane.Unbind()
			_ = os.RemoveAll(dir)
		})
		mountApp(t)
		s := mounted.Load()
		in := indexInput{Org: "acme", Repo: "app", Pushed: time.Now()}
		if err := fileGraph(s, in, Revision("abc"), nil, true); err == nil {
			t.Fatal("a whole tree was not restated: no code plane was asked")
		}
		if err := fileGraph(s, in, Revision("abc"), nil, false); err != nil {
			t.Fatalf("a cut tree was restated: %v", err)
		}
	})
}

// A REFUSAL IS NOT RETRIED, AN OUTAGE IS. The same tree is refused the same way
// however often a durable index retries it, so only a failure of the plane is
// handed back to be retried.
func TestARefusedRestatementIsNotRetried(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir, err := os.MkdirTemp("", "gitr")
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("ZIP_RUNTIME_DIR", dir)
		plane.Unbind()
		cloud.ResetPlane()
		t.Cleanup(func() {
			cloud.ResetPlane()
			plane.Unbind()
			_ = os.RemoveAll(dir)
		})
		mountApp(t)
		var answer error
		cloud.Plane().Post("/code/graph", func(context.Context, *client.Tree) (*client.Restated, error) {
			return nil, answer
		}, zip.WithOperationID(client.CodeGraph))
		stop, err := cloud.ServePlane("code", luxlog.New("test"))
		if err != nil {
			t.Fatalf("serve code plane: %v", err)
		}
		t.Cleanup(func() { _ = stop() })

		s := mounted.Load()
		in := indexInput{Org: "acme", Repo: "app", Pushed: time.Now()}
		answer = zip.Errorf(http.StatusRequestEntityTooLarge, "too much")
		if err := fileGraph(s, in, Revision("abc"), nil, true); err != nil {
			t.Errorf("a refused restatement was handed back to be retried: %v", err)
		}
		answer = zip.Errorf(http.StatusServiceUnavailable, "down")
		if err := fileGraph(s, in, Revision("abc"), nil, true); err == nil {
			t.Error("an outage was swallowed, so it is never retried")
		}
	})
}
