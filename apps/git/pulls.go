package git

// pulls.go is the PROPOSAL noun on the native git plane.
//
// A branch could already be pushed here and read here, and that was the whole
// loop: an agent finished a run, advanced refs/heads/agent/<run>, and the work
// sat at an address with nothing to say about it. There was no way to state what
// the branch was FOR, no way to list what was waiting, and no way to say yes —
// so "an agent opens a pull request per issue" had to be answered by mirroring
// the repository to GitHub and asking GitHub (propose.go). A repository that
// lives only here had no door at all.
//
// A pull request is METADATA plus TWO BRANCH NAMES. It is deliberately not a
// snapshot: base and head are short names, and they keep moving while the
// proposal is open, so merging asks the question again against the refs as they
// are now rather than against the refs as they were when someone typed a title.
//
// Merging itself lives in merge.go, on the other side of the boundary the
// Repository model draws: this file states proposals in branch names and
// revisions and never touches a plumbing.Hash.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
)

// What a proposal may carry. A title is a line and a body is a description;
// neither is a place to park a payload, and the row lives in the org's SQLite
// file alongside its repo metadata.
const (
	maxPullTitle = 300
	maxPullBody  = 64 << 10
)

// ── wire shapes ──────────────────────────────────────────────────────────────

// openReq proposes head for merging into base.
type openReq struct {
	// Name is the repo the proposal belongs to, from the :name path segment.
	Name string `json:"name"`
	// Title is the one-line summary of what is being proposed. Required.
	Title string `json:"title"`
	// Body is the longer description. Optional.
	Body string `json:"body"`
	// Head is the branch holding the work, by short name (agent/fix-503).
	// Required, and must already exist.
	Head string `json:"head"`
	// Base is the branch the work is proposed INTO, by short name. Defaults to
	// the repo's default branch, which is where a proposal goes when nobody says
	// otherwise.
	Base string `json:"base"`
}

// pullRef addresses one proposal of one repo.
type pullRef struct {
	// Name is the repo, from the :name path segment.
	Name string `json:"name"`
	// Number is the proposal's per-repo number, from the :number path segment.
	Number int64 `json:"number"`
}

// pullFilter lists a repo's proposals, optionally narrowed to one state.
type pullFilter struct {
	// Name is the repo, from the :name path segment.
	Name string `json:"name"`
	// State narrows the list to "open" or "merged". Omit it for every proposal.
	State string `json:"state"`
}

// pullView is one proposal: what was asked, and what came of it.
type pullView struct {
	// Number is the proposal's per-repo handle, dense from 1.
	Number int64 `json:"number"`
	// Repo is the repository the proposal belongs to.
	Repo string `json:"repo"`
	// Title is the one-line summary.
	Title string `json:"title"`
	// Body is the longer description; empty when none was given.
	Body string `json:"body,omitempty"`
	// Head is the branch holding the work.
	Head string `json:"head"`
	// Base is the branch the work is proposed into.
	Base string `json:"base"`
	// State is "open" or "merged".
	State string `json:"state"`
	// Author is the user who opened it; empty for a caller with no user.
	Author string `json:"author,omitempty"`
	// MergedRev is what base points at now that the merge landed. Empty while
	// the proposal is open.
	MergedRev string `json:"mergedRev,omitempty"`
	// CreatedAt is RFC 3339 UTC.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is RFC 3339 UTC.
	UpdatedAt string `json:"updatedAt"`
}

// pullList is the collection envelope for a repo's proposals.
type pullList struct {
	// Data holds the repo's pull requests, newest number first.
	Data []pullView `json:"data"`
}

func pullToView(v Pull) pullView {
	return pullView{
		Number: v.Number, Repo: v.Repo, Title: v.Title, Body: v.Body,
		Head: v.Head, Base: v.Base, State: v.State, Author: v.Author,
		MergedRev: v.MergedRev,
		CreatedAt: rfc3339(v.CreatedAt), UpdatedAt: rfc3339(v.UpdatedAt),
	}
}

// ── ops ──────────────────────────────────────────────────────────────────────

