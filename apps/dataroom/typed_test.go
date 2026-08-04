package dataroom

// typed_test.go pins the two things the typed plane exists to guarantee: that it
// did not change the wire (byte-identity against the bundle the relay carried),
// and that it DID reach the agent surface (the ops are MCP tools with schemas).
//
// The second is the point of the exercise. An untyped route reaches no MCP tool
// at all, so before typed.go an agent asking the fleet door what it could do was
// never told a data room could be opened. TestDataroomOpsAreMCPTools is the local
// form of that acceptance test; the live one is tools/list at the fleet door.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	_ "github.com/hanzoai/cloud/internal/devmaster"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountMCPApp mounts dataroom on an app whose MCP door is reachable, the way
// captable's does: Bridge at the app level so a tools/call carrying an org header
// resolves a principal, since there is no URL on that transport.
func mountMCPApp(t *testing.T) (*zip.App, *memVFS) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	vfs := newMemVFS()
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), VFS: vfs}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app, vfs
}

// demoOps is the surface the five-minute investor demo needs: open a data room,
// put documents in it, let a party in, and list what exists. Each is pinned by
// its DERIVED tool name, because that name is what an agent addresses and what
// every downstream artifact — SDK method, CLI command, call-plane name — is keyed
// on, so a change to it is a breaking change rather than a rename.
var demoOps = []struct{ tool, why string }{
	{"v1.dataroom.post_datarooms", "open a data room"},
	{"v1.dataroom.get_datarooms", "list the rooms"},
	{"v1.dataroom.get_datarooms_id", "read one room with its documents"},
	{"v1.dataroom.post_datarooms_id_documents", "add a document to a room"},
	{"v1.dataroom.post_links", "grant a party access"},
	{"v1.dataroom.get_links", "list the live share links"},
	{"v1.dataroom.get_documents", "list the documents"},
	{"v1.dataroom.get_documents_id", "read one document"},
	{"v1.dataroom.get_analytics_link_linkId", "see how a link was read"},
	{"v1.dataroom.get_analytics_dataroom_dataroomId", "see how a room was read"},
}

