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
	"github.com/hanzoai/cloud/clients/principal"
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
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("tracker.Mount: nil zip.App")
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
	s.Log.Info("tracker mounted", "brand", s.Brand)
	return nil
}

// routes registers the tracker surface. Literal routes register before their
// :param siblings so Fiber's first-match scan resolves the collection endpoints
// before the detail ones.
func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1/tracker")
	g.Post("/projects", cloud.Handle(s, createProject))
	g.Get("/projects", cloud.Handle(s, listProjects))
	g.Get("/projects/:key", cloud.Handle(s, getProject))
	g.Patch("/projects/:key", cloud.Handle(s, updateProject))
	g.Delete("/projects/:key", cloud.Handle(s, deleteProject))

	g.Post("/projects/:key/issues", cloud.Handle(s, createIssue))
	g.Get("/projects/:key/issues", cloud.Handle(s, listIssues))
	g.Get("/projects/:key/issues/:num", cloud.Handle(s, getIssue))
	g.Patch("/projects/:key/issues/:num", cloud.Handle(s, updateIssue))
	g.Delete("/projects/:key/issues/:num", cloud.Handle(s, deleteIssue))
}

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
	if err := s.Bill.Gate(c.Context(), principal.HomeOrg(c), project, projectValidated, kind, fee); err != nil {
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
	s.Bill.Meter(principal.HomeOrg(c), principal.Project(c), kind, fee, c.RequestID(), cloud.ClientIP(c))
	return c.JSON(http.StatusCreated, toProjectView(p))
}

func listProjects(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, c, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	rows, err := store.ListProjects(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]projectView, 0, len(rows))
	for _, p := range rows {
		out = append(out, toProjectView(p))
	}
	return c.JSON(http.StatusOK, out)
}

func getProject(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, c, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	p, err := store.GetProject(c.Context(), org, keyParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	return c.JSON(http.StatusOK, toProjectView(p))
}

type updateProjectReq struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func updateProject(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, c, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	p, err := store.GetProject(c.Context(), org, keyParam(c))
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("project not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	var body updateProjectReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if n == "" || len(n) > maxField {
			return zip.ErrBadRequest("name cannot be empty (<=256 chars)")
		}
		p.Name = n
	}
	if body.Description != nil {
		d := strings.TrimSpace(*body.Description)
		if len(d) > maxDesc {
			return zip.ErrBadRequest("description too long")
		}
		p.Description = d
	}
	p.UpdatedAt = time.Now().Unix()
	if err := store.UpdateProject(c.Context(), p); err != nil {
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("project not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	return c.JSON(http.StatusOK, toProjectView(p))
}

func deleteProject(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, c, org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	deleted, err := store.DeleteProject(c.Context(), org, keyParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("project not found")
	}
	return c.NoContent(http.StatusNoContent)
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
	if err := s.Bill.Gate(c.Context(), principal.HomeOrg(c), project, projectValidated, billKind, fee); err != nil {
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
	s.Bill.Meter(principal.HomeOrg(c), principal.Project(c), billKind, fee, c.RequestID(), cloud.ClientIP(c))
	return c.JSON(http.StatusCreated, toIssueView(p.Key, created))
}

func listIssues(s *cloud.Service[state], c *zip.Ctx) error {
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
	filter, err := issueFilter(c)
	if err != nil {
		return err
	}
	rows, err := store.ListIssues(c.Context(), org, p.ID, filter)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]issueView, 0, len(rows))
	for _, i := range rows {
		out = append(out, toIssueView(p.Key, i))
	}
	return c.JSON(http.StatusOK, out)
}

func getIssue(s *cloud.Service[state], c *zip.Ctx) error {
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
	num, err := numParam(c)
	if err != nil {
		return err
	}
	i, err := store.GetIssue(c.Context(), org, p.ID, num)
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("issue not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	return c.JSON(http.StatusOK, toIssueView(p.Key, i))
}

type updateIssueReq struct {
	Title       *string   `json:"title"`
	Description *string   `json:"description"`
	Status      *string   `json:"status"`
	Priority    *string   `json:"priority"`
	Assignee    *string   `json:"assignee"`
	Labels      *[]string `json:"labels"`
}

func updateIssue(s *cloud.Service[state], c *zip.Ctx) error {
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
	num, err := numParam(c)
	if err != nil {
		return err
	}
	i, err := store.GetIssue(c.Context(), org, p.ID, num)
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("issue not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	var body updateIssueReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if body.Title != nil {
		t := strings.TrimSpace(*body.Title)
		if t == "" || len(t) > maxTitle {
			return zip.ErrBadRequest("title cannot be empty (<=512 chars)")
		}
		i.Title = t
	}
	if body.Description != nil {
		d := strings.TrimSpace(*body.Description)
		if len(d) > maxDesc {
			return zip.ErrBadRequest("description too long")
		}
		i.Description = d
	}
	if body.Status != nil {
		st, err := normStatus(*body.Status)
		if err != nil {
			return err
		}
		i.Status = st
	}
	if body.Priority != nil {
		pr, err := normPriority(*body.Priority)
		if err != nil {
			return err
		}
		i.Priority = pr
	}
	if body.Assignee != nil {
		a := strings.TrimSpace(*body.Assignee)
		if len(a) > maxField {
			return zip.ErrBadRequest("assignee too long")
		}
		i.Assignee = a
	}
	if body.Labels != nil {
		lb, err := normLabels(*body.Labels)
		if err != nil {
			return err
		}
		i.Labels = lb
	}
	i.UpdatedAt = time.Now().Unix()
	if err := store.UpdateIssue(c.Context(), i); err != nil {
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("issue not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	return c.JSON(http.StatusOK, toIssueView(p.Key, i))
}

func deleteIssue(s *cloud.Service[state], c *zip.Ctx) error {
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
	num, err := numParam(c)
	if err != nil {
		return err
	}
	deleted, err := store.DeleteIssue(c.Context(), org, p.ID, num)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("issue not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- helpers ----

// org resolves the org — the org-isolation KEY — for a request, using
// c.Org() EXACTLY as SanitizeIdentity minted it from the validated IAM owner
// claim (HIP-0026). Mirrors clients/crm and clients/prompts.
func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// keyParam returns the uppercased :key path segment (project keys are stored
// uppercase; the URL is matched case-insensitively).
func keyParam(c *zip.Ctx) string { return strings.ToUpper(strings.TrimSpace(c.Param("key"))) }

// numParam parses the :num path segment into a positive issue number.
func numParam(c *zip.Ctx) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(c.Param("num")))
	if err != nil || n <= 0 {
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

// issueFilter builds an IssueFilter from the ?status=&kind=&repo=&source= query,
// rejecting any value outside a closed set (repo is a free-form binding, only
// length-bounded). This is the ONE place a surface's slice of the shared issue
// table is expressed: hanzo.team passes none/status, a git repo's Issues tab
// ?kind=issue&repo=<r>, its PRs tab ?kind=pr&repo=<r>.
func issueFilter(c *zip.Ctx) (IssueFilter, error) {
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && !statuses[status] {
		return IssueFilter{}, zip.ErrBadRequest("unknown status filter")
	}
	kind := strings.TrimSpace(c.Query("kind"))
	if kind != "" && !kinds[kind] {
		return IssueFilter{}, zip.ErrBadRequest("unknown kind filter")
	}
	source := strings.TrimSpace(c.Query("source"))
	if source != "" && !sources[source] {
		return IssueFilter{}, zip.ErrBadRequest("unknown source filter")
	}
	repo := strings.TrimSpace(c.Query("repo"))
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
