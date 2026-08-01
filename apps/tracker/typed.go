package tracker

// typed.go is tracker's TYPED half: every READ, every UPDATE and every DELETE of
// the projects/issues surface, as zip ops rather than raw fiber handlers.
//
// A typed op is ONE registry entry with N projections — the OpenAPI operation's
// schema and prose, the MCP tool, the CLI command and every generated SDK method
// all come from it. An untyped route gets a route and nothing else.
//
// THE TWO CREATES STAY UNTYPED, and it is a wire fact rather than an omission.
// POST /v1/tracker/projects and POST /v1/tracker/projects/:key/issues both run
// the pre-create balance gate and render a denial with cloud.DenyResource
// (tracker.go), which answers the fleet-wide NESTED {"error":{"code","message"}}
// at 402/503. A typed op can only refuse by RETURNING an error, which zip renders
// as its flat {"status","code","error"}; and writing the nested body from inside
// the op does not escape it either, because a nil Out makes zip stamp
// cmp.Or(op.Status, 204) over the 402 it just wrote (zip typed.go:305). The fee
// defaults to 0 — so the gate is a pass-through on a default deployment — but
// CLOUD_TRACKER_FEE_CENTS[_PROJECT|_ISSUE] prices it, and a route that changes
// shape under a supported configuration has changed shape. Same refusal, same
// reason, as apps/provisioning's creates.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops binds the service to the typed ops. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value (o.listProjects), which is
// also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// noInput is the In of an op that takes nothing off the wire — it is addressed
// entirely by the caller's validated principal.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. It is an
// ALIAS for the unnamed empty struct, not a definition: zip keys the response on
// 204 only when the Out type has no name, so a defined type here would publish
// "200 with a body" about a route that answers 204 with none.
type noContent = struct{}

// projectList is one org's tracker projects, newest first. Empty is an empty
// JSON array, never null.
type projectList []trackerProject

// issueList is one project's issues. Empty is an empty JSON array, never null.
type issueList []issueView

// scope resolves the three facts every tracker op needs and the untyped creates
// beside it resolve the same way: the request (for the IAM project sub-scope that
// picks the physical store), the VALIDATED org, and the per-(org,project) store.
//
// The org comes from principal.OrgFrom — the value cloud.Bridge parked from the
// validated bearer claim — and NEVER from an In field: an In field is
// caller-supplied, so a tenant key read from one is a cross-tenant read the
// caller asserted for itself. It fails closed off the HTTP path (a CLI
// LocalInvoke has no request and therefore no attested tenant), with the same 403
// the raw handlers answer.
func (o ops) scope(ctx context.Context) (*zip.Ctx, string, *Store, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", nil, zip.ErrForbidden("X-Org-Id required")
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, "", nil, zip.ErrForbidden("X-Org-Id required")
	}
	store, err := requestStore(o.s, c, org)
	if err != nil {
		return nil, "", nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	return c, org, store, nil
}

// requireBody replays, at the point in the sequence the raw handler reached it,
// the refusal c.Bind has always answered: a PATCH here takes a JSON body, and a
// request with none — or with a content type this service does not parse — is a
// 400, not a silent no-op update.
//
// zip's typed decode is TOLERANT by construction (it skips an empty body and
// leaves the In at its zero value), so a naive conversion would have turned every
// bodyless PATCH from 400 into 200-with-nothing-changed. That is a change in what
// the route ACCEPTS, invisible to any test that only reads the happy path. This
// is the same c.Bind the raw handler called, over an empty target, so it is the
// same decision and the same message — not a re-implementation free to drift.
//
// It runs AFTER the tenancy and lookup gates for the same reason the raw handler
// bound the body there: 403 and 404 outrank a malformed body. One residual
// difference survives and is pinned in typed_wire_test.go — a body zip itself
// cannot parse is refused before the handler runs, so a request that is BOTH
// unparseable AND aimed at a missing row now answers 400 where it answered 404.
func requireBody(ctx context.Context) error {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil // off the HTTP path there is no body to require
	}
	return c.Bind(&struct{}{})
}

// projectOf resolves one of the caller's tracker projects by its key, or answers
// the 404 the raw handler answers. The key is uppercased and trimmed exactly as
// keyParam does, because project keys are stored uppercase and the URL matches
// case-insensitively.
func projectOf(ctx context.Context, store *Store, org, key string) (Project, error) {
	p, err := store.GetProject(ctx, org, strings.ToUpper(strings.TrimSpace(key)))
	if errors.Is(err, errNotFound) {
		return Project{}, zip.ErrNotFound("project not found")
	}
	if err != nil {
		return Project{}, zip.Errorf(http.StatusInternalServerError, "get project: %v", err)
	}
	return p, nil
}

