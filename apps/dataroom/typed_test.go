package dataroom

// typed_test.go pins the two things the typed plane exists to guarantee: that it
// did not change the wire (byte-identity against the bundle the relay carried),
// and that it DID reach the agent surface (the ops are MCP tools with schemas).
//
// The second is the point of the exercise. An untyped route reaches no MCP tool
// at all, so before typed.go an agent asking the fleet MCP server what it could do
// was never told a data room could be opened. TestDataroomOpsAreMCPTools is the
// local form of that acceptance test; the live one is tools/list at the fleet MCP
// server.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/goja"
	_ "github.com/hanzoai/cloud/internal/devmaster"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountMCPApp mounts dataroom on an app whose MCP server is reachable, the way
// captable's does: Bridge at the app level so a tools/call carrying an org header
// resolves a principal, since there is no URL on that transport.
func mountMCPApp(t *testing.T) (*zip.App, *memVFS) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	vfs := newMemVFS()
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := useWith(app, cloud.Deps{}, vfs); err != nil {
		t.Fatalf("Use:  %v", err)
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
	{"post_dataroom_datarooms", "open a data room"},
	{"get_dataroom_datarooms", "list the rooms"},
	{"get_dataroom_datarooms_by_id", "read one room with its documents"},
	{"post_dataroom_datarooms_by_id_documents", "add a document to a room"},
	{"post_dataroom_links", "grant a party access"},
	{"get_dataroom_links", "list the live share links"},
	{"get_dataroom_documents", "list the documents"},
	{"get_dataroom_documents_by_id", "read one document"},
	{"get_dataroom_analytics_link_by_linkid", "see how a link was read"},
	{"get_dataroom_analytics_dataroom_by_dataroomid", "see how a room was read"},
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
		if n, _ := tool["name"].(string); n != "post_dataroom_links" {
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

// TestAgentOpensADataRoomOverMCP is the demo itself, driven the way the AGENT
// will drive it: through tools/call, where there is NO URL. The arguments object
// is the whole input and zip passes it as the body with a NIL path map, so an op
// whose address reaches it only from the path is addressable over REST and
// nowhere else — every test above would stay green and the agent would get
// not-found. Three of these ops carry an id, so this is the failure this file
// exists to make impossible.
func TestAgentOpensADataRoomOverMCP(t *testing.T) {
	app, _ := mountMCPApp(t)
	const org = "acme"

	// Open a room.
	text, isErr := toolsCall(t, app, org, "post_dataroom_datarooms",
		`{"name":"Acme Series A","description":"Diligence"}`)
	if isErr {
		t.Fatalf("an agent cannot open a data room over MCP: %s", text)
	}
	roomID := idFrom(t, text, "dataroom")
	if roomID == "" {
		t.Fatalf("opening a room over MCP answered no id: %s", text)
	}

	// Grant a party access, naming the room by ARGUMENT alone.
	text, isErr = toolsCall(t, app, org, "post_dataroom_links",
		`{"dataroomId":"`+roomID+`","allowList":["partner@sequoiacap.com"],"name":"Sequoia"}`)
	if isErr {
		t.Fatalf("an agent cannot grant access over MCP: %s", text)
	}
	if !strings.Contains(text, "partner@sequoiacap.com") {
		t.Fatalf("the access control did not survive the agent's call: %s", text)
	}

	// Read the room back by id — the path-bound op, addressed through arguments.
	text, isErr = toolsCall(t, app, org, "get_dataroom_datarooms_by_id", `{"id":"`+roomID+`"}`)
	if isErr {
		t.Fatalf("an agent cannot read a room by id over MCP — the In does not receive "+
			"id from the arguments object, so the REST wire survived and this did not: %s", text)
	}
	if !strings.Contains(text, "Acme Series A") {
		t.Fatalf("reading the room over MCP did not return it: %s", text)
	}
	// Without the address the SAME tool must not resolve a room, or "found" above
	// proves nothing.
	if _, isErr := toolsCall(t, app, org, "get_dataroom_datarooms_by_id", `{}`); !isErr {
		t.Fatal("reading a room with no id must not resolve one — the discriminator is dead")
	}

	// And the room is listed.
	text, isErr = toolsCall(t, app, org, "get_dataroom_datarooms", `{}`)
	if isErr || !strings.Contains(text, roomID) {
		t.Fatalf("the agent's room is not in the list it reads back: %s", text)
	}
}

// idFrom pulls {"<key>":{"id":…}} out of a tools/call result payload.
func idFrom(t *testing.T, text, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("tools/call result is not JSON: %v (%s)", err, text)
	}
	inner, _ := m[key].(map[string]any)
	id, _ := inner["id"].(string)
	return id
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

// toolsCall invokes op through zip's MCP endpoint with args as the tools/call
// arguments object — the WHOLE input over this transport — and returns the
// result text and whether MCP reported an error.
func toolsCall(t *testing.T, app *zip.App, org, op, args string) (string, bool) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + op + `","arguments":` + args + `}}`
	rq := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rq.Header.Set("Content-Type", "application/json")
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("tools/call %s: %v", op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	// `error` is decoded beside `result`, and it is load-bearing. A JSON-RPC
	// error frame carries NO `result`, so `Result.IsError` decodes to FALSE —
	// indistinguishable from a success. A caller asking this helper "was that
	// refused?" would read -32602 "no such tool" as permission granted, so a
	// gate would look green because the name it guards does not exist.
	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("tools/call %s: %v (%s)", op, err, raw)
	}
	if env.Error != nil {
		t.Fatalf("tools/call %s: protocol error, so the op never ran and nothing was "+
			"gated: %d %s", op, env.Error.Code, env.Error.Message)
	}
	if len(env.Result.Content) == 0 {
		t.Fatalf("tools/call %s returned no content: %s", op, raw)
	}
	return env.Result.Content[0].Text, env.Result.IsError
}
