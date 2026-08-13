package captable

// writes_test.go is the wire proof for the three BODY-CARRYING routes typed in
// this pass: PUT /company, PATCH /stakeholders/:id and POST /rounds/:id/close.
//
// The proof does not trust a recorded golden. Each case is sent BOTH ways in the
// same order — through the typed HTTP route on one tenant, and by dispatching the
// same bundle route with the same body straight on ANOTHER tenant's store, which
// is byte for byte what the untyped relay's dispatch() did — and the two answers
// are compared on status, Content-Type and body. Two tenants rather than one
// because closing a round is not idempotent: the second attempt is a 404, so the
// two arms need their own copies of the state.
//
// The cases are chosen to be exactly the ones a NARROWER Go type would have got
// wrong:
//
//   - a JSON number where the bundle reads an optString (`{"incorporationType":5}`)
//     is a 200 that stores "5"; a *string field would have refused it with a 400,
//     which is the route accepting LESS;
//   - a JSON number where the bundle reads a reqString (`{"name":123}`) is the
//     BUNDLE's 400 {success,message,errors}; a *string field would have failed in
//     zip's decoder and answered {status,code,error} instead, losing the `errors`
//     list a client renders;
//   - `null` versus an absent key on a partial update: the bundle clears the
//     column for one and leaves it alone for the other, a distinction a pointer
//     field cannot carry (encoding/json nils a pointer for null WITHOUT calling
//     UnmarshalJSON);
//   - a body that is not an object at all, which the relay handed to the bundle's
//     asObj as `{}`.

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
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// bundleWrite runs a bundle route WITH a body straight on the tenant's store,
// decoding the caller's bytes exactly as the untyped relay's dispatch() did, so
// it re-derives the pre-typing answer on every run.
func bundleWrite(t *testing.T, org, route string, params map[string]string, raw string) (int, []byte) {
	t.Helper()
	var body any
	if raw != "" {
		// The relay refused malformed JSON before the bundle ever saw it, so a
		// case that is not JSON is a bug in the case, not a wire fact.
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatalf("bundleWrite %s: case body is not JSON: %v", route, err)
		}
	}
	resp, err := mounted.State.host.Dispatch(context.Background(), org, goja.BaseRequest{
		Route: route, Params: params, Body: body,
	})
	if err != nil {
		t.Fatalf("bundle dispatch %s: %v", route, err)
	}
	return resp.Status, resp.Body
}

// bothWays sends one raw body through the typed route on typedOrg and through the
// bundle on bundleOrg, and reports the typed answer after proving the two agree.
func bothWays(t *testing.T, app *zip.App, method, path, typedOrg string,
	route string, params map[string]string, bundleOrg, raw string) (int, []byte) {
	t.Helper()
	wantStatus, want := bundleWrite(t, bundleOrg, route, params, raw)
	gotStatus, got, ct := probe(t, app, method, path, typedOrg, json.RawMessage(raw))
	if gotStatus != wantStatus {
		t.Fatalf("%s %s with %s: typed answered %d, the relay answered %d\n typed:  %s\n relay:  %s",
			method, path, raw, gotStatus, wantStatus, got, want)
	}
	if string(got) != string(want) {
		t.Fatalf("%s %s with %s is not byte-identical to the relay\n typed:  %s\n relay:  %s",
			method, path, raw, got, want)
	}
	if ct == "" {
		t.Fatalf("%s %s sent no Content-Type", method, path)
	}
	// A refusal is the bundle's own envelope under the bare application/json the
	// relay sent; a 2xx goes out through zip's typed writer, which adds a charset.
	if gotStatus/100 != 2 && ct != bundleJSON {
		t.Fatalf("%s %s moved the refusal Content-Type: %q, want %q", method, path, ct, bundleJSON)
	}
	return gotStatus, got
}

