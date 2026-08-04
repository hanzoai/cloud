package content

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/openapi"
)

// This file is the MEASUREMENT that typing POST /v1/content/generate did not move its
// wire. It was the last raw handler on the /v1/content surface, and it stayed raw for
// one specific reason: it answers a billing refusal with the FLEET'S money-wire body,
// the nested {"error":{"code","message"}} every Hanzo paywall branches on — not zip's
// flat {status,code,error} error envelope. A typed op that refused with an error would
// have swapped one for the other and broken every one of those clients while the build
// stayed green, which is exactly the class of break a status-code assertion misses.
//
// So the assertions here are on BYTES, not statuses. The two existing 402 tests
// (TestGenerateAssetBillingGate, TestRed_AssetGateDeniedNoStudioNoDebit) both pass
// against either envelope; neither would have caught the swap.

// TestGenerateIsATypedOp is the claim the rest of the file rests on: this route now
// carries a registry entry, which is what the OpenAPI operation, the MCP tool, the CLI
// command and every generated SDK method are projected from. A raw handler publishes an
// address and nothing else.
func TestGenerateIsATypedOp(t *testing.T) {
	app := mountContent(t)
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	op, ok := reg.Ops["POST /v1/content/generate"]
	if !ok {
		t.Fatal("POST /v1/content/generate has no registry entry — it is back to publishing an operationId and nothing else")
	}
	if strings.TrimSpace(op.Description) == "" {
		t.Error("the op publishes NO description — the OpenAPI prose and the MCP tool description are both empty")
	}
	if strings.TrimSpace(op.Summary) == "" {
		t.Error("the op publishes NO summary — the CLI command help and every SDK docstring are empty")
	}

	// The document must carry the request body, which is the whole point: an op with
	// no requestBody is a --data blob in the CLI and an untyped `any` in every SDK.
	doc, err := openapi.Spec(app, openapi.Info{Title: "content", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	item, ok := doc.Paths["/v1/content/generate"]
	if !ok {
		t.Fatal("the document does not serve /v1/content/generate")
	}
	post, ok := item["post"]
	if !ok {
		t.Fatal("the document has no POST on /v1/content/generate")
	}
	if post.RequestBody == nil {
		t.Fatal("the op declares NO requestBody — every one of its twelve fields is invisible to the CLI, the SDKs and the MCP tool schema")
	}
	// Both declared statuses are described, so a generated client knows the 402 is a
	// shape it can read rather than an undocumented surprise. Responses is `any` on
	// the fold, so it is read as the JSON it becomes.
	rb, err := json.Marshal(post.Responses)
	if err != nil {
		t.Fatalf("responses: %v", err)
	}
	var responses map[string]json.RawMessage
	if err := json.Unmarshal(rb, &responses); err != nil {
		t.Fatalf("responses are not an object: %v (%s)", err, rb)
	}
	for _, code := range []string{"201", "402"} {
		if _, ok := responses[code]; !ok {
			t.Errorf("the op does not describe its %s response (has %v)", code, keysOf(responses))
		}
	}
}

// keysOf names what a map actually carries, so a failure says what IS there.
func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestGenerate201IsByteIdentical pins the success body. Typed, the answer is built from
// a struct whose fields are all omitempty, so a field that stopped being set would
// VANISH from the JSON rather than appear empty — a silent narrowing this catches.
func TestGenerate201IsByteIdentical(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	install(t, app, org)
	mounted.State.gen = fakeGenerator{}

	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset, "design": "spring", "kind": "product",
	})
	if code != http.StatusCreated {
		t.Fatalf("generate want 201, got %d (%s)", code, b)
	}

	// Exactly three keys, in the order GenerateResult has always marshalled them.
	var keys []string
	dec := json.NewDecoder(strings.NewReader(string(b)))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		t.Fatalf("answer is not a JSON object: %s", b)
	}
	for dec.More() {
		k, kerr := dec.Token()
		if kerr != nil {
			t.Fatalf("decode: %v", kerr)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if derr := dec.Decode(&skip); derr != nil {
			t.Fatalf("decode: %v", derr)
		}
	}
	if strings.Join(keys, ",") != "doctype,name,status" {
		t.Fatalf("201 answers keys %v, want doctype,name,status — the success wire moved (%s)", keys, b)
	}

	var got GenerateResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("a client decoding into GenerateResult can no longer read the answer: %v (%s)", err, b)
	}
	if got.DocType != DocTypeAsset || got.Name == "" || got.Status != StatusDraft {
		t.Fatalf("201 body %+v is not the draft's identity (%s)", got, b)
	}
}

