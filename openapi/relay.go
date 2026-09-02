package openapi

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/zap-proto/zip"
)

// A RELAY is one route standing for a whole API, and reading the router alone
// publishes the route instead of the API.
//
// `app.All("/v1/iam/*")` is a single entry in this process's route table and a
// hundred and sixty in the registry mounted behind it. [Live] reads the router,
// which is the only total source of what this process serves — and the router's
// honest answer here is one wildcard. So the fleet document published
// `/v1/iam/{wildcard1}` where identity serves 157 paths, `/v1/{wildcard1}` where
// the model API serves 192, and every projection downstream inherited the hole:
// no generated SDK carried chat completions, no MCP tool list carried a login,
// and the CLI grew a `{wildcard1}` command because that is what the document
// named. [Document]'s own note has always stated the limit ("whether a route is
// a real endpoint or a PROXY PREFIX ... this document can name the prefix and
// nothing under it"). This is that limit, closed.
//
// # The registry behind the relay is a document, and the relay's owner has it
//
// The subsystem that registers the relay is the one that mounted the thing behind
// it, so it is the one that can say what is there — in the same process, from the
// same objects, at the same instant. hanzoai/iam hands its host a *zip.App;
// hanzoai/ai hands its host a route table (routers.App.Patterns()). Both reduce to
// a [Document], which is what a relay carries. Nothing is fetched, nothing is
// vendored, nothing is hand-copied: a checkout and a go.mod are the whole input,
// so the projection is reproducible offline and cannot go stale against the pin it
// was built from.
//
// # Same laws as every other client in this package
//
// Register declares BODIES and renders only on a live route. Describe declares
// PROSE and renders only on a live route. A relay declares the ROUTES BEHIND A
// ROUTE, and renders only on a live wildcard — so the registry still cannot
// invent an address this process does not answer on. What it can do, and what
// the other two cannot, is REPLACE the wildcard with what the wildcard reaches.
//
// Four refusals, each naming the source so a wrong placement is traceable to the
// repo that registered it:
//
//   - a relay that publishes nothing. That is the shrink this exists to prevent:
//     a registry that failed to build, a table that came back empty, a service
//     that could not be reached. Silently emitting the bare wildcard is how a
//     surface loses a hundred paths without a single test going red.
//   - an operation outside the relay's own prefix. hanzoai/iam publishing
//     /v1/billing/x through /v1/iam/* is not a document defect to be smoothed —
//     it is a routing bug in iam, and the relay is where it becomes visible.
//   - a name collision on the way in, through the SAME noun gate the compose uses
//     (nouns, compose.go): one schema name, one shape, whether the two claimants are
//     two apps or an app and the registry behind its relay.
//   - a duplicate declaration for one prefix. Two relays at one route is two
//     answers to one question.
//
// The router keeps its authority: an operation the HOST itself registered wins at
// an address the relay also claims, because that is what the matcher does — a
// specific route registered before a wildcard is the one that answers (apps/o11y
// mounts /v1/o11y/scope in front of the o11y relay for exactly this reason). The
// fleet-level half of that same rule is in [Compose].

// Relay is a wildcard route and the registry behind it.
//
// Behind is a func, not a Document: every app binary in production registers its
// relays at mount and never projects anything, so building the sub-document at
// declaration time would be work no serving process needs. It is called once, by
// [Spec], and its error is the caller's.
type Relay struct {
	// Source is WHO registered these operations — a module path, because the
	// point of recording it is that a misplaced operation names the repo to file
	// against. It travels onto every operation as x-app.
	Source string
	// Prefix is the relay's own address with no wildcard: "/v1/iam", "/v1". Every
	// operation behind the relay must be under it.
	Prefix string
	// Yields are subtrees INSIDE Prefix that the relay does not reach, because the
	// fleet delivers them to a sibling registered in front of it. A wildcard is
	// matched last, so a registry behind one can hold routes that no request ever
	// arrives at: ai registers /v1/metrics and /v1/admin/providers, and both 404 on
	// api.hanzo.ai because the metrics and admin apps receive those prefixes and do
	// not serve them. Publishing such a route is publishing a phantom, which is the
	// exact defect a router-derived document exists to make impossible.
	//
	// It is the relay's owner that fills this, from the fleet's own routing table
	// (manifest.Elsewhere), never a list written here — a second copy of the
	// routing order is how the pair goes wrong while each half stays sensible.
	Yields []string
	Behind func() (*Document, error)
}

