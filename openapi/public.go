package openapi

// THE WHITELIST. One document, two projections, and the split is DECLARED.
//
// api.hanzo.ai answers 1782 paths across 185 products. EIGHTEEN of them are the
// model API; the rest are admin consoles, commerce, observability, the build
// plane, and the app builder's CRUD. All of it is real and all of it is served —
// and almost none of it is a thing a customer should find in an SDK.
//
// So the fleet document is projected twice:
//
//	INTERNAL  everything, unchanged — openapi.yaml, what we generate our own
//	          clients from, admin included.
//	PUBLIC    only what an operation DECLARED about itself — public.yaml, what
//	          the published SDKs, the CLI, the MCP tool list and docs.hanzo.ai
//	          are generated from.
//
// # Default-DENY, and that is the whole design
//
// An operation that says nothing is NOT public. Not "public unless it matches a
// pattern", not "public unless a prefix says otherwise" — silent means absent.
// A product launched next month leaks by no act of omission, because omission is
// the safe answer; it ships publicly only when somebody writes the line.
//
// The alternative was a denylist — /v1/admin and friends enumerated in the
// emitter — and it is refused for a measured reason, twice over in one day:
//
//   - a case-sensitive path list let `/V1/EXEC` walk straight past a credential
//     guard, because the list and the router disagreed about what a path IS.
//   - a manifest prefix row disagreed with what an app actually served, and
//     /v1/tags 404'd in production for an operation the document published.
//
// Both are the same defect: a second copy of the routing table, living somewhere
// other than the routes, free to drift. A prefix list in this file would be a
// third. There is none here — [Publish] reads a per-operation fact and knows
// nothing about paths, products, prefixes or case.
//
// # Marked where the operation is declared, the way prose already is
//
// [Register] declares an operation's BODIES. [Describe] declares its PROSE.
// [Public] declares its AUDIENCE. Same seam, same law: a declaration renders
// only on an operation the document already carries, so this registry cannot
// invent an address — it can only say something about one that exists.
//
// # Why its own registry, keyed differently from Register and Describe
//
// Register and Describe are consulted by [From], against the FIBER PATTERN the
// route was registered under (`/v1/videos/:id`) — the only name the router
// knows. This is consulted at the END of [Spec], against the finished document's
// TEMPLATED address (`/v1/videos/{id}`), and it has to be: the largest public
// product does not exist in this process's router at all. hanzoai/ai registers
// one `All("/v1/*")` and reaches its whole surface through it, so /v1/models has
// no fiber pattern here to key on — only an address in the document the door
// hands over (openapi/relay.go).
//
// Two key spaces, then, because there are honestly two. [translate] is what keeps
// them from becoming two conventions: a declaration is normalized through the
// same function the document's own addresses are built with, so `/v1/videos/:id`
// and `/v1/videos/{id}` are one key and neither spelling can be wrong.
//
// # What holds it honest
//
// public.yaml is COMMITTED and regenerated from source by the drift gate
// (mk/fleet.mk check). Adding an operation to the public contract is therefore a
// two-line diff — the declaration, and the operation appearing in the document —
// and the two are reviewed together. A typo in a declaration renders nothing,
// which shows up as the operation missing from the document diff. That is the
// same property openapi/floor.go buys for the internal surface by counting,
// bought here exactly, because a whitelist is small enough to compare whole.

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// refPrefix is how a JSON Schema names a component in this document.
const refPrefix = "#/components/schemas/"

var (
	publicMu  sync.Mutex
	publicOps = map[opKey]bool{}
)