// TestGenerate402IsTheMoneyWireBody is the one that mattered. An out-of-funds org must
// still receive the fleet's NESTED refusal — {"error":{"code","message"}} — because
// that is what a paywall parses. zip's own error envelope is {"status","code","error"}
// with `error` a STRING, so the two are distinguishable by shape alone and this
// assertion cannot pass against the wrong one.
func TestGenerate402IsTheMoneyWireBody(t *testing.T) {
	const org = "brokebrand"
	bal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/billing/balance") {
			_, _ = w.Write([]byte(`{"user":"` + org + `","available":0}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(bal.Close)
	mc, err := metering.New(metering.Config{BaseURL: bal.URL})
	if err != nil {
		t.Fatalf("metering.New: %v", err)
	}

	app := mountWith(t, cloud.Deps{Metering: mc})
	install(t, app, org)
	code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{
		"doctype": DocTypeAsset, "design": "x", "kind": "product",
	})
	if code != http.StatusPaymentRequired {
		t.Fatalf("out-of-funds render want 402, got %d (%s)", code, b)
	}

	var body map[string]json.RawMessage
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("402 body is not an object: %v (%s)", err, b)
	}
	if len(body) != 1 {
		t.Fatalf("402 answers %d keys, want exactly {\"error\":…} — the money wire gained or lost a field (%s)", len(body), b)
	}
	raw, ok := body["error"]
	if !ok {
		t.Fatalf("402 has no `error` key — this is not the money wire body (%s)", b)
	}
	var nested struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &nested); err != nil {
		t.Fatalf("`error` is not the nested {code,message} object — a client branching on error.code is broken: %v (%s)", err, b)
	}
	if nested.Code != "insufficient_balance" {
		t.Fatalf("error.code = %q, want insufficient_balance — the code a paywall branches on moved (%s)", nested.Code, b)
	}
	if strings.TrimSpace(nested.Message) == "" {
		t.Fatalf("error.message is empty — a 402 that does not say how to clear it is a dead end with a number on it (%s)", b)
	}

	// And the draft was NOT written: the gate runs before the compute and before the
	// CMS row, so a refusal costs the caller nothing and leaves nothing behind.
	if _, hasName := body["name"]; hasName {
		t.Fatalf("the 402 carries a document name — a refused render filed a draft (%s)", b)
	}
}

// TestGenerateStillReadsTheBodyAndOnlyTheBody is the pin on `url:"-"`. zip's binder
// fills an In field from the QUERY as well as the body, so the conversion could have
// silently started accepting `?doctype=` and `?source_media=` — inputs the raw
// handler's c.Bind never took, and a `source_media` reachable from a query string is an
// SSRF surface reachable from a link. This is a wire WIDENING no status code shows.
func TestGenerateStillReadsTheBodyAndOnlyTheBody(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	install(t, app, org)
	mounted.State.gen = fakeGenerator{}

	// No body doctype, only a query one: the route must refuse exactly as it always
	// has, because the query is not an input here.
	code, b := req(t, app, http.MethodPost, "/v1/content/generate?doctype="+DocTypeAsset, org,
		map[string]any{"design": "spring"})
	if code != http.StatusBadRequest {
		t.Fatalf("a doctype supplied ONLY in the query want 400, got %d (%s) — the query string became an input", code, b)
	}

	// And a query value cannot overrule the body's.
	code, b = req(t, app, http.MethodPost, "/v1/content/generate?design=HIJACK&kind=HIJACK", org,
		map[string]any{"doctype": DocTypeAsset, "design": "spring", "kind": "product"})
	if code != http.StatusCreated {
		t.Fatalf("generate want 201, got %d (%s)", code, b)
	}
	if strings.Contains(string(b), "HIJACK") {
		t.Fatalf("a query value reached a body-only field: %s", b)
	}
}

// TestGenerateStillRefusesABodylessCall pins the 400 an empty request answers. zip's
// typed decode is TOLERANT of an absent body — it skips the decode and leaves the In at
// its zero value — where the raw c.Bind refused it outright. The refusal survives
// because the handler's own required-field check is what raises it: no doctype, no
// draft. Same status, on every transport rather than only over HTTP.
func TestGenerateStillRefusesABodylessCall(t *testing.T) {
	app := mountContent(t)
	const org = "acme"
	install(t, app, org)

	if code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, nil); code != http.StatusBadRequest {
		t.Fatalf("a bodyless generate want 400, got %d (%s)", code, b)
	}
	if code, b := req(t, app, http.MethodPost, "/v1/content/generate", org, map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("an empty-object generate want 400, got %d (%s)", code, b)
	}
}

// TestGenerateStillFailsClosedWithoutAPrincipal is the tenancy claim after the
// conversion: the org is read from the validated principal and there is no request
// field that could name one, so an anonymous caller drafts nothing.
func TestGenerateStillFailsClosedWithoutAPrincipal(t *testing.T) {
	app := mountContent(t)
	code, b := call(t, app, http.MethodPost, "/v1/content/generate", "", "",
		map[string]any{"doctype": DocTypeAsset, "design": "x"})
	if code != http.StatusForbidden {
		t.Fatalf("anonymous generate want 403, got %d (%s)", code, b)
	}
}
