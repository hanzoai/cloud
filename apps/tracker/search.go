package tracker

// search.go — find an issue, and take it.
//
// The other issue ops proxy the FORGE, which answers per repository. That is the
// right shape for browsing one project and the wrong shape for the question
// people actually ask: "is anyone tracking X?" — where X might be a GitHub issue
// mirrored under the org's default project, something a colleague filed from the
// helpdesk, or a row an agent opened. Those live in the local store, which is
// the ONE place every source lands (github_sink.go), so the search reads it.
//
// Taking an issue is the same store, one field. An agent that can find work and
// cannot claim it produces two of everything.

import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// issueSearch is what a person or an agent asks. Every field narrows; none is
// required, and an empty search is the org's open work.
type issueSearch struct {
	// Q matches an issue's title or description. A word from the issue, which is
	// what someone remembers — not its number, which is what they are looking up.
	Q string `json:"q"`
	// Project narrows to one team key; "" searches every project in the org,
	// which is the point of this op.
	Project string `json:"project"`
	// Status keeps one board column: backlog, todo, in_progress, done, canceled.
	Status string `json:"status"`
	// Kind keeps one shape: issue, pr, epic.
	Kind string `json:"kind"`
	// Repo keeps issues bound to one git repository.
	Repo string `json:"repo"`
	// Source keeps one origin: team, git, crm, helpdesk, cms, agent. "git" is
	// how you ask for the mirrored GitHub issues specifically.
	Source string `json:"source"`
	// Assignee keeps issues held by one person. Pass "me" for yourself.
	Assignee string `json:"assignee"`
	// Limit caps the answer; 0 means the default, and anything above the ceiling
	// is clamped rather than refused — a search that errors on being too broad
	// teaches people to guess.
	Limit int `json:"limit"`
}

// issueHit is one result. It carries the project key so a caller can act on the
// issue without a second lookup — a search that returns rows you cannot address
// is a list, not a tool.
type issueHit struct {
	Project  string `json:"project"`
	Number   int    `json:"number"`
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	Repo     string `json:"repo"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Assignee string `json:"assignee"`
	URL      string `json:"url"`
}

type issueHits struct {
	Issues []issueHit `json:"issues"`
	Count  int        `json:"count"`
}

const (
	searchDefault = 50
	searchCeiling = 200
)

// searchIssues answers across every project in the org.
//
// The org comes from the validated principal and never from the request: a
// caller able to name the org could read another tenant's backlog, and a search
// is exactly the shape that would quietly return it.
func (o ops) searchIssues(ctx context.Context, in *issueSearch) (*issueHits, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	if in.Status != "" && !statuses[in.Status] {
		return nil, zip.ErrBadRequest("unknown status")
	}
	if in.Kind != "" && !kinds[in.Kind] {
		return nil, zip.ErrBadRequest("unknown kind")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = searchDefault
	}
	if limit > searchCeiling {
		limit = searchCeiling
	}

	assignee := strings.TrimSpace(in.Assignee)
	if assignee == "me" {
		assignee = holder(ctx)
	}

	// The default project is where every mirrored source lands, so a search that
	// names no project searches it. Naming one narrows to that team.
	proj := strings.TrimSpace(in.Project)
	if proj == "" {
		proj = principal.DefaultProject
	}
	store, err := storeFor(o.s, org, proj)
	if err != nil {
		return nil, err
	}
	rows, err := store.ListIssues(ctx, org, "", IssueFilter{
		Status: in.Status, Kind: in.Kind, Repo: in.Repo,
		Source: in.Source, Assignee: assignee, Text: in.Q,
	})
	if err != nil {
		return nil, err
	}

	out := &issueHits{Issues: make([]issueHit, 0, len(rows))}
	for _, r := range rows {
		if len(out.Issues) >= limit {
			break
		}
		out.Issues = append(out.Issues, issueHit{
			Project: r.ProjectID, Number: r.Number, Kind: r.Kind, Source: r.Source,
			Repo: r.Repo, Title: r.Title, Status: r.Status, Priority: r.Priority,
			Assignee: r.Assignee, URL: r.ExtRef,
		})
	}
	out.Count = len(out.Issues)
	return out, nil
}

// issueClaim names the issue to take.
type issueClaim struct {
	Key string `json:"key"`
	Num int    `json:"num"`
}

// claimIssue takes an issue: it becomes yours and it moves to in_progress.
//
// The holder is the CALLER, never an argument. "Assign this to someone else" is
// a different act with different authority, and it already exists as a PATCH;
// conflating them would let anyone hand work to anyone by naming them.
//
// Claiming something already held by someone else is refused rather than
// silently taken — two agents on one issue is the failure this prevents, and a
// claim that quietly wins a race is worse than one that says no.
func (o ops) claimIssue(ctx context.Context, in *issueClaim) (*issueHit, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	if in.Num <= 0 {
		return nil, zip.ErrBadRequest("bad issue number")
	}
	who := holder(ctx)
	if who == "" {
		return nil, zip.ErrForbidden("a claim needs someone to hold it")
	}
	key := strings.TrimSpace(in.Key)
	if key == "" {
		key = principal.DefaultProject
	}
	store, err := storeFor(o.s, org, key)
	if err != nil {
		return nil, err
	}
	rows, err := store.ListIssues(ctx, org, "", IssueFilter{})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Number != in.Num {
			continue
		}
		if r.Assignee != "" && r.Assignee != who {
			return nil, zip.Errorf(409, "issue %d is already held by %s", r.Number, r.Assignee)
		}
		r.Assignee = who
		if r.Status == "backlog" || r.Status == "todo" {
			r.Status = "in_progress"
		}
		r.UpdatedAt = time.Now().Unix()
		if err := store.UpdateIssue(ctx, r); err != nil {
			return nil, err
		}
		return &issueHit{
			Project: r.ProjectID, Number: r.Number, Kind: r.Kind, Source: r.Source,
			Repo: r.Repo, Title: r.Title, Status: r.Status, Priority: r.Priority,
			Assignee: r.Assignee, URL: r.ExtRef,
		}, nil
	}
	return nil, zip.Errorf(404, "issue %d not found", in.Num)
}

// holder is who a claim binds work to. It reads the caller through zip rather
// than the package's actorOf(*zip.Ctx), because a typed op is handed a context
// and not a request — same person, the only shape available here.
//
// Empty when a run has no person behind it (a schedule, a service token), which
// is why a claim refuses rather than assigning work to nobody.
func holder(ctx context.Context) string {
	return strings.TrimSpace(zip.CallerOf(ctx).User)
}