// TestTypedCompanyUpdateIsByteIdenticalToTheRelay pins PUT /v1/captable/company.
func TestTypedCompanyUpdateIsByteIdenticalToTheRelay(t *testing.T) {
	app := mountApp(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"every field", `{"name":"Renamed Inc","incorporationType":"C_CORP","incorporationCountry":"US","incorporationState":"DE"}`, http.StatusOK},
		{"a number where the bundle reads an optString", `{"name":"Coerced","incorporationType":5}`, http.StatusOK},
		{"a bool where the bundle reads an optString", `{"name":"Coerced","incorporationState":true}`, http.StatusOK},
		{"only the required name, which clears the rest", `{"name":"Only Name"}`, http.StatusOK},
		{"explicit nulls on the optional fields", `{"name":"Nulled","incorporationType":null,"incorporationState":null}`, http.StatusOK},
		{"an unknown key is ignored", `{"name":"Extra","unknownKey":{"deep":1}}`, http.StatusOK},
		{"no name at all", `{}`, http.StatusBadRequest},
		{"an empty name", `{"name":""}`, http.StatusBadRequest},
		{"a number where the bundle reads a reqString", `{"name":123}`, http.StatusBadRequest},
		{"a bool where the bundle reads a reqString", `{"name":true}`, http.StatusBadRequest},
		{"a null name", `{"name":null}`, http.StatusBadRequest},
		{"a body that is not an object", `[1,2]`, http.StatusBadRequest},
		{"a bare scalar body", `"nope"`, http.StatusBadRequest},
		{"a null body", `null`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := bothWays(t, app, http.MethodPut, "/v1/captable/company", "acme",
				"company.update", nil, "globex", c.body)
			if code != c.want {
				t.Fatalf("want %d, got %d (%s)", c.want, code, body)
			}
			// Non-vacuity: a 400 here must be the bundle's validation envelope,
			// which carries the `errors` list zip's own has nowhere to put.
			if code == http.StatusBadRequest && !strings.Contains(string(body), `"errors"`) {
				t.Fatalf("the 400 is not the bundle's validation envelope: %s", body)
			}
		})
	}

	// The coercion actually STORED its value: the schema says `string`, and a
	// number sent for one is stored as its text — which is why a *string field
	// would have been a narrowing, not a description.
	code, body := req(t, app, http.MethodPut, "/v1/captable/company", "acme", json.RawMessage(`{"name":"Final","incorporationType":7}`))
	if code != http.StatusOK {
		t.Fatalf("coercing update: %d (%s)", code, body)
	}
	code, body = req(t, app, http.MethodGet, "/v1/captable/company", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("read back: %d (%s)", code, body)
	}
	var got captableCompany
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode company: %v (%s)", err, body)
	}
	if got.Name != "Final" || got.IncorporationType != "7" {
		t.Fatalf(`want name "Final" and incorporationType "7" (the number, stored as text), got %q / %q`,
			got.Name, got.IncorporationType)
	}
}

// addHolder writes one stakeholder through the UNTYPED relay and returns its id.
func addHolder(t *testing.T, app *zip.App, org, email string) string {
	t.Helper()
	code, body := req(t, app, http.MethodPost, "/v1/captable/stakeholders", org, map[string]any{
		"name": "Ada Lovelace", "email": email,
		"stakeholderType": "INDIVIDUAL", "currentRelationship": "FOUNDER",
		"city": "London",
	})
	if code != http.StatusCreated {
		t.Fatalf("add stakeholder for %s: %d (%s)", org, code, body)
	}
	_, body = req(t, app, http.MethodGet, "/v1/captable/stakeholders", org, nil)
	var rows []struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode stakeholders for %s: %v (%s)", org, err, body)
	}
	for _, r := range rows {
		if r.Email == email {
			return r.ID
		}
	}
	t.Fatalf("stakeholder %s not readable back for %s: %s", email, org, body)
	return ""
}

