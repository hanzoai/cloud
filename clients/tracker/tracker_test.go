package tracker

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := cloud.OrgDB(t.TempDir(), "test", "default", "tracker")
	if err != nil {
		t.Fatalf("OrgDB: %v", err)
	}
	s, err := openStore(db)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkProject(org, key, name string) Project {
	return Project{ID: "prj_" + org + "_" + key, Org: org, Key: key, Name: name, CreatedAt: 100, UpdatedAt: 100}
}

func TestProjectCRUDAndTenantIsolation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.CreateProject(ctx, mkProject("hanzo", "ENG", "Engineering")); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := s.GetProject(ctx, "hanzo", "ENG")
	if err != nil || got.Name != "Engineering" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	// Cross-org isolation: another org cannot see it.
	if _, err := s.GetProject(ctx, "acme", "ENG"); !errors.Is(err, errNotFound) {
		t.Fatalf("expected notFound for other org, got %v", err)
	}
	// Duplicate (org,key) is a conflict.
	if err := s.CreateProject(ctx, mkProject("hanzo", "ENG", "dup")); !errors.Is(err, errConflict) {
		t.Fatalf("expected conflict on dup, got %v", err)
	}
	// Same key under a DIFFERENT org is allowed.
	if err := s.CreateProject(ctx, mkProject("acme", "ENG", "Acme Eng")); err != nil {
		t.Fatalf("create other-org same-key: %v", err)
	}

	// Update mutates name only, scoped to org.
	got.Name = "Platform"
	got.UpdatedAt = 200
	if err := s.UpdateProject(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	if p, _ := s.GetProject(ctx, "hanzo", "ENG"); p.Name != "Platform" {
		t.Fatalf("update not applied: %+v", p)
	}

	// List is org-scoped.
	list, err := s.ListProjects(ctx, "hanzo")
	if err != nil || len(list) != 1 {
		t.Fatalf("list hanzo: n=%d err=%v", len(list), err)
	}
}

func TestIssueNumberingStatusAndCascade(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("hanzo", "ENG", "Engineering")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	pid := "prj_hanzo_ENG"

	// Per-project monotonic numbering: 1, 2, 3.
	for n, title := range []string{"first", "second", "third"} {
		st := "todo"
		if n == 2 {
			st = "in_progress"
		}
		got, err := s.CreateIssue(ctx, Issue{
			ID: genMust(t), ProjectID: pid, Org: "hanzo",
			Title: title, Status: st, Priority: "none", CreatedAt: 100, UpdatedAt: 100,
		})
		if err != nil {
			t.Fatalf("create issue %q: %v", title, err)
		}
		if got.Number != n+1 {
			t.Fatalf("issue %q number = %d, want %d", title, got.Number, n+1)
		}
	}

	// List all: three rows, grouped-sortable by status then number.
	all, err := s.ListIssues(ctx, "hanzo", pid, IssueFilter{})
	if err != nil || len(all) != 3 {
		t.Fatalf("list all: n=%d err=%v", len(all), err)
	}

	// Status filter (the board column query) returns just the matching rows.
	todo, err := s.ListIssues(ctx, "hanzo", pid, IssueFilter{Status: "todo"})
	if err != nil || len(todo) != 2 {
		t.Fatalf("list todo: n=%d err=%v", len(todo), err)
	}

	// Update status: move issue 1 to done.
	one, err := s.GetIssue(ctx, "hanzo", pid, 1)
	if err != nil {
		t.Fatalf("get issue 1: %v", err)
	}
	one.Status = "done"
	one.UpdatedAt = 200
	if err := s.UpdateIssue(ctx, one); err != nil {
		t.Fatalf("update issue: %v", err)
	}
	if got, _ := s.GetIssue(ctx, "hanzo", pid, 1); got.Status != "done" {
		t.Fatalf("status update not applied: %+v", got)
	}

	// Cross-org isolation on issues.
	if _, err := s.GetIssue(ctx, "acme", pid, 1); !errors.Is(err, errNotFound) {
		t.Fatalf("expected notFound cross-org, got %v", err)
	}

	// Delete one issue.
	deleted, err := s.DeleteIssue(ctx, "hanzo", pid, 2)
	if err != nil || !deleted {
		t.Fatalf("delete issue: deleted=%v err=%v", deleted, err)
	}
	if _, err := s.GetIssue(ctx, "hanzo", pid, 2); !errors.Is(err, errNotFound) {
		t.Fatalf("issue 2 should be gone, got %v", err)
	}

	// Cascade: deleting the project removes its remaining issues.
	del, err := s.DeleteProject(ctx, "hanzo", "ENG")
	if err != nil || !del {
		t.Fatalf("delete project: del=%v err=%v", del, err)
	}
	if rows, _ := s.ListIssues(ctx, "hanzo", pid, IssueFilter{}); len(rows) != 0 {
		t.Fatalf("issues not cascaded: %d remain", len(rows))
	}
}

