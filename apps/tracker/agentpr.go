package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// agentpr.go is the IN-PROCESS door for opening a native "PR" work item from a
// coding-agent run (clients/coding), the twin of the createIssue HTTP handler.
// It writes the SAME tracker Issue table through the SAME store — a Kind:"pr",
// Source:"agent" row bound to the git Repo, with the pushed branch as the ExtRef
// anchor — so the row shows up on the repo's PRs tab (Filter{Repo, Kind:"pr"})
// and an agent's queue (Filter{Source:"agent"}) with no parallel store.
//
// ISOLATION: org is threaded to the org-scoped store on every call (the per-
// (org, project) SQLite file is opened by org), so a coding run for org A can
// only ever file a row in org A's tracker. A nil singleton (tracker unmounted)
// fails closed.

// AgentPRInput is the closed set a coding run supplies to open its PR row. The
// four discriminators are set here: Kind is always "pr", Source always "agent",
// Repo binds the git repo, and Head (the pushed branch) is the ExtRef anchor.
type AgentPRInput struct {
	Org      string // tenant (isolation key)
	Project  string // IAM project slug (physical DB scope); "" = the org default
	Repo     string // git repo the branch was pushed to (the Repo binding)
	Base     string // base branch the PR targets (recorded in the body)
	Head     string // the pushed branch (the ExtRef anchor)
	Title    string // work-item title (from the task)
	Body     string // description: summary / diffstat + session link
	Assignee string // the agent ref (e.g. "hanzo")
}

// AgentPR is the created row's stable handle: KEY-N plus the parts to build it.
type AgentPR struct {
	Identifier string // "KEY-N" — the human handle
	ProjectKey string // the tracker project KEY the row lives under
	Number     int    // per-project monotonic number
}

// CreateAgentPR opens the PR work item for a coding run: it get-or-creates the
// tracker project keyed off the repo (so a repo's PRs share one board), then
// inserts the Kind:"pr" Source:"agent" issue and returns its KEY-N identifier.
// Idempotent at the project layer (a concurrent create resolves back to the same
// project); the issue itself is always a new row (per-run PR).
func CreateAgentPR(ctx context.Context, in AgentPRInput) (AgentPR, error) {
	if mounted == nil {
		return AgentPR{}, fmt.Errorf("tracker: not mounted")
	}
	org := strings.TrimSpace(in.Org)
	repo := strings.TrimSpace(in.Repo)
	if org == "" || repo == "" {
		return AgentPR{}, fmt.Errorf("tracker: org and repo required")
	}
	if len(repo) > maxField {
		return AgentPR{}, fmt.Errorf("tracker: repo too long")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "Agent changes to " + repo
	}
	if len(title) > maxTitle {
		title = title[:maxTitle]
	}
	body := in.Body
	if len(body) > maxDesc {
		body = body[:maxDesc]
	}
	head := strings.TrimSpace(in.Head)
	if len(head) > maxField {
		return AgentPR{}, fmt.Errorf("tracker: head branch too long")
	}
	assignee := strings.TrimSpace(in.Assignee)
	if len(assignee) > maxField {
		assignee = assignee[:maxField]
	}

	store, err := mounted.State.stores.For(org, strings.TrimSpace(in.Project))
	if err != nil {
		return AgentPR{}, fmt.Errorf("tracker: open store: %w", err)
	}
	proj, err := ensureAgentProject(ctx, store, org, repo)
	if err != nil {
		return AgentPR{}, err
	}

	id, err := genID("issue")
	if err != nil {
		return AgentPR{}, fmt.Errorf("tracker: rng: %w", err)
	}
	now := time.Now().Unix()
	created, err := store.CreateIssue(ctx, Issue{
		ID: id, ProjectID: proj.ID, Org: org,
		Kind: "pr", Source: "agent", Repo: repo, ExtRef: head,
		Title: title, Description: body, Status: "todo", Priority: "none",
		Assignee: assignee, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return AgentPR{}, fmt.Errorf("tracker: create pr: %w", err)
	}
	return AgentPR{
		Identifier: fmt.Sprintf("%s-%d", proj.Key, created.Number),
		ProjectKey: proj.Key,
		Number:     created.Number,
	}, nil
}

// ensureAgentProject resolves (or creates) the tracker project a repo's agent PRs
// live under. The key is derived from the repo name (deriveKey), validated, and
// falls back to a stable "AGENT" board when the derived key would be invalid. A
// create that loses a race (errConflict) re-reads the winner — get-or-create is
// idempotent.
func ensureAgentProject(ctx context.Context, store *Store, org, repo string) (Project, error) {
	key := deriveKey(repo)
	if !keyRE.MatchString(key) {
		key = "AGENT"
	}
	p, err := store.GetProject(ctx, org, key)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, errNotFound) {
		return Project{}, fmt.Errorf("tracker: get project: %w", err)
	}
	id, gerr := genID("prj")
	if gerr != nil {
		return Project{}, fmt.Errorf("tracker: rng: %w", gerr)
	}
	now := time.Now().Unix()
	np := Project{ID: id, Org: org, Key: key, Name: repo, CreatedAt: now, UpdatedAt: now}
	cerr := store.CreateProject(ctx, np)
	if cerr == nil {
		return np, nil
	}
	if errors.Is(cerr, errConflict) {
		// Lost the race — the winner is now readable under the same key.
		return store.GetProject(ctx, org, key)
	}
	return Project{}, fmt.Errorf("tracker: create project: %w", cerr)
}
