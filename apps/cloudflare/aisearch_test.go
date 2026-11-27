package cloudflare

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// mustNamespace is aiSearchNamespace for a test's own org name.
func mustNamespace(t *testing.T, org string) string {
	t.Helper()
	ns, err := aiSearchNamespace(org)
	if err != nil {
		t.Fatalf("aiSearchNamespace(%q): %v", org, err)
	}
	return ns
}

// aiSearchCalls returns the captured requests that reached Cloudflare's AI Search
// API, so a refusal can be shown to have sent it nothing (account discovery is a
// separate call and is not counted).
func aiSearchCalls(rec *capture) []capturedReq {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []capturedReq
	for _, r := range rec.reqs {
		if strings.Contains(r.path, "/ai-search/") {
			out = append(out, r)
		}
	}
	return out
}

// The namespace is a function of the org and nothing else: the same org always gets
// the same name, two orgs never share one, every name satisfies Cloudflare's own rule
// (1–28 of [a-z0-9-], alphanumeric at both ends), and none of them is `default`.
func TestAISearchNamespaceIsDerivedFromTheOrg(t *testing.T) {
	// "Acme" and "acme" are two orgs to the fold (it disambiguates case with a hash
	// of the raw name), so they must be two namespaces here too.
	orgs := []string{"acme", "Acme", "acme_corp", "hanzo", "a", "org-with-a-very-long-name-that-goes-on-and-on", "zoo", "lux"}
	seen := map[string]string{}
	for _, org := range orgs {
		ns := mustNamespace(t, org)
		if again := mustNamespace(t, org); again != ns {
			t.Fatalf("%q derived %q then %q: the namespace must be stable", org, ns, again)
		}
		if !aiSearchNamespaceRE.MatchString(ns) || len(ns) > 28 {
			t.Fatalf("%q derived %q, which Cloudflare refuses as a namespace name", org, ns)
		}
		if ns == "default" || !strings.HasPrefix(ns, aiSearchNamespacePrefix) {
			t.Fatalf("%q derived %q: every derived namespace starts %q", org, ns, aiSearchNamespacePrefix)
		}
		if prev, dup := seen[ns]; dup {
			t.Fatalf("%q and %q share namespace %q", prev, org, ns)
		}
		seen[ns] = org
	}

	// An org the shared fold refuses names no namespace either: no fallback name,
	// and certainly not `default`.
	for _, org := range []string{"", "acme corp", "acme\tcorp"} {
		if ns, err := aiSearchNamespace(org); err == nil {
			t.Fatalf("%q derived %q; an org the fold refuses must name no namespace", org, ns)
		}
	}
}

// The namespace answer needs only a validated org: no Cloudflare call, no token, and
// nothing for a request with no principal.
func TestAISearchNamespaceGetAnswersFromTheOrg(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{}, rec, nil) // no org has connected Cloudflare

	status, body := do(t, app, http.MethodGet, "/v1/cloudflare/ai-search/namespace", "ua", "orga", "")
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var got AISearchNamespace
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	want := AISearchNamespace{Namespace: mustNamespace(t, "orga"), Scope: "org", Org: "orga"}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(rec.reqs) != 0 {
		t.Fatalf("the namespace answer contacted Cloudflare %d time(s); it is derived, not looked up", len(rec.reqs))
	}

	if status, _ := do(t, app, http.MethodGet, "/v1/cloudflare/ai-search/namespace", "", "orga", ""); status != http.StatusUnauthorized {
		t.Fatalf("no principal: status=%d, want 401", status)
	}
}