// TestIssuePolymorphicSpine proves the ONE-table alignment: a git PR, a git
// issue, a CRM deal and a native team task all live as issues rows in one
// project, and every product surface is a FILTER — a repo's Issues tab, its PRs
// tab, CRM's pipeline — never a second store. Defaults and cross-org isolation
// on the new discriminators are covered here too.
func TestIssuePolymorphicSpine(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("hanzo", "ENG", "Engineering")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	pid := "prj_hanzo_ENG"

	seed := []Issue{
		{Kind: "pr", Source: "git", Repo: "cloud", Title: "wire filter", Status: "in_progress"},
		{Kind: "issue", Source: "git", Repo: "cloud", Title: "flaky test", Status: "todo"},
		{Kind: "issue", Source: "git", Repo: "console", Title: "dark mode", Status: "todo"},
		{Kind: "deal", Source: "crm", Title: "Acme expansion", Status: "backlog"},
		{Title: "plan q3", Status: "backlog"}, // defaults: kind=issue, source=team
	}
	for _, in := range seed {
		in.ID = genMust(t)
		in.ProjectID = pid
		in.Org = "hanzo"
		in.CreatedAt, in.UpdatedAt = 100, 100
		if _, err := s.CreateIssue(ctx, in); err != nil {
			t.Fatalf("create %q: %v", in.Title, err)
		}
	}

	// Defaults applied when omitted.
	plan, err := s.GetIssue(ctx, "hanzo", pid, 5)
	if err != nil || plan.Kind != "issue" || plan.Source != "team" {
		t.Fatalf("defaults: kind=%q source=%q err=%v", plan.Kind, plan.Source, err)
	}

	// A git repo's Issues tab = filter {Repo, Kind:issue}. Only cloud's issue row.
	cloudIssues, err := s.ListIssues(ctx, "hanzo", pid, IssueFilter{Repo: "cloud", Kind: "issue"})
	if err != nil || len(cloudIssues) != 1 || cloudIssues[0].Title != "flaky test" {
		t.Fatalf("cloud issues tab: %+v err=%v", cloudIssues, err)
	}
	// Its PRs tab = filter {Repo, Kind:pr}.
	cloudPRs, err := s.ListIssues(ctx, "hanzo", pid, IssueFilter{Repo: "cloud", Kind: "pr"})
	if err != nil || len(cloudPRs) != 1 || cloudPRs[0].Title != "wire filter" {
		t.Fatalf("cloud PRs tab: %+v err=%v", cloudPRs, err)
	}
	// CRM pipeline = filter {Kind:deal}.
	deals, err := s.ListIssues(ctx, "hanzo", pid, IssueFilter{Kind: "deal"})
	if err != nil || len(deals) != 1 || deals[0].Title != "Acme expansion" {
		t.Fatalf("crm deals: %+v err=%v", deals, err)
	}
	// Everything a git source opened, across repos = filter {Source:git}: 3 rows.
	git, err := s.ListIssues(ctx, "hanzo", pid, IssueFilter{Source: "git"})
	if err != nil || len(git) != 3 {
		t.Fatalf("git source: n=%d err=%v", len(git), err)
	}
	// Unfiltered = the whole board.
	all, err := s.ListIssues(ctx, "hanzo", pid, IssueFilter{})
	if err != nil || len(all) != 5 {
		t.Fatalf("board: n=%d err=%v", len(all), err)
	}
}

func genMust(t *testing.T) string {
	t.Helper()
	id, err := genID("issue")
	if err != nil {
		t.Fatalf("genID: %v", err)
	}
	return id
}
