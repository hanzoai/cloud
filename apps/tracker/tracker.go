// Package tracker mounts the Hanzo Cloud /v1/tracker/* surface: a native-Go,
// per-org issue tracker (projects + issues) on SQLite. It is the durable
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
//	GET    /v1/tracker/projects/:key/issues[?status=&kind=&repo=&source=]  list  -> [Issue]
//	GET    /v1/tracker/projects/:key/issues/:num         issue detail            -> Issue
//	PATCH  /v1/tracker/projects/:key/issues/:num         update an issue         -> Issue
//	DELETE /v1/tracker/projects/:key/issues/:num         delete an issue
//
// Order 129: binds /v1/tracker/* before the AI subsystem's /v1/* catch-all
// (150). serve.go auto-registers GET /v1/tracker/health.
package tracker

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
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
	"github.com/hanzoai/cloud/apps/principal"
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

// storeFor resolves the caller's project-scoped tracker store, opening the
// per-(org,project) file ({DataDir}/orgs/{orgSlug}/projects/{projectSlug}/
// tracker.db) once via the shared cache. tracker is project-scoped: the IAM
// project (principal.Project, "default" when none is selected) is the physical
// tenant boundary — the tracker's own KEY-based projects are rows WITHIN it.
func storeFor(s *cloud.Service[state], c *zip.Ctx, org string) (*Store, error) {
	return s.State.stores.For(org, principal.Project(c))
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
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "tracker"), State: state{
		stores: cloud.NewOrgStore(deps.DataDir, "tracker", openStore),
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

// routes registers the tracker surface. Literal routes register before their
// :param siblings so Fiber's first-match scan resolves the collection endpoints
// before the detail ones.
//
// Every read and mutation is a TYPED op — registered on the App with its
// ABSOLUTE path, because the op registry (the one value OpenAPI, MCP and the CLI
// are projected from) keys on it — except the two METERED creates. Those answer
// a billing denial with the fleet-wide contract (cloud.DenyResource: 402
// insufficient_balance / spend_cap_exceeded, 503 balance_unavailable, each a
// {"error":{"code","message"}} body), which zip's error type cannot express, so
// typing them would reshape that error for every metered client.
func routes(app cloud.Router, s *cloud.Service[state]) {
	z := cloud.ZipApp(app)
	o := ops{s: s}
	g := app.Group("/v1/tracker")
	// The bridge FIRST: fiber runs middleware in registration order, so one
	// installed after these leaves would never run — and every op below reaches
	// its request (org, project, billing identity) through it.
	g.Use(cloud.Bridge())
	g.Post("/projects", cloud.Handle(s, createProject)) // metered — see above
	zip.Get(z, "/v1/tracker/projects", o.listProjects)
	zip.Get(z, "/v1/tracker/projects/:key", o.getProject)
	zip.Patch(z, "/v1/tracker/projects/:key", o.updateProject)
	zip.Delete(z, "/v1/tracker/projects/:key", o.deleteProject)

	g.Post("/projects/:key/issues", cloud.Handle(s, createIssue)) // metered — see above
	zip.Get(z, "/v1/tracker/projects/:key/issues", o.listIssues)
	zip.Get(z, "/v1/tracker/projects/:key/issues/:num", o.getIssue)
	zip.Patch(z, "/v1/tracker/projects/:key/issues/:num", o.updateIssue)
	zip.Delete(z, "/v1/tracker/projects/:key/issues/:num", o.deleteIssue)
}

// ops binds the service to tracker's typed handlers: a typed handler takes only a
// context and its decoded In, so the service arrives as a RECEIVER.
type ops struct{ s *cloud.Service[state] }

// scope is the ONE gate every typed op opens with: the request the bridge parked
// plus the VALIDATED org, and the per-(org,project) store both address. Off the
// HTTP path there is no request and no validated org, so the op refuses rather
// than reading across tenants.
func (o ops) scope(ctx context.Context) (*zip.Ctx, string, *Store, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", nil, zip.ErrForbidden("X-Org-Id required")
	}
	orgID, ok := org(c)
	if !ok {
		return nil, "", nil, zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(o.s, c, orgID)
	if err != nil {
		return nil, "", nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	return c, orgID, store, nil
}

// ---- declared shapes (the published contract) ----

// projectRef addresses one project.
type projectRef struct {
	// Key is the project key from the path (e.g. "ENG"), matched case-insensitively.
	Key string `json:"key"`
}

// issueRef addresses one issue inside a project.
type issueRef struct {
	// Key is the project key from the path.
	Key string `json:"key"`
	// Number is the issue number from the path — the N in KEY-N.
	Number int `json:"num"`
}

// issueQuery is the issue list read: the project from the path, narrowed by the
// closed-set filters. This is the ONE place a surface's slice of the shared issue
// table is expressed: hanzo.team passes none or status, a git repo's Issues tab
// kind=issue&repo=<r>, its PRs tab kind=pr&repo=<r>.
type issueQuery struct {
	// Key is the project key from the path.
	Key string `json:"key"`
	// Status narrows to one lifecycle state (backlog|todo|in_progress|done|canceled).
	Status string `json:"status"`
	// Kind narrows to one work-item kind (issue|pr|epic).
	Kind string `json:"kind"`
	// Repo narrows to issues bound to one git repo.
	Repo string `json:"repo"`
	// Source narrows to one origin (team|git|crm|helpdesk|cms|agent).
	Source string `json:"source"`
}

// projectList is the bare array the project list answers with.
type projectList []projectView

// issueList is the bare array the issue list answers with.
type issueList []issueView

// ---- HTTP response shapes (the published contract) ----

type projectView struct {
	ID          string `json:"id"`
	Org         string `json:"org"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

func toProjectView(p Project) projectView {
	return projectView{
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
		Labels:    splitLabels(i.Labels),
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
	store, err := storeFor(s, c, org)
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

// listProjects lists the caller org's tracker projects, newest first.
// Only the caller's own org is ever read: every store query binds org.
//
// Response: [{"id": "prj_1", "org": "acme", "key": "ENG", "name": "Engineering", "createdAt": 1780000000, "updatedAt": 1780000000}]
func (o ops) listProjects(ctx context.Context, _ *struct{}) (*projectList, error) {
	_, orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := store.ListProjects(ctx, orgID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make(projectList, 0, len(rows))
	for _, p := range rows {
		out = append(out, toProjectView(p))
	}
	return &out, nil
}

// getProject returns one project of the caller org by key.
// A key another org owns reads as not found — the query binds org.
//
// Example: {"key": "ENG"}
func (o ops) getProject(ctx context.Context, in *projectRef) (*projectView, error) {
	_, orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectByKey(ctx, store, orgID, normKey(in.Key))
	if err != nil {
		return nil, err
	}
	v := toProjectView(p)
	return &v, nil
}

type updateProjectReq struct {
	// Key is the project to update, taken from the path — a body key is ignored.
	Key string `json:"key"`
	// Name replaces the display name when present; must be 1..256 characters.
	Name *string `json:"name"`
	// Description replaces the description when present; max 8192 characters.
	Description *string `json:"description"`
}

// updateProject edits a project's name or description; an absent field is left alone.
// A key another org owns reads as not found — the query binds org.
//
// Example: {"key": "ENG", "name": "Engineering"}
func (o ops) updateProject(ctx context.Context, in *updateProjectReq) (*projectView, error) {
	_, orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectByKey(ctx, store, orgID, normKey(in.Key))
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > maxField {
			return nil, zip.ErrBadRequest("name cannot be empty (<=256 chars)")
		}
		p.Name = n
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if len(d) > maxDesc {
			return nil, zip.ErrBadRequest("description too long")
		}
		p.Description = d
	}
	p.UpdatedAt = time.Now().Unix()
	if err := store.UpdateProject(ctx, p); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("project not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	v := toProjectView(p)
	return &v, nil
}

// deleteProject deletes a project and every issue in it, and answers 204.
// A key another org owns is a 404, never a delete — the DELETE binds org.
//
// Example: {"key": "ENG"}
func (o ops) deleteProject(ctx context.Context, in *projectRef) (*struct{}, error) {
	_, orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := store.DeleteProject(ctx, orgID, normKey(in.Key))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("project not found")
	}
	return nil, nil
}

// ---- issue handlers ----

// project resolves the caller's project by :key from the given per-org store,
// or answers the right HTTP error.
func project(s *cloud.Service[state], c *zip.Ctx, store *Store, org string) (Project, error) {
	return projectByKey(c.Context(), store, org, keyParam(c))
}

// projectByKey is the ONE project lookup both the raw handlers and the typed ops
// run: the key is already normalized, the store already bound to (org, project).
func projectByKey(ctx context.Context, store *Store, org, key string) (Project, error) {
	p, err := store.GetProject(ctx, org, key)
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
}

func createIssue(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, c, org)
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
		Assignee: assignee, Labels: labels, CreatedAt: now, UpdatedAt: now,
	}
	created, err := store.CreateIssue(c.Context(), i)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	s.Bill.Meter(principal.Ledger(c), principal.Project(c), billKind, fee, c.RequestID(), cloud.ClientIP(c))
	return c.JSON(http.StatusCreated, toIssueView(p.Key, created))
}

// listIssues lists a project's issues, optionally narrowed by status, kind, repo or source.
// A filter value outside its closed set is a 400; a project another org owns
// reads as not found.
//
// Example: {"key": "ENG", "kind": "pr", "repo": "hanzoai/cloud"}
func (o ops) listIssues(ctx context.Context, in *issueQuery) (*issueList, error) {
	_, orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectByKey(ctx, store, orgID, normKey(in.Key))
	if err != nil {
		return nil, err
	}
	filter, err := filterOf(in)
	if err != nil {
		return nil, err
	}
	rows, err := store.ListIssues(ctx, orgID, p.ID, filter)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make(issueList, 0, len(rows))
	for _, i := range rows {
		out = append(out, toIssueView(p.Key, i))
	}
	return &out, nil
}

// getIssue returns one issue of a project by its number.
// An issue in another org's project reads as not found — every query binds org.
//
// Example: {"key": "ENG", "num": 42}
func (o ops) getIssue(ctx context.Context, in *issueRef) (*issueView, error) {
	_, orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectByKey(ctx, store, orgID, normKey(in.Key))
	if err != nil {
		return nil, err
	}
	num, err := issueNumber(in.Number)
	if err != nil {
		return nil, err
	}
	i, err := store.GetIssue(ctx, orgID, p.ID, num)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("issue not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	v := toIssueView(p.Key, i)
	return &v, nil
}

type updateIssueReq struct {
	// Key is the project the issue belongs to, taken from the path.
	Key string `json:"key"`
	// Number is the issue number, taken from the path.
	Number int `json:"num"`
	// Title replaces the title when present; must be 1..512 characters.
	Title *string `json:"title"`
	// Description replaces the description when present; max 8192 characters.
	Description *string `json:"description"`
	// Status moves the issue in the lifecycle (backlog|todo|in_progress|done|canceled).
	Status *string `json:"status"`
	// Priority sets the priority (none|low|medium|high|urgent).
	Priority *string `json:"priority"`
	// Assignee sets the assignee handle; max 256 characters.
	Assignee *string `json:"assignee"`
	// Labels replaces the whole label set; a label may not contain a comma.
	Labels *[]string `json:"labels"`
}

// updateIssue edits an issue's fields; an absent field is left alone.
// An unknown status, priority or label is a 400; an issue in another org's
// project reads as not found.
//
// Example: {"key": "ENG", "num": 42, "status": "in_progress", "assignee": "z"}
func (o ops) updateIssue(ctx context.Context, in *updateIssueReq) (*issueView, error) {
	_, orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectByKey(ctx, store, orgID, normKey(in.Key))
	if err != nil {
		return nil, err
	}
	num, err := issueNumber(in.Number)
	if err != nil {
		return nil, err
	}
	i, err := store.GetIssue(ctx, orgID, p.ID, num)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("issue not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" || len(t) > maxTitle {
			return nil, zip.ErrBadRequest("title cannot be empty (<=512 chars)")
		}
		i.Title = t
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if len(d) > maxDesc {
			return nil, zip.ErrBadRequest("description too long")
		}
		i.Description = d
	}
	if in.Status != nil {
		st, err := normStatus(*in.Status)
		if err != nil {
			return nil, err
		}
		i.Status = st
	}
	if in.Priority != nil {
		pr, err := normPriority(*in.Priority)
		if err != nil {
			return nil, err
		}
		i.Priority = pr
	}
	if in.Assignee != nil {
		a := strings.TrimSpace(*in.Assignee)
		if len(a) > maxField {
			return nil, zip.ErrBadRequest("assignee too long")
		}
		i.Assignee = a
	}
	if in.Labels != nil {
		lb, err := normLabels(*in.Labels)
		if err != nil {
			return nil, err
		}
		i.Labels = lb
	}
	i.UpdatedAt = time.Now().Unix()
	if err := store.UpdateIssue(ctx, i); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("issue not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	v := toIssueView(p.Key, i)
	return &v, nil
}

// deleteIssue deletes one issue of a project and answers 204.
// An issue in another org's project is a 404, never a delete — the query binds org.
//
// Example: {"key": "ENG", "num": 42}
func (o ops) deleteIssue(ctx context.Context, in *issueRef) (*struct{}, error) {
	_, orgID, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectByKey(ctx, store, orgID, normKey(in.Key))
	if err != nil {
		return nil, err
	}
	num, err := issueNumber(in.Number)
	if err != nil {
		return nil, err
	}
	deleted, err := store.DeleteIssue(ctx, orgID, p.ID, num)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("issue not found")
	}
	return nil, nil
}

// ---- helpers ----

// org resolves the org — the org-isolation KEY — for a request, using
// c.Org() EXACTLY as SanitizeIdentity minted it from the validated IAM owner
// claim (HIP-0026). Mirrors clients/crm and clients/prompts.
func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// keyParam returns the uppercased :key path segment (project keys are stored
// uppercase; the URL is matched case-insensitively).
func keyParam(c *zip.Ctx) string { return normKey(c.Param("key")) }

// normKey is the ONE project-key normalization both the raw handlers and the
// typed ops run on whatever the URL carried.
func normKey(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// numParam parses the :num path segment into a positive issue number.
func numParam(c *zip.Ctx) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(c.Param("num")))
	if err != nil || n <= 0 {
		return 0, zip.ErrBadRequest("issue number must be a positive integer")
	}
	return n, nil
}

// issueNumber bounds an issue number a typed op already decoded. A value the URL
// could not parse arrives as 0, which is the same rejection numParam raises.
func issueNumber(n int) (int, error) {
	if n <= 0 {
		return 0, zip.ErrBadRequest("issue number must be a positive integer")
	}
	return n, nil
}

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

// filterOf builds an IssueFilter from the decoded issueQuery, rejecting any value
// outside a closed set (repo is a free-form binding, only length-bounded). The
// filters ride the URL as ?status=&kind=&repo=&source=, which is what issueQuery
// declares them as.
func filterOf(in *issueQuery) (IssueFilter, error) {
	status := strings.TrimSpace(in.Status)
	if status != "" && !statuses[status] {
		return IssueFilter{}, zip.ErrBadRequest("unknown status filter")
	}
	kind := strings.TrimSpace(in.Kind)
	if kind != "" && !kinds[kind] {
		return IssueFilter{}, zip.ErrBadRequest("unknown kind filter")
	}
	source := strings.TrimSpace(in.Source)
	if source != "" && !sources[source] {
		return IssueFilter{}, zip.ErrBadRequest("unknown source filter")
	}
	repo := strings.TrimSpace(in.Repo)
	if len(repo) > maxField {
		return IssueFilter{}, zip.ErrBadRequest("repo filter too long")
	}
	return IssueFilter{Status: status, Kind: kind, Repo: repo, Source: source}, nil
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
