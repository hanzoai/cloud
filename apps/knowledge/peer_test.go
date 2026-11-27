package knowledge

// peer_test.go — knowledge as it is DEPLOYED: framework, knowledge and the index
// each in a process of its own. A hook runs where the framework runs, a passage
// store is where knowledge runs, and a lexical row is where the index runs, so a
// save reaches its legs only across the plane. These tests take the plane leg on
// purpose, over a real unix socket to a stand-in peer, because the fused suite
// never does.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// runDir points this test's plane at a directory of its own. It is not
// t.TempDir(): that name carries the test's name, and a unix socket path is
// capped near 104 bytes.
func runDir(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "kx")
	if err != nil {
		t.Fatalf("run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
}

// called is one call a stand-in answered: the tenant it came from and its In.
type called[In any] struct {
	org string
	in  In
}

// recorder keeps the calls one stand-in op answered, in order.
type recorder[In any] struct {
	mu    sync.Mutex
	calls []called[In]
}

func (r *recorder[In]) seen() []called[In] {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]called[In](nil), r.calls...)
}

// record answers op on app, keeping each call.
func record[In any](app *zip.App, path, op string) *recorder[In] {
	r := &recorder[In]{}
	app.Post(path, func(ctx context.Context, in *In) (*client.Unit, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.calls = append(r.calls, called[In]{org: cloud.Who(ctx).Org, in: *in})
		return nil, nil
	}, zip.WithOperationID(op))
	return r
}

// serve listens for app on its canonical socket, as the peer process does.
func serve(t *testing.T, app *zip.App, name string) {
	t.Helper()
	go func() { _ = app.Listen(zip.SocketPath(name)) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for range 200 {
		if c, err := net.Dial("unix", zip.SocketPath(name)); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s stand-in never began listening", name)
}

// TestSaveReachesTheKnowledgeProcess is plugin/framework: it links knowledge for
// its hooks and mounts none of it. A save and a delete there must reach the
// knowledge process, named by document and asked as the org — the save as a
// document to read back, the delete as one that is gone. Before this, the hooks
// found no vector leg in this process and did nothing.
func TestSaveReachesTheKnowledgeProcess(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if idx != nil {
			t.Fatal("split test: knowledge is mounted in this process")
		}
		runDir(t)
		peer := zip.New(zip.Config{AppName: "knowledge", DisableStartupMessage: true})
		got := record[client.KnowledgeIndexIn](peer, "/knowledge/index", client.KnowledgeIndex)
		serve(t, peer, "knowledge")

		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		compose(app)
		planetest.Manager(t)
		planetest.Entitled(t, func(_, product string) bool { return product == Module })
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
		if err := framework.Use(app, cloud.Deps{}); err != nil {
			t.Fatalf("framework.Use: %v", err)
		}
		t.Cleanup(func() { _ = framework.Shutdown() })
		install(t, app, "A")
		name := create(t, app, "A", "kb.page", map[string]any{"title": "Runbook", "slug": "runbook", "body": "rotate the key"})
		if code, b := req(t, app, http.MethodDelete, "/v1/framework/kb.page/"+name, "A", nil); code/100 != 2 {
			t.Fatalf("delete: %d %s", code, b)
		}

		want := []called[client.KnowledgeIndexIn]{
			{org: "A", in: client.KnowledgeIndexIn{DocType: "kb.page", Name: name}},
			{org: "A", in: client.KnowledgeIndexIn{DocType: "kb.page", Name: name, Gone: true}},
		}
		if calls := got.seen(); len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
			t.Fatalf("the knowledge process was asked %+v, want %+v", calls, want)
		}
	})
}

// TestSaveReachesTheIndexProcess is plugin/knowledge beside plugin/index: the
// lexical row a save writes and a delete takes out must reach the index process.
// Before this, both answered "index: not mounted" here and the row never moved.
func TestSaveReachesTheIndexProcess(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if lexical.Ready() {
			t.Fatal("split test: an index is mounted in this process")
		}
		runDir(t)
		peer := zip.New(zip.Config{AppName: "index", DisableStartupMessage: true})
		puts := record[client.IndexPutIn](peer, "/index/put", client.IndexPut)
		removes := record[client.IndexRemoveIn](peer, "/index/remove", client.IndexRemove)
		serve(t, peer, "index")

		app := mountKB(t)
		install(t, app, "A")
		name := create(t, app, "A", "kb.page", map[string]any{"title": "Runbook", "slug": "runbook", "body": "rotate the key"})
		search(t, app, "A", map[string]any{"query": "rotate"}) // the org's first build has ended
		if code, b := req(t, app, http.MethodDelete, "/v1/framework/kb.page/"+name, "A", nil); code/100 != 2 {
			t.Fatalf("delete: %d %s", code, b)
		}

		key := "kb.page/" + name
		put := puts.seen()
		if len(put) == 0 {
			t.Fatal("the save never reached the index process")
		}
		var row map[string]any
		if err := json.Unmarshal(put[0].in.Doc, &row); err != nil {
			t.Fatal(err)
		}
		if put[0].org != "A" || put[0].in.UID != lexicalIndex || put[0].in.PrimaryKey != lexicalKey || row[lexicalKey] != key {
			t.Fatalf("the row was written as %+v %v", put[0], row)
		}
		rm := removes.seen()
		if len(rm) == 0 || rm[len(rm)-1].org != "A" || len(rm[len(rm)-1].in.Keys) != 1 || rm[len(rm)-1].in.Keys[0] != key {
			t.Fatalf("the delete reached the index process as %+v", rm)
		}
	})
}

// TestIndexOpReadsTheStore is the op the knowledge process answers: it makes a
// document's passages match what the framework store holds, and takes them out
// for a document that is gone.
func TestIndexOpReadsTheStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountKB(t)
		install(t, app, "A")
		name := create(t, app, "A", "kb.memory", map[string]any{"title": "rota", "content": "the rota is on the wall", "kind": "fact"})
		st, err := idx.storeFor("A")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err := st.drop(ctx, DTMemory.String(), name); err != nil {
			t.Fatal(err)
		}
		as := cloud.For(ctx, "A")
		if _, err := planeIndex(as, &client.KnowledgeIndexIn{DocType: "kb.memory", Name: name}); err != nil {
			t.Fatalf("index: %v", err)
		}
		if n := passages(t, "A", DTMemory, name); n != 1 {
			t.Fatalf("the op left %d passages, want the memory's 1", n)
		}
		if _, err := planeIndex(as, &client.KnowledgeIndexIn{DocType: "kb.memory", Name: name, Gone: true}); err != nil {
			t.Fatalf("gone: %v", err)
		}
		if n := passages(t, "A", DTMemory, name); n != 0 {
			t.Fatalf("a gone document kept %d passages", n)
		}
		if _, err := planeIndex(as, &client.KnowledgeIndexIn{DocType: "kb.connector", Name: "github"}); err == nil {
			t.Fatal("a doctype that is not knowledge was indexed")
		}
		if _, err := planeIndex(ctx, &client.KnowledgeIndexIn{DocType: "kb.memory", Name: name}); err == nil {
			t.Fatal("a call with no org was served")
		}
	})
}