// Public declares one operation part of the PUBLIC contract, keyed by its
// address in the document and its method. Called from the owning subsystem's
// init, next to the route table or the door that serves it.
//
// ONE operation per call, never a path and never a prefix. A method is not a
// detail to be defaulted: GET /v1/models is the model catalog and a POST there
// would be something else entirely, so a declaration that covered "every method
// at this address" would publish tomorrow's write endpoint on the strength of
// today's read. That is the leak default-deny exists to make impossible, and a
// wildcard is refused for the same reason — a `*` in a whitelist is a prefix
// list wearing a declaration's clothes.
//
// A duplicate declaration is a programming error at init time and panics, as
// [Register] and [Describe] do: two claims on one operation is two answers to
// one question.
func Public(path, method string) {
	if strings.ContainsAny(path, "*+") {
		panic(fmt.Sprintf("openapi: Public(%q) — a whitelist names operations, never a wildcard; "+
			"a pattern here would publish whatever grows behind it", path))
	}
	m := strings.ToUpper(method)
	if !methods[m] {
		panic(fmt.Sprintf("openapi: Public(%q, %q) — this generator publishes %v and nothing else, "+
			"so a declaration for any other method marks an operation no document carries", path, method, Methods()))
	}
	at, _ := translate(path)
	key := opKey{method: m, path: at}
	publicMu.Lock()
	defer publicMu.Unlock()
	if publicOps[key] {
		panic(fmt.Sprintf("openapi: duplicate Public for %s %s", key.method, key.path))
	}
	publicOps[key] = true
}

// stamp records the audience on every operation that declared one.
//
// It runs ONCE, at the end of [Spec], and that is the only place it can run.
// Earlier is too early: [Fold] replaces a structural operation wholesale with the
// typed one, and [Project] replaces a door with the registry behind it, so
// anything written before either would be discarded by it. Later is too late:
// the subsets are already on disk and the weave is a pure function of them.
func stamp(d *Document) {
	publicMu.Lock()
	defer publicMu.Unlock()
	if len(publicOps) == 0 {
		return
	}
	for path, item := range d.Paths {
		for method, op := range item {
			if publicOps[opKey{method: strings.ToUpper(method), path: path}] {
				op.Public = true
			}
		}
	}
}

// Publish projects the public document out of the full one.
//
// It reads ONE fact per operation and nothing else — no path, no product, no
// prefix, no method. Everything that is not declared is absent, including the
// document's own furniture: /v1/openapi.json, /health, /v1/event.js and the rest
// of what a route table carries and a product surface does not. They are not
// excluded; they were never included.
//
// Components are pruned to the transitive closure of what the surviving
// operations actually reference, so the public document carries no named type
// that only an internal operation binds — an SDK generated from it mints the
// types its own calls need and no others.
//
// It refuses to produce an EMPTY document. A whitelist that yields nothing is a
// whitelist that did not load: declarations live in the apps' init functions, so
// a projection run over a document composed without them would emit a valid,
// well-formed OpenAPI file describing an API with no operations, and every SDK
// generated from it would be an empty package. Silence is the safe answer for one
// operation and the wrong answer for all of them.
func Publish(d *Document) (*Document, error) {
	out := &Document{
		OpenAPI: d.OpenAPI,
		Info:    publicInfo,
		Servers: d.Servers,
		Paths:   map[string]PathItem{},
	}
	need := map[string]bool{}
	for _, path := range sortedKeys(d.Paths) {
		item := d.Paths[path]
		for _, method := range sortedKeys(item) {
			op := item[method]
			if !op.Public {
				continue
			}
			// PUBLIC and COMPAT are contradictory declarations about one address,
			// and they meet honestly: [Compat] marks a LEGACY SPELLING of another
			// operation — served so a pinned consumer does not break, explicitly not
			// part of the contract — while this marks the contract. Publishing both
			// would put two SDK methods, two CLI verbs and two MCP tools on one call,
			// which is the exact surface Compat exists to keep out. Refused rather
			// than resolved: which of the two facts is wrong is a question for
			// whoever declared them, and picking one here would publish a guess.
			if slices.Contains(op.Tags, Compat) {
				return nil, fmt.Errorf("%s %s is declared PUBLIC and tagged %q — a legacy spelling cannot "+
					"be part of the contract it is a legacy spelling of. Either the address is the one we "+
					"publish (drop the compat tag where the route is served) or it is not (drop the "+
					"openapi.Public line)", strings.ToUpper(method), path, Compat)
			}
			if out.Paths[path] == nil {
				out.Paths[path] = PathItem{}
			}
			out.Paths[path][method] = op
			if err := refs(op, need); err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}
		}
	}
	if len(out.Paths) == 0 {
		return nil, fmt.Errorf("the public projection is EMPTY: none of the %d paths in this document "+
			"declared itself public. Either nothing has been whitelisted yet (openapi.Public, called "+
			"from the owning subsystem's init), or this document was composed without the declarations "+
			"— publishing it would ship an SDK with no calls in it", len(d.Paths))
	}

	said := make(map[string]string, len(d.Tags))
	for _, t := range d.Tags {
		said[t.Name] = t.Description
	}
	names := map[string]bool{}
	for _, item := range out.Paths {
		for _, op := range item {
			for _, t := range Products(op.Tags) {
				names[t] = true
			}
		}
	}
	for name := range names {
		out.Tags = append(out.Tags, Tag{Name: name, Description: said[name]})
	}
	sort.Slice(out.Tags, func(i, j int) bool { return out.Tags[i].Name < out.Tags[j].Name })

	if len(need) > 0 {
		var have map[string]any
		if d.Components != nil {
			have = d.Components.Schemas
		}
		kept, err := closure(have, need)
		if err != nil {
			return nil, err
		}
		out.Components = &Components{Schemas: kept}
	}
	return out, nil
}

