package openapi

// THE API DESCRIBES ITSELF FROM ITS OWN ROOT.
//
// A client that knows only https://api.hanzo.ai can GET /v1 and be told what
// this deployment answers: one row per capability, each with the address to
// follow. Following one lands on that capability's own index — every operation
// it serves, with the address and the method to call it. Nothing has to be
// known in advance except the root, which is the whole of what hypermedia
// means (RFC 8288 for the links; the rows are the same values the document
// already carries).
//
// # It is a PROJECTION, never a second source
//
// Every value here is read off the woven document the host already serves at
// [Path]: the capability is the operation's own tag (HIP-0139 §4, which is
// x-app), its sentence is that tag's description (openapi/synopsis.go lifted it
// from the package doc), its stage is x-stage and whether it is shown at all is
// x-public — the audience rule openapi.yaml is projected by ([stamp]). There is
// no list here to keep current: a capability that ships next month is in the
// index the day its routes are in the document, and one that goes beta leaves
// it the day its manifest row says so.
//
// # Why the front door answers this ahead of the router
//
// /v1 is a claimed address. ai's manifest row is the REMAINDER — its prefix is
// the bare "/v1" — and zip mounts a prefix as All(prefix) plus All(prefix+"/*"),
// so a host ROUTE at /v1 is two definitions claiming one address and the
// composition is refused outright. The same is true one segment down for every
// capability that claims its own root: /v1/kms is kms's.
//
// So these two doors are middleware on the front door rather than routes on it,
// which is the same seam a co-resident app already uses to answer inside a
// sibling's subtree (apps/zen). What keeps that from SHADOWING anything is one
// line rather than a policy: an address the document already carries an
// operation at is the capability's own, and the index does not answer there —
// GET /v1/agents is the agents collection, not an index of it. The day kms
// serves its own root, the document says so and this steps aside with no edit
// here at all.
//
// The document still describes both doors, and does it the way the agent door
// is described (openapi/mcp.go): [core] mounts a stub at each address so [Spec]
// projects it, and the prose and bodies are declared below. A published address
// with an operationId and nothing else is an SDK method nobody can explain.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/manifest/door"
	"github.com/zap-proto/zip"
)

const (
	// RootPath is the API root — the capability index a client starts from.
	// House law: /v1 only, and never a v2.
	RootPath = "/v1"
	// IndexPath is one capability's index as the DOCUMENT spells it; the
	// router registers the same address as [indexRoute]. Both spellings are
	// answered by [Door], which is asked with either.
	IndexPath  = "/v1/{name}"
	indexRoute = "/v1/:name"
)

// Link is one RFC 8288 link target: where to go for that relation.
type Link struct {
	Href string `json:"href"`
}

// Capability is one row of the [Root] index: what a capability is called, where
// it answers, whether it is generally available, and its own sentence about
// itself.
type Capability struct {
	// Name is the capability, which is also the package, the tag, the MCP tool
	// and the CLI command group (HIP-0139 §1).
	Name string `json:"name"`
	// Href is the address it answers under, always /v1/<name>.
	Href string `json:"href"`
	// Stage is "ga", "beta" or "alpha" (HIP-0139 §8).
	Stage string `json:"stage"`
	// Description is what the capability says about itself — the tag's
	// synopsis, lifted from its package doc.
	Description string `json:"description,omitempty"`
}

// Root is what GET /v1 answers: every capability this deployment publishes, and
// the links a client follows from here.
type Root struct {
	Capabilities []Capability    `json:"capabilities"`
	Links        map[string]Link `json:"_links"`
}

// Op is one operation as a caller runs it: the name to call it by, the method
// and address to send, and the sentence that says what it does.
type Op struct {
	OperationID string `json:"operationId"`
	Method      string `json:"method"`
	Href        string `json:"href"`
	Summary     string `json:"summary,omitempty"`
}

// Index is what GET /v1/<name> answers: one capability's whole published
// surface, and the way back to the root.
type Index struct {
	Name        string          `json:"name"`
	Stage       string          `json:"stage"`
	Description string          `json:"description,omitempty"`
	Operations  []Op            `json:"operations"`
	Links       map[string]Link `json:"_links"`
}

