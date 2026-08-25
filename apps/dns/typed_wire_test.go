package dns

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// This file makes the DNS head's untyped-ness a MEASURED fact rather than a
// sentence in Mount's comment. The refusal is real (see untypedByDesign below),
// but a refusal nobody can re-check is how a convertible route stays untyped
// forever — and how a reason that has stopped being true keeps being believed.
//
// The cost being pinned is now much smaller than it was. This subsystem used to
// publish five operations at ONE greedy wildcard: /v1/dns/{wildcard1}, an address
// no client can call, which openapi/public.go's audience refuses the customer
// document outright — so the whole DNS product reached no generated SDK, no CLI
// command and no agent tool. It publishes the plane's twelve real addresses now.
// What remains untyped is the RESPONSE, not the address, and that is the fact
// TestATypedOpAlwaysAnswersJSON runs.

// untypedByDesign is the closed set of operations that are NOT typed ops, each
// with the WIRE FACT that typing it would move. Every one of them is a relay
// address, so it is DERIVED from [routes] rather than copied beside it: two
// copies of a route table drift, and the copy is what goes stale.
//
// Keyed by the address form the DOCUMENT uses, rendered by zip.Template — the
// same rule openapi's own translate applies. If the two ever disagreed, every
// key here would name an operation the document does not carry and
// TestEveryRouteIsTypedOrNamed says so, so the derivation checks itself.
var untypedByDesign = func() map[string]string {
	m := make(map[string]string, len(routes))
	for _, r := range routes {
		m[r.method+" "+zip.Template(r.path)] = reasonRelay
	}
	return m
}()

// reasonRelay is the one reason all of them share, because all of them ARE one
// handler: every row of [routes] registers e.forward. The wire fact, re-read
// against the PINNED zip rather than inherited from an older pass:
//
//	a VERBATIM response. forward answers c.Bytes(a.Status, a.Body) under the
//	upstream's own Content-Type and its Location on a 3xx. Two of the three gaps
//	this used to name are closed — WithStatus takes a SET of codes and the answer
//	picks one (StatusCoder), and WithResponseHeader lets an answer carry a
//	declared header (HeaderCoder), which would cover Location. The one that
//	decides it did not move: a typed op's only response path is `c.JSON(out)`
//	(typed.go:567), and fiber's JSON writes `application/json; charset=utf-8`
//	over whatever a header coder set. A relay of a plane that answers zone files
//	and redirects cannot be a route that always claims JSON — and a declared SET
//	of statuses is not a relay of ANY status either.
//
// TWO FACTS STOOD HERE AND HAVE EXPIRED, which is why this file is a test and
// not a comment. The first: zip's Template rewrote only `:name` segments, so its
// registry published a wildcard path verbatim while cloud's router reading named
// the segment {wildcard1}, and Fold then refused the whole document. Template
// names a wildcard {wildcardN} now, which is what lets this file DERIVE its keys
// from the router's own patterns. The second was structural rather than about
// the wire: one All() registration for every method, against per-method typed
// registrars (typed.go:85-107) and no All[In, Out]. That is gone because the
// registration is gone — the plane's addresses are declared per method, so the
// only thing left holding this untyped is the response.
//
// Typing what remains means giving the DNS control plane a typed surface IN THAT
// PLANE, not wrapping it here. See dns.go's package note.
const reasonRelay = "relay. Every address in routes registers e.forward, which answers the DNS " +
	"plane's own status code and Content-Type verbatim. A typed op answers c.JSON(out) " +
	"(typed.go:567), which fiber stamps application/json over. WithStatus and WithResponseHeader " +
	"exist and still do not reach: a relay passes ANY status, not a declared set, and no header " +
	"coder survives c.JSON's content type."

// TestATypedOpAlwaysAnswersJSON runs the fact that decides this head, on the
// same shape it would have to take: whatever an op returns, the answer is
// application/json. A relay that must carry a zone file's own Content-Type
// therefore cannot be one, and the reason is a property of the framework rather
// than a claim about this package.
//
// The day a typed op can answer bytes under the upstream's own type, this goes
// red and says so — which is the whole point of running a refusal instead of
// writing it down. It replaced one that ran a wildcard fact which expired: a
// refusal that cannot fail outlives its cause.
func TestATypedOpAlwaysAnswersJSON(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	g := app.Group("/v1/probe")
	type out struct {
		Zone string `json:"zone"`
	}
	zip.Get(g, "/zone", func(context.Context, *struct{}) (*out, error) {
		return &out{Zone: "example.test. IN A 192.0.2.1"}, nil
	})

	res, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/v1/probe/zone", nil))
	if err != nil {
		t.Fatalf("drive the typed op: %v", err)
	}
	defer res.Body.Close()
	// A refusal answers application/problem+json, which is also not the upstream's
	// type — so the status is checked FIRST and a request that never reached the op
	// is reported as that, rather than as a discovery about what an op can answer.
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the request did not reach the typed op: status %d", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("a typed op answered %q — it can carry a content type of its own now, so the "+
			"reason in reasonRelay has stopped being true and this head may be convertible.", got)
	}
}