// Every call that reaches Cloudflare addresses the CALLER'S derived namespace, by
// URL, with the caller's own token — and two orgs land in two namespaces.
func TestAISearchInstancesLiveInTheDerivedNamespace(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A", "orgb": "tok-B"}, rec, nil)
	nsA, nsB := mustNamespace(t, "orga"), mustNamespace(t, "orgb")
	base := func(ns string) string { return "/accounts/" + testAccountID + "/ai-search/namespaces/" + ns }

	cases := []struct {
		method, path, body string
		admin              bool
		upMethod, upPath   string
	}{
		{http.MethodPost, "/v1/cloudflare/ai-search/namespace", `{"description":"support"}`, true,
			http.MethodPost, "/accounts/" + testAccountID + "/ai-search/namespaces"},
		{http.MethodGet, "/v1/cloudflare/ai-search/instances?page=2", "", false,
			http.MethodGet, base(nsA) + "/instances"},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances", `{"id":"docs","type":"r2","source":"docs-bucket","chunk_size":512}`, true,
			http.MethodPost, base(nsA) + "/instances"},
		{http.MethodGet, "/v1/cloudflare/ai-search/instances/docs", "", false,
			http.MethodGet, base(nsA) + "/instances/docs"},
		{http.MethodPut, "/v1/cloudflare/ai-search/instances/docs", `{"max_num_results":10}`, true,
			http.MethodPut, base(nsA) + "/instances/docs"},
		{http.MethodDelete, "/v1/cloudflare/ai-search/instances/docs", "", true,
			http.MethodDelete, base(nsA) + "/instances/docs"},
	}
	for _, c := range cases {
		rec.reqs = nil
		status, body, hdr := doReq(t, app, c.method, c.path, "ua", "orga", c.admin, c.body)
		if status != http.StatusOK {
			t.Fatalf("%s %s: status=%d body=%s", c.method, c.path, status, body)
		}
		calls := aiSearchCalls(rec)
		if len(calls) != 1 {
			t.Fatalf("%s %s: %d AI Search call(s) upstream, want 1", c.method, c.path, len(calls))
		}
		up := calls[0]
		if up.method != c.upMethod || up.path != c.upPath {
			t.Fatalf("%s %s reached %s %s, want %s %s", c.method, c.path, up.method, up.path, c.upMethod, c.upPath)
		}
		if up.auth != "Bearer tok-A" {
			t.Fatalf("%s %s used %q, want orga's own token", c.method, c.path, up.auth)
		}
		if got := hdr.Get(actingOrgHeader); got != "orga" {
			t.Fatalf("%s %s: %s=%q, want orga (the payer)", c.method, c.path, actingOrgHeader, got)
		}
	}

	// The namespace create names the derived namespace, never one the caller chose.
	rec.reqs = nil
	doReq(t, app, http.MethodPost, "/v1/cloudflare/ai-search/namespace", "ua", "orga", true, `{"description":"support"}`)
	var created map[string]string
	if err := json.Unmarshal(aiSearchCalls(rec)[0].body, &created); err != nil {
		t.Fatalf("namespace create body: %v", err)
	}
	if created["name"] != nsA || created["description"] != "support" {
		t.Fatalf("namespace create sent %v, want name=%s description=support", created, nsA)
	}

	// Instance fields the plane does not name still reach Cloudflare.
	rec.reqs = nil
	doReq(t, app, http.MethodPost, "/v1/cloudflare/ai-search/instances", "ua", "orga", true,
		`{"id":"docs","type":"r2","source":"docs-bucket","chunk_size":512}`)
	var sent map[string]any
	if err := json.Unmarshal(aiSearchCalls(rec)[0].body, &sent); err != nil {
		t.Fatalf("instance create body: %v", err)
	}
	if sent["id"] != "docs" || sent["source"] != "docs-bucket" || sent["chunk_size"] != float64(512) {
		t.Fatalf("instance create forwarded %v, want every field the caller sent", sent)
	}

	// Another org's request lands in ITS namespace with ITS token.
	rec.reqs = nil
	if status, body := do(t, app, http.MethodGet, "/v1/cloudflare/ai-search/instances", "ub", "orgb", ""); status != http.StatusOK {
		t.Fatalf("orgb list: status=%d body=%s", status, body)
	}
	calls := aiSearchCalls(rec)
	if len(calls) != 1 || calls[0].path != base(nsB)+"/instances" || calls[0].auth != "Bearer tok-B" {
		t.Fatalf("orgb reached %+v, want its own namespace %s with tok-B", calls, nsB)
	}
	if nsA == nsB {
		t.Fatal("two orgs share an AI Search namespace")
	}
}

// A caller cannot choose the namespace, cannot pick `default`, and cannot move an
// instance: each is a 400 that sends Cloudflare nothing. Repeating the derived name
// is accepted and is not forwarded, since the URL already says it.
func TestAISearchRefusesACallerNamedNamespace(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	ns := mustNamespace(t, "orga")

	refused := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/cloudflare/ai-search/namespace", `{"name":"default"}`},
		{http.MethodPost, "/v1/cloudflare/ai-search/namespace", `{"name":"support"}`},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances", `{"id":"docs","namespace":"default"}`},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances", `{"id":"docs","namespace":"` + mustNamespace(t, "orgb") + `"}`},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances", `{"id":"docs","namespace":7}`},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances", `{"id":"docs","new_namespace":"` + ns + `"}`},
		{http.MethodPut, "/v1/cloudflare/ai-search/instances/docs", `{"namespace":"default"}`},
		{http.MethodPut, "/v1/cloudflare/ai-search/instances/docs", `{"new_namespace":"support"}`},
		{http.MethodPut, "/v1/cloudflare/ai-search/instances/docs", `{"new_namespace":null}`},
		{http.MethodPut, "/v1/cloudflare/ai-search/instances/docs", `["not","an","object"]`},
	}
	for _, c := range refused {
		rec.reqs = nil
		status, body, _ := doReq(t, app, c.method, c.path, "ua", "orga", true, c.body)
		if status != http.StatusBadRequest {
			t.Fatalf("%s %s %s: status=%d, want 400; body=%s", c.method, c.path, c.body, status, body)
		}
		if n := len(rec.reqs); n != 0 {
			t.Fatalf("%s %s %s: Cloudflare contacted %d time(s) for a refused request", c.method, c.path, c.body, n)
		}
	}

	// The refusal names the namespace the org IS in, so the caller can fix the request.
	_, body, _ := doReq(t, app, http.MethodPost, "/v1/cloudflare/ai-search/instances", "ua", "orga", true, `{"id":"docs","namespace":"default"}`)
	if !strings.Contains(body, ns) {
		t.Fatalf("refusal does not name the org's namespace %s: %s", ns, body)
	}

	// Stating the derived name is fine, and it is not forwarded in the body.
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/cloudflare/ai-search/namespace", `{"name":"` + ns + `"}`},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances", `{"id":"docs","namespace":"` + ns + `"}`},
		{http.MethodPut, "/v1/cloudflare/ai-search/instances/docs", `{"namespace":"` + ns + `","max_num_results":5}`},
	} {
		rec.reqs = nil
		status, body, _ := doReq(t, app, c.method, c.path, "ua", "orga", true, c.body)
		if status != http.StatusOK {
			t.Fatalf("%s %s %s: status=%d body=%s", c.method, c.path, c.body, status, body)
		}
		calls := aiSearchCalls(rec)
		if len(calls) != 1 {
			t.Fatalf("%s %s: %d AI Search call(s), want 1", c.method, c.path, len(calls))
		}
		var sent map[string]json.RawMessage
		_ = json.Unmarshal(calls[0].body, &sent)
		if _, ok := sent["namespace"]; ok {
			t.Fatalf("%s %s forwarded a namespace field %s; the URL is the only address", c.method, c.path, calls[0].body)
		}
		if !strings.Contains(calls[0].path, "/ai-search/namespaces") {
			t.Fatalf("%s %s reached %s", c.method, c.path, calls[0].path)
		}
	}
}

