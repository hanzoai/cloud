package tracker

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// github_sink_test.go proves the external-issue mirror sink: a GitHub issue upserts
// into the tracker idempotently by ExtRef (create then update-in-place), the team is
// auto-created, open/closed maps to todo/done, and orgs stay isolated. Exercised
// through cloud.UpsertIssue (the registered seam), so it also proves the wiring.

func mountTracker(t *testing.T) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
}

func TestGitHubIssueMirrorUpsert(t *testing.T) {
	mountTracker(t)
	ctx := context.Background()

	in := cloud.IssueUpsert{
		Org: "hanzo", ProjectKey: "GH", ProjectName: "GitHub",
		Repo: "cloud", ExtRef: "github:hanzoai/cloud#42", Kind: "issue", Source: "git",
		Title: "Login loops", Description: "silent SSO loop", State: "open",
		Assignee: "z", Labels: []string{"bug", "p0"},
	}

	// Create: a new #1 in the auto-created GH team.
	res, err := cloud.UpsertIssue(ctx, in)
	if err != nil {
		t.Fatalf("upsert create: %v", err)
	}
	if !res.Created || res.Number != 1 || res.Identifier != "GH-1" {
		t.Fatalf("create result: %+v", res)
	}

	store, err := mounted.State.stores.For("hanzo", "default")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	p, err := store.GetProject(ctx, "hanzo", "GH")
	if err != nil || p.Name != "GitHub" {
		t.Fatalf("team not ensured: %+v err=%v", p, err)
	}
	got, err := store.GetIssueByExtRef(ctx, "hanzo", p.ID, in.ExtRef)
	if err != nil {
		t.Fatalf("get by extref: %v", err)
	}
	if got.Status != "todo" || got.Source != "git" || got.Repo != "cloud" ||
		got.Assignee != "z" || got.Labels != "bug,p0" || got.Kind != "issue" {
		t.Fatalf("mirrored issue wrong: %+v", got)
	}

	// Update-in-place: same ExtRef, closed + retitled + relabeled → still #1, done.
	in.Title, in.State, in.Labels = "Login loops (fixed)", "closed", []string{"bug"}
	res2, err := cloud.UpsertIssue(ctx, in)
	if err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	if res2.Created || res2.Number != 1 {
		t.Fatalf("second upsert must UPDATE #1, got %+v", res2)
	}
	got2, _ := store.GetIssueByExtRef(ctx, "hanzo", p.ID, in.ExtRef)
	if got2.Status != "done" || got2.Title != "Login loops (fixed)" || got2.Labels != "bug" {
		t.Fatalf("update did not land: %+v", got2)
	}
	// Idempotent: exactly ONE row, never a duplicate.
	list, _ := store.ListIssues(ctx, "hanzo", p.ID, IssueFilter{})
	if len(list) != 1 {
		t.Fatalf("want 1 issue after idempotent re-upsert, got %d", len(list))
	}

	// A distinct ExtRef creates #2 in the same team.
	in2 := in
	in2.ExtRef, in2.Title, in2.State = "github:hanzoai/cloud#43", "Another", "open"
	res3, err := cloud.UpsertIssue(ctx, in2)
	if err != nil || !res3.Created || res3.Number != 2 {
		t.Fatalf("distinct extref must create #2, got %+v err=%v", res3, err)
	}

	// Cross-org isolation: acme's mirror lands in acme's own store as #1.
	acme := in
	acme.Org, acme.ExtRef = "acme", "github:acme/widgets#1"
	res4, err := cloud.UpsertIssue(ctx, acme)
	if err != nil || !res4.Created || res4.Number != 1 {
		t.Fatalf("acme mirror should be its own #1, got %+v err=%v", res4, err)
	}
}

func TestStateToStatus(t *testing.T) {
	for in, want := range map[string]string{"open": "todo", "closed": "done", "CLOSED": "done", "": "todo", "weird": "todo"} {
		if got := stateToStatus(in); got != want {
			t.Fatalf("stateToStatus(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCleanLabels(t *testing.T) {
	got := cleanLabels([]string{" bug ", "", "a,b", "keep"})
	// comma folded to space, empties dropped.
	if len(got) != 3 || got[0] != "bug" || got[1] != "a b" || got[2] != "keep" {
		t.Fatalf("cleanLabels: %#v", got)
	}
}

// TestGetIssueByExtRefRejectsAnEmptyAnchor pins the mirror lookup's fail-closed edge.
// Every NATIVE issue carries an empty ext_ref, so a blank anchor must miss rather than
// select one of them — otherwise the upsert's "found ⇒ update in place" branch would
// overwrite a real team issue with mirrored external content.
func TestGetIssueByExtRefRejectsAnEmptyAnchor(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	p := mkProject("hanzo", "ENG", "Engineering")
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	// A native team issue: no external anchor.
	native, err := s.CreateIssue(ctx, Issue{
		ID: "iss_native", ProjectID: p.ID, Org: "hanzo",
		Title: "Native work item", Status: "todo", Priority: "none",
		CreatedAt: 100, UpdatedAt: 100,
	})
	if err != nil {
		t.Fatalf("create native issue: %v", err)
	}
	if native.ExtRef != "" {
		t.Fatalf("a native issue should carry no anchor, got %q", native.ExtRef)
	}

	for _, anchor := range []string{"", "   ", "\t"} {
		got, err := s.GetIssueByExtRef(ctx, "hanzo", p.ID, anchor)
		if !errors.Is(err, errNotFound) {
			t.Fatalf("anchor %q: want errNotFound, got issue %+v err=%v", anchor, got, err)
		}
	}

	// A real anchor still resolves — the guard rejects blanks, not lookups.
	mirrored, err := s.CreateIssue(ctx, Issue{
		ID: "iss_mirror", ProjectID: p.ID, Org: "hanzo", Source: "git",
		ExtRef: "github:hanzoai/cloud#7", Title: "Mirrored", Status: "todo",
		Priority: "none", CreatedAt: 101, UpdatedAt: 101,
	})
	if err != nil {
		t.Fatalf("create mirrored issue: %v", err)
	}
	found, err := s.GetIssueByExtRef(ctx, "hanzo", p.ID, "github:hanzoai/cloud#7")
	if err != nil || found.Number != mirrored.Number {
		t.Fatalf("real anchor should resolve to #%d, got %+v err=%v", mirrored.Number, found, err)
	}
}