// Discover projects a woven document into the hypermedia index: the root, and
// one index per capability keyed by its name.
//
// The surface is the PUBLIC one — an operation carries the audience the weave
// stamped on it, so the operator's admin product, the relay doors, the legacy
// spellings and every capability that is not yet ga are absent from both halves
// at once. That is the same rule openapi.yaml is projected by and it is asked
// once, here, rather than restated: a beta capability being missing from the
// root and its name 404ing one segment down are the same fact, which is what
// keeps the index from telling an unflagged caller that a capability exists.
//
// A capability that READS its own root is in the root index — the href is real
// and a client can follow it — but has NO entry in the map: GET /v1/agents is
// the agents collection, and an index of the capability in front of it would be
// the same address answering two things.
//
// Per (method, path), which is the unit an operation is everywhere else in this
// package: a capability that only ACTS at its root reads nothing there, so the
// GET is free and its href resolves to an index rather than a 405.
//
// # The rows are CAPABILITIES, not address roots
//
// The two agree for all but the six lines of openapi/misfiled.txt plus the
// vendor-compatible wire ai serves by standard, and that ledger may only shrink.
// Keying on the capability is what makes this the SAME list every other
// projection carries — the MCP tools, the CLI command groups and the SDK classes
// are one per capability (HIP-0139 §1) — and what puts each operation in exactly
// one index. An operation filed under an address that is not its capability's
// name is still reachable from it, because every href here is absolute.
func Discover(d *Document) (*Root, map[string]*Index) {
	said := make(map[string]string, len(d.Tags))
	for _, t := range d.Tags {
		said[t.Name] = t.Description
	}

	ops := map[string][]Op{}
	stages := map[string]string{} // capability -> the stage its operations carry
	for _, path := range sortedKeys(d.Paths) {
		item := d.Paths[path]
		for _, method := range sortedKeys(item) {
			op := item[method]
			if !op.Public {
				continue
			}
			for _, name := range Products(op.Tags) {
				ops[name] = append(ops[name], Op{
					OperationID: op.OperationID,
					Method:      strings.ToUpper(method),
					Href:        path,
					Summary:     op.Summary,
				})
				stages[name] = op.Stage
			}
		}
	}

	root := &Root{
		Capabilities: make([]Capability, 0, len(ops)),
		Links: map[string]Link{
			"self":        {Href: RootPath},
			"describedby": {Href: Path},
			"mcp":         {Href: door.Path},
			// Where to knock, beside where to knock ON. The door 401s with a
			// WWW-Authenticate naming this same document (RFC 9728), which is how a
			// spec-following MCP client discovers it — but only AFTER being refused.
			// A caller reading the index learns both in the one call it already makes.
			"auth": {Href: door.Metadata},
		},
	}
	per := make(map[string]*Index, len(ops))
	for _, name := range sortedKeys(ops) {
		href := RootPath + "/" + name
		stage := stages[name]
		if stage == "" {
			stage = "ga"
		}
		root.Capabilities = append(root.Capabilities, Capability{
			Name: name, Href: href, Stage: stage, Description: said[name],
		})
		// Lower-case because that is how a document keys a method, where the door
		// itself is declared with http.MethodGet.
		if d.Paths[href][strings.ToLower(http.MethodGet)] != nil {
			continue // the capability reads its own root; this is not the index's address
		}
		list := ops[name]
		sort.Slice(list, func(i, j int) bool {
			if list[i].Href != list[j].Href {
				return list[i].Href < list[j].Href
			}
			return list[i].Method < list[j].Method
		})
		per[name] = &Index{
			Name: name, Stage: stage, Description: said[name], Operations: list,
			Links: map[string]Link{
				"self":        {Href: href},
				"describedby": {Href: Path},
				"up":          {Href: RootPath},
			},
		}
	}
	return root, per
}

// MountIndex installs the hypermedia layer on the FRONT DOOR: the two index
// doors, and the RFC 8288 links every /v1 answer carries.
//
// It must be composed BEFORE the subsystems are mounted. zip visits an included
// App with the middleware stack as it stood at the inclusion site, so a Use
// written after the mounts reaches none of them — and both halves of this are
// about requests the mounts would otherwise answer.
//
// The index is rendered ONCE, on the first request that needs it, from the same
// subsets the document is woven from. The weave decodes bytes already in this
// binary — no subsystem starts and no socket opens — and measured on this tree
// (1,771 paths, 2026-08-20) it costs 56ms, paid by one request per process.
//
// The woven Document is DROPPED once the answers are bytes, which is why this
// weaves rather than sharing [serve]'s. Sharing would mean one of them holding
// the whole decoded document alive for the life of the process, and this host is
// the one that has been evicted for the memory it holds.
func MountIndex(app *zip.App, subsets func() ([]Part, error)) {
	render := sync.OnceValues(func() (*rendered, error) {
		parts, err := subsets()
		if err != nil {
			return nil, err
		}
		d, err := Fleet(parts)
		if err != nil {
			return nil, err
		}
		root, per := Discover(d)
		out := make(map[string][]byte, len(per)+1)
		if out[RootPath], err = json.Marshal(root); err != nil {
			return nil, err
		}
		for name, ix := range per {
			if out[RootPath+"/"+name], err = json.Marshal(ix); err != nil {
				return nil, err
			}
		}
		return &rendered{doors: out, known: addressesOf(d)}, nil
	})

	app.Use(zip.H(func(c *zip.Ctx) error {
		// A deployment that cannot weave its own document still answers the
		// request the caller actually made. It answers without the links it could
		// not derive, which is why everything below reads through a nil receiver.
		r, _ := render()
		if body, mine := answer(c, r); mine {
			c.SetHeader("Content-Type", "application/json")
			refer(c, r)
			return c.Bytes(http.StatusOK, body)
		}
		err := c.Next()
		refer(c, r)
		return err
	}))
}