// TestNoAddressIsAWildcard holds the thing this subsystem just bought.
//
// A greedy `*` publishes one operation per method at /v1/dns/{wildcard1}: no
// schema, no path a client can spell, and openapi/public.go's audience refuses
// the whole family the customer document ("a `{wildcardN}` address publishes
// whatever grows behind it and names nothing a client can call"). So a single
// convenience registration takes an entire product out of every generated SDK,
// every CLI command group and every agent tool list, silently, and the document
// still looks populated. One route added with a `/*` leaf brings all of that
// back, which is why this is a gate and not a note.
func TestNoAddressIsAWildcard(t *testing.T) {
	doc, err := openapi.Spec(dnsApp(t, "http://127.0.0.1:1"), openapi.Info{Title: "dns", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	var wild []string
	for path := range doc.Paths {
		if strings.HasPrefix(path, prefix) && strings.Contains(path, "{wildcard") {
			wild = append(wild, path)
		}
	}
	if len(wild) > 0 {
		sort.Strings(wild)
		t.Fatalf("published as a wildcard: %s\nThe DNS control plane's addresses are a known, finite "+
			"set (hanzoai/dns, plugin/hanzodns/api.go) and routes declares them. Declare the new one "+
			"there instead of catching it with a `*`.", strings.Join(wild, ", "))
	}
}

// TestTheHeadDeclaresNoRelayBody is the half of this subsystem's cost that CAN
// be closed elsewhere in the fleet and cannot be closed here.
//
// books, company and tools each kept an untyped route and still declared its
// bodies through openapi.Register, so the SDKs stopped offering a call with
// nowhere to put its payload. This head cannot do that, and the reason is not
// effort: it AUTHORS no body. The request bytes are the caller's, relayed
// unread; the response bytes are the DNS plane's, relayed unparsed. The only
// JSON this file writes is its own refusal ({code, message}), which is not the
// operation's response. Declaring a shape for a body we neither read nor produce
// would be inventing a contract on another process's behalf — the one thing the
// whole registry exists to prevent.
//
// So it asserts the SILENCE, and asserts the prose that replaces it: twelve
// addresses that each say what they do, where five wildcard operations could say
// nothing. What would close the rest is a route table or a *zip.App from
// hanzoai/dns, which openapi.Table and openapi.Front already consume
// (openapi/relay.go). That is a module boundary, not a cloud edit.
func TestTheHeadDeclaresNoRelayBody(t *testing.T) {
	doc, err := openapi.Spec(dnsApp(t, "http://127.0.0.1:1"), openapi.Info{Title: "dns", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for key := range untypedByDesign {
		method, path, _ := strings.Cut(key, " ")
		op := doc.Paths[path][strings.ToLower(method)]
		if op == nil {
			t.Fatalf("%s is not in the document", key)
		}
		if op.RequestBody != nil {
			t.Errorf("%s declares a request body. This head never reads one — it relays the caller's "+
				"bytes to the DNS plane unparsed — so a declared shape here is a contract invented on "+
				"another process's behalf. The honest fix is a route table from hanzoai/dns, which "+
				"openapi.Table consumes.", key)
		}
		if strings.TrimSpace(op.Description) == "" {
			t.Errorf("%s carries no description — the one thing this head CAN state about itself", key)
		}
		if strings.TrimSpace(op.Summary) == "" {
			t.Errorf("%s carries no summary — an SDK method and a CLI command are named from it", key)
		}
	}
}

// TestEveryRouteIsTypedOrNamed fails when a dns operation is neither a typed op
// nor a declared relay address, so the next route added here is typed BY
// DEFAULT: a handler cloud itself writes answers its own status and its own
// shape, and nothing about that forbids a typed op. It also fails on a reason
// naming a route dns no longer serves — the half that keeps a refusal honest as
// the code moves under it, and the half that catches [routes] and the router
// disagreeing about how an address is spelled.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := dnsApp(t, "http://127.0.0.1:1")
	doc, err := openapi.Spec(app, openapi.Info{Title: "dns", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served := map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	if len(served) == 0 {
		t.Fatal("the dns head serves nothing at all — the router moved and this gate is now blind")
	}
	typed := map[string]bool{}
	for key := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && strings.HasPrefix(key[i+1:], prefix) {
			typed[key] = true
		}
	}
	var untyped []string
	for key := range served {
		if typed[key] || untypedByDesign[key] != "" {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("untyped and unnamed: %s\nAn untyped route projects to NOTHING — no prose, no MCP tool, "+
			"no CLI command, no typed SDK method. Convert it (zip.Get/Post/... on the group), or add it to "+
			"routes, whose every address is a relay of the DNS plane's own.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %s, which dns does not serve — routes and the router "+
				"disagree about that address, or the route is gone", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %s, which IS a typed op — remove the reason", key)
		}
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
}