// refs collects the component names one operation reaches.
//
// Through JSON rather than by walking the Go value, because an operation's
// bodies arrive in two shapes that are JSON-identical and structurally nothing
// alike: [Register] builds closed *RequestBody / map[string]*Response values,
// while a woven or folded operation carries zip's decoded map[string]any. One
// reader for both is the only way this cannot miss a $ref by arriving through
// the wrong seam.
func refs(op *Operation, need map[string]bool) error {
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return err
	}
	walk(tree, need)
	return nil
}

// walk adds every component name a decoded JSON tree references.
func walk(n any, need map[string]bool) {
	switch v := n.(type) {
	case map[string]any:
		for k, e := range v {
			if k == "$ref" {
				if s, isString := e.(string); isString {
					if name, ours := strings.CutPrefix(s, refPrefix); ours {
						need[name] = true
					}
				}
				continue
			}
			walk(e, need)
		}
	case []any:
		for _, e := range v {
			walk(e, need)
		}
	}
}

// closure returns the named schemas reachable from need, following the $refs
// inside the schemas themselves — a kept type's own fields may name others, and
// a document missing those is a document no generator can bind.
//
// A referenced name the document does not define is refused rather than dropped.
// That is a broken document either way; the difference is whether it breaks here,
// naming the operation, or in whichever SDK generator meets the dangling $ref
// first.
func closure(schemas map[string]any, need map[string]bool) (map[string]any, error) {
	out := map[string]any{}
	queue := make([]string, 0, len(need))
	for name := range need {
		queue = append(queue, name)
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if _, done := out[name]; done {
			continue
		}
		s, defined := schemas[name]
		if !defined {
			return nil, fmt.Errorf("component %q is referenced by a public operation and defined nowhere "+
				"in this document — every generated client would bind a type that does not exist", name)
		}
		out[name] = s
		more := map[string]bool{}
		raw, err := json.Marshal(s)
		if err != nil {
			return nil, fmt.Errorf("component %q: %w", name, err)
		}
		var tree any
		if err := json.Unmarshal(raw, &tree); err != nil {
			return nil, fmt.Errorf("component %q: %w", name, err)
		}
		walk(tree, more)
		for next := range more {
			queue = append(queue, next)
		}
		sort.Strings(queue)
	}
	return out, nil
}
