// Package tracker is your org's issue tracker: projects, issues, and the filters to
// find them.
//
// It mounts the Hanzo Cloud /v1/tracker/* surface — a native-Go, per-org tracker on
// SQLite. It is the durable
// replacement for the prior Svelte hanzo.team tracker, whose upstream each-block
// reactive-batching render race left issue lists rendering zero rows. Native
// @hanzo/gui over this one store sidesteps that entire class of bug: the rows
// come back as plain JSON and render deterministically.
//
// Org isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED
// bearer owner claim (HIP-0026) — and NEVER a client-supplied header. Every
// store query filters WHERE org=?, so one org can never read or mutate
// another's projects or issues.
//
// Surface (all org-scoped; /v1 only):
//
//	POST   /v1/tracker/projects                          create a project        -> Project (201)
//	GET    /v1/tracker/projects                          list projects           -> [Project]
//	GET    /v1/tracker/projects/:key                     project detail          -> Project
//	PATCH  /v1/tracker/projects/:key                     update a project        -> Project
//	DELETE /v1/tracker/projects/:key                     delete a project (+ issues)
//	POST   /v1/tracker/projects/:key/issues              create an issue         -> Issue (201)
//	GET    /v1/tracker/projects/:key/issues[?status=&kind=&repo=&source=&scheduled=]  -> [Issue]
//	GET    /v1/tracker/projects/:key/issues/:num         issue detail            -> Issue
//	PATCH  /v1/tracker/projects/:key/issues/:num         update an issue         -> Issue
//	DELETE /v1/tracker/projects/:key/issues/:num         delete an issue
//
// And the UI the surface exists for, embedded in this binary (ui/):
//
//	GET    /tracker, /tracker/*                          the board + timeline SPA
//
// Order 129: binds /v1/tracker/* before the AI subsystem's /v1/* catch-all
// (150). serve.go auto-registers GET /v1/tracker/health.
package tracker

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/apps/principal"
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

// sources is the closed set of opening surfaces — which product ORIGINATED the
// work item. Empty defaults to "team". Orthogonal to kind and validated
// independently. source=helpdesk means "an engineering issue opened FROM a
// support escalation", not "a helpdesk ticket" (a ticket is a DocType).
var sources = map[string]bool{
	"team": true, "git": true, "crm": true, "helpdesk": true, "cms": true, "agent": true,
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

// requestStore is storeFor for a request: the project is the one the gateway
// validated, never a value the handler names itself.
func requestStore(s *cloud.Service[state], c *zip.Ctx, org string) (*Store, error) {
	return storeFor(s, org, principal.Project(c))
}

// Mount wires the tracker surface onto app per HIP-0106. Complex flavour: it
// holds a package-global (mounted) so Shutdown can close every per-tenant store,
// so it constructs the Service value directly rather than via cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("tracker.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("tracker.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("tracker.Mount: empty DataDir")
	}
	b := cloud.NewBase(deps, "tracker")
	s := &cloud.Service[state]{Base: b, State: state{
		stores: cloud.NewOrgStore(b, "tracker", openStore),
	}}
	mounted = s
	routes(app, s)
	// Register the external-issue mirror sink (cloud.UpsertIssue) now that the store
	// cache is live — the GitHub App webhook + backfill (clients/integrations) reach
	// the tracker through it without importing this package (tracker_seam inversion).
	registerIssueSink()
	s.Log.Info("tracker mounted", "brand", s.Brand)
	return nil
}