// yields reports whether path is delivered to somebody other than this relay.
func (r Relay) yields(path string) bool {
	for _, p := range r.Yields {
		if path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

// Mounted is a relay over a sub-app the host mounts behind its wildcard: the
// SAME [Spec] the host's own document is, over the app that actually answers.
//
// It is the richest shape a relay has — the sub-app carries its typed registry, so
// its operations arrive with schemas, parameters and the prose zipdoc lifted from
// its handlers, exactly as the host's own typed ops do.
//
// The caller passes the app it MOUNTED, not a fresh one. A second construction
// would be a second route table, free to differ from the one answering requests —
// which is the whole defect this package exists to make unexpressible.
func Mounted(source, prefix string, sub *zip.App) Relay {
	return Relay{Source: source, Prefix: prefix, Behind: func() (*Document, error) {
		return Spec(sub, relayInfo)
	}}
}

// Said is what a foreign registry says about one of its operations: the sentence
// a reader is owed, and the whole comment it opens.
type Said struct{ Summary, Description string }

// Table is a relay over a route table, for a registry that is not a zip app.
//
// hanzoai/ai is the case: its surface is a beego ControllerRegister reached
// through one adapter, so what it hands a host is not an App but the two readings
// an App would have given — `path -> methods` (routers.App.Patterns) and
// `"METHOD /path" -> sentence` (routers.Prose). Those are exactly [Live] and
// [Typed] for a registry that is not zip's, so they compose the same way: the
// table is the total set of addresses, and the sentences are detail for them.
//
// Structure therefore comes from [From], the same builder the router projection
// uses, so a relayed operation earns the SAME operationId, path parameters and
// product tag a native one does — one convention across the document, whoever
// registered the route. Only the prose is the registry's, because only the
// registry has it. It earns no schemas: a table has none, and that is a true
// statement about the table rather than a gap in the projection.
//
// A "*" method means the table dispatches EVERY method at that address. It expands
// to [Methods], the set this generator publishes, which is the same expansion the
// route itself gets — so the two halves of one wildcard cannot disagree about which
// verbs exist, and each expanded verb inherits the one handler's sentence, because
// one handler is what answers all of them.
func Table(source, prefix string, patterns func() map[string][]string, prose func() map[string]Said) Relay {
	return Relay{Source: source, Prefix: prefix, Behind: func() (*Document, error) {
		seen := map[Route]bool{}
		star := map[string]bool{} // path -> the table answers every verb there
		for path, ms := range patterns() {
			for _, m := range ms {
				expand := []string{strings.ToUpper(m)}
				if m == "*" {
					expand, star[path] = Methods(), true
				}
				for _, each := range expand {
					if methods[each] {
						seen[Route{Method: each, Path: path}] = true
					}
				}
			}
		}
		rs := make([]Route, 0, len(seen))
		for r := range seen {
			rs = append(rs, r)
		}
		sort.Slice(rs, func(i, j int) bool {
			if rs[i].Path != rs[j].Path {
				return rs[i].Path < rs[j].Path
			}
			return rs[i].Method < rs[j].Method
		})
		doc, err := From(rs, relayInfo)
		if err != nil {
			return nil, err
		}
		said := prose()
		var mute []string
		for _, r := range rs {
			path, _ := translate(r.Path)
			d, ok := said[r.Method+" "+path]
			if !ok && star[r.Path] {
				d, ok = said["* "+path]
			}
			if !ok || strings.TrimSpace(d.Summary) == "" {
				mute = append(mute, r.Method+" "+path)
				continue
			}
			op := doc.Paths[path][strings.ToLower(r.Method)]
			op.Summary, op.Description = d.Summary, d.Description
		}
		if len(mute) > 0 {
			sort.Strings(mute)
			return nil, fmt.Errorf("%s serves %d operation(s) it says nothing about:\n  %s\n\n"+
				"A relay publishes what is behind it, and an operation that says nothing about itself is "+
				"published into every SDK, the MCP tool list and the CLI with an address and no sentence. "+
				"The sentence lives on the handler in %s; nothing here can supply it",
				source, len(mute), strings.Join(mute, "\n  "), source)
		}
		return doc, nil
	}}
}

// relayInfo is the identity a sub-document is built under and is never published:
// [Project] takes the operations and the schemas, never the info block. The
// composed document has ONE identity and it comes from fleet.go — see [Compose],
// which states the same rule for the same reason.
var relayInfo = Info{Title: "relay", Version: "v1"}

// host is how the noun gate names the side of a schema conflict that is NOT a
// relay: the routes already in the document being projected. A [Spec] run
// describes one app, so "the app that mounted the relay" is the whole identity a
// reader needs — the run and the file it writes say which app that is.
const host = "the app that mounted the relay"

var (
	relayMu  sync.Mutex
	relayReg = map[string]Relay{} // prefix → relay
)

// Front declares what is behind a relay. Called from the owning subsystem's
// Mount, next to the registration of the route itself — the two are one fact and
// must be written in one place.
//
// It panics on a duplicate prefix, the way [Register] and [Describe] do: two
// relays at one route is a programming error at wire time, not a runtime
// condition to degrade through.
func Front(r Relay) {
	if r.Source == "" || r.Prefix == "" || r.Behind == nil {
		panic(fmt.Sprintf("openapi: incomplete relay %+v — a relay names its source, its prefix and what is behind it", r))
	}
	relayMu.Lock()
	defer relayMu.Unlock()
	if prev, dup := relayReg[r.Prefix]; dup {
		panic(fmt.Sprintf("openapi: duplicate Front for %s — already relayed to %s", r.Prefix, prev.Source))
	}
	relayReg[r.Prefix] = r
}

// relays returns the declared relays, deepest prefix first.
//
// Deepest first is what makes overlapping relays composable: /v1/iam is inside
// /v1, so identity's relay has to be answered before the model API's fallback is
// asked whether it covers the same ground. Sorting here rather than at each call
// site is why [Project] can be a plain loop.
func relays() []Relay {
	relayMu.Lock()
	defer relayMu.Unlock()
	out := make([]Relay, 0, len(relayReg))
	for _, r := range relayReg {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Prefix) != len(out[j].Prefix) {
			return len(out[i].Prefix) > len(out[j].Prefix)
		}
		return out[i].Prefix < out[j].Prefix
	})
	return out
}