// TestTypedStakeholderPatchIsByteIdenticalToTheRelay pins
// PATCH /v1/captable/stakeholders/:id.
func TestTypedStakeholderPatchIsByteIdenticalToTheRelay(t *testing.T) {
	app := mountApp(t)
	const email = "ada@example.com"
	acme := addHolder(t, app, "acme", email)
	globex := addHolder(t, app, "globex", email)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"one field", `{"city":"Paris"}`, http.StatusOK},
		{"several fields", `{"name":"Ada L","city":"Lyon","taxId":"999"}`, http.StatusOK},
		{"an explicit null clears a column", `{"city":null}`, http.StatusOK},
		{"a number is stored as sent", `{"zipcode":75001}`, http.StatusOK},
		{"this route does not check the vocabularies", `{"stakeholderType":"BOGUS"}`, http.StatusOK},
		{"this route does not check the email shape", `{"email":"not-an-email"}`, http.StatusOK},
		{"a stray id in the body does not eat the patch", `{"name":"Kept","id":123}`, http.StatusOK},
		{"no updatable field", `{}`, http.StatusBadRequest},
		{"only unknown keys", `{"nope":1}`, http.StatusBadRequest},
		{"a body that is not an object", `[1,2]`, http.StatusBadRequest},
		{"a null body", `null`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, body := bothWays(t, app, http.MethodPatch, "/v1/captable/stakeholders/"+acme, "acme",
				"stakeholders.update", map[string]string{"id": globex}, "globex", c.body)
			if code != c.want {
				t.Fatalf("want %d, got %d (%s)", c.want, code, body)
			}
		})
	}

	// An id this org does not hold is not found — including one that exists in
	// another tenant, which is the cross-tenant arm.
	code, body := req(t, app, http.MethodPatch, "/v1/captable/stakeholders/"+globex, "acme", json.RawMessage(`{"city":"Nice"}`))
	if code != http.StatusNotFound {
		t.Fatalf("patching globex's stakeholder as acme: want 404, got %d (%s)", code, body)
	}

	// No principal, no patch.
	code, body = req(t, app, http.MethodPatch, "/v1/captable/stakeholders/"+acme, "", json.RawMessage(`{"city":"Nice"}`))
	if code != http.StatusForbidden {
		t.Fatalf("patching with no org: want 403, got %d (%s)", code, body)
	}
}

// TestStakeholderPatchDistinguishesNullFromAbsent is the semantic proof of the
// scalar carrier: on a PARTIAL update the bundle keys on `!== undefined`, so an
// absent key must leave the column alone while an explicit null must clear it.
// A pointer field would collapse the two — encoding/json nils a pointer for a
// JSON null without calling UnmarshalJSON — and this is the test that fails if
// anyone makes these fields pointers.
func TestStakeholderPatchDistinguishesNullFromAbsent(t *testing.T) {
	app := mountApp(t)
	id := addHolder(t, app, "acme", "ada@example.com")

	city := func() *string {
		t.Helper()
		_, body := req(t, app, http.MethodGet, "/v1/captable/stakeholders", "acme", nil)
		var rows []captableStakeholder
		if err := json.Unmarshal(body, &rows); err != nil || len(rows) != 1 {
			t.Fatalf("decode stakeholders: %v (%s)", err, body)
		}
		return rows[0].City
	}

	if got := city(); got == nil || *got != "London" {
		t.Fatalf("seeded city should be London, got %v", got)
	}

	// An absent key leaves it alone.
	if code, body := req(t, app, http.MethodPatch, "/v1/captable/stakeholders/"+id, "acme", json.RawMessage(`{"name":"Ada L"}`)); code != http.StatusOK {
		t.Fatalf("patch without city: %d (%s)", code, body)
	}
	if got := city(); got == nil || *got != "London" {
		t.Fatalf("an absent key must leave the column alone, got %v", got)
	}

	// An explicit null clears it.
	if code, body := req(t, app, http.MethodPatch, "/v1/captable/stakeholders/"+id, "acme", json.RawMessage(`{"city":null}`)); code != http.StatusOK {
		t.Fatalf("patch with a null city: %d (%s)", code, body)
	}
	if got := city(); got != nil {
		t.Fatalf("an explicit null must clear the column, got %q", *got)
	}

	// An empty string is its own third answer: stored, not cleared, not ignored.
	if code, body := req(t, app, http.MethodPatch, "/v1/captable/stakeholders/"+id, "acme", json.RawMessage(`{"city":""}`)); code != http.StatusOK {
		t.Fatalf("patch with an empty city: %d (%s)", code, body)
	}
	if got := city(); got == nil || *got != "" {
		t.Fatalf(`an empty string must be stored as "", got %v`, got)
	}
}