// The PROSE for the two creates. Every other operation on this surface is a typed
// op whose description zipdoc lifts from its doc comment; these two are raw by
// construction (typed.go names the blocker), so they have no comment to lift and
// were reaching the document — and every SDK and CLI generated from it — as an
// operationId and nothing else. That left the tracker able to explain reading,
// updating and deleting a board, and unable to explain making one.
//
// Written as the missing half of the SAME story the typed siblings tell: the KEY is
// what listProjects and getProject address a board by, and the identifier
// getIssue answers with is the number assigned HERE. Keyed on the fiber pattern the
// routes below register, so a description whose route moved stops rendering rather
// than drifting.
func init() {
	openapi.Describe("/v1/tracker/projects", http.MethodPost,
		"Open a tracker board in your org",
		"Creates a board and returns it, including the KEY that will prefix every issue "+
			"identifier filed under it — the same key GET, PATCH and DELETE address the board by, and "+
			"the ENG in ENG-14.\n\n"+
			"`name` is required. `key` is optional and is UPPERCASED: omit it and one is derived from "+
			"the name — its first four letters and digits, or PRJ when that yields nothing usable. A "+
			"key that is not 2-8 characters starting with a letter is 400.\n\n"+
			"THE KEY IS UNIQUE PER ORG AND A COLLISION IS REFUSED, NOT MERGED: a second board on a key "+
			"already taken is 409, and the derived key is not made unique for you, so two similarly "+
			"named boards collide and the second caller must name a key. Re-POSTing is therefore not "+
			"idempotent — it fails rather than returning the existing board.\n\n"+
			"The org is the validated bearer's own, never a client header, and the board is stored "+
			"under the caller's selected IAM PROJECT: the same key in two IAM projects is two "+
			"unrelated boards. 403 without a validated org.\n\n"+
			"Free by default. The create runs the shared per-org balance gate at a fee of zero unless "+
			"a deployment prices it, and a priced deployment out of balance refuses with the nested "+
			"{\"error\":{\"code\",\"message\"}} body at 402/503 rather than a flat error.")

	openapi.Describe("/v1/tracker/projects/:key/issues", http.MethodPost,
		"File an issue on a tracker board",
		"Files a work item on one board and returns it, carrying the `identifier` — KEY-<number> — "+
			"it will be known by everywhere else.\n\n"+
			"THE NUMBER IS THE SERVER'S TO ASSIGN and is not accepted from the caller: it is the "+
			"board's highest plus one, taken inside the insert's own transaction, and it counts PER "+
			"BOARD — ENG-1 and OPS-1 are different issues.\n\n"+
			"`title` is required; everything else is optional and defaults. `kind` (issue, pr, epic) "+
			"says what the item IS, `source` (team, git, crm, helpdesk, cms, agent) says which surface "+
			"OPENED it, and the two are orthogonal — an issue escalated from support is "+
			"kind=issue&source=helpdesk. `status` defaults to backlog, `priority` to none. A value "+
			"outside one of these closed sets is 400, never silently defaulted. `labels` may not "+
			"contain a comma, the storage separator.\n\n"+
			"`startAt` and `dueAt` place the item on the TIMELINE, in unix seconds, and both "+
			"default to unset. A due date on its own is a milestone — an interval of zero length "+
			"— and the two together are a bar; a start with no due date is work under way with no "+
			"deadline. There is no separate milestone resource: a milestone is this row, dated. A "+
			"negative bound, or a due date before its start, is 400 rather than a silently "+
			"reordered interval.\n\n"+
			"`repo` and `extRef` RECORD an external binding; they do not create one. Filing here "+
			"writes to your tracker and reaches no external system — nothing is pushed to GitHub. The "+
			"GitHub integration runs the other way, mirroring upstream issues INTO this tracker.\n\n"+
			"404 when the caller's org has no board under that key. The org is the validated bearer's "+
			"own and the board is resolved within the caller's selected IAM project; 403 without a "+
			"validated org. Free by default, on the same balance gate as the board create — an epic, "+
			"a pull request and an issue are priced identically, since the fee is per work item rather "+
			"than per kind.")
}

// routes registers the tracker surface. Literal routes register before their
// :param siblings so Fiber's first-match scan resolves the collection endpoints
// before the detail ones.
//
// Everything but the two creates is a TYPED op (typed.go) — one registry entry
// carrying the schema, the prose, an MCP tool, a CLI command and an SDK method.
// The creates stay raw because their balance denial is a body zip's error type
// cannot express; typed.go states that refusal in full. Their prose is declared
// above instead, through the registry openapi.Register shares.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	g := app.Group("/v1/tracker", requireCSRFOnWrites())
	// cloud.Bridge is not installed here: the composer installs it once at the
	// root, after the identity check that mints the validated org and before any
	// subsystem registers a route — an order only the whole program can assert.
	// The ops below read what it parks off the context.

	// UNTYPED BY DESIGN — the pre-create balance gate renders its denial with
	// cloud.DenyResource, the fleet's nested {"error":{"code","message"}} at
	// 402/503, which a typed op's returned error cannot carry. See typed.go.
	g.Post("/projects", cloud.Handle(s, createProject))
	zip.Get(g, "/projects", o.listProjects)
	zip.Get(g, "/projects/:key", o.getProject)
	zip.Patch(g, "/projects/:key", o.updateProject)
	zip.Delete(g, "/projects/:key", o.deleteProject)

	// UNTYPED BY DESIGN — same balance gate, same nested denial. See typed.go.
	g.Post("/projects/:key/issues", cloud.Handle(s, createIssue))
	zip.Get(g, "/projects/:key/issues", o.listIssues)
	zip.Get(g, "/projects/:key/issues/:num", o.getIssue)
	zip.Patch(g, "/projects/:key/issues/:num", o.updateIssue)
	zip.Delete(g, "/projects/:key/issues/:num", o.deleteIssue)

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

