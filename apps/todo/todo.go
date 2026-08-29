// Package todo is Hanzo Todo: boards, the work items on them, and the filters
// that make a board.
//
// ONE NAME. The product, the package, the route, the host and the image all say
// `todo`, because a work item has one home and it should take one word to say
// where. The package was called `tracker` while the product was called Todo, on
// the argument that the package named the WORK-ITEM PLANE (contract.go) and the
// product named what a person opens — which is a real distinction and was still
// two names for one thing: /v1/tracker under todo.hanzo.ai, a `tracker_` skill
// telling an agent to call an endpoint the title bar calls something else.
//
// It mounts the Hanzo Cloud /v1/todo/* surface. THE FORGE IS THE STORE
// (source.go): a board is a repository on git.hanzo.ai and an issue is its issue,
// so the reads below are reads OF the forge and nothing here mirrors them — a
// second copy of a work item is a second answer to what its state is. The local
// per-org SQLite (store.go) holds only what the forge cannot answer: the org-wide
// index every source lands in (github_sink.go), which is what makes "is anyone
// tracking X" a question with an answer.
//
// Org isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED
// bearer owner claim (HIP-0026) — and NEVER a client-supplied header. Every
// store query filters WHERE org=?, so one org can never read or mutate
// another's projects or issues.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/todo/projects                          list boards             -> [Project]
//	GET    /v1/todo/projects/:key                     board detail            -> Project
//	POST   /v1/todo/projects                          405, named at the forge
//	PATCH  /v1/todo/projects/:key                     405, named at the forge
//	DELETE /v1/todo/projects/:key                     405, named at the forge
//	GET    /v1/todo/projects/:key/issues[?status=&kind=&repo=&label=&source=&scheduled=]  -> [Issue]
//	POST   /v1/todo/projects/:key/issues              file an issue           -> Issue (201)
//	GET    /v1/todo/projects/:key/issues/:num         one issue, in full      -> Issue
//	PATCH  /v1/todo/projects/:key/issues/:num         update an issue         -> Issue
//	GET    /v1/todo/board[?status=&kind=&repo=&label=&source=&scheduled=]         -> [Issue]
//	GET    /v1/todo/issues                            search across the org   -> [Issue]
//	POST   /v1/todo/projects/:key/issues/:num/claim   take a piece of work
//
// THE UI IS NOT HERE. todo.hanzo.ai serves the SPA at its own root from its own
// image (hanzoai/admin apps/todo -> ghcr.io/hanzoai/todo), and this binary used
// to serve a SECOND copy of the same bundle at /todo/*. Two builds of one source
// on two origins drift, and they did: measured live, the two answered different
// asset hashes under different titles. The API is cross-origin from the page by
// design, so nothing about the request path wanted them in one process.
//
// Order 129: binds /v1/todo/* before the AI subsystem's /v1/* catch-all
// (150). serve.go auto-registers GET /v1/todo/health.
package todo

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

const (
	// maxTitle / maxField / maxDesc cap text so an unbounded body can't amplify
	// the shared DB or a list response.
	maxTitle = 512
	maxField = 256
	maxDesc  = 32768
	maxLabel = 48
)

