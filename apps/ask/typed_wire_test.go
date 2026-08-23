package ask

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// askApp mounts the endpoint with a stand-in books peer and a stubbed model — the
// same harness the behaviour suite uses, which is the point: one harness, so a
// wire proof and a behaviour proof are made against the same endpoint.
func askApp(t *testing.T) *zip.App {
	t.Helper()
	noNetworkSearch(t)
	return newAskApp(t, &webAI{answer: "Clojure was created by Rich Hickey."},
		byOrg{"acme": money("$4,200")}, nil, nil)
}

func askRaw(t *testing.T, app *zip.App, body string, hdr map[string]string) *http.Response {
	t.Helper()
	rq := httptest.NewRequest(http.MethodPost, "/v1/ask", strings.NewReader(body))
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("X-Org-Id", "acme")
	rq.Header.Set("X-User-Id", "u-acme")
	for k, v := range hdr {
		rq.Header.Set(k, v)
	}
	resp, err := app.Test(rq)
	if err != nil {
		t.Fatalf("POST /v1/ask: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestAskRefusalIsTheWire measures the facts that keep POST /v1/ask an untyped
// handler. They are named at the registration in ask.go; this is what makes that
// prose a MEASUREMENT rather than a claim, so the day zip can express them the
// conversion is a test edit away instead of a re-derivation.
func TestAskRefusalIsTheWire(t *testing.T) {
	app := askApp(t)

	// (1) ONE ROUTE, TWO SUCCESS SHAPES. A typed op declares exactly one Out.
	advisor := askRaw(t, app, `{"question":"what is my MRR?"}`, nil)
	web := askRaw(t, app, `{"q":"who created clojure","mode":"search"}`, nil)
	advKeys, webKeys := keysOfBody(t, advisor), keysOfBody(t, web)
	if strings.Join(advKeys, ",") == strings.Join(webKeys, ",") {
		t.Fatalf("both branches answer %v — if the two success shapes have converged, "+
			"this route is typable and the refusal in ask.go is stale", advKeys)
	}
	wantAdvisor := []string{"answer", "domain", "figures", "followups", "sources"}
	wantWeb := []string{"answer", "domain", "figures", "follow_ups", "followups", "mode", "model", "sources"}
	sort.Strings(wantAdvisor)
	sort.Strings(wantWeb)
	if strings.Join(advKeys, ",") != strings.Join(wantAdvisor, ",") {
		t.Errorf("advisor answer keys = %v, want %v", advKeys, wantAdvisor)
	}
	if strings.Join(webKeys, ",") != strings.Join(wantWeb, ",") {
		t.Errorf("web answer keys = %v, want %v", webKeys, wantWeb)
	}

	// (2) THE WEB BRANCH STREAMS. An SSE answer is not a JSON value, and zip's
	// typed path has no Out that means "I already streamed".
	sse := askRaw(t, app, `{"q":"who created clojure","mode":"search"}`,
		map[string]string{"Accept": "text/event-stream"})
	if ct := sse.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Accept: text/event-stream → Content-Type %q, want text/event-stream", ct)
	}
}

// TestAskDeclaresItsRequestAndNotItsResponse holds the other half of the refusal
// to account. Staying untyped costs the prose, the MCP tool and the CLI command;
// it must not also cost a document that says this route takes no body. The
// RESPONSE stays undeclared on purpose — the test above is why: one declared shape
// would be a false statement about the other branch.
func TestAskDeclaresItsRequestAndNotItsResponse(t *testing.T) {
	doc, err := openapi.Spec(askApp(t), openapi.Info{Title: "ask", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	op := doc.Paths["/v1/ask"]["post"]
	if op == nil {
		t.Fatal("the document does not carry POST /v1/ask")
	}
	rb, ok := op.RequestBody.(*openapi.RequestBody)
	if !ok {
		t.Fatalf("request body is %T, want the declared *openapi.RequestBody — without it "+
			"every generated SDK offers an ask with nowhere to put the question", op.RequestBody)
	}
	if _, jsonBody := rb.Content["application/json"]; !jsonBody {
		t.Errorf("request body content = %v, want application/json", rb.Content)
	}
	if op.Responses != nil {
		t.Errorf("responses = %#v, want none: the success body is polymorphic and a single "+
			"declared shape would be false about the branch it does not describe", op.Responses)
	}
}

func keysOfBody(t *testing.T, resp *http.Response) []string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", resp.StatusCode, b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// proseless is the CLOSED list of published properties carrying NO description
// because the CLIENT they arrive through cannot carry one — not because nobody wrote
// it. Every field named here HAS its doc comment in the source; the generator
// cannot reach it from where the schema is built.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day the
// generator learns, and the ledger must shrink then rather than outlive the gap.
var proseless = map[string]bool{
	// REFLECTION CLIENT. POST /v1/ask cannot be a typed op — TestAskRefusalIsTheWire
	// measures the three wire facts that keep it untyped — so its body reaches the
	// document through openapi.Register (ask.go's init) instead. Register derives a
	// schema by REFLECTION, and Go drops comments at compile time, so zipdoc, which
	// walks zip's TYPED registrations, can never reach a type that arrives this way.
	// These eleven carry their doc comments on askRequest; reflection cannot see one.
	// The op's own prose is declared beside the wire fact (openapi.Describe), which
	// is the client for exactly the operations the wire refuses to type; there is no
	// matching client for a field.
	"askRequest.question":   true,
	"askRequest.q":          true,
	"askRequest.mode":       true,
	"askRequest.sources":    true,
	"askRequest.model":      true,
	"askRequest.stream":     true,
	"askRequest.language":   true,
	"askRequest.maxSources": true,
	"askRequest.maxQueries": true,
	"askRequest.followUps":  true,
	"askRequest.system":     true,
}

// TestEveryPublishedFieldIsDescribed closes the half of the surface the gates
// above cannot see. They prove this route's ADDRESS and the SHAPE it declares
// reach the document; neither says anything about whether that shape's FIELDS mean
// anything to a reader, and those come from a different place — a doc comment on
// each field, which zipdoc lifts one at a time.
//
// It matters here because these fields bound what an answer COSTS and how long it
// takes, and every one of them is asymmetric in a way the name hides. `maxSources`
// and `maxQueries` can only LOWER the mode's budget — a caller can buy a cheaper
// answer, never a bigger one — and 0 means the mode's own rather than none.
// `mode` is the fork between the web engine and the org-figure advisor, and an
// unrecognised value is not an error: it silently takes the advisor. `model`
// REPLACES the fallback chain rather than heading it, so naming one that is down
// fails the answer instead of degrading to the next. On the way back, a source's
// `snippet` is what a client renders while the model reads the whole fetched page,
// which never rides the wire at all.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(askApp(t), openapi.Info{Title: "ask", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("ask publishes no schemas at all — the gate would pass vacuously")
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
			"the first of them alone — then run: make -C apps/ask describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}

// untypedByDesign is the CLOSED list of addresses this app serves raw, each with
// the wire fact that keeps it there.
//
// The package already MEASURES each of these — the tests above drive the wires
// themselves, which is the stronger form and stays. What this list adds is the
// SUM: those tests go red when a refused route's WIRE changes, and nothing went
// red when a route was ADDED raw beside them. openapi/untyped.json catches that
// fleet-wide by count, and a count is flat when one route converts and another
// arrives raw in the same change, which is exactly what this catches.
var untypedByDesign = map[string]string{
	"POST /v1/ask": "three independent facts, each measured by TestAskRefusalIsTheWire above. ONE " +
		"address answers TWO success shapes — the advisor's five-key answer and the web engine's " +
		"eight-key one — and an op declares one Out. It STREAMS SSE on one branch (c.SendStreamWriter), " +
		"and there is no Out meaning 'I already streamed'. And a balance denial answers " +
		"cloud.DenyResource's BARE nested {\"error\":{code,message}}, which Detail could now carry but " +
		"would wrap in a problem-details envelope — a money-path wire change, not a mechanical one.",
}

// TestEveryRouteIsTypedOrNamed requires the two ledgers to SUM to the served
// surface, so a route added raw goes red without anyone remembering this file, and
// a reason that stops being true goes red the moment its op is written.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := askApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "ask", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/ask") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/ask") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if !typed[key] {
			if _, named := untypedByDesign[key]; !named {
				untyped = append(untyped, key)
			}
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK method. "+
			"Convert it, or name it in untypedByDesign with the wire fact that keeps it raw.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this app no longer serves", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("the two ledgers must sum to the served surface: typed %d + named %d = %d, served %d",
			len(typed), len(untypedByDesign), got, want)
	}
}