// ---- HTTP response shapes (the published contract) ----

type trackerProject struct {
	ID          string `json:"id"`
	Org         string `json:"org"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

func toProjectView(p Project) trackerProject {
	return trackerProject{
		ID: p.ID, Org: p.Org, Key: p.Key, Name: p.Name, Description: p.Description,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
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

func toIssueView(projectKey string, i Issue) issueView {
	return issueView{
		ID:         i.ID,
		Identifier: fmt.Sprintf("%s-%d", projectKey, i.Number),
		ProjectKey: projectKey,
		Number:     i.Number,
		Kind:       i.Kind, Source: i.Source, Repo: i.Repo, ExtRef: i.ExtRef,
		Title: i.Title, Description: i.Description,
		Status: i.Status, Priority: i.Priority, Assignee: i.Assignee,
		Labels:  splitLabels(i.Labels),
		StartAt: i.StartAt, DueAt: i.DueAt,
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
	}
}

// ---- project handlers ----

type createProjectReq struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

func createProject(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := requestStore(s, c, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	var body createProjectReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || len(name) > maxField {
		return zip.ErrBadRequest("name is required (<=256 chars)")
	}
	key := strings.ToUpper(strings.TrimSpace(body.Key))
	if key == "" {
		key = deriveKey(name)
	}
	if !keyRE.MatchString(key) {
		return zip.ErrBadRequest("key must match ^[A-Z][A-Z0-9]{1,7}$")
	}
	desc := strings.TrimSpace(body.Description)
	if len(desc) > maxDesc {
		return zip.ErrBadRequest("description too long")
	}

	kind := "project"
	fee := createFeeCents(kind)
	project, projectValidated := principal.ValidatedProject(c)
	if err := s.Bill.Gate(c.Context(), principal.Ledger(c), project, projectValidated, kind, fee); err != nil {
		return cloud.DenyResource(c, err)
	}

	id, err := genID("prj")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	p := Project{ID: id, Org: org, Key: key, Name: name, Description: desc, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateProject(c.Context(), p); err != nil {
		if errors.Is(err, errConflict) {
			return zip.ErrConflict("project key already exists in this org")
		}
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	s.Bill.Meter(principal.Ledger(c), principal.Project(c), kind, fee, c.RequestID(), cloud.ClientIP(c))
	return c.JSON(http.StatusCreated, toProjectView(p))
}

// ---- issue handlers ----

// project resolves the caller's project by :key from the given per-org store,
// or answers the right HTTP error.
func project(s *cloud.Service[state], c *zip.Ctx, store *Store, org string) (Project, error) {
	p, err := store.GetProject(c.Context(), org, keyParam(c))
	if errors.Is(err, errNotFound) {
		return Project{}, zip.ErrNotFound("project not found")
	}
	if err != nil {
		return Project{}, zip.Errorf(http.StatusInternalServerError, "get project: %v", err)
	}
	return p, nil
}

type createIssueReq struct {
	Kind        string   `json:"kind"`   // issue|pr|epic, default issue
	Source      string   `json:"source"` // team|git|crm|helpdesk|cms|agent, default team
	Repo        string   `json:"repo"`   // git repo binding (kind pr/issue from git)
	ExtRef      string   `json:"extRef"` // external anchor (PR branch, or link into another plane)
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority"`
	Assignee    string   `json:"assignee"`
	Labels      []string `json:"labels"`
	StartAt     int64    `json:"startAt"` // unix seconds; 0/omitted = unscheduled
	DueAt       int64    `json:"dueAt"`   // unix seconds; 0/omitted = no due date
}

