package knowledge

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/apps/framework"
	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestLexicalRow: the row carries the identity the legs fuse on, the text it is
// scored on, and its owner as the index's user — "" for the org's own.
func TestLexicalRow(t *testing.T) {
	row := lexicalRow(DTMemory, "m1", map[string]any{"title": "api rotation", "content": "rotate monthly", "owner": "alice", "project": "p"})
	if row["key"] != "kb.memory/m1" || row["doctype"] != "kb.memory" || row["name"] != "m1" {
		t.Fatalf("identity: %v", row)
	}
	if row["text"] != "api rotation\n\nrotate monthly" || row["user"] != "alice" || row["project"] != "p" {
		t.Fatalf("text/user/project: %v", row)
	}
	if _, has := row["url"]; has {
		t.Fatal("an absent field is absent, not empty")
	}
	if lexicalRow(DTSource, "s", map[string]any{"title": "t", "body": "b"})["user"] != "" {
		t.Fatal("the org's own document has no user")
	}
}

// mountIndexed mounts the framework, the index and knowledge on one app, as the
// fused binary composes them.
func mountIndexed(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	planetest.Manager(t)
	planetest.Entitled(t, func(_, product string) bool { return product == Module })
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("CLOUD_DOMAIN", "api.test")
	if err := framework.Use(app, cloud.Deps{}); err != nil {
		t.Fatal(err)
	}
	if err := lexical.Use(app, cloud.Deps{}); err != nil {
		t.Fatal(err)
	}
	if err := Use(app, cloud.Deps{Embed: fakeAI{dims: 1024}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = Shutdown()
		_ = lexical.Shutdown()
		_ = framework.Shutdown()
	})
	return app
}

// TestPlantedRowIsNoAnswer: the org's "kb" index takes writes from any member
// through the public Meilisearch dialect, so a row there points at a document
// and is never the answer itself. bob plants a page nobody wrote, handed to
// alice; a memory nobody wrote, for the whole org; and a row re-pointing alice's
// private memory at the org. No search answers any of them, nobody but alice
// reaches her memory, and she sees it as she wrote it.
func TestPlantedRowIsNoAnswer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountIndexed(t)
		install(t, app, "A")
		mine := map[string]any{"title": "my rotation note", "content": "password reset steps", "kind": "note", "owner": "alice"}
		code, b := as(t, app, http.MethodPost, "/v1/framework/kb.memory", "A", "alice", mine)
		if code != http.StatusCreated {
			t.Fatalf("alice's memory: %d %s", code, b)
		}
		var doc struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(b, &doc); err != nil || doc.Name == "" {
			t.Fatalf("no name in %s", b)
		}
		planted := []map[string]any{
			{"key": "kb.page/planted", "doctype": "kb.page", "name": "planted", "title": "SSO password reset",
				"text": "password reset sso", "url": "https://evil.example/sso", "user": "alice"},
			{"key": "kb.memory/everyone", "doctype": "kb.memory", "name": "everyone", "title": "IT policy",
				"text": "password reset policy", "url": "https://evil.example/it"},
			{"key": "kb.memory/" + doc.Name, "doctype": "kb.memory", "name": doc.Name, "title": "bait",
				"text": "password reset", "url": "https://evil.example/bait"},
		}
		if code, b := as(t, app, http.MethodPost, "/v1/index/indexes/kb/documents", "A", "bob", planted); code/100 != 2 {
			t.Fatalf("the dialect write this test is about: %d %s", code, b)
		}
		for _, who := range []string{"alice", "carol", ""} {
			code, raw := as(t, app, http.MethodPost, "/v1/knowledge/search", "A", who, map[string]any{"query": "password reset"})
			var out searchOut
			if err := json.Unmarshal(raw, &out); code != http.StatusOK || err != nil {
				t.Fatalf("search as %q: %d %s", who, code, raw)
			}
			found := false
			for _, h := range out.Hits {
				switch {
				case h.URL != "" || h.Name == "planted" || h.Name == "everyone":
					t.Errorf("%q was answered a planted row: %+v", who, h)
				case h.Name == doc.Name && who != "alice":
					t.Errorf("%q reached alice's memory: %+v", who, h)
				case h.Name == doc.Name && h.Title != "my rotation note":
					t.Errorf("alice's memory answered with a planted title: %+v", h)
				case h.Name == doc.Name:
					found = true
				}
			}
			if who == "alice" && !found {
				t.Errorf("alice must still find her memory: %+v", out.Hits)
			}
		}
	})
}
