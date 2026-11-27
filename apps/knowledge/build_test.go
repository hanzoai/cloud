package knowledge

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// build_test.go proves an org's passage store is built from the documents it
// already has: the deploy that replaced one vector per document left every org
// with documents and an empty store.

// gateAI is fakeAI holding every embedding of a text that holds hold, until
// release closes; entered closes when the first one arrives.
type gateAI struct {
	fakeAI
	hold    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGateAI(hold string) *gateAI {
	return &gateAI{fakeAI: fakeAI{dims: 1024}, hold: hold, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateAI) Embed(ctx context.Context, r *infer.EmbedRequest) ([][]float32, error) {
	for _, in := range r.Inputs {
		if strings.Contains(in, g.hold) {
			g.once.Do(func() { close(g.entered) })
			<-g.release
			break
		}
	}
	return g.fakeAI.Embed(ctx, r)
}

// before writes the org's knowledge the way the fleet did before passages: the
// framework alone, with no knowledge process to reach, so no document is ever
// embedded. It answers the long page's name.
func before(t *testing.T, dir string) string {
	t.Helper()
	runDir(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	planetest.Manager(t)
	planetest.Entitled(t, func(_, product string) bool { return product == Module })
	t.Setenv("CLOUD_DATA_DIR", dir)
	if err := framework.Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("framework.Use: %v", err)
	}
	install(t, app, "A")
	name := create(t, app, "A", "kb.page", map[string]any{"title": "Operations handbook", "slug": "handbook", "body": longPage()})
	for range 3 {
		create(t, app, "A", "kb.memory", map[string]any{"title": "lunch", "content": "lunch menu for the offsite", "kind": "note"})
	}
	if err := framework.Shutdown(); err != nil {
		t.Fatal(err)
	}
	return name
}

// TestExistingKnowledgeIsBuilt: the org's first search after the deploy builds
// its store from the documents it holds, finds the fact in the long page's tail
// with a real similarity, and is not degraded; the store is marked built, so it
// is not built again.
func TestExistingKnowledgeIsBuilt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		name := before(t, dir)
		app := mountKBAt(t, dir, fakeAI{dims: 1024})
		install(t, app, "A") // opens the framework store the pages were written to

		out := search(t, app, "A", map[string]any{"query": tailQuery, "limit": 1})
		if out.Degraded || len(out.Hits) != 1 || out.Hits[0].Name != name || out.Hits[0].Score <= 0 {
			t.Fatalf("the page written before the store existed must be found: %+v", out)
		}
		if n := passages(t, "A", framework.ID{}, ""); n < 13 {
			t.Fatalf("the built store holds %d passages", n)
		}
		st, err := idx.storeFor("A")
		if err != nil {
			t.Fatal(err)
		}
		var v int
		if err := st.db.QueryRow(`SELECT value FROM marks WHERE name = 'built'`).Scan(&v); err != nil || v != 1 {
			t.Fatalf("the store is not marked built: %d %v", v, err)
		}
	})
}

// TestUnbuiltStoreIsDegraded: while the org's build runs, the vector leg answers
// that its passages are not built — /v1/knowledge/search degraded, Semantic an
// error — rather than an empty that reads as "nothing matches". Once the build
// ends, the same search answers.
func TestUnbuiltStoreIsDegraded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		name := before(t, dir)
		ai := newGateAI("backup rotation")
		app := mountKBAt(t, dir, ai)
		install(t, app, "A") // opens the framework store the pages were written to

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, degraded := retrieve(ctx, luxlog.New("test"), searchReq{org: "A", query: tailQuery}); !degraded {
			t.Fatal("a search while the store is being built must be degraded")
		}
		<-ai.entered
		ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if hits, err := Semantic(ctx, SemanticReq{Org: "A", Query: tailQuery}); err == nil {
			t.Fatalf("the vector leg answered %+v from a store being built", hits)
		}

		close(ai.release)
		out := search(t, app, "A", map[string]any{"query": tailQuery, "limit": 1})
		if out.Degraded || len(out.Hits) != 1 || out.Hits[0].Name != name || out.Hits[0].Score <= 0 {
			t.Fatalf("once built, the page must be found: %+v", out)
		}
	})
}

// TestOverlappingSavesLandTheLatest: two saves of one page overlap, and the older
// one's embedding finishes last. The page's passages are the newer text's, and
// the words only the older text had find nothing.
func TestOverlappingSavesLandTheLatest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ai := newGateAI("stale")
		app := mountKBWith(t, ai)
		install(t, app, "A")
		name := create(t, app, "A", "kb.page", map[string]any{"title": "T", "slug": "s", "body": "first version"})
		search(t, app, "A", map[string]any{"query": "first"}) // the org's first build has ended

		stale := map[string]any{"title": "T", "slug": "s", "body": "stale secret rotated password hunter2"}
		latest := map[string]any{"title": "T", "slug": "s", "body": "latest text only"}
		var wg sync.WaitGroup
		put := func(body map[string]any) {
			defer wg.Done()
			req(t, app, http.MethodPut, "/v1/framework/kb.page/"+name, "A", body)
		}
		wg.Add(2)
		go put(stale)
		<-ai.entered // the stale save has landed, and its hook is embedding
		go put(latest)
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if _, b := req(t, app, http.MethodGet, "/v1/framework/kb.page/"+name, "A", nil); strings.Contains(string(b), "latest text only") {
				break
			}
			if time.Now().After(deadline) {
				close(ai.release)
				t.Fatal("the latest save never landed")
			}
		}
		close(ai.release)
		wg.Wait()

		want := split(docText(DTPage, "T", latest))
		if got := spans(t, "A", DTPage.String(), name); len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("the page has passages %v, want the latest text's %v", got, want)
		}
		hits, err := Semantic(context.Background(), SemanticReq{Org: "A", Query: "secret rotated password hunter2"})
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hits {
			if h.Score > 0 {
				t.Fatalf("words the page no longer has still find it: %+v", h)
			}
		}
	})
}
