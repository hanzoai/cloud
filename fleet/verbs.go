// Copyright © 2026 Hanzo AI. MIT License.

package fleet

import "strings"

// AN OPERATION IS NAMED FOR WHAT IT DOES. It was named for where it lived.
//
// Measured on the deployed door: the `projects` tool offered 37 operations and
// the enum read
//
//	delete_v1_projects_by_slug
//	delete_v1_projects_by_slug_domains_by_host
//	get_v1_projects
//	get_v1_projects_by_slug
//	get_v1_projects_by_slug_deployments
//
// with no prose beside any of them. Those are ROUTES. zip derives an operation
// id from the route when nobody declared one — lower(method) plus the path with
// '/'→'_' and a parameter rendered `by_<name>` (zip@v1.27.0 openapi.go, ID) —
// so the model's first job was to reverse-engineer a URL back into an intention,
// its second was to call [Describe] to find out what the intention took, and only
// its third was to act. Three round trips to use a menu written in URL, and the
// assistant reads as stupid for the whole of the first two.
//
// The route is not wrong, it is just not the ANSWER to "what can you do". A
// derived id already contains everything a verb phrase needs; it is spelled in
// the wrong order and padded with scaffolding that names nothing. So [phrase]
// reads it back:
//
//	get_v1_projects_by_slug_deployments        list_project_deployments
//	post_v1_projects_by_slug_domains_by_host_verify   verify_project_domain
//	post_v1_projects_by_slug_deploy            deploy_project
//	delete_v1_projects_by_slug                 delete_project
//
// # Where this happens, and why THERE
//
// After the gate, in the projection, and never before either.
//
// [refuse] reads a tool NAME to decide whether the fleet will project it at all
// (fleet/surface.go), and it is the only gate there is. If a rename ran first, an
// operation whose route says `post_v1_iam_users` would be offered up as something
// whose words no longer trip clause 2 — a credential-minting operation projected
// because it was renamed politely. So [Door.gather] refuses the CHILD's own name,
// exactly as it always did, and [offer] runs over what survives. The refused set
// is therefore identical by construction and not by luck: the gate's input never
// changed. fleet/verbs_test.go asserts that over the fleet's whole corpus.
//
// [rank] reads the same child name for the same reason — it matches route stems
// (fleet/surface.go, productStems), so it must see a route.
//
// # The presented name is a DECODING, exactly like the envelope
//
// A tools/call arrives carrying whatever the door published, so the mapping back
// must be exact. [offer] guarantees it by REFUSING to rename rather than by
// guessing: a phrase that two operations would share, or that collides with some
// operation's own id, is not used and both keep their ids. The door then holds
// published-name → id beside its tool → app routing (see [Door.alias]), which is
// the same kind of fact with the same lifetime — written by every gather, read by
// call and describe, remembered no longer than the routing table it rides with.
//
// An operation's own id keeps working, and that is not a compatibility shim: it
// is forced. [Door.describe] hands back the OWNING subsystem's descriptor bytes
// verbatim, and those bytes carry the child's own name — so a model that reads a
// descriptor and calls what it saw must be right.

// phrase is one operation's id read back as a verb on an object.
//
// A DECLARED id is returned untouched. `GetUserPreference` is already a verb
// followed by its object, which is what this function exists to produce, and the
// subsystems that declare ids (o11y, iam) would only be damaged by a second
// opinion about their own naming.
//
// For a derived id the shape is decided by the ROUTE, not by a dictionary:
//
//	.../{id}/ACTION   a mutating method, a singular segment sitting after a
//	                  parameter and last — the author already wrote the verb, so
//	                  it leads and the rest is its object: deploy_project.
//	.../{id}          the path identifies one row, so the method is the verb and
//	                  every noun is singular: get_project, delete_project_domain.
//	.../things        GET of a plural tail is a list and keeps it plural:
//	                  list_project_deployments. Every other method acts on one
//	                  member: create_project_domain.
//
// The method supplies the verb when the route does not, and PUT and PATCH are
// deliberately different words — `set` replaces, `update` merges — because they
// are different operations at one address and one word for both would collide.
func phrase(op string) string {
	method, segs, endsInID, ok := route(op)
	if !ok || len(segs) == 0 {
		return op
	}
	tail := segs[len(segs)-1]

	if changes(method) && tail.afterID && !endsInID && !plural(tail.word) && len(segs) > 1 {
		return join(tail.word, nouns(segs[:len(segs)-1], false))
	}

	verb := methodVerb[method]
	if method == "get" && !endsInID && plural(tail.word) {
		verb = "list"
	}
	return join(verb, nouns(segs, verb == "list"))
}

// segment is one LITERAL segment of a route, and whether a path parameter stood
// immediately before it — which is the difference between a sub-collection and
// an action on a row, and the only thing the parameter itself is good for here.
type segment struct {
	word    string
	afterID bool
}

// route splits a derived operation id back into the parts of the route it was
// made from. ok is false for a declared id, which is not a route at all.
//
// A parameter contributes NO word: `by_slug` says a row is addressed, not which
// one, and the name of the addressing column is the caller's business at call
// time and nobody's here. The version is dropped for the same reason — it names
// nothing, and every operation in the fleet carries the same one.
//
// zip renders a parameter as `by_<name>` after reducing the name to [a-z0-9.-],
// so a parameter whose name contains an underscore (`{file_id}` → `by_file_id`)
// spends one token on `by` and TWO on the name, and this reads the second as a
// literal segment. Three of the fleet's 2,060 derived operations are shaped that
// way; they get a clumsier phrase, not a wrong one, and if the phrase they get
// is ambiguous [offer] keeps their id instead. Guessing where a parameter's name
// stops would trade a cosmetic defect for an inexact mapping.
func route(op string) (method string, segs []segment, endsInID, ok bool) {
	head, rest, found := strings.Cut(op, "_")
	method = strings.ToLower(head)
	if !found || !httpMethod[method] {
		return "", nil, false, false
	}
	after := false
	toks := strings.Split(rest, "_")
	for i := 0; i < len(toks); i++ {
		if toks[i] == "by" && i+1 < len(toks) {
			i++ // the parameter's own name
			after, endsInID = true, true
			continue
		}
		if len(segs) == 0 && isVersion(toks[i]) {
			continue
		}
		segs = append(segs, segment{word: toks[i], afterID: after})
		after, endsInID = false, false
	}
	return method, segs, endsInID, true
}