func createIssue(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := requestStore(s, c, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	p, err := project(s, c, store, org)
	if err != nil {
		return err
	}
	var body createIssueReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	title := strings.TrimSpace(body.Title)
	if title == "" || len(title) > maxTitle {
		return zip.ErrBadRequest("title is required (<=512 chars)")
	}
	kind, err := normKind(body.Kind)
	if err != nil {
		return err
	}
	source, err := normSource(body.Source)
	if err != nil {
		return err
	}
	status, err := normStatus(body.Status)
	if err != nil {
		return err
	}
	priority, err := normPriority(body.Priority)
	if err != nil {
		return err
	}
	labels, err := normLabels(body.Labels)
	if err != nil {
		return err
	}
	desc := strings.TrimSpace(body.Description)
	if len(desc) > maxDesc {
		return zip.ErrBadRequest("description too long")
	}
	assignee := strings.TrimSpace(body.Assignee)
	if len(assignee) > maxField {
		return zip.ErrBadRequest("assignee too long")
	}
	repo := strings.TrimSpace(body.Repo)
	if len(repo) > maxField {
		return zip.ErrBadRequest("repo too long")
	}
	extRef := strings.TrimSpace(body.ExtRef)
	if len(extRef) > maxField {
		return zip.ErrBadRequest("extRef too long")
	}
	if err := checkSchedule(body.StartAt, body.DueAt); err != nil {
		return err
	}

	// Billing category is the constant "issue" tracker row — an issue costs the
	// same whatever kind it discriminates into, and ops prices it via
	// CLOUD_TRACKER_FEE_CENTS_ISSUE. Decoupled from the polymorphic work-item Kind.
	const billKind = "issue"
	fee := createFeeCents(billKind)
	project, projectValidated := principal.ValidatedProject(c)
	if err := s.Bill.Gate(c.Context(), principal.Ledger(c), project, projectValidated, billKind, fee); err != nil {
		return cloud.DenyResource(c, err)
	}

	id, err := genID("issue")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	i := Issue{
		ID: id, ProjectID: p.ID, Org: org,
		Kind: kind, Source: source, Repo: repo, ExtRef: extRef,
		Title: title, Description: desc, Status: status, Priority: priority,
		Assignee: assignee, Labels: labels,
		StartAt: body.StartAt, DueAt: body.DueAt,
		CreatedAt: now, UpdatedAt: now,
	}
	created, err := store.CreateIssue(c.Context(), i)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	s.Bill.Meter(principal.Ledger(c), principal.Project(c), billKind, fee, c.RequestID(), cloud.ClientIP(c))
	return c.JSON(http.StatusCreated, toIssueView(p.Key, created))
}

// ---- helpers ----

// org resolves the org — the org-isolation KEY — for a request, using
// c.Org() EXACTLY as SanitizeIdentity minted it from the validated IAM owner
// claim (HIP-0026). Mirrors clients/crm and clients/prompts.
func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// keyParam returns the uppercased :key path segment (project keys are stored
// uppercase; the URL is matched case-insensitively).
func keyParam(c *zip.Ctx) string { return strings.ToUpper(strings.TrimSpace(c.Param("key"))) }

func normStatus(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "backlog", nil
	}
	if !statuses[s] {
		return "", zip.ErrBadRequest("unknown status")
	}
	return s, nil
}

func normPriority(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "none", nil
	}
	if !priorities[s] {
		return "", zip.ErrBadRequest("unknown priority")
	}
	return s, nil
}

func normKind(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "issue", nil
	}
	if !kinds[s] {
		return "", zip.ErrBadRequest("unknown kind")
	}
	return s, nil
}

func normSource(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "team", nil
	}
	if !sources[s] {
		return "", zip.ErrBadRequest("unknown source")
	}
	return s, nil
}

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

// normLabels trims, validates and comma-joins labels for storage. A label is a
// short tag; empty entries are dropped and a comma inside a label is rejected
// (the storage separator).
func normLabels(in []string) (string, error) {
	var out []string
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if len(l) > maxLabel {
			return "", zip.ErrBadRequest("label too long")
		}
		if strings.ContainsRune(l, ',') {
			return "", zip.ErrBadRequest("label must not contain a comma")
		}
		out = append(out, l)
	}
	return strings.Join(out, ","), nil
}

func splitLabels(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
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

// createFeeCents resolves the flat create fee for a tracker resource. Unlike
// provisioning (infra that always costs), a project tracker is FREE by default —
// charging per issue is the wrong product. The billing seam is still wired
// (Gate + Meter) for uniformity, and ops can price it per deployment via
// CLOUD_TRACKER_FEE_CENTS[_PROJECT|_ISSUE]; default 0 = free and un-gated.
func createFeeCents(kind string) int64 {
	if v, ok := parseFee(os.Getenv("CLOUD_TRACKER_FEE_CENTS_" + strings.ToUpper(kind))); ok {
		return v
	}
	if v, ok := parseFee(os.Getenv("CLOUD_TRACKER_FEE_CENTS")); ok {
		return v
	}
	return 0
}

func parseFee(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
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
