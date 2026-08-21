package dns

import (
	"context"
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
// registration: `app.Group("/v1/dns").All("/*", e.forward)` (dns.go). Three wire
// facts each independently forbid a typed op, all three re-verified against the
// PINNED zip (v1.31.0) rather than inherited from an older pass:
//
//   - ONE registration, EVERY method. zip's typed registrars are per-method —
//     Get, Post, Put, Patch, Delete (typed.go:85-107) — and there is no
//     All[In, Out]; five ops would each have to declare a body this relay never
//     parses.
//   - a GREEDY wildcard, and the two readings SPELL IT DIFFERENTLY. zip's own
//     Template (address.go:61) rewrites only `:name` segments, so its registry
//     would publish the path verbatim as `/v1/dns/*`, while cloud's router
//     reading names the segment `{wildcard1}` (openapi/openapi.go:795-811). Fold
//     looks the typed op up by zip's spelling, finds no live route, and REFUSES
//     the whole document (openapi/openapi.go:687). That is stronger than the
//     older statement here — it is not that a bound field and a published
//     parameter disagree, it is that there is no document at all.
//     TestTheDoorCannotBeATypedOp runs it.
//   - a VERBATIM response. forward answers c.Bytes(res.StatusCode, out) with the
//     upstream's own Content-Type and its Location on a 3xx. zip v1.31.0 closed
//     two of the three gaps this used to name — WithStatus takes a SET of codes
//     and the answer picks one (StatusCoder, typed.go:174-208), and
//     WithResponseHeader lets an answer carry a declared header (HeaderCoder,
//     typed.go:230-260), which would cover Location. The third did not move and
//     is the one that decides it: a typed op's only response path is
//     `c.JSON(out)` (typed.go:558), and fiber's JSON writes
//     `application/json; charset=utf-8` over whatever a header coder set
//     (fiber v3 res.go:501-514). A relay of a plane that answers zone files and
//     redirects cannot be a route that always claims JSON — and a declared SET
//     of statuses is not a relay of ANY status either.
//
// Typing this means giving the DNS control plane a typed surface IN THAT PLANE,
// not wrapping it here. See dns.go's package note for the module that owes it.
const reasonForward = "forward. One All() registration for every method, over a greedy wildcard, " +
	"relaying the DNS plane's own status code and Content-Type verbatim. zip v1.31.0 has no " +
	"All[In, Out] (typed.go:85-107); its Template leaves a wildcard verbatim while cloud's reading " +
	"names it {wildcard1}, so the fold refuses the document outright (openapi/openapi.go:687); and a " +
	"typed op answers c.JSON(out) (typed.go:558), which fiber stamps application/json over " +
	"(res.go:501-514). WithStatus and WithResponseHeader now exist and still do not reach: a relay " +
	"passes ANY status, not a declared set, and no header coder survives c.JSON's content type."

// TestTheDoorCannotBeATypedOp is the middle fact above, RUN rather than
// asserted. It registers a typed op on the same greedy wildcard this head serves
// and watches openapi.Spec refuse to produce a document — which is why the
// refusal is a refusal and not a backlog item.
//
// A test that PROVES a refusal is what stops one outliving its cause: the day
// zip's Template names a wildcard the way cloud's reading does, this goes red
// and says so.
func TestTheDoorCannotBeATypedOp(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	g := app.Group("/v1/probe")
	zip.Get(g, "/*", func(context.Context, *struct{}) (*struct{}, error) { return nil, nil })
	if _, err := openapi.Spec(app, openapi.Info{Title: "probe", Version: "v1"}); err == nil {
		t.Fatal("openapi.Spec accepted a typed op on a greedy wildcard — zip and cloud now agree " +
			"about how to name that segment, so the second fact in reasonForward has stopped being " +
			"true. Re-read the other two before deleting anything.")
	} else if !strings.Contains(err.Error(), "no live route") {
		t.Fatalf("refused for a different reason than the one recorded: %v", err)
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
