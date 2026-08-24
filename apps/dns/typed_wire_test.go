package dns

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
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
// The consequence being pinned is worth stating plainly: /v1/dns publishes five
// operations at one greedy wildcard, and NOT ONE of them carries a description, a
// summary, an MCP tool or a CLI command. This whole subsystem projects to nothing
// a caller can read. That is a deliberate cost, and this file is where it stops
// being deliberate the moment someone adds a route that did not have to pay it.

// untypedByDesign is the closed list of operations that are NOT typed ops, each
// with the WIRE FACT that typing it would move. Keyed by the address form the
// document uses, so a stale entry names something and can be checked.
var untypedByDesign = map[string]string{
	"DELETE /v1/dns/{wildcard1}": reasonForward,
	"GET /v1/dns/{wildcard1}":    reasonForward,
	"PATCH /v1/dns/{wildcard1}":  reasonForward,
	"POST /v1/dns/{wildcard1}":   reasonForward,
	"PUT /v1/dns/{wildcard1}":    reasonForward,
}

// reasonForward is the one reason all five share, because all five ARE one
// registration: `app.Group("/v1/dns").All("/*", e.forward)` (dns.go). Two wire
// facts each independently forbid a typed op, both re-read against the PINNED
// zip rather than inherited from an older pass:
//
//   - ONE registration, EVERY method. zip's typed registrars are per-method —
//     Get, Post, Put, Patch, Delete (typed.go:85-107) — and there is no
//     All[In, Out]; five ops would each have to declare a body this relay never
//     parses.
//   - a VERBATIM response. forward answers c.Bytes(res.StatusCode, out) with the
//     upstream's own Content-Type and its Location on a 3xx. Two of the three
//     gaps this used to name are closed — WithStatus takes a SET of codes and the
//     answer picks one (StatusCoder), and WithResponseHeader lets an answer carry
//     a declared header (HeaderCoder), which would cover Location. The one that
//     decides it did not move: a typed op's only response path is `c.JSON(out)`
//     (typed.go:567), and fiber's JSON writes `application/json; charset=utf-8`
//     over whatever a header coder set. A relay of a plane that answers zone
//     files and redirects cannot be a route that always claims JSON — and a
//     declared SET of statuses is not a relay of ANY status either.
//     TestATypedOpAlwaysAnswersJSON runs it.
//
// A THIRD fact stood here and has expired, which is why this file is a test and
// not a comment. It read: zip's Template rewrites only `:name` segments, so its
// registry publishes a wildcard path verbatim while cloud's router reading names
// the segment {wildcard1}, and Fold then refuses the whole document. zip's
// document builder asks Template for every path now, and Template names a
// wildcard {wildcardN} the way ID always did, so the two readings agree and a
// typed op on a wildcard produces a document. It was never the fact that decided
// this head; the two above are.
//
// Typing this means giving the DNS control plane a typed surface IN THAT PLANE,
// not wrapping it here. See dns.go's package note for the module that owes it.
const reasonForward = "forward. One All() registration for every method, relaying the DNS plane's " +
	"own status code and Content-Type verbatim. zip has no All[In, Out] (typed.go:85-107), so five " +
	"ops would each declare a body this relay never parses; and a typed op answers c.JSON(out) " +
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
// writing it down. It replaced one that ran the wildcard fact above, which
// expired: a refusal that cannot fail outlives its cause.
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
			"second fact in reasonForward has stopped being true and this head may be convertible. "+
			"Re-read the other one before deleting anything.", got)
	}
}

// TestTheWildcardNoLongerRefusesTheDocument records what CHANGED, so the expired
// fact above cannot quietly come back. A typed op on a greedy wildcard produces a
// document; if this ever refuses again, the two readings have diverged and every
// wildcard-addressed op in the fleet is unpublishable.
func TestTheWildcardNoLongerRefusesTheDocument(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	g := app.Group("/v1/probe")
	zip.Get(g, "/*", func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })

	doc, err := openapi.Spec(app, openapi.Info{Title: "probe", Version: "v1"})
	if err != nil {
		t.Fatalf("a typed op on a wildcard refused the document again: %v", err)
	}
	if _, ok := doc.Paths["/v1/probe/{wildcard1}"]; !ok {
		t.Fatalf("the document does not carry the wildcard at its published name; it carries %v",
			slices.Sorted(maps.Keys(doc.Paths)))
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
// So the gate asserts the SILENCE, and names what would end it: a route table or
// a *zip.App from hanzoai/dns, which openapi.Table and openapi.Front already
// consume (openapi/relay.go). That is a module boundary, not a cloud edit.
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
	}
}

// TestEveryRouteIsTypedOrNamed fails when a dns operation is neither a typed op
// nor named above, so the next route added here is typed BY DEFAULT. It also
// fails on a stale reason naming a route dns no longer serves — the half that
// keeps a refusal honest as the code moves under it.
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
		if !strings.HasPrefix(path, "/v1/dns") {
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
		if i := strings.Index(key, " "); i > 0 && strings.HasPrefix(key[i+1:], "/v1/dns") {
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
			"untypedByDesign with the WIRE FACT that typing it would move.", strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %s, which dns does not serve — a stale reason nobody can re-check", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %s, which IS a typed op — remove the reason", key)
		}
	}
	if len(typed)+len(untypedByDesign) != len(served) {
		t.Errorf("%d typed + %d named != %d served", len(typed), len(untypedByDesign), len(served))
	}
}
