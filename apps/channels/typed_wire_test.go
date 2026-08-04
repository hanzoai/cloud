package channels

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// This file is the MEASUREMENT that typing /v1/channels did not move its wire,
// and the CLOSED ledger of the one route that stayed untyped. Before this pass
// all seven of the subsystem's operations published no summary and no
// description — the set that projects to NOTHING: no prose, no MCP tool, no CLI
// command, no typed SDK method.

// untypedByDesign is the closed list of channels operations that are NOT typed
// ops, each with the WIRE FACT that typing it would move.
var untypedByDesign = map[string]string{
	"POST /v1/channels/{channel}/send": "send has TWO independent blockers. (1) A PACKAGE-LOCAL body " +
		"cap: it reads c.Body() and refuses anything over sendMaxBody (1 MiB) with 400 \"body exceeds 1 " +
		"MiB\" (routes.go). A typed op never sees the raw bytes — zip decodes first — and cloud's global " +
		"zip BodyLimit is far larger, so the cap would silently vanish. (2) DisallowUnknownFields: the " +
		"outbound body is the envelope's NARROW projection (C2-6), so identity fields (sender, account, " +
		"channel) are deliberately NOT decodable and a request carrying one is refused LOUDLY with 400 " +
		"rather than having it dropped. zip's decode is jsonenc.Unmarshal with no strictness option, so " +
		"every one of those requests would start succeeding with the field ignored — the silent " +
		"acceptance this route exists to prevent. TestSendKeepsItsCapAndItsStrictness pins both.",
}

// channelOps reads BOTH projections of the LIVE router at their one shared
// address form: what the document says is served, and which of those carry a
// typed registry entry. Reading the router rather than the source is what makes
// this a gate and not prose.
func channelOps(t *testing.T) (served map[string]bool, typed map[string]*openapi.Operation) {
	t.Helper()
	e := newApp(t)
	doc, err := openapi.Spec(e.app, openapi.Info{Title: "channels", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(e.app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/channels") }
	served, typed = map[string]bool{}, map[string]*openapi.Operation{}
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
			typed[key] = op
		}
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when a channels operation is neither a typed
// op nor one named in untypedByDesign. The two ledgers must SUM to the served
// surface, so a stale reason cannot hide behind a route that no longer exists.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := channelOps(t)
	if len(served) != 7 {
		t.Fatalf("channels serves %d operations, not the 7 these ledgers know: %s", len(served), sortedOps(served))
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
			"A route that is not a typed op has no prose, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it (zip.Get/Post/... in routes()), or add it to untypedByDesign with the "+
			"wire fact that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %s, which channels does not serve — a stale reason nobody can re-check", key)
		}
		if _, isTyped := typed[key]; isTyped {
			t.Errorf("untypedByDesign names %s, which IS a typed op — remove the reason", key)
		}
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
}

// TestEveryTypedOpIsDescribed proves the prose actually reached the registry.
// zipdoc lifts a handler's doc comment into zipdoc_gen.go at BUILD time, so a
// package that loses its //go:generate directive keeps compiling perfectly while
// every one of its operations goes back to publishing nothing at all — which is
// the state this whole subsystem was in.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := channelOps(t)
	if len(typed) != 6 {
		t.Fatalf("the registry carries %d operations, want 6", len(typed))
	}
	for key, op := range typed {
		if strings.TrimSpace(op.Description) == "" {
			t.Errorf("%s publishes NO description — the OpenAPI prose and the MCP tool description are both empty", key)
		}
		if strings.TrimSpace(op.Summary) == "" {
			t.Errorf("%s publishes NO summary — the CLI command help and every SDK docstring are empty", key)
		}
		if strings.Contains(op.Summary, "\n") {
			t.Errorf("%s has a line break in its one-line summary: %q", key, op.Summary)
		}
	}
}

// TestTheCollectionRootHasNoTrailingSlash pins failure mode #9: a group's EMPTY
// leaf normalises to "<prefix>/", and op.Path is the identity the document, the
// operationId, the MCP tool name and every generated SDK's URL all key on.
func TestTheCollectionRootHasNoTrailingSlash(t *testing.T) {
	served, _ := channelOps(t)
	for key := range served {
		if strings.HasSuffix(key, "/") {
			t.Errorf("%s publishes a trailing slash for a path this API has never served", key)
		}
	}
}