// keyRE constrains a project key to a short, uppercase, DNS/identifier-safe
// token. The key is the org-unique handle AND the URL segment AND the issue
// identifier prefix (KEY-<number>), so this is the injection/traversal guard at
// the boundary. Input is uppercased before matching.
var keyRE = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,7}$`)

// statuses is the closed board-column set (Linear-style). A create/update with
// an unknown status is rejected; empty defaults to "backlog".
var statuses = map[string]bool{
	"backlog": true, "todo": true, "in_progress": true, "done": true, "canceled": true,
}

// priorities is the closed priority set. Empty defaults to "none".
var priorities = map[string]bool{
	"none": true, "urgent": true, "high": true, "medium": true, "low": true,
}

// kinds is the closed set of work-item shapes — what a row IS. Empty defaults to
// "issue". Deliberately minimal (see contract.go): a work item is an issue, a
// git pull request, or a parent epic. NOT "task" (that word is the async plane,
// hanzoai/tasks) and NOT "deal"/"ticket"/"doc" (those are domain records on
// framework.DocType / crm, a different plane — keep them there).
var kinds = map[string]bool{
	"issue": true, "pr": true, "epic": true,
}

// state is todo's own data; shared deps (logger, billing meter) live in the
// embedded cloud.Base, reached as s.Log / s.Bill.
//
// The bill meter is the shared per-org resource gate+meter. A project todo is
// FREE by default (charging per issue is the wrong product), so the create fee
// defaults to 0 → Gate is a pass-through and Meter a no-op; the client is wired
// uniformly with every other subsystem and ops can price it per deployment via
// CLOUD_TODO_FEE_CENTS[_PROJECT|_ISSUE].
type state struct {
	stores *cloud.OrgStore[*Store] // per-(org,project) todo DBs, opened once each

	// forge is the SOURCE OF TRUTH for the board (source.go). The reads below are
	// reads OF the forge; nothing here caches or mirrors its rows, because a
	// second copy of a work item is a second answer to what its state is.
	forge *forgeSource
}

// mounted is the active service so Shutdown can release the stores.
var mounted *cloud.Service[state]

// store is where this app's per-tenant rows live, and it is NOT the app's name.
//
// cek derives each database's encryption key with HKDF over the namespace AND
// this string (cek.DeriveKey), and namespace.Path renders it into the filename.
// So it is a KEY BINDING, and changing it is a re-key of every tenant's database
// rather than a rename. What actually happens is worse than an error: the old
// file stays on disk untouched, a NEW empty one opens beside it, and the board
// comes up blank with nothing logged. Measured, on the rename that produced this
// constant — `tracker.db` intact, the read answering "no such table: issues".
//
// It therefore keeps the spelling the rows were written under, and the product's
// name is the value that moved. Migrating it would be a full logical copy per
// tenant under two keys — and it buys nothing but a filename nobody sees.
// (A sidecar-backed store would in fact move with its .dek under the same key;
// cek.Convert is the primitive for the derived case. Possible, just pointless.)
// TestStoreKeepsTheNameItsRowsWereWrittenUnder pins it.
const store = "tracker"

// storeFor is the ONE way this package reaches a todo store. It names the
// database through cloud.OrgNamespace — the single path a validated org takes —
// and asks the registry for that name, so "which file does this request touch"
// has one answer derived from one input.
//
// todo is project-scoped: the IAM project (principal.Project, "default" when
// none is selected) is the physical tenant boundary — the todo's own
// KEY-based projects are rows WITHIN it. org and project MUST already be
// validated: principal.Org/principal.Project for a request, or the caller's own
// server-side resolution for an in-process client.
func storeFor(s *cloud.Service[state], org, project string) (*Store, error) {
	ns, err := cloud.OrgNamespace(org, project)
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// Mount wires the todo surface onto app per HIP-0106. Complex flavour: it
// holds a package-global (mounted) so Shutdown can close every per-tenant store,
// so it constructs the Service value directly rather than via cloud.Use.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("todo.Use:  nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("todo.Use:  empty DataDir")
	}
	b := cloud.NewBase(deps, "todo")
	s := &cloud.Service[state]{Base: b, State: state{
		stores: cloud.NewOrgStore(b, store, openStore),
		forge:  &forgeSource{},
	}}
	mounted = s
	routes(app, s)
	// Register the external-issue mirror sink (cloud.UpsertIssue) now that the store
	// cache is live — the GitHub App webhook + backfill (clients/integrations) reach
	// the todo through it without importing this package (todo.go inversion).
	registerIssueSink()
	// The board becomes callable by an agent. Registered here, after the store
	// cache is live, for the same reason the issue sink is: a provider that can
	// be listed before it can reach a store would answer its first call with an
	// error that reads like a missing board.
	tools.Register(todoToolProvider{})
	// The same upsert, offered to the process the feeder actually runs in —
	// integrations holds the GitHub App, and it is not this one (upsert_plane.go).
	exposeUpsert()
	s.Log.Info("todo mounted", "brand", s.Brand)
	return nil
}

// The PROSE for the three refusals. Every other operation on this surface is a
// typed op whose description zipdoc lifts from its doc comment; these three are
// raw handlers answering ONE refusal (projectLifecycle in source.go), so there is
// no comment to lift and they would otherwise reach the document — and every SDK
// and CLI generated from it — as an operationId and nothing else, which reads
// exactly like a route nobody bothered to describe rather than one that is
// deliberately not here.
//
// Keyed on the fiber pattern the routes below register, so a description whose
// route moved stops rendering rather than drifting.
func init() {
	for _, m := range []struct{ path, method string }{
		{"/v1/todo/projects", http.MethodPost},
		{"/v1/todo/projects/:key", http.MethodPatch},
		{"/v1/todo/projects/:key", http.MethodDelete},
	} {
		openapi.Describe(m.path, m.method,
			"Refused — a board is a repository on the forge",
			"Answers 405. A todo board IS a repository on this deployment's forge, so creating, "+
				"renaming and deleting one is a FORGE operation carried out with FORGE permissions.\n\n"+
				"Offering it here would put a second endpoint on the same object, guarded by this surface "+
				"instead of by the forge — a weaker guard on the same thing. So the route exists and "+
				"refuses, rather than 404ing: \"not this service's job\" and \"no such thing\" are "+
				"different facts, and the body names the forge so a caller knows where the job IS done.\n\n"+
				"What this surface DOES own is the work on a board: list the boards you can see, read "+
				"and file their issues, move a card between columns, and read the schedule "+
				"a milestone is. Those are the routes beside this one.")
	}
}

// routes registers the todo surface. Literal routes register before their
// :param siblings so Fiber's first-match scan resolves the collection endpoints
// before the detail ones.
//
// Everything but the three refusals is a TYPED op — one registry entry carrying
// the schema, the prose, an MCP tool, a CLI command and an SDK method. The
// refusals stay raw because they have one answer and no shape to describe; their
// prose is declared above instead, through the registry openapi.Describe shares.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/todo", requireCSRFOnWrites())
	// cloud.Bridge is not installed here: the composer installs it once at the
	// root, after the identity check that mints the validated org and before any
	// subsystem registers a route — an order only the whole program can assert.
	// The ops below read what it parks off the context.

	// THE FORGE IS THE STORE (source.go). Every route below reads and writes
	// git.hanzo.ai; none of them touches a local table. That is not a preference —
	// two stores under one prefix would be two answers to "what is the state of
	// this work", and they would disagree the first time anyone used the forge
	// directly, which is every day.
	//
	// A BOARD IS A REPOSITORY, so the repository lifecycle is NOT on this surface:
	// creating, renaming and deleting a board are forge operations with forge
	// permissions, and re-exposing them here would be a second endpoint onto the
	// same object with its own weaker guard. Those four routes answer 405 naming the
	// forge (projectLifecycle), rather than 404 — the distinction between "no such
	// route" and "not this service's job" is the whole point.
	zip.Get(g, "/projects", o.forgeProjects)
	zip.Get(g, "/projects/:key", o.forgeProject)
	g.Post("/projects", projectLifecycle)
	g.Patch("/projects/:key", projectLifecycle)
	g.Delete("/projects/:key", projectLifecycle)

	zip.Get(g, "/projects/:key/issues", o.forgeIssues)
	zip.Post(g, "/projects/:key/issues", o.forgeCreateIssue)
	// ONE work item, at the address PATCH already accepts. Every other read here
	// answers a board, and a board is a summary — it carries no description, which
	// is where the content of a work item actually is. An item you can move and
	// cannot read is half an address.
	zip.Get(g, "/projects/:key/issues/:num", o.forgeIssue)
	zip.Patch(g, "/projects/:key/issues/:num", o.forgePatchIssue)

	// THE GLOBAL BOARD — every board's work in one set of columns. Same op as the
	// per-board list above with its key left unbound, because the key is a FILTER
	// and not an address: the forge fan-out those routes make is org-wide already
	// and then narrows, so this is the narrowing declined rather than a second
	// implementation to keep in step.
	//
	// It is also how a board smaller than a repository exists. The estate's apps
	// are directories inside one repository, so there is no repo per app to point
	// at; `?label=app/meet` is the meet board, and nothing had to be provisioned
	// for it. 140 boards cost 140 queries, not 140 objects.
	zip.Get(g, "/board", o.forgeIssues)

	// FIND WORK, AND TAKE IT. These read the local store rather than the forge:
	// the forge answers per repository, and the question people ask is "is anyone
	// tracking X" — where X may be a mirrored GitHub issue under the default
	// project, or something filed from the helpdesk. Every source lands in the
	// store (github_sink.go), so it is the only place that can answer.
	zip.Get(g, "/issues", o.searchIssues)
	zip.Post(g, "/projects/:key/issues/:num/claim", o.claimIssue)

	// A CHANNEL'S WORK, AT A GLANCE (room.go). Same store and same filter as
	// /issues?room=, projected to counts instead of rows, so the header a channel
	// renders and the list beneath it are two readings of one query rather than
	// two answers to it. It reads the local index and needs no forge, which is
	// what lets a channel show its work in an org that has no forge namespace.
	zip.Get(g, "/rooms/:room", o.roomWorkOf)

}

// changes and reads say what an operation DOES, so no call site carries a bare
// boolean. It cannot be read off the HTTP method: over the MCP door every
// operation arrives as one POST to /mcp, so the method describes the transport
// and says nothing about the operation.
const (
	changes = true
	reads   = false
)

// csrf asks the estate's ONE anti-CSRF control (apps/account) from inside a typed
// op, for the operations that change a board.
//
// IT IS HERE AND NOT ONLY ON THE GROUP, because a typed op is TWO fields of one
// registry entry — the route's handler and the op — and zip wraps only the
// handler. Six seams reach the op: the REST route, MCP, the call plane, GraphQL,
// the CLI and Here. A tools/call at POST /mcp therefore never meets
// requireCSRFOnWrites below, while the depth-0 identity middleware still
// authenticates the caller. The preamble is the one place every seam passes
// through; the group keeps the same control for the untyped routes, which have no
// preamble to put it in. One rule, two ways to ask it, never two rules.
//
// source_test.go measures all three writes on both doors.
func csrf(ctx context.Context, act bool) error {
	if act == reads {
		return nil
	}
	return account.CSRF(ctx)
}

// unsafe is the set of methods that CHANGE a board. Everything else is a read.
var unsafe = map[string]bool{
	http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true,
}

// requireCSRFOnWrites is the same control on the ROUTE, for the routes that are
// not typed ops and so have no preamble to put it in.
//
// It is not what protects the three typed writes — csrf above is, because this
// never runs for them off the REST door. It stays because a raw handler under
// /v1/todo has only this: the three repository-lifecycle refusals today, and
// whatever untyped route is written next, which would otherwise be the one write
// on this surface with no control at all.
//
// THREAT. A browser authenticates this surface from an httpOnly session COOKIE,
// which is AMBIENT: a page on any other origin that can reach us carries it too.
// The deployment's CORS policy reflects `*.hanzo.ai` with credentials, and that
// wildcard covers hosts that serve arbitrary user content — so a page there can
// read and write another org's boards with the visitor's own session. The
// positive control is a token the caller can only obtain by READING a same-origin
// response (GET /v1/account/csrf; the Same-Origin Policy stops a cross-site page reading
// it) and must echo in a CUSTOM header (which a simple form POST cannot set
// without a preflight we do not grant).
//
// The gate itself is apps/account's — the estate has ONE anti-CSRF token, minted
// by GET /v1/account/csrf and bound to the caller's validated identity, and it verifies
// here byte-identically because both processes key it from the same KMS-sourced
// CONSOLE_CSRF_KEY. This does not re-implement any of that; it only decides WHEN
// to apply it.
//
// BY METHOD, which is sound HERE and only here: this reads the path the
// TRANSPORT carried, so it runs on a real /v1/todo/* request whose method really
// is the operation. That is exactly what an op cannot assume — hence the act
// constant above. Reads pass through untouched (a GET changes nothing, and
// requiring a token to list a board would break every server-side reader), and
// account's own gate is a no-op for a Bearer/gateway caller, which cannot be
// CSRF'd — so this costs an API client nothing.
func requireCSRFOnWrites() zip.Handler {
	gate := account.RequireCSRF()
	return func(c *zip.Ctx) error {
		if !unsafe[c.Method()] {
			return c.Next()
		}
		return gate(c)
	}
}

// ---- the published response shapes ----
//
// Built by the forge reads (source.go) and by the org-wide search over the local
// index (search.go). Both spell the same two views, because a caller must not be
// able to tell which source answered from the shape of the answer.

type todoProject struct {
	// ID is the board's opaque handle, and it is NOT how you address it — Key is.
	// Its shape says which source answered: a forge board's is the repository's
	// full name ("hanzoai/cloud"), an index board's a minted "prj_" id.
	ID string `json:"id"`
	// Org is the IAM org the board belongs to, taken from the validated principal
	// and never from the request. Every board a caller can see is in it.
	Org string `json:"org"`
	// Key addresses the board everywhere else — /v1/todo/projects/<key>/issues —
	// and prefixes every issue identifier on it. An index board's key is 2-8
	// uppercase alphanumerics starting with a letter ("ENG", "OPS2") and is matched
	// case-insensitively; a forge board's is the repository name as the forge
	// spells it.
	Key string `json:"key"`
	// Name is the board's display name. For a forge board it is the repository
	// name, so it equals Key; an index board carries its own.
	Name string `json:"name"`
	// Description is whatever an index board was created with. Absent on a forge
	// board: this projection takes the repository's name and nothing else about the
	// repository.
	Description string `json:"description,omitempty"`
	// CreatedAt is when the board was created, in unix seconds. 0 on a forge board
	// for the same reason Description is absent.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the board record last changed, in unix seconds — the BOARD,
	// not the work on it, so filing an issue does not move it. 0 on a forge board.
	UpdatedAt int64 `json:"updatedAt"`
}

type issueView struct {
	// ID is the work item's opaque handle, and it is NOT how you address it —
	// ProjectKey plus Number is. Its shape says which source answered: a forge
	// issue's is the forge's own numeric id in decimal, an index row's a minted
	// "issue_" id.
	ID string `json:"id"`
	// Identifier is the human handle, "<key>#<number>" — the board and the number
	// on it, joined. ONE spelling whichever source answered, because a list where
	// forge rows read cli#1 and index rows read OPS-3 is two products in one list.
	Identifier string `json:"identifier"`
	// ProjectKey is the board this item is on: the repository name for a forge
	// issue, the index board's key otherwise. With Number it is the item's address
	// in every other route.
	ProjectKey string `json:"projectKey"`
	// Number is the item's number ON ITS BOARD, from 1 and monotonic there — the
	// forge's own issue number for a forge row, allocated inside the create
	// transaction for an index row so it cannot race. Unique per board, never
	// across the org.
	Number int `json:"number"`
	// Kind is what the item IS: issue, pr or epic. Set once at create and never
	// changed, so a row does not migrate between surfaces. Deliberately not "task"
	// — that word is the async plane (contract.go).
	Kind string `json:"kind"`
	// Source is which surface OPENED it: team, git, crm, helpdesk, cms or agent.
	// Also set once. It is the ORIGIN, not the subject — source=helpdesk is an
	// engineering issue opened from a support escalation, not a support ticket.
	Source string `json:"source"`
	// Repo is the git repository the item is bound to, so a repository's Issues and
	// PRs tabs are filters over this one table. Absent when the item is not
	// repo-bound.
	Repo string `json:"repo,omitempty"`
	// ExtRef anchors the item to something outside the todo — a mirrored issue
	// ("github:owner/repo#123"), a pushed PR branch, or a record on another plane.
	// It is the idempotency key the mirror upsert matches on. Absent when the item
	// has no external origin.
	ExtRef string `json:"extRef,omitempty"`
	// Title is the item's one-line summary.
	Title string `json:"title"`
	// Description is the body, markdown as its author wrote it. Absent when empty.
	Description string `json:"description,omitempty"`
	// Status is the board column: backlog, todo, in_progress, done or canceled, and
	// nothing else. On a forge row it is read off a LABEL, so relabelling in the
	// forge web UI moves the card here and vice versa — and a CLOSED forge issue
	// reads done whatever its labels say. Never empty: "backlog" when nothing names
	// a column.
	Status string `json:"status"`
	// Priority is urgent, high, medium, low or none. Also a label on a forge row.
	// Never empty: "none" when nothing names one, so callers compare a value rather
	// than test for absence.
	Priority string `json:"priority"`
	// Assignee is who holds the work — an IAM username, or the login of the FIRST
	// assignee when a forge issue has several. Absent when nobody holds it, which
	// is exactly the state a claim needs.
	Assignee string `json:"assignee,omitempty"`
	// Labels are the item's remaining tags, with the status and priority labels
	// lifted OUT — a column that stayed here would render twice, once as the card's
	// column and once as a chip on the card. Always present; empty is [].
	Labels []string `json:"labels"`
	// StartAt is when the work starts, in unix seconds; absent means unscheduled.
	// A forge row takes it from when the issue was opened, but only once the issue
	// has a due date — an interval needs both ends.
	StartAt int64 `json:"startAt,omitempty"`
	// DueAt is when the work is due, in unix seconds; absent means no due date. A
	// forge row takes it from its MILESTONE's due date, since a forge issue has no
	// deadline of its own. Never before StartAt, and never past 2200-01-01.
	DueAt int64 `json:"dueAt,omitempty"`
	// CreatedAt is when the item was opened, in unix seconds. 0 when the source
	// gave no parseable timestamp.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when it last changed, in unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// ---- helpers ----

// maxScheduleAt is the far edge of the interval this todo will store:
// 2200-01-01T00:00:00Z. A project plan does not reach past it, and the number
// beyond it is never a date — it is a value that got into a date field.
//
// It exists because "non-negative" was not a bound. A caller could store
// dueAt = 2^63-1, which is a legal int64 and a nonsense instant, and the
// timeline then tried to draw a grid from today to the year 292 billion: one
// accepted write, and every member of that org loading the board's timeline hit
// an unrecoverable render. A stored value that breaks the reader for everyone
// who looks at it is the write's fault, so it is refused at the write.
//
// The client clamps too (ui timeline.ts domainOf) — the two are not redundant.
// This stops the row existing; that stops any row, however it arrived, from
// taking the view down. Neither is load-bearing alone.
const maxScheduleAt int64 = 7258118400

// checkSchedule validates the timeline interval an issue carries. Both bounds
// are unix seconds and 0 means unset, so the three legal shapes are: neither
// (unscheduled), a due date alone (a milestone — an interval of zero length),
// and both (a bar). The refusals are the ones an interval cannot survive:
//
//   - a negative bound, which is not a point in time this todo recognises and
//     would render a bar reaching off the left edge of every viewport;
//   - a bound past maxScheduleAt, which is not a plan (see above);
//   - an end before its beginning, which is not an interval at all. Refused at
//     the boundary rather than normalised, because silently swapping a caller's
//     dates is a mutation they did not ask for and cannot see.
//
// A start with no due date IS legal: work that has begun and has no deadline is
// a real state, and the timeline draws it from its start to today.
func checkSchedule(startAt, dueAt int64) error {
	if startAt < 0 || dueAt < 0 {
		return zip.ErrBadRequest("startAt and dueAt are unix seconds and cannot be negative")
	}
	if startAt > maxScheduleAt || dueAt > maxScheduleAt {
		return zip.ErrBadRequest("startAt and dueAt must be before 2200-01-01")
	}
	if startAt > 0 && dueAt > 0 && dueAt < startAt {
		return zip.ErrBadRequest("dueAt cannot be before startAt")
	}
	return nil
}

// deriveKey builds a fallback project key from a display name: the leading
// letters, uppercased, capped — used when the caller omits an explicit key. The
// result is validated by keyRE before use.
func deriveKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteByte(byte(r))
		}
		if b.Len() >= 4 {
			break
		}
	}
	k := b.String()
	if k == "" || (k[0] >= '0' && k[0] <= '9') {
		return "PRJ"
	}
	return k
}

// Shutdown closes every open per-(org,project) todo store. Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.stores.CloseAll()
	mounted = nil
	return err
}