// endpoints returns the path keys in doc that ARE the relay for prefix: the bare
// prefix and its wildcard form, which is what [translate] makes of `prefix/*`.
//
// Both, because a subsystem registers both — `/v1/iam/*` already matches
// `/v1/iam`, but the document derives its paths from the route table, so without
// the bare form the resource's own address appears nowhere (apps/iam/iam.go says
// this at the registration site). Neither survives projection: leaving the
// wildcard beside the routes it stands for would publish one address twice, and
// `{wildcard1}` is what a spec-derived CLI turns into a phantom command.
func endpoints(doc *Document, prefix string) []string {
	var out []string
	for _, p := range []string{prefix, prefix + "/{wildcard1}"} {
		if _, ok := doc.Paths[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

// Project replaces each declared relay's route with the registry behind it.
//
// A relay whose route is NOT in doc does not apply. That is the same law
// [Register] and [Describe] obey — a declaration renders only on a route this
// process carries — and it is what makes one binary per app work: the relay
// registry is process-wide, a describe run mounts one subsystem, and a relay
// declared by a subsystem that is not mounted has no route to replace. The
// fleet-level guarantee that a route never quietly goes missing is not this
// function's job and cannot be: it is the ratchet in floor.go, which is the only
// place that can see the whole surface at once.
func Project(doc *Document, rs []Relay) error {
	n := newNouns()
	if doc.Components != nil {
		if err := n.add(host, doc.Components.Schemas); err != nil {
			return err
		}
	}
	for _, r := range rs {
		open := endpoints(doc, r.Prefix)
		if len(open) == 0 {
			continue
		}
		behind, err := r.Behind()
		if err != nil {
			return fmt.Errorf("%s is mounted at %s and could not describe itself: %w — "+
				"a relay that cannot say what is behind it publishes one wildcard where a whole API is, "+
				"which is a silently smaller document; refusing instead", r.Source, r.Prefix, err)
		}
		reach := map[string]PathItem{}
		count := 0
		for path, item := range behind.Paths {
			if path != r.Prefix && !strings.HasPrefix(path, r.Prefix+"/") {
				return fmt.Errorf("%s publishes %q through the relay at %s — an operation outside its own "+
					"prefix is unreachable there, so this is a routing bug in %s and not a document to smooth over",
					r.Source, path, r.Prefix, r.Source)
			}
			if r.yields(path) {
				continue // the fleet delivers it to a sibling; the relay never sees the request
			}
			reach[path] = item
			count += len(item)
		}
		if count == 0 {
			return fmt.Errorf("%s is mounted at %s and published no operations — "+
				"emitting the bare wildcard instead would shrink the surface by everything behind that relay",
				r.Source, r.Prefix)
		}
		if behind.Components != nil {
			if err := n.add(r.Source, behind.Components.Schemas); err != nil {
				return err
			}
		}
		for _, d := range open {
			delete(doc.Paths, d)
		}
		for path, item := range reach {
			for method, op := range item {
				// The HOST's own route wins. A specific path registered before the
				// wildcard is what the matcher picks, so publishing the relay's
				// operation there would name a handler that never runs.
				//
				// It wins the ADDRESS, not the SENTENCE. A host route that says
				// nothing is one the registry behind the relay still answers —
				// hanzoai/ai promotes /v1/models and the enso access pair onto this
				// router at their real patterns, pointing at the same relay the glob
				// uses — so the words belong to the handler either way, and taking
				// them here is what keeps them from being written a second time in
				// this repo. A host route that DOES say something has its own
				// handler and its own prose (apps/o11y's /v1/o11y/scope), and is
				// left exactly as it is.
				if have := doc.Paths[path][method]; have != nil {
					if strings.TrimSpace(have.Summary) == "" && strings.TrimSpace(have.Description) == "" {
						have.Summary, have.Description = op.Summary, op.Description
					}
					continue
				}
				op.App = r.Source
				if doc.Paths[path] == nil {
					doc.Paths[path] = PathItem{}
				}
				doc.Paths[path][method] = op
			}
		}
	}

	n.into(doc)
	// Tags are recomputed from the operations rather than appended to, for the
	// reason [Compose] states: the tag list is a function of the document's
	// operations, so a relay that was one product's wildcard and is now twenty
	// products' worth of routes carries exactly the twenty.
	retag(doc)
	return uniqueOperationIDs(doc)
}

// retag makes the tag list a function of the operations again, preserving any
// description a tag already carries.
func retag(doc *Document) {
	said := make(map[string]string, len(doc.Tags))
	for _, t := range doc.Tags {
		said[t.Name] = t.Description
	}
	names := map[string]bool{}
	for _, item := range doc.Paths {
		for _, op := range item {
			for _, t := range op.Tags {
				names[t] = true
			}
		}
	}
	doc.Tags = doc.Tags[:0]
	for name := range names {
		doc.Tags = append(doc.Tags, Tag{Name: name, Description: said[name]})
	}
	sort.Slice(doc.Tags, func(i, j int) bool { return doc.Tags[i].Name < doc.Tags[j].Name })
}