// answer is the index's half of the decision: the body for this request, or
// "not mine, carry on".
//
// Only a GET, and only at the root or one segment under it — everything deeper
// belongs to the capability that owns the subtree, and this never looks at it.
func answer(c *zip.Ctx, r *rendered) ([]byte, bool) {
	// A nil r is a deployment that could not describe itself. That is not a
	// reason to refuse the request — the address may be a capability's own — so
	// this yields rather than answering 500 on a path it does not own.
	if c.Method() != http.MethodGet || r == nil {
		return nil, false
	}
	path := c.Path()
	if path != RootPath && leaf(path) == "" {
		return nil, false
	}
	body, mine := r.doors[path]
	return body, mine
}

// refer stamps the RFC 8288 links on a /v1 answer: where this response came
// from, where the whole API is described, and where the index of it starts.
//
// ADDED, never set. A capability may already have answered with links of its
// own — pagination is the obvious one — and Link is a list header precisely so
// two authorities can each name a relation without either erasing the other.
//
// After the handler, because a proxied answer is written by the far end into
// this very response: anything stamped before the hop is overwritten by it.
func refer(c *zip.Ctx, r *rendered) {
	if !under(c.Path()) {
		return
	}
	h := &c.Fiber().Response().Header
	if at := here(c); at != "" {
		h.Add("Link", "<"+at+`>; rel="self"`)
	}
	h.Add("Link", "<"+Path+`>; rel="describedby"`)
	h.Add("Link", "<"+RootPath+`>; rel="index"`)

	a, ok := r.at(c.Path())
	if !ok {
		return
	}
	// What this address accepts. A caller cannot read that off a body, and RFC
	// 9110 §10.2.1 already has the field for it. Never over a capability that
	// answered the question itself.
	if a.allow != "" && len(h.Peek("Allow")) == 0 {
		h.Set("Allow", a.allow)
	}
	// The address ABOVE, spelled from the request rather than from the template,
	// so what a caller follows is an address and not a pattern with a hole in it.
	// A member's parent is the collection it belongs to (RFC 6573); anything
	// else's parent is simply up.
	if a.hasUp {
		if up := above(c.Path()); up != "" {
			rel := "up"
			if a.member {
				rel = "collection"
			}
			h.Add("Link", "<"+up+`>; rel="`+rel+`"`)
		}
	}
}

// under reports whether path is inside the contract's one namespace: /v1 itself
// or an address beneath it. The trailing slash is what makes it SEGMENT-wise —
// /v1beta carries the same string prefix and is not under /v1, which is the same
// trap the product axis is read segment-wise to avoid (see [Product]).
func under(path string) bool {
	return path == RootPath || strings.HasPrefix(path, RootPath+"/")
}

// leaf is the one segment a path names under the root, or "" when it names none
// — the root itself, a deeper address inside a capability's own subtree, or
// anything outside the namespace.
func leaf(path string) string {
	rest, ok := strings.CutPrefix(path, RootPath+"/")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return ""
	}
	return rest
}

// rendered is what one weave of the fleet document leaves behind: the bodies of
// the two index doors, and what the contract says about every other address.
//
// The document itself is dropped — see [MountIndex] — and this is deliberately
// the small residue of it. Per address that is a method list and two booleans.
type rendered struct {
	doors map[string][]byte
	known *addresses
}

// at reports what the contract says about the address this path matched.
func (r *rendered) at(path string) (address, bool) {
	if r == nil || r.known == nil {
		return address{}, false
	}
	return r.known.at(path)
}

// address is one row of the contract as the links need it.
type address struct {
	segs   []string // the template's segments; one beginning with '{' matches anything
	allow  string   // "DELETE, GET, POST" — the public methods registered here
	member bool     // the last segment is a parameter, so the address above is a collection
	hasUp  bool     // the address above is itself in the contract
}

// addresses recovers an operation's template from a concrete request path.
//
// It exists because the front door PROXIES: the fiber route a request matched
// here is the proxy's own, not the operation's, so the template cannot be read
// off the route and has to be found by shape. Templates are bucketed by segment
// count, so a lookup compares only the candidates that could possibly match —
// on this tree that is a few dozen of seventeen hundred.
type addresses struct {
	byDepth map[int][]address
}