// TestSendKeepsItsCapAndItsStrictness is the untypedByDesign entry's
// measurement: BOTH facts a typed op would drop, asserted on the live router.
func TestSendKeepsItsCapAndItsStrictness(t *testing.T) {
	e := newApp(t)
	// (1) The package-local 1 MiB cap, which is far below cloud's global zip
	// BodyLimit and is invisible to a typed op.
	big := bytes.Repeat([]byte("x"), sendMaxBody+1)
	body := append(append([]byte(`{"room":{"id":"r1"},"text":"`), big...), []byte(`"}`)...)
	rq := httptest.NewRequest(http.MethodPost, "/v1/channels/telegram/send", bytes.NewReader(body))
	rq.Header.Set("Content-Type", "application/json")
	rq.Header.Set("X-Org-Id", "acme")
	rq.Header.Set("X-User-Id", "u-acme")
	resp, err := e.app.Test(rq)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an over-cap body got %d, want 400 — the package-local cap is gone", resp.StatusCode)
	}
	// (2) DisallowUnknownFields: an identity field the outbound projection does
	// NOT carry is refused loudly, never dropped silently.
	got := req(t, e, http.MethodPost, "/v1/channels/telegram/send", "acme",
		map[string]any{"room": map[string]any{"id": "r1"}, "text": "hi", "sender": "spoofed"})
	if got.Code != http.StatusBadRequest {
		t.Fatalf("a body carrying an identity field got %d, want 400 — the strict decode is gone (%s)", got.Code, got.Body)
	}
}

// TestMutationsRequireOrgAdmin pins the gate cloud.Request exists for in this
// package. Approving a pairing and editing an allowlist decide who may talk to
// the org's bots, so both need org-admin-ness — X-User-IsOrgAdmin, a claim
// principal.OrgFrom does not carry, so an op that could not reach the request
// would have silently dropped the check.
func TestMutationsRequireOrgAdmin(t *testing.T) {
	e := newApp(t)
	if got := req(t, e, http.MethodPost, "/v1/channels/pairing/approve", "acme",
		map[string]any{"channel": "telegram", "code": "X"}); got.Code != http.StatusForbidden {
		t.Errorf("approve as a non-admin got %d, want 403 (%s)", got.Code, got.Body)
	}
	if got := req(t, e, http.MethodPut, "/v1/channels/allowlist", "acme",
		map[string]any{"channel": "telegram", "dmPolicy": "open"}); got.Code != http.StatusForbidden {
		t.Errorf("allowlist PUT as a non-admin got %d, want 403 (%s)", got.Code, got.Body)
	}
	// And an ADMIN still gets through — the gate is not simply always-403.
	if got := reqAdmin(t, e, http.MethodPut, "/v1/channels/allowlist", "acme",
		map[string]any{"channel": "telegram", "dmPolicy": "open"}); got.Code != http.StatusOK {
		t.Errorf("allowlist PUT as an org admin got %d, want 200 (%s)", got.Code, got.Body)
	}
}

// TestAbsentIsNotEmptyOnTheAllowlistPut is the pin on the three-way body
// contract. A null or ABSENT list leaves the stored entries alone; an EMPTY list
// clears them. A pointer-based In would collapse null and absent — encoding/json
// sets a pointer field to nil for an explicit null WITHOUT calling its
// UnmarshalJSON — but the distinction this route needs is nil-vs-empty, which a
// plain slice carries exactly. This asserts it end to end.
func TestAbsentIsNotEmptyOnTheAllowlistPut(t *testing.T) {
	e := newApp(t)
	set := func(body map[string]any) allowlistView {
		t.Helper()
		got := reqAdmin(t, e, http.MethodPut, "/v1/channels/allowlist", "acme", body)
		if got.Code != http.StatusOK {
			t.Fatalf("allowlist PUT got %d (%s)", got.Code, got.Body)
		}
		var v allowlistView
		decodeJSON(t, got.Body, &v)
		return v
	}
	v := set(map[string]any{"channel": "telegram", "dm": []string{"@alice", "@bob"}})
	if len(v.DM) != 2 {
		t.Fatalf("the write did not land: %+v", v.DM)
	}
	// ABSENT: the list is untouched.
	if v = set(map[string]any{"channel": "telegram", "dmPolicy": "allowlist"}); len(v.DM) != 2 {
		t.Fatalf("an ABSENT dm list cleared the entries: %+v", v.DM)
	}
	// NULL: also untouched — encoding/json gives a nil slice for both.
	if v = set(map[string]any{"channel": "telegram", "dm": nil}); len(v.DM) != 2 {
		t.Fatalf("a NULL dm list cleared the entries: %+v", v.DM)
	}
	// EMPTY: cleared.
	if v = set(map[string]any{"channel": "telegram", "dm": []string{}}); len(v.DM) != 0 {
		t.Fatalf("an EMPTY dm list did not clear the entries: %+v", v.DM)
	}
}

