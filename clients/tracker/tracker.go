// Package tracker mounts the Hanzo Cloud /v1/tracker/* surface: a native-Go,
// per-org issue tracker (projects + issues) on SQLite. It is the durable
// replacement for the Huly/Svelte hanzo.team tracker, whose upstream each-block
// reactive-batching render race left issue lists rendering zero rows. Native
// @hanzo/gui over this one store sidesteps that entire class of bug: the rows
// come back as plain JSON and render deterministically.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Tenant(c) — the value SanitizeIdentity minted from the VALIDATED
// bearer owner claim (HIP-0026) — and NEVER a client-supplied header. Every
// store query filters WHERE org=?, so one tenant can never read or mutate
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
//	GET    /v1/tracker/projects/:key/issues[?status=]    list issues (board/list) -> [Issue]
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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
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

type svc struct {
	store *Store
	log   luxlog.Logger
	// bill is the shared per-org resource gate+meter. A project tracker is FREE
	// by default (charging per issue is the wrong product), so the create fee
	// defaults to 0 → Gate is a pass-through and Meter a no-op; the seam is wired
	// uniformly with every other subsystem and ops can price it per deployment via
	// CLOUD_TRACKER_FEE_CENTS[_PROJECT|_ISSUE].
	bill *cloud.ResourceMeter
}

// mounted is the active service so Shutdown can release the store.
var mounted *svc

// Mount wires the tracker surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("tracker.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("tracker.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "tracker")
	if deps.DataDir == "" {
		return fmt.Errorf("tracker.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("tracker.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "tracker.db"))
	if err != nil {
		return fmt.Errorf("tracker.Mount: open store: %w", err)
	}
	s := &svc{store: store, log: log, bill: cloud.NewResourceMeter(deps, "tracker")}
	mounted = s

	// Literal routes register before their :param siblings so Fiber's first-match
	// scan resolves the collection endpoints before the detail ones.
	app.Post("/v1/tracker/projects", s.createProject)
	app.Get("/v1/tracker/projects", s.listProjects)
	app.Get("/v1/tracker/projects/:key", s.getProject)
	app.Patch("/v1/tracker/projects/:key", s.updateProject)
	app.Delete("/v1/tracker/projects/:key", s.deleteProject)

	app.Post("/v1/tracker/projects/:key/issues", s.createIssue)
	app.Get("/v1/tracker/projects/:key/issues", s.listIssues)
	app.Get("/v1/tracker/projects/:key/issues/:num", s.getIssue)
	app.Patch("/v1/tracker/projects/:key/issues/:num", s.updateIssue)
	app.Delete("/v1/tracker/projects/:key/issues/:num", s.deleteIssue)

	log.Info("tracker mounted", "brand", deps.Brand)
	return nil
}

func init() {
	cloud.Register("tracker", 129, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("tracker.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
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
		Title:      i.Title, Description: i.Description,
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

func (s *svc) createProject(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
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
	if err := s.bill.Gate(c.Context(), org, principal.Project(c), kind, fee); err != nil {
		return cloud.DenyResource(c, err)
	}

	id, err := genID("prj")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	p := Project{ID: id, Org: org, Key: key, Name: name, Description: desc, CreatedAt: now, UpdatedAt: now}
	if err := s.store.CreateProject(c.Context(), p); err != nil {
		if errors.Is(err, errConflict) {
			return zip.ErrConflict("project key already exists in this org")
		}
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	s.bill.Meter(org, principal.Project(c), kind, fee, c.RequestID(), cloud.ClientIP(c))
	return c.JSON(http.StatusCreated, toProjectView(p))
}

func (s *svc) listProjects(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.store.ListProjects(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]projectView, 0, len(rows))
	for _, p := range rows {
		out = append(out, toProjectView(p))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *svc) getProject(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, keyParam(c))
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

func (s *svc) updateProject(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.store.GetProject(c.Context(), org, keyParam(c))
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
	if err := s.store.UpdateProject(c.Context(), p); err != nil {
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("project not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	return c.JSON(http.StatusOK, toProjectView(p))
}

func (s *svc) deleteProject(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	deleted, err := s.store.DeleteProject(c.Context(), org, keyParam(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("project not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- issue handlers ----

// project resolves the caller's project by :key or answers the right HTTP error.
func (s *svc) project(c *zip.Ctx, org string) (Project, error) {
	p, err := s.store.GetProject(c.Context(), org, keyParam(c))
	if errors.Is(err, errNotFound) {
		return Project{}, zip.ErrNotFound("project not found")
	}
	if err != nil {
		return Project{}, zip.Errorf(http.StatusInternalServerError, "get project: %v", err)
	}
	return p, nil
}

type createIssueReq struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Priority    string   `json:"priority"`
	Assignee    string   `json:"assignee"`
	Labels      []string `json:"labels"`
}

func (s *svc) createIssue(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.project(c, org)
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

	kind := "issue"
	fee := createFeeCents(kind)
	if err := s.bill.Gate(c.Context(), org, principal.Project(c), kind, fee); err != nil {
		return cloud.DenyResource(c, err)
	}

	id, err := genID("issue")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	now := time.Now().Unix()
	i := Issue{
		ID: id, ProjectID: p.ID, Org: org,
		Title: title, Description: desc, Status: status, Priority: priority,
		Assignee: assignee, Labels: labels, CreatedAt: now, UpdatedAt: now,
	}
	created, err := s.store.CreateIssue(c.Context(), i)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	s.bill.Meter(org, principal.Project(c), kind, fee, c.RequestID(), cloud.ClientIP(c))
	return c.JSON(http.StatusCreated, toIssueView(p.Key, created))
}

func (s *svc) listIssues(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.project(c, org)
	if err != nil {
		return err
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && !statuses[status] {
		return zip.ErrBadRequest("unknown status filter")
	}
	rows, err := s.store.ListIssues(c.Context(), org, p.ID, status)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]issueView, 0, len(rows))
	for _, i := range rows {
		out = append(out, toIssueView(p.Key, i))
	}
	return c.JSON(http.StatusOK, out)
}

func (s *svc) getIssue(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.project(c, org)
	if err != nil {
		return err
	}
	num, err := numParam(c)
	if err != nil {
		return err
	}
	i, err := s.store.GetIssue(c.Context(), org, p.ID, num)
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

func (s *svc) updateIssue(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.project(c, org)
	if err != nil {
		return err
	}
	num, err := numParam(c)
	if err != nil {
		return err
	}
	i, err := s.store.GetIssue(c.Context(), org, p.ID, num)
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
	if err := s.store.UpdateIssue(c.Context(), i); err != nil {
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("issue not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "update: %v", err)
	}
	return c.JSON(http.StatusOK, toIssueView(p.Key, i))
}

func (s *svc) deleteIssue(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	p, err := s.project(c, org)
	if err != nil {
		return err
	}
	num, err := numParam(c)
	if err != nil {
		return err
	}
	deleted, err := s.store.DeleteIssue(c.Context(), org, p.ID, num)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return zip.ErrNotFound("issue not found")
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- helpers ----

// tenant resolves the org — the tenant-isolation KEY — for a request, using
// c.Org() EXACTLY as SanitizeIdentity minted it from the validated IAM owner
// claim (HIP-0026). Mirrors clients/crm and clients/prompts.
func tenant(c *zip.Ctx) (string, bool) { return principal.Tenant(c) }

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

// Shutdown closes the tracker store. Idempotent.
func Shutdown() error {
	if mounted == nil || mounted.store == nil {
		return nil
	}
	err := mounted.store.Close()
	mounted = nil
	return err
}