// openPull proposes a branch for merging and returns it with its number. Answers
// 201. Both branches must already exist — a proposal naming a branch nobody
// pushed is a typo, not a plan — and base defaults to the repo's default branch.
//
// Proposing the same head into the same base twice is a 409 while the first
// proposal is still open, so a retried agent run leaves ONE thing to review
// rather than a pile of identical ones. A repo outside the caller's scope is a
// 404, exactly as reading it is.
//
// Example: {"name": "widgets", "title": "cache the catalog read",
//
//	"head": "agent/cache-catalog", "base": "main"}
func (o ops) openPull(ctx context.Context, in *openReq) (*pullView, error) {
	t, name, herr := o.scoped(ctx, in.Name)
	if herr != nil {
		return nil, herr
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, zip.ErrBadRequest("title is required")
	}
	if len(title) > maxPullTitle {
		return nil, zip.ErrBadRequest(fmt.Sprintf("title exceeds %d bytes", maxPullTitle))
	}
	if len(in.Body) > maxPullBody {
		return nil, zip.ErrBadRequest(fmt.Sprintf("body exceeds %d bytes", maxPullBody))
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	meta, err := store.Get(ctx, t.org, t.project, name)
	if err != nil {
		return nil, zip.ErrNotFound("repo not found")
	}

	head := strings.TrimSpace(in.Head)
	if !branchRE.MatchString(head) {
		return nil, zip.ErrBadRequest("head must be a branch name")
	}
	base := strings.TrimSpace(in.Base)
	if base == "" {
		base = cmp.Or(strings.TrimSpace(meta.DefaultBranch), defaultBranchName)
	}
	if !branchRE.MatchString(base) {
		return nil, zip.ErrBadRequest("base must be a branch name")
	}
	if head == base {
		return nil, zip.ErrBadRequest("head and base are the same branch")
	}
	if !branchExists(o, t, name, head) {
		return nil, zip.ErrBadRequest("head branch does not exist: " + head)
	}
	if !branchExists(o, t, name, base) {
		return nil, zip.ErrBadRequest("base branch does not exist: " + base)
	}

	now := time.Now().Unix()
	saved, err := store.CreatePull(ctx, Pull{
		ID: mint.ID("pr"), Org: t.org, Project: t.project, Repo: name,
		Title: title, Body: in.Body, Base: base, Head: head,
		State: pullOpen, Author: t.user, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		if errors.Is(err, errConflict) {
			return nil, zip.ErrConflict("an open pull request already proposes " + head + " into " + base)
		}
		return nil, internalErr(err)
	}
	view := pullToView(saved)
	return &view, nil
}

// listPulls returns a repo's pull requests, newest number first — what is
// waiting to be reviewed, and what has already landed. Narrow it with
// ?state=open or ?state=merged; omit state for every proposal.
//
// Example: {"name": "widgets", "state": "open"}
//
//	Response: {"data": [{"number": 4, "repo": "widgets",
//		"title": "cache the catalog read", "head": "agent/cache-catalog",
//		"base": "main", "state": "open", "createdAt": "2026-08-07T10:00:00Z",
//		"updatedAt": "2026-08-07T10:00:00Z"}]}
func (o ops) listPulls(ctx context.Context, in *pullFilter) (*pullList, error) {
	t, name, herr := o.scoped(ctx, in.Name)
	if herr != nil {
		return nil, herr
	}
	state := strings.TrimSpace(in.State)
	if state != "" && state != pullOpen && state != pullMerged {
		return nil, zip.ErrBadRequest(`state must be "open" or "merged"`)
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	rows, err := store.ListPulls(ctx, t.org, t.project, name, state)
	if err != nil {
		return nil, internalErr(err)
	}
	out := make([]pullView, 0, len(rows))
	for _, v := range rows {
		out = append(out, pullToView(v))
	}
	return &pullList{Data: out}, nil
}

// getPull returns one pull request by its per-repo number. A number belonging to
// another tenant's repo is not found, exactly as the repo itself is not.
//
// Example: {"name": "widgets", "number": 4}
func (o ops) getPull(ctx context.Context, in *pullRef) (*pullView, error) {
	t, name, herr := o.scoped(ctx, in.Name)
	if herr != nil {
		return nil, herr
	}
	if in.Number <= 0 {
		return nil, zip.ErrBadRequest("number must be a positive integer")
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	v, err := store.GetPull(ctx, t.org, t.project, name, in.Number)
	if err != nil {
		return nil, zip.ErrNotFound("pull request not found")
	}
	view := pullToView(v)
	return &view, nil
}

// mergePull merges an open pull request by FAST-FORWARDING base to head, and
// answers the proposal in its merged state with the revision base now points at.
//
// It merges only when base is already an ancestor of head — the case where head
// contains every commit base has, so moving the branch loses nothing and invents
// nothing. When base has moved on independently, this REFUSES with 409 and says
// so: a real three-way merge is not implemented here, and reporting one would
// claim a result these bytes do not produce. Rebase head onto base and merge
// again.
//
// The move is judged by the same ref policy a `git push` of it would face, and
// fires the same build and notify reactions, so merging is not a way around
// either. Merging an already-merged proposal is a 409.
//
// Example: {"name": "widgets", "number": 4}
func (o ops) mergePull(ctx context.Context, in *pullRef) (*pullView, error) {
	t, name, herr := o.scoped(ctx, in.Name)
	if herr != nil {
		return nil, herr
	}
	if in.Number <= 0 {
		return nil, zip.ErrBadRequest("number must be a positive integer")
	}
	store, err := storeFor(o.s, t.org)
	if err != nil {
		return nil, internalErr(err)
	}
	p, err := store.GetPull(ctx, t.org, t.project, name, in.Number)
	if err != nil {
		return nil, zip.ErrNotFound("pull request not found")
	}
	if p.State != pullOpen {
		return nil, zip.ErrConflict("pull request is already " + p.State)
	}

	rev, err := fastForward(o, ctx, t, name, p)
	if err != nil {
		return nil, err
	}
	// The row is settled AFTER the ref moved. MarkMerged's own `state=open`
	// guard is what makes two concurrent merges resolve to one.
	merged, err := store.MarkMerged(ctx, t.org, t.project, name, p.Number, rev, time.Now().Unix())
	if err != nil {
		return nil, internalErr(err)
	}
	if !merged {
		return nil, zip.ErrConflict("pull request was merged concurrently")
	}
	p.State, p.MergedRev, p.UpdatedAt = pullMerged, rev, time.Now().Unix()
	view := pullToView(p)
	return &view, nil
}
