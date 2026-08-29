package campaign

// typed_wire_test.go pins the wire the typed conversion had to carry over, none
// of which the store/launch suites measure: the 201 on create, the 204-with-no-
// body on delete, the {"data": …} list envelope, the query binding on the list
// and the metrics window, the body REQUIREMENT on the three writes that bind one,
// and the body TOLERANCE of launch and pause, which have never read one.
//
// It also holds the route oracle and the CLOSED list of the two operations that
// are not typed ops, each with the measured wire fact that keeps it out.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
)

const wireTimeout = 10 * time.Second

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer does, once at the root. In a test
// the test IS the composer, so it owes the same install — skipping it does not
// test a stricter program, it tests one where every org-scoped op answers 403
// for a reason production could never produce.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func mountWire(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Use(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// send issues one request with a VERBATIM body and content type, so the
// assertions about what a route ACCEPTS can send bytes no JSON encoder would.
func send(t *testing.T, app *zip.App, method, path, org, ctype, body string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewReader([]byte(body))
	}
	rq := httptest.NewRequest(method, path, r)
	if ctype != "" {
		rq.Header.Set("Content-Type", ctype)
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org) // a validated principal (principal.Org gate)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func doJSON(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	if body == nil {
		return send(t, app, method, path, org, "", "")
	}
	b, _ := json.Marshal(body)
	return send(t, app, method, path, org, "application/json", string(b))
}

