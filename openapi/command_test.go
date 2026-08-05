package openapi

// The fifth projection, pinned the way the other four are (fleet.go): not by
// asserting a list of commands — that would be the hand-authored list this whole
// package exists to delete — but by requiring the served projection to agree
// with the document it is a projection OF.
//
// Every test here runs against the PUBLISHED document, openapi.yaml, through the
// real serve() path. So the subject is the fleet's actual 2,349 operations and
// the actual wiring, not a two-route fixture that would prove the shape and
// nothing about the scale that motivated a second address.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
	"sigs.k8s.io/yaml"
)

// golden is the committed fleet document. Named again here rather than shared
// with weave_test.go's identical constant because that file is the EXTERNAL test
// package and these tests need serve(), which is unexported.
const golden = "../openapi.yaml"

// published is the committed fleet document as JSON — the same bytes the weave
// writes openapi.yaml from, read back.
func published(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v — run `make describe`", golden, err)
	}
	doc, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatalf("%s: %v", golden, err)
	}
	return doc
}

// mounted serves the published document through serve(), which is the ONE
// registrar both Mount and MountFleet go through — so what these tests read is
// the wiring production uses, given production's document.
func mounted(t *testing.T) *zip.App {
	t.Helper()
	var doc Document
	if err := json.Unmarshal(published(t), &doc); err != nil {
		t.Fatalf("decode %s: %v", golden, err)
	}
	app := newApp()
	serve(app, func() (*Document, error) { return &doc, nil })
	return app
}

// ask performs one GET and returns the status, the headers and the body.
func ask(t *testing.T, app *zip.App, path string, header ...string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://cloud"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	body := new(bytes.Buffer)
	if _, err := body.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, body.Bytes()
}

// colon converts an OpenAPI "{name}" template to the router's ":name" form —
// the spelling a Command carries, whichever derivation read it.
func colon(path string) string {
	if !strings.Contains(path, "{") {
		return path
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, "{") && strings.HasSuffix(p, "}") {
			parts[i] = ":" + p[1:len(p)-1]
		}
	}
	return strings.Join(parts, "/")
}

// PIN 1 — a command is a route. Every command served names a (method, path) the
// document carries, which is what makes the bar unable to offer an operation the
// fleet does not answer. The reverse direction is pin 2's equality.
func TestEveryCommandIsARouteTheDocumentCarries(t *testing.T) {
	var doc Document
	if err := json.Unmarshal(published(t), &doc); err != nil {
		t.Fatal(err)
	}
	routes := map[string]bool{}
	for path, item := range doc.Paths {
		for method := range item {
			routes[strings.ToUpper(method)+" "+colon(path)] = true
		}
	}

	_, _, body := ask(t, mounted(t), CommandPath)
	var cmds []zip.Command
	if err := json.Unmarshal(body, &cmds); err != nil {
		t.Fatalf("decode %s: %v", CommandPath, err)
	}
	if len(cmds) == 0 {
		t.Fatalf("%s served no commands from a %d-operation document", CommandPath, len(routes))
	}
	for _, c := range cmds {
		if !routes[c.Method+" "+c.Path] {
			t.Errorf("command %s %s (%s %s) is not a route the document carries",
				c.Service, c.Name, c.Method, c.Path)
		}
	}
	t.Logf("%d commands over %d document operations", len(cmds), len(routes))
}

// PIN 2 — the endpoint IS the projection, with nothing added and nothing
// dropped. Compared as BYTES against zip.CommandsFromSpec over the same
// document, both put in the endpoint's total order first, so the pin is exact:
// were the endpoint to filter by method, by product or by caller, or to enrich a
// command with a field the registry does not have, this fails.
//
// Two further properties of the SAME contract are checked here rather than in
// tests of their own, because they are one claim — that what is served is a
// function of the document and says so:
//
//   - Two independent mounts of one document serve identical bytes under an
//     identical ETag. This is not a formality: 41 commands share zip's
//     (Service, Name) sort key, so before order() the tie fell to map iteration
//     and this failed nearly every run.
//   - A caller holding the tag gets 304 and no body, not half a megabyte twice.
func TestServedCommandsAreTheProjectionOfTheDocument(t *testing.T) {
	want, err := zip.CommandsFromSpec(published(t))
	if err != nil {
		t.Fatalf("CommandsFromSpec: %v", err)
	}
	order(want)
	wantBody, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	app := mounted(t)
	status, head, body := ask(t, app, CommandPath)
	if status != http.StatusOK {
		t.Fatalf("GET %s: status %d", CommandPath, status)
	}
	if !bytes.Equal(body, wantBody) {
		t.Errorf("served list is not CommandsFromSpec(%s): served %d bytes, projection %d",
			golden, len(body), len(wantBody))
	}

	etag := head.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag: the list is immutable for the process lifetime and must say so")
	}

	// A SECOND app, weaving the same document from scratch — a different process
	// would be a different map walk, and must not be a different artifact.
	againStatus, againHead, again := ask(t, mounted(t), CommandPath)
	if againStatus != http.StatusOK {
		t.Fatalf("second mount: status %d", againStatus)
	}
	if !bytes.Equal(body, again) {
		t.Errorf("two mounts of one document served different bytes (%d vs %d)", len(body), len(again))
	}
	if got := againHead.Get("ETag"); got != etag {
		t.Errorf("two mounts of one document served different ETags: %s vs %s", etag, got)
	}

	status, _, body = ask(t, app, CommandPath, "If-None-Match", etag)
	if status != http.StatusNotModified {
		t.Errorf("If-None-Match %s: status %d, want %d", etag, status, http.StatusNotModified)
	}
	if len(body) != 0 {
		t.Errorf("304 carried %d bytes of body", len(body))
	}
}

// PIN 3 — the size, which is the ONLY reason this is a second address rather
// than a field on the document. If the projection ever stops being smaller than
// what it projects, the separate endpoint has lost its whole justification and
// this says so.
//
// The margin is 1.32x, not the 4.5x the design assumed from a different artifact
// (see command.go). The logged numbers are the real ones — read them before
// quoting a ratio anywhere.
func TestCommandsAreSmallerThanTheDocument(t *testing.T) {
	doc := published(t)
	_, _, body := ask(t, mounted(t), CommandPath)

	if len(body) >= len(doc) {
		t.Fatalf("projection %d bytes is not smaller than the %d-byte document it derives from",
			len(body), len(doc))
	}
	// A palette loads this over the wire, so the honest measure is compressed.
	var zipped bytes.Buffer
	w := gzip.NewWriter(&zipped)
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("document %d bytes → commands %d bytes (%.1fx smaller), %d gzipped",
		len(doc), len(body), float64(len(doc))/float64(len(body)), zipped.Len())
}