// addOpenRound writes one OPEN round through the UNTYPED relay and returns its id.
func addOpenRound(t *testing.T, app *zip.App, org, name string) string {
	t.Helper()
	code, body := req(t, app, http.MethodPost, "/v1/captable/rounds", org, map[string]any{
		"name": name, "roundType": "SAFE", "targetAmount": 500000,
	})
	if code != http.StatusCreated {
		t.Fatalf("create round for %s: %d (%s)", org, code, body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.ID == "" {
		t.Fatalf("decode created round for %s: %v (%s)", org, err, body)
	}
	return created.ID
}

// TestTypedRoundCloseIsByteIdenticalToTheRelay pins
// POST /v1/captable/rounds/:id/close. Each case needs a FRESH open round in both
// tenants, because closing one is not idempotent.
func TestTypedRoundCloseIsByteIdenticalToTheRelay(t *testing.T) {
	app := mountApp(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"an explicit date", `{"closeDate":"2026-06-15"}`, http.StatusOK},
		{"no date at all, which means today", `{}`, http.StatusOK},
		{"an empty date, which also means today", `{"closeDate":""}`, http.StatusOK},
		{"a null date, which also means today", `{"closeDate":null}`, http.StatusOK},
		{"a number where the bundle reads an optDateString", `{"closeDate":20260101}`, http.StatusOK},
		{"an unparsed string is stored as sent", `{"closeDate":"not-a-date"}`, http.StatusOK},
		{"a body that is not an object", `[1,2]`, http.StatusOK},
		{"a null body", `null`, http.StatusOK},
		{"no body at all", ``, http.StatusOK},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			acme := addOpenRound(t, app, "acme", "R-acme")
			globex := addOpenRound(t, app, "globex", "R-globex")
			code, body := bothWays(t, app, http.MethodPost, "/v1/captable/rounds/"+acme+"/close", "acme",
				"rounds.close", map[string]string{"id": globex}, "globex", c.body)
			if code != c.want {
				t.Fatalf("case %d: want %d, got %d (%s)", i, c.want, code, body)
			}
			// Closing the SAME round again is the bundle's 404, both ways.
			code, body = bothWays(t, app, http.MethodPost, "/v1/captable/rounds/"+acme+"/close", "acme",
				"rounds.close", map[string]string{"id": globex}, "globex", c.body)
			if code != http.StatusNotFound {
				t.Fatalf("case %d: re-closing want 404, got %d (%s)", i, code, body)
			}
		})
	}

	// The coerced date was stored as its text, unparsed.
	id := addOpenRound(t, app, "acme", "R-stored")
	if code, body := req(t, app, http.MethodPost, "/v1/captable/rounds/"+id+"/close", "acme", json.RawMessage(`{"closeDate":20260101}`)); code != http.StatusOK {
		t.Fatalf("close with a numeric date: %d (%s)", code, body)
	}
	_, body := req(t, app, http.MethodGet, "/v1/captable/rounds/"+id, "acme", nil)
	var detail captableRoundDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		t.Fatalf("decode round detail: %v (%s)", err, body)
	}
	if detail.Round.CloseDate == nil || *detail.Round.CloseDate != "20260101" {
		t.Fatalf(`want closeDate "20260101" (the number, stored as text), got %v`, detail.Round.CloseDate)
	}
	if detail.Round.Status != "CLOSED" {
		t.Fatalf("want status CLOSED, got %q", detail.Round.Status)
	}
}

