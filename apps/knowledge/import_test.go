package knowledge

import (
	arzip "archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"
)

// reqRaw posts a raw body (an export archive/file) with a validated principal, the
// contract POST /v1/kb/import accepts alongside multipart.
func reqRaw(t *testing.T, app *zip.App, path, org string, body []byte) (int, []byte) {
	t.Helper()
	hr := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	hr.Header.Set("Content-Type", "application/octet-stream")
	hr.Header.Set("X-Org-Id", org)
	hr.Header.Set("X-User-Id", "u_"+org)
	resp, err := app.Fiber().Test(hr)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := arzip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func installKB(t *testing.T, app *zip.App, org string) {
	t.Helper()
	if code, b := req(t, app, http.MethodPost, "/v1/framework/modules/kb/install", org, nil); code != http.StatusOK {
		t.Fatalf("install kb (%s): %d %s", org, code, b)
	}
}

// doImport posts an export and returns the imported page count, failing on a
// non-200.
func doImport(t *testing.T, app *zip.App, path, org string, body []byte) int {
	t.Helper()
	code, b := reqRaw(t, app, path, org, body)
	if code != http.StatusOK {
		t.Fatalf("import status %d: %s", code, b)
	}
	var r struct {
		Imported int `json:"imported"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("decode import resp: %v (%s)", err, b)
	}
	return r.Imported
}

const (
	engID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	runID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// TestImport_AllFormats drives POST /v1/kb/import end-to-end for every format and
// asserts the pages land AND the link structure survives into the graph — the
// SAME after_save hook that indexes a page extracts its wikilinks, so the importer
// needs no link path of its own.
func TestImport_AllFormats(t *testing.T) {
	fv := newFakeVector(t)
	resetIndexer(t, fv.server.URL)
	app := mountKB(t)

	t.Run("obsidian", func(t *testing.T) {
		org := "obs"
		installKB(t, app, org)
		zipData := makeZip(t, map[string]string{
			"Q3 Roadmap.md":              "# Q3 Roadmap\n\nSee the [[Incident Runbook]].\n",
			"people/Incident Runbook.md": "# Incident Runbook\n\nSteps here.\n",
		})
		n := doImport(t, app, "/v1/kb/import?format=obsidian", org, zipData)
		if n != 2 {
			t.Fatalf("obsidian imported %d, want 2", n)
		}
		g := graphOf(t, app, org)
		if !g.hasNode("kb-page:q3-roadmap") || !g.hasNode("kb-page:incident-runbook") {
			t.Fatalf("obsidian pages missing: %+v", g.Nodes)
		}
		if !g.hasEdge("kb-page:q3-roadmap", "kb-page:incident-runbook", "link") {
			t.Fatalf("obsidian wikilink not resolved into an edge: %+v", g.Edges)
		}
	})

	t.Run("notion", func(t *testing.T) {
		org := "notion"
		installKB(t, app, org)
		parent := "Engineering " + engID + ".md"
		child := "Engineering " + engID + "/Runbook " + runID + ".md"
		zipData := makeZip(t, map[string]string{
			parent: "# Engineering\n\nStart with the [Runbook](Engineering%20" + engID + "/Runbook%20" + runID + ".md).\n",
			child:  "# Runbook\n\nBack to [Engineering](../Engineering%20" + engID + ".md).\n",
		})
		n := doImport(t, app, "/v1/kb/import?format=notion", org, zipData)
		if n != 2 {
			t.Fatalf("notion imported %d, want 2", n)
		}
		g := graphOf(t, app, org)
		if !g.hasNode("kb-page:engineering") || !g.hasNode("kb-page:runbook") {
			t.Fatalf("notion pages missing: %+v", g.Nodes)
		}
		// Folder nesting → parent tree; relative links → wikilinks → edges.
		if !g.hasEdge("kb-page:runbook", "kb-page:engineering", "parent") {
			t.Fatalf("notion parent tree missing: %+v", g.Edges)
		}
		if !g.hasEdge("kb-page:engineering", "kb-page:runbook", "link") {
			t.Fatalf("notion link edge missing: %+v", g.Edges)
		}
	})

	t.Run("roam", func(t *testing.T) {
		org := "roam"
		installKB(t, app, org)
		body := []byte(`[{"title":"Project Atlas","children":[{"string":"needs [[Design System]]"}]},{"title":"Design System","children":[{"string":"tokens"}]}]`)
		n := doImport(t, app, "/v1/kb/import?format=roam", org, body)
		if n != 2 {
			t.Fatalf("roam imported %d, want 2", n)
		}
		g := graphOf(t, app, org)
		if !g.hasEdge("kb-page:project-atlas", "kb-page:design-system", "link") {
			t.Fatalf("roam wikilink not resolved: %+v", g.Edges)
		}
	})

	t.Run("evernote", func(t *testing.T) {
		org := "ever"
		installKB(t, app, org)
		body := []byte(`<?xml version="1.0"?><en-export><note><title>Meeting</title><content><![CDATA[<en-note><div>Discussed [[Roadmap]]</div></en-note>]]></content></note></en-export>`)
		n := doImport(t, app, "/v1/kb/import?format=evernote", org, body)
		if n != 1 {
			t.Fatalf("evernote imported %d, want 1", n)
		}
		g := graphOf(t, app, org)
		if !g.hasNode("kb-page:meeting") {
			t.Fatalf("evernote page missing: %+v", g.Nodes)
		}
		// Roadmap page doesn't exist → the authored wikilink dangles.
		if !g.hasEdge("kb-page:meeting", "unresolved:roadmap", "link") {
			t.Fatalf("evernote authored wikilink not extracted: %+v", g.Edges)
		}
	})

	t.Run("project scope", func(t *testing.T) {
		org := "scoped"
		installKB(t, app, org)
		zipData := makeZip(t, map[string]string{"Note.md": "# Note\n\nbody\n"})
		n := doImport(t, app, "/v1/kb/import?format=obsidian&project=teamx", org, zipData)
		if n != 1 {
			t.Fatalf("scoped import %d, want 1", n)
		}
		// The page is only visible under its project scope.
		g := graphOf(t, app, org)
		found := false
		for _, nd := range g.Nodes {
			if nd.ID == "kb-page:note" && nd.Project == "teamx" {
				found = true
			}
		}
		if !found {
			t.Fatalf("imported page not scoped to project teamx: %+v", g.Nodes)
		}
	})

	t.Run("unknown format", func(t *testing.T) {
		org := "bad"
		installKB(t, app, org)
		code, _ := reqRaw(t, app, "/v1/kb/import?format=bogus", org, []byte("x"))
		if code != http.StatusBadRequest {
			t.Fatalf("unknown format must be 400, got %d", code)
		}
	})
}
