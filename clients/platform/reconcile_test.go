package platform

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestJobOutcome locks the ONE terminal-state classifier the build cap and the
// reconciler both rely on: a Job is done+succeeded only on succeeded>0 or a true
// Complete condition; done+failed on failed>0 or a true Failed condition; not
// done while still running.
func TestJobOutcome(t *testing.T) {
	job := func(status map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"status": status}}
	}
	cases := []struct {
		name          string
		status        map[string]any
		wantDone      bool
		wantSucceeded bool
	}{
		{"succeeded-count", map[string]any{"succeeded": int64(1)}, true, true},
		{"failed-count", map[string]any{"failed": int64(1)}, true, false},
		{"running", map[string]any{"active": int64(1)}, false, false},
		{"empty", map[string]any{}, false, false},
		{"complete-condition", map[string]any{"conditions": []any{
			map[string]any{"type": "Complete", "status": "True"},
		}}, true, true},
		{"failed-condition", map[string]any{"conditions": []any{
			map[string]any{"type": "Failed", "status": "True"},
		}}, true, false},
		{"complete-condition-false", map[string]any{"conditions": []any{
			map[string]any{"type": "Complete", "status": "False"},
		}}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			done, succeeded := jobOutcome(job(c.status))
			if done != c.wantDone || succeeded != c.wantSucceeded {
				t.Fatalf("jobOutcome=(%v,%v) want (%v,%v)", done, succeeded, c.wantDone, c.wantSucceeded)
			}
			// jobFinished must agree with jobOutcome's done bit.
			if jobFinished(job(c.status)) != c.wantDone {
				t.Fatalf("jobFinished disagrees with jobOutcome for %s", c.name)
			}
		})
	}
}

// TestListBuildingDeployments proves the reconciler input query returns ONLY
// "building" deployments, spanning orgs, oldest-first — the restart-safe basis
// for resuming in-flight git builds after a cloud restart.
func TestListBuildingDeployments(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	seed := func(org, appSlug, depID, status string, created int64) {
		_ = s.CreateProject(ctx, mkProject(org, "proj", "Proj"))
		proj, _ := s.GetProject(ctx, org, "proj")
		a := mkApp(org, proj.ID, appSlug)
		_ = s.CreateApplication(ctx, a)
		if err := s.InsertDeployment(ctx, Deployment{
			ID: depID, Org: org, ApplicationID: a.ID, Version: 1, Status: status,
			Source: "git", Image: "img", BuildID: "bld_" + depID, CreatedAt: created, UpdatedAt: created,
		}); err != nil {
			t.Fatalf("seed %s: %v", depID, err)
		}
	}

	seed("maxpower", "a", "dep_mp_build", "building", 200)
	seed("acme", "b", "dep_ac_build", "building", 100) // older ⇒ first
	seed("maxpower", "c", "dep_mp_live", "live", 300)
	seed("acme", "d", "dep_ac_err", "error", 400)

	got, err := s.ListBuildingDeployments(ctx)
	if err != nil {
		t.Fatalf("ListBuildingDeployments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 building deployments across orgs, got %d: %+v", len(got), got)
	}
	if got[0].ID != "dep_ac_build" || got[1].ID != "dep_mp_build" {
		t.Fatalf("want oldest-first [dep_ac_build, dep_mp_build], got [%s, %s]", got[0].ID, got[1].ID)
	}
	for _, d := range got {
		if d.Status != "building" {
			t.Fatalf("non-building deployment leaked: %s status=%s", d.ID, d.Status)
		}
	}
}