// TestTypedWritesKeepTheBodyCap pins the 413 the relay answered for a body over
// maxBody. A typed op never sees the request, so the size is recorded in the
// input's own UnmarshalJSON and read back AFTER the tenant is resolved — which is
// what keeps a 403 ahead of a 413 for a caller that has both problems, the order
// the relay used.
func TestTypedWritesKeepTheBodyCap(t *testing.T) {
	app := mountApp(t)
	id := addHolder(t, app, "acme", "ada@example.com")
	huge := `{"name":"` + strings.Repeat("x", maxBody) + `"}`

	for _, c := range []struct {
		method, path string
	}{
		{http.MethodPut, "/v1/captable/company"},
		{http.MethodPatch, "/v1/captable/stakeholders/" + id},
	} {
		code, body := req(t, app, c.method, c.path, "acme", json.RawMessage(huge))
		if code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s %s with an oversized body: want 413, got %d (%s)", c.method, c.path, code, body)
		}
		// No tenant, and too big: the 403 still comes first.
		code, body = req(t, app, c.method, c.path, "", json.RawMessage(huge))
		if code != http.StatusForbidden {
			t.Fatalf("%s %s oversized with no org: want 403 first, got %d (%s)", c.method, c.path, code, body)
		}
	}
}

// TestScalarCarriesEveryJSONToken pins the carrier itself: what goes in comes out,
// and the zero value means ABSENT rather than a key with an empty value.
func TestScalarCarriesEveryJSONToken(t *testing.T) {
	for _, tok := range []string{`"Acme"`, `123`, `1.5`, `true`, `null`, `""`, `"quote\"inside"`, `"ünïcode"`} {
		var s goja.Scalar
		if err := json.Unmarshal([]byte(tok), &s); err != nil {
			t.Fatalf("scalar cannot hold %s: %v", tok, err)
		}
		b, err := s.MarshalJSON()
		if err != nil {
			t.Fatalf("scalar %s does not marshal: %v", tok, err)
		}
		if string(b) != tok {
			t.Fatalf("scalar changed the token: %s -> %s", tok, b)
		}
	}

	// A value that never came off the wire is the string it spells, so the type
	// is total: every scalar marshals to valid JSON.
	b, err := goja.Scalar("bare words").MarshalJSON()
	if err != nil || string(b) != `"bare words"` {
		t.Fatalf(`hand-built scalar: got %s (%v), want "bare words" quoted`, b, err)
	}

	// The zero value contributes no key at all.
	body, err := goja.Body(map[string]goja.BodyField{
		"absent":  goja.Scalar(""),
		"present": goja.Scalar(`"here"`),
		"nulled":  goja.Scalar(`null`),
	})
	if err != nil {
		t.Fatalf("bundleBody: %v", err)
	}
	obj, ok := body.(map[string]any)
	if !ok {
		t.Fatalf("bundleBody should build an object, got %T", body)
	}
	if _, found := obj["absent"]; found {
		t.Fatalf("a zero scalar must contribute no key: %v", obj)
	}
	if obj["present"] != "here" {
		t.Fatalf(`want present == "here", got %v`, obj["present"])
	}
	v, found := obj["nulled"]
	if !found || v != nil {
		t.Fatalf("an explicit null must survive as a present, nil key: %v (found=%v)", v, found)
	}
}

// newAppMCP mounts captable the way the SERVER does: one cloud.Bridge at the app
// ROOT. zip's own projections of the typed-op registry — the MCP endpoint at /mcp
// and the call plane at /.well-known/zip/op/ — are ordinary routes on the app
// itself, outside the /v1/captable group, so only the root install reaches them.
// cloud.Listen makes that root install in production.
func newAppMCP(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	// zip installs /mcp in prepare(), which Listen would call; a Fiber().Test app
	// never listens. Once-guarded, so calling it here is safe.
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

// toolsCall invokes op through zip's MCP endpoint with args as the tools/call
// arguments object — the WHOLE input over this transport — and returns the result
// text and whether MCP reported an error.
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
	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("tools/call %s: %v (%s)", op, err, raw)
	}
	if len(env.Result.Content) == 0 {
		t.Fatalf("tools/call %s returned no content: %s", op, raw)
	}
	return env.Result.Content[0].Text, env.Result.IsError
}