// TestTheQueryStringCannotRedirectAWrite is the pin on `url:"-"`. zip's binder
// fills an In field from the QUERY as well as the body, so a converted write
// silently starts accepting `?dmPolicy=open` — which on this route would open an
// org's DMs from a URL. The untyped handler read c.Body(), which is the body and
// nothing else.
func TestTheQueryStringCannotRedirectAWrite(t *testing.T) {
	e := newApp(t)
	got := reqAdmin(t, e, http.MethodPut, "/v1/channels/allowlist?dmPolicy=open&channel=slack", "acme",
		map[string]any{"channel": "telegram", "dmPolicy": "allowlist"})
	if got.Code != http.StatusOK {
		t.Fatalf("allowlist PUT got %d (%s)", got.Code, got.Body)
	}
	var v allowlistView
	decodeJSON(t, got.Body, &v)
	if v.DMPolicy != DMAllowlist {
		t.Fatalf("the query string overrode the body's dmPolicy: %q", v.DMPolicy)
	}
	// And the write landed on telegram, not on the channel the query named.
	got = req(t, e, http.MethodGet, "/v1/channels/allowlist?channel=telegram", "acme", nil)
	decodeJSON(t, got.Body, &v)
	if v.DMPolicy != DMAllowlist {
		t.Fatalf("telegram's policy is %q — the query string redirected the write", v.DMPolicy)
	}
}

// TestInboxStillRefusesANonIntegerCursor is why Since and Limit are STRINGS on
// the In. zip's setScalar silently leaves an unparseable value at the field's
// zero, so an int64 field would turn this 400 into a read from the beginning.
func TestInboxStillRefusesANonIntegerCursor(t *testing.T) {
	e := newApp(t)
	if got := req(t, e, http.MethodGet, "/v1/channels/inbox?since=abc", "acme", nil); got.Code != http.StatusBadRequest {
		t.Errorf("?since=abc got %d, want 400 (%s)", got.Code, got.Body)
	}
	if got := req(t, e, http.MethodGet, "/v1/channels/inbox?limit=abc", "acme", nil); got.Code != http.StatusBadRequest {
		t.Errorf("?limit=abc got %d, want 400 (%s)", got.Code, got.Body)
	}
	if got := req(t, e, http.MethodGet, "/v1/channels/inbox?since=0&limit=10", "acme", nil); got.Code != http.StatusOK {
		t.Errorf("a well-formed cursor got %d, want 200 (%s)", got.Code, got.Body)
	}
}

// TestFailsClosedWithoutAValidatedPrincipal is the tenancy claim: no request
// field can name the tenant, so an anonymous caller reads and writes nothing.
func TestFailsClosedWithoutAValidatedPrincipal(t *testing.T) {
	e := newApp(t)
	for _, r := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/channels", nil},
		{http.MethodGet, "/v1/channels/inbox", nil},
		{http.MethodGet, "/v1/channels/pairing", nil},
		{http.MethodPost, "/v1/channels/pairing/approve", map[string]any{"channel": "telegram", "code": "X"}},
		{http.MethodGet, "/v1/channels/allowlist?channel=telegram", nil},
		{http.MethodPut, "/v1/channels/allowlist", map[string]any{"channel": "telegram"}},
	} {
		if got := req(t, e, r.method, r.path, "", r.body); got.Code != http.StatusForbidden {
			t.Errorf("%s %s anonymous got %d, want 403 (%s)", r.method, r.path, got.Code, got.Body)
		}
	}
}

func sortedOps(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