// TestDataroomOpsAreMCPTools is the acceptance test for this whole file: every
// demo op must appear in the tool list an agent reads, WITH a description and an
// input schema. A tool with neither is worth little to an agent — it can see the
// name and nothing about how to call it — so the schema and prose are asserted,
// not just presence.
func TestDataroomOpsAreMCPTools(t *testing.T) {
	app, _ := mountMCPApp(t)

	byName := map[string]map[string]any{}
	for _, tool := range app.MCPTools() {
		if n, _ := tool["name"].(string); n != "" {
			byName[n] = tool
		}
	}
	if len(byName) == 0 {
		t.Fatalf("the app publishes NO MCP tools at all — dataroom reaches no agent")
	}

	for _, want := range demoOps {
		tool, ok := byName[want.tool]
		if !ok {
			t.Errorf("no MCP tool %q — an agent cannot %s.\npublished: %v",
				want.tool, want.why, names(byName))
			continue
		}
		desc, _ := tool["description"].(string)
		if strings.TrimSpace(desc) == "" {
			t.Errorf("tool %q has no description — zipdoc_gen.go is stale, so the agent "+
				"is told the op exists and nothing about when to use it", want.tool)
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		if schema == nil {
			t.Errorf("tool %q has no inputSchema — this is what an untyped route "+
				"publishes, and typing it was the whole point", want.tool)
		}
	}
}

// TestGrantAccessSchemaDescribesItsLists pins the one field shape that a wrong
// type would break SILENTLY. The bundle substitutes an EMPTY list for anything
// that is not an array, so an agent told `allowList` is a string sends one, the
// data room discards it, and the call SUCCEEDS having ignored the access control.
// A link that was meant to admit one investor would admit everyone.
func TestGrantAccessSchemaDescribesItsLists(t *testing.T) {
	app, _ := mountMCPApp(t)
	for _, tool := range app.MCPTools() {
		if n, _ := tool["name"].(string); n != "v1.dataroom.post_links" {
			continue
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		for _, list := range []string{"allowList", "denyList"} {
			f, ok := props[list].(map[string]any)
			if !ok {
				t.Fatalf("%s is not in the grant-access schema at all", list)
			}
			if f["type"] != "array" {
				t.Errorf("%s is published as %v, not an array — an agent will send the "+
					"wrong shape and the data room will silently ignore the gate", list, f["type"])
			}
		}
		return
	}
	t.Fatal("no grant-access tool to inspect")
}

// TestDemoFlowThroughTypedOps drives the whole demo step over the TYPED routes:
// open a room, upload a document, put it in the room, grant a party access, and
// read back what exists. It asserts on the answers an agent would chain, so a
// break in any link of that chain fails here rather than on stage.
func TestDemoFlowThroughTypedOps(t *testing.T) {
	app, _ := mountFlowApp(t)
	const org = "acme"

	// 1. Open a data room.
	code, room := jsonReq(t, app, "POST", "/v1/dataroom/datarooms", org,
		map[string]any{"name": "Acme Series A", "description": "Diligence"})
	if code != 200 {
		t.Fatalf("open a data room: %d %v", code, room)
	}
	roomID := str(room, "dataroom", "id")
	if roomID == "" {
		t.Fatalf("a new room must answer with the id everything else addresses it by: %v", room)
	}
	if got := str(room, "dataroom", "name"); got != "Acme Series A" {
		t.Errorf("room name = %q, want %q", got, "Acme Series A")
	}
	if str(room, "dataroom", "pId") == "" {
		t.Errorf("a room must carry the short public id it is shared under: %v", room)
	}

	// 2. Upload a document (untyped by wire — the file IS the body).
	code, doc := jsonUpload(t, app, org, "deck.pdf", []byte("%PDF-1.7 pitch"))
	if code != 200 {
		t.Fatalf("upload: %d %v", code, doc)
	}
	docID := str(doc, "document", "id")
	if docID == "" {
		t.Fatalf("upload must answer with a document id: %v", doc)
	}

	// 3. Put the document in the room.
	code, member := jsonReq(t, app, "POST", "/v1/dataroom/datarooms/"+roomID+"/documents", org,
		map[string]any{"documentId": docID, "orderIndex": 1})
	if code != 200 {
		t.Fatalf("add document to room: %d %v", code, member)
	}
	if str(member, "dataroomDocumentId") == "" {
		t.Fatalf("attaching must answer with the membership id: %v", member)
	}

	// 4. Grant a party access — the link is what the investor actually opens.
	code, link := jsonReq(t, app, "POST", "/v1/dataroom/links", org, map[string]any{
		"dataroomId": roomID,
		"name":       "Sequoia",
		"allowList":  []string{"partner@sequoiacap.com"},
	})
	if code != 200 {
		t.Fatalf("grant access: %d %v", code, link)
	}
	linkID := str(link, "link", "id")
	if linkID == "" {
		t.Fatalf("granting access must answer with the link id a visitor opens: %v", link)
	}
	// The allow list must have SURVIVED as a list — the silent-failure case.
	got, _ := link["link"].(map[string]any)["allowList"].([]any)
	if len(got) != 1 || got[0] != "partner@sequoiacap.com" {
		t.Fatalf("the access control did not survive the typed op: allowList = %v", got)
	}

	// 5. The room now reads back with its document, in the order a visitor sees.
	code, detail := jsonReq(t, app, "GET", "/v1/dataroom/datarooms/"+roomID, org, nil)
	if code != 200 {
		t.Fatalf("read room: %d %v", code, detail)
	}
	docs, _ := detail["dataroom"].(map[string]any)["documents"].([]any)
	if len(docs) != 1 {
		t.Fatalf("the room must contain the document that was added: %v", detail)
	}
	if d, _ := docs[0].(map[string]any); d["id"] != docID {
		t.Errorf("room holds %v, want the uploaded document %s", d["id"], docID)
	}

	// 6. And the room is in the org's list.
	code, rooms := jsonReq(t, app, "GET", "/v1/dataroom/datarooms", org, nil)
	if code != 200 {
		t.Fatalf("list rooms: %d %v", code, rooms)
	}
	if list, _ := rooms["datarooms"].([]any); len(list) != 1 {
		t.Fatalf("the org's room list must hold exactly the room that was opened: %v", rooms)
	}
}

// TestGrantAccessStillWritesTheCrossTenantIndex pins the side effect that makes a
// link USABLE. An anonymous visitor resolves the owning org from the link index,
// so a link absent from it opens for nobody — the typed op reimplements that
// write, and this is what proves it did not get lost in the move.
func TestGrantAccessStillWritesTheCrossTenantIndex(t *testing.T) {
	app, _ := mountFlowApp(t)
	const org = "acme"

	_, room := jsonReq(t, app, "POST", "/v1/dataroom/datarooms", org, map[string]any{"name": "R"})
	roomID := str(room, "dataroom", "id")
	code, link := jsonReq(t, app, "POST", "/v1/dataroom/links", org, map[string]any{"dataroomId": roomID})
	if code != 200 {
		t.Fatalf("grant access: %d %v", code, link)
	}
	linkID := str(link, "link", "id")

	// The public viewer route carries NO principal and resolves the org from the
	// index alone. It answering at all is the proof the index was written.
	code, view := jsonReq(t, app, "GET", "/v1/dataroom/view/"+linkID, "", nil)
	if code != 200 {
		t.Fatalf("a granted link must open for an anonymous visitor, got %d %v — "+
			"the typed op did not write the link index", code, view)
	}
	if str(view, "link", "id") != linkID {
		t.Errorf("the visitor opened %v, want the link that was granted %s", view, linkID)
	}
}

// TestTypedReadsAreByteIdenticalToTheBundle pins that typing changed no answer.
// The models declare their fields in alphabetical json-tag order for exactly this
// reason: the bundle's rows are re-marshalled by encoding/json, which sorts keys,
// so a field added out of order shows up HERE rather than as drift a client
// notices later.
func TestTypedReadsAreByteIdenticalToTheBundle(t *testing.T) {
	app, _ := mountFlowApp(t)
	const org = "acme"

	_, room := jsonReq(t, app, "POST", "/v1/dataroom/datarooms", org,
		map[string]any{"name": "Byte Room", "description": "d"})
	roomID := str(room, "dataroom", "id")
	_, doc := jsonUpload(t, app, org, "a.pdf", []byte("bytes"))
	docID := str(doc, "document", "id")
	jsonReq(t, app, "POST", "/v1/dataroom/datarooms/"+roomID+"/documents", org,
		map[string]any{"documentId": docID})
	jsonReq(t, app, "POST", "/v1/dataroom/links", org, map[string]any{"dataroomId": roomID})

	for _, c := range []struct {
		path, route string
		params      map[string]string
	}{
		{"/v1/dataroom/datarooms", "datarooms.list", nil},
		{"/v1/dataroom/documents", "documents.list", nil},
		{"/v1/dataroom/links", "links.list", nil},
		{"/v1/dataroom/datarooms/" + roomID, "datarooms.get", map[string]string{"id": roomID}},
		{"/v1/dataroom/documents/" + docID, "documents.get", map[string]string{"id": docID}},
		{"/v1/dataroom/analytics/dataroom/" + roomID, "analytics.dataroom", map[string]string{"dataroomId": roomID}},
	} {
		_, typed := req(t, app, "GET", c.path, org, "", nil)
		resp, err := mounted.State.host.Dispatch(context.Background(), org,
			goja.BaseRequest{Route: c.route, Params: c.params})
		if err != nil {
			t.Fatalf("%s: bundle dispatch: %v", c.route, err)
		}
		if !jsonEqualBytes(t, typed, resp.Body) {
			t.Errorf("%s is NOT byte-identical to the bundle it relays:\n typed:  %s\n bundle: %s",
				c.path, typed, resp.Body)
		}
	}
}

// TestTypedOpsAreOrgScoped pins that the tenant comes from the PRINCIPAL and
// never from an input the caller controls. A room opened by one org must be
// invisible — and unaddressable — to another, and unreachable with no principal
// at all.
func TestTypedOpsAreOrgScoped(t *testing.T) {
	app, _ := mountFlowApp(t)

	_, room := jsonReq(t, app, "POST", "/v1/dataroom/datarooms", "acme", map[string]any{"name": "Private"})
	roomID := str(room, "dataroom", "id")

	// Another tenant cannot see it in a list...
	_, rooms := jsonReq(t, app, "GET", "/v1/dataroom/datarooms", "other", nil)
	if list, _ := rooms["datarooms"].([]any); len(list) != 0 {
		t.Errorf("another org sees %v — the tenant is not the principal's", rooms)
	}
	// ...nor reach it by its id, which it knows.
	if code, _ := jsonReq(t, app, "GET", "/v1/dataroom/datarooms/"+roomID, "other", nil); code != 404 {
		t.Errorf("another org addressing a known room id got %d, want 404", code)
	}
	// ...and no principal is refused outright.
	if code, _ := jsonReq(t, app, "GET", "/v1/dataroom/datarooms", "", nil); code != 403 {
		t.Errorf("no principal got %d, want 403", code)
	}
}

// TestRefusalKeepsTheDataRoomsOwnEnvelope pins that a typed op did NOT replace
// the bundle's refusal with zip's. The data room says {"error": …}; a client
// reading that key must keep reading it.
func TestRefusalKeepsTheDataRoomsOwnEnvelope(t *testing.T) {
	app, _ := mountFlowApp(t)

	code, body := jsonReq(t, app, "POST", "/v1/dataroom/datarooms", "acme", map[string]any{})
	if code != 400 {
		t.Fatalf("a room with no name must be refused 400, got %d %v", code, body)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Errorf("the refusal lost the data room's own envelope: %v", body)
	}
	if code, body := jsonReq(t, app, "GET", "/v1/dataroom/datarooms/nope", "acme", nil); code != 404 {
		t.Errorf("unknown room = %d %v, want 404", code, body)
	}
}

// --- helpers ---------------------------------------------------------------

func names(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for n := range m {
		if strings.Contains(n, "dataroom") {
			out = append(out, n)
		}
	}
	return out
}

// jsonUpload posts file bytes to the upload route, which takes the file ITSELF as
// the body — the wire reason that route is not a typed op.
func jsonUpload(t *testing.T, app *zip.App, org, name string, raw []byte) (int, map[string]any) {
	t.Helper()
	code, b := req(t, app, "POST", "/v1/dataroom/documents?name="+name, org, "application/pdf", raw)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return code, m
}

// jsonEqualBytes compares two JSON documents byte for byte, reporting a decode
// failure rather than a silent false.
func jsonEqualBytes(t *testing.T, a, b []byte) bool {
	t.Helper()
	return string(a) == string(b)
}
