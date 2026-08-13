// Package tracker is your org's issue tracker: projects, issues, and the filters to
// find them.
//
// It mounts the Hanzo Cloud /v1/tracker/* surface. THE FORGE IS THE STORE
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
//	GET    /v1/tracker/projects                          list boards             -> [Project]
//	GET    /v1/tracker/projects/:key                     board detail            -> Project
//	POST   /v1/tracker/projects                          405, named at the forge
//	PATCH  /v1/tracker/projects/:key                     405, named at the forge
//	DELETE /v1/tracker/projects/:key                     405, named at the forge
//	GET    /v1/tracker/projects/:key/issues[?status=&kind=&repo=&source=&scheduled=]  -> [Issue]
//	POST   /v1/tracker/projects/:key/issues              file an issue           -> Issue (201)
//	PATCH  /v1/tracker/projects/:key/issues/:num         update an issue         -> Issue
//	GET    /v1/tracker/milestones                        org rollup              -> [Milestone]
//	GET    /v1/tracker/issues                            search across the org   -> [Issue]
//	POST   /v1/tracker/projects/:key/issues/:num/claim   take a piece of work
//
// And the UI the surface exists for, embedded in this binary (ui/):
//
//	GET    /tracker, /tracker/*                          the board + timeline SPA
//
// Order 129: binds /v1/tracker/* before the AI subsystem's /v1/* catch-all
// (150). serve.go auto-registers GET /v1/tracker/health.
package tracker

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	trackerui "github.com/hanzoai/cloud/apps/tracker/ui"
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

// state is tracker's own data; shared deps (logger, billing meter) live in the
// embedded cloud.Base, reached as s.Log / s.Bill.
//
// The bill meter is the shared per-org resource gate+meter. A project tracker is
// FREE by default (charging per issue is the wrong product), so the create fee
// defaults to 0 → Gate is a pass-through and Meter a no-op; the seam is wired
// uniformly with every other subsystem and ops can price it per deployment via
// CLOUD_TRACKER_FEE_CENTS[_PROJECT|_ISSUE].
type state struct {
	stores *cloud.OrgStore[*Store] // per-(org,project) tracker DBs, opened once each

	// forge is the SOURCE OF TRUTH for the board (source.go). The reads below are
	// reads OF the forge; nothing here caches or mirrors its rows, because a
	// second copy of a work item is a second answer to what its state is.
	forge *forgeSource
}

// mounted is the active service so Shutdown can release the stores.
var mounted *cloud.Service[state]

// storeFor is the ONE way this package reaches a tracker store. It names the
// database through cloud.OrgNamespace — the single door a validated org walks
// through — and asks the registry for that name, so "which file does this
// request touch" has one answer derived from one input.
//
// tracker is project-scoped: the IAM project (principal.Project, "default" when
// none is selected) is the physical tenant boundary — the tracker's own
// KEY-based projects are rows WITHIN it. org and project MUST already be
// validated: principal.Org/principal.Project for a request, or the caller's own
// server-side resolution for an in-process seam.
func storeFor(s *cloud.Service[state], org, project string) (*Store, error) {
	ns, err := cloud.OrgNamespace(org, project)
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

// Mount wires the tracker surface onto app per HIP-0106. Complex flavour: it
// holds a package-global (mounted) so Shutdown can close every per-tenant store,
// so it constructs the Service value directly rather than via cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("tracker.Mount: nil app")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("tracker.Mount: empty DataDir")
	}
	b := cloud.NewBase(deps, "tracker")
	s := &cloud.Service[state]{Base: b, State: state{
		stores: cloud.NewOrgStore(b, "tracker", openStore),
		forge:  &forgeSource{},
	}}
	mounted = s
	routes(app, s)
	// Register the external-issue mirror sink (cloud.UpsertIssue) now that the store
	// cache is live — the GitHub App webhook + backfill (clients/integrations) reach
	// the tracker through it without importing this package (tracker.go inversion).
	registerIssueSink()
	// The same upsert, offered to the process the feeder actually runs in —
	// integrations holds the GitHub App, and it is not this one (upsert_plane.go).
	exposeUpsert()
	s.Log.Info("tracker mounted", "brand", s.Brand)
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
		{"/v1/tracker/projects", http.MethodPost},
		{"/v1/tracker/projects/:key", http.MethodPatch},
		{"/v1/tracker/projects/:key", http.MethodDelete},
	} {
		openapi.Describe(m.path, m.method,
			"Refused — a board is a repository on the forge",
			"Answers 405. A tracker board IS a repository on this deployment's forge, so creating, "+
				"renaming and deleting one is a FORGE operation carried out with FORGE permissions.\n\n"+
				"Offering it here would put a second door on the same object, guarded by this surface "+
				"instead of by the forge — a weaker guard on the same thing. So the route exists and "+
				"refuses, rather than 404ing: \"not this service's job\" and \"no such thing\" are "+
				"different facts, and the body names the forge so a caller knows where the job IS done.\n\n"+
				"What this surface DOES own is the work on a board: list the boards you can see, read "+
				"and file their issues, move a card between columns, and roll milestones up across the "+
				"org. Those are the routes beside this one.")
	}

	openapi.DescribeSPA("/tracker", "tracker board")
}