// toolNamed finds the tool zip derived for one op, by the segments of its path.
func toolNamed(t *testing.T, app *zip.App, want ...string) string {
	t.Helper()
	for _, tool := range app.MCPTools() {
		n, _ := tool["name"].(string)
		if n == "" {
			continue
		}
		all := true
		for _, w := range want {
			if !strings.Contains(n, w) {
				all = false
				break
			}
		}
		if all {
			return n
		}
	}
	t.Fatalf("no derived tool matching %v — the op is not in the registry", want)
	return ""
}

// TestTypedWritesAddressThroughArgumentsAlone pins the rule a typed op must obey:
// it has to be addressable through its In ALONE. Over MCP and the ZAP call plane
// there is no URL — the arguments object IS the whole input, and zip passes it as
// the body with a NIL path map — so an op whose address reaches it only from the
// path is addressable over REST and nowhere else.
//
// This is the failure a REST test cannot see, and these two ops are exactly the
// shape that risks it: a struct In carrying an id field beside a body, decoded by
// an UnmarshalJSON of its own. The id survives because it is a field of the type
// that UnmarshalJSON decodes, so `"id"` in the arguments binds it; over REST
// bindURL then overwrites it from the path, which is the authority there. Were the
// id ever moved out of the decoded set, every REST test above would stay green and
// both tools would answer not-found.
func TestTypedWritesAddressThroughArgumentsAlone(t *testing.T) {
	app := newAppMCP(t)
	holder := addHolder(t, app, "acme", "ada@example.com")
	round := addOpenRound(t, app, "acme", "R-mcp")

	// Named the way zip v1.26.0 settled it: ONE rule, an id derives from the
	// absolute path the occurrence answers at (zip.ID), so these are
	// patch_captable_stakeholders_by_id and post_captable_rounds_by_id_close.
	// The pins below were the pre-v1.26 spelling ("patch_stakeholders",
	// "post_rounds"), which bab535c2 taught the bots/agents projections and missed
	// here. The segments still name the same two ops unambiguously — no other
	// derived tool carries all of them — and what this test proves is unchanged:
	// the op is reachable through its In alone.
	patch := toolNamed(t, app, "captable", "patch", "stakeholders")
	closer := toolNamed(t, app, "captable", "post", "rounds", "close")

	// The stakeholder the arguments name must reach the handler.
	text, isErr := toolsCall(t, app, "acme", patch, `{"id":"`+holder+`","city":"Paris"}`)
	if isErr {
		t.Fatalf("%s cannot address a stakeholder over MCP: its In does not receive id from the "+
			"arguments object, so the REST wire survived and this projection did not (%s)", patch, text)
	}
	if !strings.Contains(text, `"success":true`) {
		t.Fatalf("%s must apply the patch over MCP, got %s", patch, text)
	}
	// Without the address the SAME tool cannot find it — so "found" above discriminates.
	if _, isErr := toolsCall(t, app, "acme", patch, `{"city":"Lyon"}`); !isErr {
		t.Fatalf("%s with no id must not resolve a stakeholder — the discriminator is dead", patch)
	}

	// And the round.
	text, isErr = toolsCall(t, app, "acme", closer, `{"id":"`+round+`","closeDate":"2026-06-15"}`)
	if isErr {
		t.Fatalf("%s cannot address a round over MCP: %s", closer, text)
	}
	if !strings.Contains(text, `"success":true`) {
		t.Fatalf("%s must close the round over MCP, got %s", closer, text)
	}
	if _, isErr := toolsCall(t, app, "acme", closer, `{"closeDate":"2026-06-15"}`); !isErr {
		t.Fatalf("%s with no id must not resolve a round — the discriminator is dead", closer)
	}

	// The body reached the bundle too, not just the address: the patch above set
	// Paris through the arguments object alone.
	_, body := req(t, app, http.MethodGet, "/v1/captable/stakeholders", "acme", nil)
	var rows []captableStakeholder
	if err := json.Unmarshal(body, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("read back stakeholders: %v (%s)", err, body)
	}
	if rows[0].City == nil || *rows[0].City != "Paris" {
		t.Fatalf("the MCP patch must carry its body: want city Paris, got %v", rows[0].City)
	}
}