// nouns is the object of the phrase: every literal segment, singular, except
// that a list keeps its last noun plural because a list is what it returns.
func nouns(segs []segment, listing bool) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		if listing && i == len(segs)-1 {
			out[i] = s.word
			continue
		}
		out[i] = singular(s.word)
	}
	return out
}

// join puts a verb and its object together as one operation name, normalising
// the separators a path segment may legally carry ('-', '.') onto the one this
// name already uses.
func join(verb string, obj []string) string {
	s := verb
	if len(obj) > 0 {
		s += "_" + strings.Join(obj, "_")
	}
	return strings.NewReplacer("-", "_", ".", "_").Replace(s)
}

// methodVerb is what an HTTP method MEANS, as the leading word of a phrase.
var methodVerb = map[string]string{
	"get": "get", "head": "check", "post": "create",
	"put": "set", "patch": "update", "delete": "delete", "options": "options",
}

// changes reports whether a method mutates, which is what makes a trailing
// singular segment an action rather than a thing.
//
// It does NOT read [mutatingVerb]. That set is the gate's, it holds policy words
// like `grant` and `login` that are not HTTP methods, and an edit made there for
// a security reason must not silently rename operations. Same four words, two
// jobs, two places — because they are two jobs.
func changes(method string) bool {
	switch method {
	case "post", "put", "patch", "delete":
		return true
	}
	return false
}

// isVersion reports whether a segment is an API version — `v1` and nothing else
// in this fleet, but the shape rather than the value, because a `v2` would be
// just as much scaffolding.
func isVersion(w string) bool {
	if len(w) < 2 || w[0] != 'v' {
		return false
	}
	for _, r := range w[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// plural reports whether a segment names a COLLECTION.
//
// It is a spelling test and it is allowed to be one: it decides `list_` against
// `get_` and whether to strip an `s`, so its failures are cosmetic and its
// caller is never a gate. The guards are the endings that are not plurals at all
// — `status`, `address`, `analysis` — and a short word, because `dns` is not
// several `dn`.
func plural(w string) bool {
	if len(w) < 4 || !strings.HasSuffix(w, "s") {
		return false
	}
	return !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is")
}

// singular is the member of a collection: `policies`→`policy`,
// `processes`→`process`, `releases`→`release`.
//
// `es` comes off only after a sibilant that REQUIRED it (`sses`, `uses`, `xes`);
// everything else loses one letter, which is what keeps `releases` from becoming
// `releas` and `sizes` from becoming `siz`.
func singular(w string) string {
	switch {
	case !plural(w):
		return w
	case strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "sses"), strings.HasSuffix(w, "uses"), strings.HasSuffix(w, "xes"):
		return w[:len(w)-2]
	}
	return w[:len(w)-1]
}

// offer gives every gathered operation the name the door will publish for it,
// in place, and it is where the mapping is made EXACT.
//
// A phrase is used only when it is unambiguous in both directions across the
// whole gathered set: no second operation produces it, and no operation is
// already called it. Otherwise the operation keeps its id — both of them do,
// when two collide — because a tools/call arrives carrying whatever was
// published and a door that guessed which of two operations was meant would be
// dispatching on a coin toss. 7.3% of the fleet's operations keep their ids, and
// they are overwhelmingly the fleet's own duplicates: `/tasks` and `/v1/tasks`
// serving one handler at two addresses, `post_v1_agent` beside `post_v1_agents`
// in a different subsystem.
//
// This runs over the SURVIVORS. Everything [refuse] withheld is already gone
// (see [Door.gather]), so no phrase can name a refused operation and no refused
// operation can be reached by naming one.
func offer(all []named) {
	ids := make(map[string]string, len(all))
	for _, t := range all {
		ids[t.name] = t.name
	}
	count := make(map[string]int, len(all))
	for i := range all {
		all[i].as = phrase(all[i].name)
		count[all[i].as]++
	}
	for i := range all {
		if taken, held := ids[all[i].as]; count[all[i].as] > 1 || (held && taken != all[i].name) {
			all[i].as = all[i].name
		}
	}
}

// summaryMax is how much of an operation's own documentation fits beside its
// name. See [summary]; the fleet's median first sentence is 63 characters and
// its 90th percentile is 157, so this keeps nearly all of them whole and clips
// the essays.
const summaryMax = 120

// summary is the ONE line of an operation's own documentation that goes in an
// enum: the first sentence of the first paragraph, unwrapped, clipped on a word.
//
// The prose is already there — it is the doc comment zip's generator lifts into
// the descriptor, the same bytes [Door.describe] hands back whole — so this is a
// projection of what the fleet wrote about itself, never a second description
// that could disagree with the first.
func summary(doc string) string {
	para, _, _ := strings.Cut(strings.TrimSpace(doc), "\n\n")
	para = strings.Join(strings.Fields(para), " ")
	if i := strings.Index(para, ". "); i >= 0 {
		para = para[:i+1]
	}
	if len(para) <= summaryMax {
		return para
	}
	cut := para[:summaryMax]
	if i := strings.LastIndexByte(cut, ' '); i > summaryMax/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:—-") + "…"
}