// ----- projects -------------------------------------------------------------

// ListProjects returns every tracker project in the caller's org, newest first.
//
// A project is the board: it owns a KEY (the uppercase handle that prefixes every
// issue identifier, "ENG-14") and the issues filed under it. The listing is
// org-scoped server-side — the org is the validated bearer claim, never a
// client-supplied header — so one org can never see another's boards.
func (o ops) listProjects(ctx context.Context, _ *noInput) (*projectList, error) {
	_, org, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := store.ListProjects(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make(projectList, 0, len(rows))
	for _, p := range rows {
		out = append(out, toProjectView(p))
	}
	return &out, nil
}

// projectRef addresses ONE of the caller's tracker projects by its key. The key
// is the path segment — the URL is the addressing authority — and GET and DELETE
// carry no request body at all, so there is nothing a caller could smuggle a
// second key in through.
type projectRef struct {
	// Key is the project's org-unique handle: 2-8 uppercase alphanumerics starting
	// with a letter ("ENG", "OPS2"). Matched case-insensitively.
	Key string `json:"key"`
}

// GetProject returns one tracker project of the caller's org by its key —
// its name, description and timestamps. 404 when the org has no project
// under that key.
func (o ops) getProject(ctx context.Context, in *projectRef) (*trackerProject, error) {
	_, org, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectOf(ctx, store, org, in.Key)
	if err != nil {
		return nil, err
	}
	v := toProjectView(p)
	return &v, nil
}

// projectPatch updates a tracker project. Every field is OPTIONAL and absent
// means "leave it alone" — a field the caller omits is not touched.
type projectPatch struct {
	// Key is the project to update, from the path.
	Key string `json:"key"`
	// Name is the project's display name. Non-empty, at most 256 characters.
	Name *string `json:"name"`
	// Description is the board's free-form blurb, at most 32768 characters.
	Description *string `json:"description"`
}

// UpdateProject renames a tracker project or rewrites its description, and
// returns the updated project. Both fields are optional: one the caller omits
// keeps its stored value.
//
// The project KEY is never editable — it prefixes every issue identifier already
// filed under the board, so changing it would rewrite the human handle of every
// issue in it.
//
// Example: {"name": "Platform Engineering"}
func (o ops) updateProject(ctx context.Context, in *projectPatch) (*trackerProject, error) {
	_, org, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectOf(ctx, store, org, in.Key)
	if err != nil {
		return nil, err
	}
	if err := requireBody(ctx); err != nil {
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

// DeleteProject removes one tracker project of the caller's org AND every issue
// filed under it, and answers 204 with no body. 404 when the org has no project
// under that key.
//
// The cascade is the point: an issue has no meaning without the board whose key
// names it, so deleting the board deletes them together rather than leaving
// orphans addressable by an identifier that no longer resolves.
func (o ops) deleteProject(ctx context.Context, in *projectRef) (*noContent, error) {
	_, org, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := store.DeleteProject(ctx, org, strings.ToUpper(strings.TrimSpace(in.Key)))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("project not found")
	}
	return nil, nil
}

// ----- issues ---------------------------------------------------------------

// issueQuery lists a project's issues, optionally narrowed. Every filter is a
// query parameter and every one is optional; omitting all of them returns the
// whole board.
type issueQuery struct {
	// Key is the project whose issues to list, from the path.
	Key string `json:"key"`
	// Status keeps only issues in that board column: backlog, todo, in_progress,
	// done or canceled. An unknown value is refused with 400.
	Status string `json:"status"`
	// Kind keeps only work items of that shape: issue, pr or epic. An unknown
	// value is refused with 400.
	Kind string `json:"kind"`
	// Repo keeps only issues bound to that git repository.
	Repo string `json:"repo"`
	// Source keeps only issues opened from that surface: team, git, crm,
	// helpdesk, cms or agent. An unknown value is refused with 400.
	Source string `json:"source"`
}

// ListIssues returns the issues of one tracker project, optionally filtered by
// status, kind, repo and source.
//
// This is the ONE place a surface takes its slice of the shared issue table:
// hanzo.team passes no filter or a status, a git repository's Issues tab passes
// kind=issue&repo=<r> and its Pull Requests tab kind=pr&repo=<r>. A filter value
// outside its closed set is refused with 400 rather than silently returning an
// empty board.
//
// Example: {"key": "ENG", "kind": "pr", "repo": "hanzoai/cloud"}
func (o ops) listIssues(ctx context.Context, in *issueQuery) (*issueList, error) {
	_, org, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectOf(ctx, store, org, in.Key)
	if err != nil {
		return nil, err
	}
	filter, err := issueQueryFilter(in)
	if err != nil {
		return nil, err
	}
	rows, err := store.ListIssues(ctx, org, p.ID, filter)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make(issueList, 0, len(rows))
	for _, i := range rows {
		out = append(out, toIssueView(p.Key, i))
	}
	return &out, nil
}

// issueQueryFilter validates the typed listing's filters against the SAME closed
// sets the untyped issueFilter checks — one vocabulary, two readers, so the typed
// route and the raw one can never disagree about what a legal filter is.
func issueQueryFilter(in *issueQuery) (IssueFilter, error) {
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

// issueRef addresses ONE issue: the project key and the issue's per-project
// number, which together spell the human identifier KEY-<number>.
type issueRef struct {
	// Key is the issue's project, from the path.
	Key string `json:"key"`
	// Num is the issue's number within that project — the digits of KEY-14.
	// Positive; anything else is refused with 400.
	Num int `json:"num"`
}

// GetIssue returns one issue of one tracker project by its per-project number —
// title, description, status, priority, assignee, labels, kind, source and its
// git bindings. 404 when the project or the issue does not exist in the caller's
// org.
func (o ops) getIssue(ctx context.Context, in *issueRef) (*issueView, error) {
	_, org, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectOf(ctx, store, org, in.Key)
	if err != nil {
		return nil, err
	}
	if in.Num <= 0 {
		return nil, zip.ErrBadRequest("issue number must be a positive integer")
	}
	i, err := store.GetIssue(ctx, org, p.ID, in.Num)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("issue not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	v := toIssueView(p.Key, i)
	return &v, nil
}

// issuePatch updates one issue. Every field is OPTIONAL and absent means "leave
// it alone" — a field the caller omits is not touched.
type issuePatch struct {
	// Key is the issue's project, from the path.
	Key string `json:"key"`
	// Num is the issue's number within that project, from the path.
	Num int `json:"num"`
	// Title is the issue's one-line summary. Non-empty, at most 512 characters.
	Title *string `json:"title"`
	// Description is the issue body, at most 32768 characters.
	Description *string `json:"description"`
	// Status moves the issue between board columns: backlog, todo, in_progress,
	// done or canceled. Empty resets it to backlog.
	Status *string `json:"status"`
	// Priority is none, urgent, high, medium or low. Empty resets it to none.
	Priority *string `json:"priority"`
	// Assignee is who owns the issue, at most 256 characters. Empty unassigns it.
	Assignee *string `json:"assignee"`
	// Labels REPLACES the issue's labels with exactly this set. Each label is at
	// most 48 characters and may not contain a comma (the storage separator);
	// empty entries are dropped.
	Labels *[]string `json:"labels"`
}

// UpdateIssue edits one issue in place and returns it — retitle it, rewrite its
// body, move it between board columns, reprioritize, reassign, or replace its
// labels. Every field is optional: one the caller omits keeps its stored value,
// and `labels` REPLACES the set rather than adding to it.
//
// The issue's kind, source and git bindings are not editable here: they record
// where the work item came FROM, which is a fact about its origin rather than
// its current state.
//
// Example: {"key": "ENG", "num": 14, "status": "in_progress", "assignee": "z"}
func (o ops) updateIssue(ctx context.Context, in *issuePatch) (*issueView, error) {
	_, org, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectOf(ctx, store, org, in.Key)
	if err != nil {
		return nil, err
	}
	if in.Num <= 0 {
		return nil, zip.ErrBadRequest("issue number must be a positive integer")
	}
	i, err := store.GetIssue(ctx, org, p.ID, in.Num)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("issue not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	if err := requireBody(ctx); err != nil {
		return nil, err
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

// DeleteIssue removes one issue from a tracker project and answers 204 with no
// body. 404 when the project or the issue does not exist in the caller's org.
//
// The issue's number is NOT reused: the next issue on the board takes the next
// number, so a deleted identifier stays retired rather than silently pointing at
// different work.
func (o ops) deleteIssue(ctx context.Context, in *issueRef) (*noContent, error) {
	_, org, store, err := o.scope(ctx)
	if err != nil {
		return nil, err
	}
	p, err := projectOf(ctx, store, org, in.Key)
	if err != nil {
		return nil, err
	}
	if in.Num <= 0 {
		return nil, zip.ErrBadRequest("issue number must be a positive integer")
	}
	deleted, err := store.DeleteIssue(ctx, org, p.ID, in.Num)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("issue not found")
	}
	return nil, nil
}
