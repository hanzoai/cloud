package openapi

// THE AUDIENCE IS DERIVED, and public.yaml is what every projection reads.
//
// api.hanzo.ai answers ~1,800 paths across ~185 products, and the whole document
// is served unauthenticated at /v1/openapi.json — a client has to be able to read
// the contract before it holds a credential. So the split between the two
// projections was never secrecy. It is AUDIENCE: what the published SDKs, the
// CLI, the MCP door and docs.hanzo.ai present to a customer, against what an
// operator reaches through the same origin.
//
//	INTERNAL  everything, unchanged — openapi.yaml, the golden the weave is held to.
//	PUBLIC    the customer surface — public.yaml, what every client is generated from.
//
// # One rule, read off facts the operation already carries
//
// An operation is public when all of these hold, and nothing else decides it:
//
//   - its address is under /v1/ — the contract's one namespace (HIP-0128). The
//     document's furniture outside it (/.well-known/openapi.json, /_/…, /zap,
//     /ws/…) is served and is not a product surface;
//   - its product is not [Operator] — the first segment after /v1/ is the product
//     (Product), the axis every tag is already read off, and /v1/admin/* is the
//     one family the fleet reserves for the operator whichever app serves a leaf
//     of it (admin itself, pricing's /v1/admin/pricing, affiliates' operator view);
//   - it is not a relay door — a `{wildcardN}` address publishes whatever grows
//     behind it and names nothing a client can call;
//   - it is not tagged [Compat] — a legacy spelling is served so a pinned caller
//     keeps working and is, by its own declaration, not the contract;
//   - its capability is `ga` (HIP-0139 §8) — a beta or alpha capability is
//     reached by flag and by nobody else, so it is in no generated client, no
//     tool list, no command group and no public page. It is still in the
//     internal document, which is where an operator and a flagged-in customer
//     read it.
//
// It used to be a whitelist: eighteen operations, each declared by hand in the
// app that served it, everything else internal by default. That held the
// inference surface public and the other hundred-odd products — commerce, git,
// observability, accounting, the cap table — out of every generated client,
// while those same clients quietly read the internal document instead. A
// whitelist nothing reads is a statement of intent. The rule above says what the
// customer surface IS, from the address, so a product launched next month is
// public the day it answers — unless it is the operator's, which its address
// already says.
//
// # Why this is not the prefix list the whitelist refused
//
// The whitelist refused a prefix list in the EMITTER because a second copy of the
// routing table drifts from the first. Nothing here is a copy of anything: the
// product is read from the operation's own address at the moment it is stamped,
// the relay door from its own parameters, the legacy spelling from its own tag.
// There is no list to keep current and no address a reader could spell
// differently from the router — translate has already rendered every path in the
// one form the document publishes before [stamp] runs.
//
// # What holds it honest
//
// public.yaml is COMMITTED and regenerated from source by the drift gate
// (mk/fleet.mk check), beside openapi.yaml. An operation entering or leaving the
// contract is a diff in that file, reviewed next to the route that caused it.

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// refPrefix is how a JSON Schema names a component in this document.
const refPrefix = "#/components/schemas/"

// Operator is the product reserved for the operator: /v1/admin/*. Whichever app
// serves a leaf of it, the address says who it is for, and the public projection
// reads that rather than a list of apps.
const Operator = "admin"

// audience is the rule, asked of one operation at its published address.
func audience(path string, op *Operation) bool {
	// The root IS the contract's namespace, not a path outside it: /v1 is where a
	// client that knows nothing else starts, so a public projection that dropped it
	// would generate clients unable to reach the index of their own API.
	if path != RootPath && !strings.HasPrefix(path, "/v1/") {
		return false
	}
	if Product(path) == Operator {
		return false
	}
	if strings.Contains(path, "{wildcard") {
		return false
	}
	if op.Stage != "" {
		return false
	}
	return !slices.Contains(op.Tags, Compat)
}

// stamp writes the audience on every operation.
//
// It runs at the end of [Spec], and AGAIN at the end of [Weave]. That is not two
// rules — it is one rule asked at the two points where the facts it reads are
// complete, and the second reading subsumes the first.
//
// Within an app, earlier than the end of Spec is too early: [Fold] replaces a
// structural operation wholesale with the typed one, and [Project] replaces a
// door with the registry behind it, so a mark written before either would be
// discarded by it.
//
// Across the fleet, the end of Spec is too EARLY for one term of the rule. A
// subset is written by the app's own binary, which does not read the fleet's
// manifest and so cannot know its own stage (HIP-0139 §8); the stage arrives with
// [Part], and Weave is where it is stamped. So Weave asks the whole rule again on
// the finished composition, where every term — address, product, door, compat and
// stage — is finally in hand. It is still a pure function of the parts.
func stamp(d *Document) {
	for path, item := range d.Paths {
		for _, op := range item {
			op.Public = audience(path, op)
		}
	}
}

// Publish projects the public document out of the full one.
//
// It reads ONE fact per operation — the audience [stamp] wrote — and nothing
// else. Components are pruned to the transitive closure of what the surviving
// operations actually reference, so the public document carries no named type
// that only an internal operation binds — an SDK generated from it mints the
// types its own calls need and no others.
//
// It refuses to produce an EMPTY document: a projection with no customer surface
// is a document composed wrong, and publishing it would ship an SDK with no
// calls in it.
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
			"is a customer operation under /v1/ — publishing it would ship an SDK with no calls in it", len(d.Paths))
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
	// LAST, after the components block this projection builds for itself: the
	// credential rides in it, and a fresh block assigned over it would drop the one
	// component every published SDK needs to send a token at all.
	secure(out)
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