func addressesOf(d *Document) *addresses {
	known := make(map[string]bool, len(d.Paths))
	for path := range d.Paths {
		known[path] = true
	}
	a := &addresses{byDepth: map[int][]address{}}
	for path, item := range d.Paths {
		var methods []string
		for _, m := range sortedKeys(item) {
			if op := item[m]; op != nil && op.Public {
				methods = append(methods, strings.ToUpper(m))
			}
		}
		segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
		a.byDepth[len(segs)] = append(a.byDepth[len(segs)], address{
			segs:   segs,
			allow:  strings.Join(methods, ", "),
			member: strings.HasPrefix(segs[len(segs)-1], "{"),
			hasUp:  known[above(path)],
		})
	}
	return a
}

// at matches a concrete path against the templates of its own depth. A template
// made only of literals wins outright — /v1/node/peer is an address in its own
// right and is not the /v1/node/{id} whose shape it also fits.
func (a *addresses) at(path string) (address, bool) {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var hit address
	var found bool
	for _, cand := range a.byDepth[len(segs)] {
		exact := true
		fits := true
		for i, s := range cand.segs {
			if strings.HasPrefix(s, "{") {
				exact = false
			} else if s != segs[i] {
				fits = false
				break
			}
		}
		if !fits {
			continue
		}
		if exact {
			return cand, true
		}
		if !found {
			hit, found = cand, true
		}
	}
	return hit, found
}

// above is the address one segment up, or "" when there is none worth naming:
// outside the namespace, or the root, which every answer already links as the
// index and would otherwise carry twice under two relations.
func above(path string) string {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return ""
	}
	p := path[:i]
	if p == RootPath || !under(p) {
		return ""
	}
	return p
}

// here is the request's own address as a Link header may carry it, or "" when
// it cannot.
//
// Read from the REQUEST LINE rather than from the parsed path, which is
// percent-decoded and could therefore hold a CR or LF that a header value must
// never contain. '<' and '>' delimit a URI-Reference, so a path carrying either
// would end the value early; such a request cannot name itself and simply does
// not get the relation.
func here(c *zip.Ctx) string {
	p := string(c.Fiber().Request().URI().PathOriginal())
	if p == "" || strings.ContainsAny(p, "<>\r\n") {
		return ""
	}
	return p
}

// stubIndex puts both doors on core's throwaway router so [Spec] projects them.
// The handlers are never reached: the real doors are the front door's
// middleware, for the reason stated at the top of this file.
func stubIndex(app *zip.App) {
	held := func(*zip.Ctx) error {
		return zip.ErrInternal("the document's stub for the index — the front door serves it")
	}
	app.Get(RootPath, held)
	app.Get(indexRoute, held)
}

func init() {
	Describe(RootPath, http.MethodGet,
		"Every capability this deployment answers, and where to follow each one",
		"The API root. One row per capability — its name, the address it answers under, whether "+
			"it is generally available, and the sentence it says about itself — plus the links to "+
			"the document at "+Path+" and the agent door.\n\n"+
			"It is a projection of that same document and carries the same surface a customer "+
			"calls: the operator's admin product, the relay doors, the legacy spellings and any "+
			"capability that is not yet generally available are in neither.\n\n"+
			"Unauthenticated by design, exactly as the document it derives from: a client has to "+
			"be able to read the contract before it holds a credential, and a list of capability "+
			"names grants nothing.")
	Register(RootPath, http.MethodGet, nil, Root{})
	Open(RootPath, http.MethodGet)
	// NAMED, because /v1 is nothing but the segment [zip.ID] drops — the derived
	// id is the bare "get". See [identify]. The pair reads as the collection and
	// the item it holds, which is what they are, in every projection: an SDK
	// method, a CLI command and an MCP tool name.
	identify(RootPath, http.MethodGet, "get_capabilities")

	Describe(indexRoute, http.MethodGet,
		"One capability's operations, with the address and method to call each",
		"What the capability named in the path answers: every published operation, its "+
			"operationId, its method and its address, and the sentence lifted from the handler "+
			"that serves it — plus the way back to "+RootPath+".\n\n"+
			"It answers where the capability serves nothing at its own root. Where it does, that "+
			"operation is the answer and is described at its own address; a client following the "+
			"root index reaches the capability either way.\n\n"+
			"Unauthenticated, and scoped to the same customer surface the root is. A name that is "+
			"not a published capability is answered exactly as any other unrouted address, so this "+
			"cannot be asked whether something exists that it would not have listed.")
	Register(indexRoute, http.MethodGet, nil, Index{})
	Open(indexRoute, http.MethodGet)
	identify(indexRoute, http.MethodGet, "get_capability")
}