// routes registers the tracker surface. Literal routes register before their
// :param siblings so Fiber's first-match scan resolves the collection endpoints
// before the detail ones.
//
// Everything but the three refusals is a TYPED op — one registry entry carrying
// the schema, the prose, an MCP tool, a CLI command and an SDK method. The
// refusals stay raw because they have one answer and no shape to describe; their
// prose is declared above instead, through the registry openapi.Describe shares.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/tracker", requireCSRFOnWrites())
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
	// permissions, and re-exposing them here would be a second door onto the same
	// object with its own weaker guard. Those four routes answer 405 naming the
	// forge (projectLifecycle), rather than 404 — the distinction between "no such
	// route" and "not this service's job" is the whole point.
	zip.Get(g, "/projects", o.forgeProjects)
	zip.Get(g, "/projects/:key", o.forgeProject)
	g.Post("/projects", projectLifecycle)
	g.Patch("/projects/:key", projectLifecycle)
	g.Delete("/projects/:key", projectLifecycle)

	zip.Get(g, "/projects/:key/issues", o.forgeIssues)
	zip.Post(g, "/projects/:key/issues", o.forgeCreateIssue)
	zip.Patch(g, "/projects/:key/issues/:num", o.forgePatchIssue)

	// The org rollup the forge does not offer: milestones are repo-scoped
	// upstream, so the org view is a server-side fan-out (forge.Client.Milestones).
	zip.Get(g, "/milestones", o.forgeMilestones)

	// FIND WORK, AND TAKE IT. These read the local store rather than the forge:
	// the forge answers per repository, and the question people ask is "is anyone
	// tracking X" — where X may be a mirrored GitHub issue under the default
	// project, or something filed from the helpdesk. Every source lands in the
	// store (github_sink.go), so it is the only place that can answer.
	zip.Get(g, "/issues", o.searchIssues)
	zip.Post(g, "/projects/:key/issues/:num/claim", o.claimIssue)

	// The UI is a static asset bundle embedded in THIS binary (ui/) — the board
	// and timeline over the surface above. Serving it here is what lets cloud
	// front tracker.hanzo.ai and retire the Huly tracker that answered that host.
	//
	// NOT a typed op and never will be: these routes answer HTML and hashed
	// assets under their own content types and cache hints, plus index.html for
	// every path the client router owns. A typed op publishes JSON. ui/
	// embed_test.go pins the bytes.
	//
	// Same origin as /v1/tracker above, which is the point rather than a
	// convenience: the SPA sends no tenancy of its own because the composer's
	// identity check has already minted the validated org for both halves.
	ui := zip.AdaptNetHTTP(http.StripPrefix("/tracker", trackerui.Handler()))
	app.All("/tracker", ui)
	app.All("/tracker/*", ui)
}

// unsafe is the set of methods that CHANGE a board. Everything else is a read.
var unsafe = map[string]bool{
	http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true,
}

// requireCSRFOnWrites is the anti-CSRF gate for every tracker write, installed
// ONCE on the group rather than six times on the routes.
//
// THREAT. A browser authenticates this surface from an httpOnly session COOKIE,
// which is AMBIENT: a page on any other origin that can reach us carries it too.
// The deployment's CORS policy reflects `*.hanzo.ai` with credentials, and that
// wildcard covers hosts that serve arbitrary user content — so a page there can
// read and write another org's boards with the visitor's own session. The
// positive control is a token the caller can only obtain by READING a same-origin
// response (GET /v1/csrf; the Same-Origin Policy stops a cross-site page reading
// it) and must echo in a CUSTOM header (which a simple form POST cannot set
// without a preflight we do not grant).
//
// The gate itself is apps/account's — the estate has ONE anti-CSRF token, minted
// by GET /v1/csrf and bound to the caller's validated identity, and it verifies
// here byte-identically because both processes key it from the same KMS-sourced
// CONSOLE_CSRF_KEY. This does not re-implement any of that; it only decides WHEN
// to apply it.
//
// ON THE GROUP, BY METHOD, not per route: a gate written six times is a gate the
// seventh write forgets. Reads pass through untouched (a GET changes nothing, and
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

type trackerProject struct {
	ID          string `json:"id"`
	Org         string `json:"org"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type issueView struct {
	ID          string   `json:"id"`
	Identifier  string   `json:"identifier"` // KEY-<number>, the human handle
	ProjectKey  string   `json:"projectKey"`
	Number      int      `json:"number"`
	Kind        string   `json:"kind"`             // issue | pr | epic
	Source      string   `json:"source"`           // team | git | crm | helpdesk | cms | agent
	Repo        string   `json:"repo,omitempty"`   // git repo binding
	ExtRef      string   `json:"extRef,omitempty"` // external anchor
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority"`
	Assignee    string   `json:"assignee,omitempty"`
	Labels      []string `json:"labels"`
	StartAt     int64    `json:"startAt,omitempty"` // unix seconds; absent = unscheduled
	DueAt       int64    `json:"dueAt,omitempty"`   // unix seconds; absent = no due date
	CreatedAt   int64    `json:"createdAt"`
	UpdatedAt   int64    `json:"updatedAt"`
}

// ---- helpers ----

// maxScheduleAt is the far edge of the interval this tracker will store:
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
//   - a negative bound, which is not a point in time this tracker recognises and
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

// Shutdown closes every open per-(org,project) tracker store. Idempotent.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	err := mounted.State.stores.CloseAll()
	mounted = nil
	return err
}