// Instance ids are checked against Cloudflare's own rule before they reach a URL.
func TestAISearchInstanceIDValidated(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/cloudflare/ai-search/instances", `{"type":"r2","source":"b"}`},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances", `{"id":"Docs"}`},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances", `{"id":" docs"}`},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances", `{"id":"` + strings.Repeat("a", 65) + `"}`},
		{http.MethodGet, "/v1/cloudflare/ai-search/instances/a.b", ""},
		{http.MethodDelete, "/v1/cloudflare/ai-search/instances/UPPER", ""},
	} {
		rec.reqs = nil
		status, body, _ := doReq(t, app, c.method, c.path, "ua", "orga", true, c.body)
		if status != http.StatusBadRequest {
			t.Fatalf("%s %s %s: status=%d, want 400; body=%s", c.method, c.path, c.body, status, body)
		}
		if n := len(rec.reqs); n != 0 {
			t.Fatalf("%s %s: Cloudflare contacted %d time(s) for an invalid id", c.method, c.path, n)
		}
	}
}

// Every AI Search mutation takes the admin gate; a member is refused before custody
// or Cloudflare is reached. Reads stay open to members.
func TestAISearchMutationsRequireOrgAdmin(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/cloudflare/ai-search/namespace", `{}`},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances", `{"id":"docs"}`},
		{http.MethodPut, "/v1/cloudflare/ai-search/instances/docs", `{"max_num_results":3}`},
		{http.MethodDelete, "/v1/cloudflare/ai-search/instances/docs", ""},
	} {
		rec.reqs = nil
		status, body, _ := doReq(t, app, c.method, c.path, "ua", "orga", false, c.body)
		if status != http.StatusForbidden {
			t.Fatalf("member %s %s: status=%d, want 403; body=%s", c.method, c.path, status, body)
		}
		if n := len(rec.reqs); n != 0 {
			t.Fatalf("member %s %s: Cloudflare contacted %d time(s)", c.method, c.path, n)
		}
	}
	for _, p := range []string{"/v1/cloudflare/ai-search/instances", "/v1/cloudflare/ai-search/instances/docs"} {
		if status, body := do(t, app, http.MethodGet, p, "ua", "orga", ""); status != http.StatusOK {
			t.Fatalf("member GET %s: status=%d body=%s", p, status, body)
		}
	}
}

// The plane serves no move and no query. Cloudflare moves an instance with a PATCH
// carrying `new_namespace`, and answers searches per namespace; neither has a route
// here, so neither can be reached through Hanzo, and a caller-named namespace has no
// URL to ride either.
func TestAISearchServesNoMoveNoQueryNoNamespacePath(t *testing.T) {
	rec := &capture{}
	app := harness(t, map[string]string{"orga": "tok-A"}, rec, nil)
	for _, c := range []struct{ method, path string }{
		{http.MethodPatch, "/v1/cloudflare/ai-search/instances/docs"},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances/docs/search"},
		{http.MethodPost, "/v1/cloudflare/ai-search/instances/docs/chat/completions"},
		{http.MethodPost, "/v1/cloudflare/ai-search/search"},
		{http.MethodGet, "/v1/cloudflare/ai-search/namespaces/default/instances"},
		{http.MethodDelete, "/v1/cloudflare/ai-search/namespace"},
	} {
		rec.reqs = nil
		status, body, _ := doReq(t, app, c.method, c.path, "ua", "orga", true, `{"new_namespace":"x","query":"q"}`)
		if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s: status=%d, want 404/405 (no such route); body=%s", c.method, c.path, status, body)
		}
		if n := len(rec.reqs); n != 0 {
			t.Fatalf("%s %s: Cloudflare contacted %d time(s)", c.method, c.path, n)
		}
	}
}