func TestTypedOpsPreserveTheCampaignWire(t *testing.T) {
	resetChannels()
	app := mountWire(t)
	const org = "org_wire"

	code, raw := doJSON(t, app, http.MethodPost, "/v1/campaign", org, map[string]any{
		"name": "Spring", "budget": 1000, "content": []string{"a"},
		"channels": []map[string]any{{"kind": "paid", "platform": "meta"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", code, raw)
	}
	var created map[string]any
	_ = json.Unmarshal(raw, &created)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id: %s", raw)
	}
	if created["status"] != StatusDraft {
		t.Errorf("create status = %v, want draft", created["status"])
	}
	// A create never accepts a caller-asserted channel status.
	if chs, _ := created["channels"].([]any); len(chs) != 1 ||
		chs[0].(map[string]any)["status"] != chanPending {
		t.Errorf("channels = %v, want one pending channel", created["channels"])
	}

	t.Run("the list keeps its {data: …} envelope and binds ?status/?limit", func(t *testing.T) {
		code, raw := send(t, app, http.MethodGet, "/v1/campaign", org, "", "")
		if code != http.StatusOK {
			t.Fatalf("list = %d: %s", code, raw)
		}
		var page map[string]any
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatalf("list is not an object: %v (%s)", err, raw)
		}
		if data, _ := page["data"].([]any); len(data) != 1 {
			t.Fatalf("list data = %v, want one campaign", page["data"])
		}
		// A status filter that matches nothing returns an empty page, not a 400.
		code, raw = send(t, app, http.MethodGet, "/v1/campaign?status=live", org, "", "")
		if code != http.StatusOK {
			t.Fatalf("filtered list = %d: %s", code, raw)
		}
		_ = json.Unmarshal(raw, &page)
		if data, _ := page["data"].([]any); len(data) != 0 {
			t.Errorf("?status=live returned %v, want none", page["data"])
		}
		// An unparseable limit falls back to the default rather than refusing —
		// exactly what limitOf did off the query string.
		if code, _ := send(t, app, http.MethodGet, "/v1/campaign?limit=abc", org, "", ""); code != http.StatusOK {
			t.Errorf("?limit=abc = %d, want 200", code)
		}
	})

	t.Run("the three writes still REQUIRE a JSON body", func(t *testing.T) {
		// zip's typed decode skips an empty body and leaves the In at its zero
		// value; c.Bind refuses it. Measured against the untyped handlers: all of
		// these answered 400 before the conversion, and must still.
		for _, tc := range []struct{ name, method, path, ctype, body string }{
			{"create, no body", http.MethodPost, "/v1/campaign", "", ""},
			{"create, json ctype, no body", http.MethodPost, "/v1/campaign", "application/json", ""},
			{"update, no body", http.MethodPut, "/v1/campaign/" + id, "", ""},
			{"update, text ctype", http.MethodPut, "/v1/campaign/" + id, "text/plain", `{"name":"X"}`},
			{"addChannel, no body", http.MethodPost, "/v1/campaign/" + id + "/channels", "", ""},
		} {
			if code, raw := send(t, app, tc.method, tc.path, org, tc.ctype, tc.body); code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400: %s", tc.name, code, raw)
			}
		}
	})

	t.Run("metrics binds its window off the query string", func(t *testing.T) {
		code, raw := send(t, app, http.MethodGet, "/v1/campaign/"+id+"/metrics?range=7d", org, "", "")
		if code != http.StatusOK {
			t.Fatalf("metrics = %d: %s", code, raw)
		}
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["range"] != "7d" {
			t.Errorf("range = %v, want 7d", m["range"])
		}
		// An unknown range falls back to the 30-day default, never a 400.
		_, raw = send(t, app, http.MethodGet, "/v1/campaign/"+id+"/metrics?range=nope", org, "", "")
		_ = json.Unmarshal(raw, &m)
		if m["range"] != "30d" {
			t.Errorf("unknown range = %v, want the 30d default", m["range"])
		}
	})

	t.Run("removing a channel answers the campaign; deleting it answers 204", func(t *testing.T) {
		code, raw := send(t, app, http.MethodDelete, "/v1/campaign/"+id+"/channels/paid", org, "", "")
		if code != http.StatusOK {
			t.Fatalf("remove channel = %d, want 200: %s", code, raw)
		}
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		if chs, _ := out["channels"].([]any); len(chs) != 0 {
			t.Errorf("channels after removal = %v, want none", out["channels"])
		}
		if code, _ := send(t, app, http.MethodDelete, "/v1/campaign/"+id+"/channels/paid", org, "", ""); code != http.StatusNotFound {
			t.Errorf("removing it twice = %d, want 404", code)
		}

		code, raw = send(t, app, http.MethodDelete, "/v1/campaign/"+id, org, "", "")
		if code != http.StatusNoContent {
			t.Fatalf("delete = %d, want 204", code)
		}
		if len(raw) != 0 {
			t.Errorf("delete body = %q, want empty", raw)
		}
		if code, _ := send(t, app, http.MethodDelete, "/v1/campaign/"+id, org, "", ""); code != http.StatusNotFound {
			t.Errorf("second delete = %d, want 404", code)
		}
	})
}

// TestCampaignTenancyIsNeverACallerField pins the one rule a typed op can
// silently break: the org comes from the VALIDATED principal cloud.Bridge parks,
// never from an In field. An unvalidated caller is refused on every op, and one
// org can never reach another's campaign even by naming its id.
func TestCampaignTenancyIsNeverACallerField(t *testing.T) {
	resetChannels()
	app := mountWire(t)

	code, raw := doJSON(t, app, http.MethodPost, "/v1/campaign", "acme", map[string]any{"name": "Secret"})
	if code != http.StatusCreated {
		t.Fatalf("seed = %d: %s", code, raw)
	}
	var created map[string]any
	_ = json.Unmarshal(raw, &created)
	id := created["id"].(string)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/campaign/" + id},
		{http.MethodPost, "/v1/campaign/" + id + "/launch"},
		{http.MethodPost, "/v1/campaign/" + id + "/pause"},
		{http.MethodGet, "/v1/campaign/" + id + "/metrics"},
		{http.MethodDelete, "/v1/campaign/" + id + "/channels/paid"},
	} {
		if code, _ := send(t, app, tc.method, tc.path, "other", "", ""); code != http.StatusNotFound {
			t.Errorf("cross-org %s %s = %d, want 404", tc.method, tc.path, code)
		}
	}
	// A cross-org delete reports "not found" too — never 204 over someone's row.
	if code, _ := send(t, app, http.MethodDelete, "/v1/campaign/"+id, "other", "", ""); code != http.StatusNotFound {
		t.Errorf("cross-org DELETE = %d, want 404", code)
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/campaign"},
		{http.MethodGet, "/v1/campaign/summary"},
		{http.MethodPost, "/v1/campaign"},
		{http.MethodGet, "/v1/campaign/" + id},
		{http.MethodPut, "/v1/campaign/" + id},
		{http.MethodDelete, "/v1/campaign/" + id},
		{http.MethodPost, "/v1/campaign/" + id + "/launch"},
		{http.MethodPost, "/v1/campaign/" + id + "/pause"},
		{http.MethodGet, "/v1/campaign/" + id + "/metrics"},
		{http.MethodPost, "/v1/campaign/" + id + "/channels"},
		{http.MethodDelete, "/v1/campaign/" + id + "/channels/paid"},
	} {
		if code, _ := send(t, app, tc.method, tc.path, "", "", ""); code != http.StatusForbidden {
			t.Errorf("%s %s with no principal = %d, want 403", tc.method, tc.path, code)
		}
	}
}

