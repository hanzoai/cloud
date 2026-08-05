package tracker

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := cloud.OrgDB(t.TempDir(), cloud.MustOrgNamespace("test", "default"), "tracker")
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

// TestIssuePolymorphicSpine proves the ONE-table alignment: a git PR, git
// issues, a parent epic and a native team issue all live as issues rows in one
// project, and every work-item surface is a FILTER — a repo's Issues tab, its
// PRs tab, an epic view, a source view — never a second store. Defaults are
// covered too. (Domain records like CRM deals live on another plane; see
// contract.go — they are NOT tracker kinds.)
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
		{Kind: "epic", Source: "team", Title: "Q3 platform", Status: "backlog"},
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
	// An epic view = filter {Kind:epic}.
	epics, err := s.ListIssues(ctx, "hanzo", pid, IssueFilter{Kind: "epic"})
	if err != nil || len(epics) != 1 || epics[0].Title != "Q3 platform" {
		t.Fatalf("epics: %+v err=%v", epics, err)
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

// TestIssueScheduleAndTimelineFilter proves the timeline is the SAME table the
// board reads, sliced by one more filter — not a second store and not a
// milestone table. Three shapes go in (a bar, a milestone, an undated row), the
// Scheduled filter selects exactly the two a gantt can draw, and a reschedule is
// ordinary mutable board state that survives the round trip.
func TestIssueScheduleAndTimelineFilter(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("hanzo", "ENG", "Engineering")); err != nil {
		t.Fatalf("create project: %v", err)
	}
	pid := "prj_hanzo_ENG"

	const day = int64(86400)
	base := int64(1_700_000_000)
	seed := []Issue{
		// A bar: both bounds.
		{Title: "migrate store", Status: "in_progress", StartAt: base, DueAt: base + 7*day},
		// A milestone: a due date and no start — an interval of zero length.
		{Title: "GA", Kind: "epic", Status: "todo", DueAt: base + 30*day},
		// Undated: on the board, off the timeline.
		{Title: "triage inbox", Status: "backlog"},
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

	// The dates round-trip through the store, not just the insert.
	bar, err := s.GetIssue(ctx, "hanzo", pid, 1)
	if err != nil || bar.StartAt != base || bar.DueAt != base+7*day {
		t.Fatalf("bar schedule = (%d,%d) err=%v, want (%d,%d)", bar.StartAt, bar.DueAt, err, base, base+7*day)
	}
	milestone, err := s.GetIssue(ctx, "hanzo", pid, 2)
	if err != nil || milestone.StartAt != 0 || milestone.DueAt != base+30*day {
		t.Fatalf("milestone schedule = (%d,%d) err=%v", milestone.StartAt, milestone.DueAt, err)
	}

	// The timeline's slice: everything that carries a date, and nothing else.
	timeline, err := s.ListIssues(ctx, "hanzo", pid, IssueFilter{Scheduled: true})
	if err != nil || len(timeline) != 2 {
		t.Fatalf("timeline: n=%d err=%v, want 2", len(timeline), err)
	}
	for _, i := range timeline {
		if i.StartAt == 0 && i.DueAt == 0 {
			t.Fatalf("undated row %q leaked into the timeline", i.Title)
		}
	}
	// The board still sees all three — one table, two views.
	if all, _ := s.ListIssues(ctx, "hanzo", pid, IssueFilter{}); len(all) != 3 {
		t.Fatalf("board: n=%d, want 3", len(all))
	}
	// Composes with the other filters rather than replacing them.
	epics, err := s.ListIssues(ctx, "hanzo", pid, IssueFilter{Scheduled: true, Kind: "epic"})
	if err != nil || len(epics) != 1 || epics[0].Title != "GA" {
		t.Fatalf("scheduled epics: %+v err=%v", epics, err)
	}

	// A reschedule is mutable board state: the update persists both bounds.
	bar.StartAt = base + day
	bar.DueAt = base + 14*day
	bar.UpdatedAt = 200
	if err := s.UpdateIssue(ctx, bar); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	got, _ := s.GetIssue(ctx, "hanzo", pid, 1)
	if got.StartAt != base+day || got.DueAt != base+14*day {
		t.Fatalf("reschedule not applied: (%d,%d)", got.StartAt, got.DueAt)
	}
	// Clearing a schedule drops the row off the timeline without touching the board.
	got.StartAt, got.DueAt = 0, 0
	if err := s.UpdateIssue(ctx, got); err != nil {
		t.Fatalf("clear schedule: %v", err)
	}
	if timeline, _ := s.ListIssues(ctx, "hanzo", pid, IssueFilter{Scheduled: true}); len(timeline) != 1 {
		t.Fatalf("after clearing: timeline n=%d, want 1", len(timeline))
	}
	if all, _ := s.ListIssues(ctx, "hanzo", pid, IssueFilter{}); len(all) != 3 {
		t.Fatalf("after clearing: board n=%d, want 3", len(all))
	}
}

// TestCheckSchedule pins the three legal interval shapes and the two refusals.
// The refusals are boundary decisions rather than normalisations: a caller's
// dates are never silently swapped.
func TestCheckSchedule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		start, due  int64
		wantRefusal bool
	}{
		{name: "unscheduled", start: 0, due: 0},
		{name: "milestone (due only)", start: 0, due: 100},
		{name: "started, no deadline", start: 100, due: 0},
		{name: "bar", start: 100, due: 200},
		{name: "zero-length bar", start: 100, due: 100},
		{name: "negative start", start: -1, due: 0, wantRefusal: true},
		{name: "negative due", start: 0, due: -1, wantRefusal: true},
		{name: "due before start", start: 200, due: 100, wantRefusal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSchedule(tc.start, tc.due)
			if tc.wantRefusal && err == nil {
				t.Fatalf("checkSchedule(%d,%d) = nil, want a refusal", tc.start, tc.due)
			}
			if !tc.wantRefusal && err != nil {
				t.Fatalf("checkSchedule(%d,%d) = %v, want nil", tc.start, tc.due, err)
			}
		})
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