// campaignRoutes reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. Reading the router (not the source) is what makes this a
// gate rather than prose — a route added anywhere in routes() shows up here.
func campaignRoutes(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	resetChannels()
	app := mountWire(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "campaign", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	// The health route is Serve's, not this package's.
	ours := func(p string) bool {
		return strings.HasPrefix(p, "/v1/campaign") && p != "/v1/campaign/health"
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op.Description
		}
	}
	return served, typed
}

// TestLaunchAndPauseIgnoreTheirBody is the MEASUREMENT behind the two entries in
// untypedByDesign, kept as an assertion so the refusal stays checkable rather
// than becoming prose. Neither route has ever read a request body; zip's invoke
// refuses one it cannot parse before the handler runs, so typing them as they
// stand would turn each of these 200s into a 400.
func TestLaunchAndPauseIgnoreTheirBody(t *testing.T) {
	resetChannels()
	app := mountWire(t)
	const org = "org_tolerant"

	code, raw := doJSON(t, app, http.MethodPost, "/v1/campaign", org, map[string]any{
		"name": "Spring", "channels": []map[string]any{{"kind": "paid", "platform": "meta"}},
	})
	if code != http.StatusCreated {
		t.Fatalf("seed = %d: %s", code, raw)
	}
	var created map[string]any
	_ = json.Unmarshal(raw, &created)
	id := created["id"].(string)

	for _, path := range []string{"/v1/campaign/" + id + "/launch", "/v1/campaign/" + id + "/pause"} {
		if code, raw := send(t, app, http.MethodPost, path, org, "application/json", `{not json`); code != http.StatusOK {
			t.Errorf("POST %s with an unparseable body = %d, want 200 — the route ignores its body: %s",
				path, code, raw)
		}
	}
}

// untypedByDesign is the CLOSED list of campaign operations that are NOT typed
// ops, each with the measured wire fact that keeps it out. Addresses are written
// the way the DOCUMENT writes them, which is the identity every projection keys
// on.
var untypedByDesign = map[string]string{
	"POST /v1/campaign/{id}/launch": "has never read a request body — the id comes from the URL and " +
		"whatever is posted is ignored — and zip's invoke refuses a body it cannot parse BEFORE the " +
		"handler runs (typed.go:239). An In that tolerates one cannot rescue it either: encoding/json " +
		"validates the whole document before it will call a custom UnmarshalJSON. Measured: 200 untyped, " +
		"400 typed, pinned by TestLaunchAndPauseIgnoreTheirBody. The fix is in zip — invoke should read " +
		"the body only when hasRequestBody is true.",
	"POST /v1/campaign/{id}/pause": "same: a body-less POST that zip cannot declare as body-less.",
}

// TestEveryCampaignRouteIsTypedOrNamed fails when a campaign operation is neither
// a typed op nor one of the two named above — so the next route added here is
// typed by default, and dropping one out of the registry takes a deliberate edit
// with a reason.
func TestEveryCampaignRouteIsTypedOrNamed(t *testing.T) {
	served, typed := campaignRoutes(t)
	if len(served) == 0 {
		t.Fatal("the router serves no campaign routes")
	}
	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and "+
			"no SDK method.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this router does not serve", key)
		}
	}
}

// TestEveryTypedCampaignOpIsDescribed fails when a typed op reaches the document
// with no prose. zipdoc lifts the handler's doc comment into BOTH the OpenAPI
// description and the MCP tool description, so an op whose comment does not lift
// is an SDK method and an agent tool with nothing to read.
func TestEveryTypedCampaignOpIsDescribed(t *testing.T) {
	_, typed := campaignRoutes(t)
	if len(typed) == 0 {
		t.Fatal("no typed campaign ops — the conversion is not wired")
	}
	var bare []string
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			bare = append(bare, key)
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("typed op(s) with no description: %s\n"+
			"Run: go generate -run zipdoc ./apps/campaigns/...", strings.Join(bare, ", "))
	}
}

// proseless is the CLOSED list of published properties that carry NO description
// because the CLIENT they arrived through cannot carry one — not because nobody wrote
// it.
//
// EMBEDDED STRUCT. campaignUpdate is `{ID string; campaignWrite}` (typed.go), and the
// schema fold FLATTENS that embedding, so the six writable properties reach the
// document on campaignUpdate itself. zipdoc files a field's prose under the type that
// DECLARES it, which is campaignWrite — the six comments exist, are lifted, and are
// published one component over as campaignWrite.name, campaignWrite.budget and the
// rest. The lookup for campaignUpdate.name simply has nowhere to find them.
//
// The two workarounds available both trade one true statement for two that drift.
// Unrolling the embedding into six copies duplicates the description of what a caller
// may write, and the copies part company the first time a rule changes. Hand-writing
// campaignUpdate's schema beside the struct does the same to the SHAPE. Both also
// change the published document to dodge a missing description, which is a wire
// decision taken for a prose reason. So the comments stay on campaignWrite, where the
// fields are, and this names the gap until the fold learns to walk an embedding.
var proseless = map[string]bool{
	"campaignUpdate.name":       true,
	"campaignUpdate.audience":   true,
	"campaignUpdate.content":    true,
	"campaignUpdate.channels":   true,
	"campaignUpdate.scheduleAt": true,
	"campaignUpdate.budget":     true,
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the two gates
// above cannot see. They prove every route has an ADDRESS and a typed SHAPE; the
// shape's FIELDS come from a different place — a doc comment on each one, which
// zipdoc lifts one at a time.
//
// It matters here because this surface reports money in TWO units at once and the
// names do not say which is which: budget and spendCents are CENTS, while revenue,
// cac and roas are whole dollars, and ctr and cvr are fractions rather than
// percentages (0.0123 is 1.23%). `status` is likewise two closed vocabularies — the
// campaign's draft|live|paused|failed and each channel's
// pending|live|paused|failed|unavailable — and a live CAMPAIGN means at least one
// channel launched, not all of them, so the channel rows are where the truth is. And
// scheduleAt is a unix time handed to each executor: nothing in this service wakes up
// to launch a campaign for you.
//
// Presence is all a gate can check. A description restating the field's name is worse
// than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	resetChannels()
	doc, err := openapi.Spec(mountWire(t), openapi.Info{Title: "campaign", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("campaign publishes no schemas at all — the gate would pass vacuously")
	}
	published, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}

	var bare, stale []string
	seen := map[string]bool{}
	for _, path := range published {
		seen[path] = true
		if !proseless[path] {
			bare = append(bare, path)
		}
	}
	for path := range proseless {
		if !seen[path] {
			stale = append(stale, path)
		}
	}

	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/campaigns describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
