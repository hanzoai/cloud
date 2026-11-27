package platform

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
)

// reaperService wires the reaper's three collaborators: the app store, a fake IAM
// project store, and a fake cluster.
func reaperService(t *testing.T, projects ProjectStore) *cloud.Service[state] {
	t.Helper()
	return &cloud.Service[state]{
		Log: luxlog.New("test"), Brand: "hanzo",
		State: state{store: newTestStore(t), projects: projects, k8s: fakeK8s()},
	}
}

// brokenProjects answers every existence question with a failure — an IAM that is
// down, which must never be read as "the project was deleted".
type brokenProjects struct{ ProjectStore }

func (brokenProjects) Exists(context.Context, string, string) (bool, error) {
	return false, errors.New("iam unavailable")
}

// TestReaperRemovesAppsWhoseProjectIsGone: the project was deleted in IAM, where
// projects live. The app under it must not stay deployed and billing.
func TestReaperRemovesAppsWhoseProjectIsGone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		fp := newFakeProjects()
		if _, err := fp.Create(ctx, "acme", "live", "Live", ""); err != nil {
			t.Fatalf("seed project: %v", err)
		}
		s := reaperService(t, fp)

		for _, a := range []Application{mkApp("acme", "live", "keeper"), mkApp("acme", "dead", "orphan"), mkApp("acme", principal.DefaultProject, "home")} {
			if err := s.State.store.CreateApplication(ctx, a); err != nil {
				t.Fatalf("seed %s: %v", a.Slug, err)
			}
		}

		reapOrphans(s, ctx)

		if _, err := s.State.store.GetApplication(ctx, "acme", "live", "keeper"); err != nil {
			t.Fatalf("an app under a live project was reaped: %v", err)
		}
		// The default project is part of what an org is, IAM row or none.
		if _, err := s.State.store.GetApplication(ctx, "acme", principal.DefaultProject, "home"); err != nil {
			t.Fatalf("an app in the implicit default project was reaped: %v", err)
		}
		if _, err := s.State.store.GetApplication(ctx, "acme", "dead", "orphan"); !errors.Is(err, errNotFound) {
			t.Fatalf("orphan survived the reap: err=%v", err)
		}
	})
}

// TestReaperReapsNothingWhenIAMIsUnreachable is the guard that keeps this safe.
// "the project store is down" and "the project was deleted" must never be the same
// signal, or one IAM blip deletes every tenant's apps.
func TestReaperReapsNothingWhenIAMIsUnreachable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s := reaperService(t, brokenProjects{newFakeProjects()})

		if err := s.State.store.CreateApplication(ctx, mkApp("acme", "proj", "api")); err != nil {
			t.Fatalf("seed: %v", err)
		}

		reapOrphans(s, ctx)

		if _, err := s.State.store.GetApplication(ctx, "acme", "proj", "api"); err != nil {
			t.Fatalf("an unreachable project store reaped an app: %v", err)
		}
	})
}

// countingProjects reports every project alive and counts the questions asked.
type countingProjects struct {
	ProjectStore
	calls int
}

func (c *countingProjects) Exists(context.Context, string, string) (bool, error) {
	c.calls++
	return true, nil
}

// TestReaperAsksOncePerScope: three apps in one project is one question about that
// project, not three.
func TestReaperAsksOncePerScope(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		counter := &countingProjects{ProjectStore: newFakeProjects()}
		s := reaperService(t, counter)

		for _, slug := range []string{"a", "b", "c"} {
			if err := s.State.store.CreateApplication(ctx, mkApp("acme", "proj", slug)); err != nil {
				t.Fatalf("seed %s: %v", slug, err)
			}
		}

		reapOrphans(s, ctx)

		if counter.calls != 1 {
			t.Fatalf("Exists called %d times for one (org,project); want 1", counter.calls)
		}
	})
}

// TestPlatformCannotCreateOrDeleteProjects pins the boundary in the type system:
// a project is IAM's, so ProjectStore offers no way to make or destroy one. If
// someone re-adds Create or Delete to the interface, this stops compiling.
func TestPlatformCannotCreateOrDeleteProjects(t *testing.T) {
	var ps ProjectStore = newFakeProjects()
	if _, ok := ps.(interface {
		Create(context.Context, string, string, string, string) (any, error)
	}); ok {
		t.Fatal("ProjectStore exposes Create — projects belong to IAM")
	}
	if _, ok := ps.(interface {
		Delete(context.Context, string, string) (bool, error)
	}); ok {
		t.Log("note: the fake still has Delete; what matters is that ProjectStore does not require it")
	}
}

// TestReaperLeavesAppsWhoseTenantKeyIsNotTheirIAMName: an org named like a wallet's
// (wallet-evm-<16 hex>) is stored under a suffixed tenant key, and IAM knows the org
// only by its own name. The reaper used to ask IAM about the key, hear "no such
// project", and delete every app of the org within five minutes of its deploy.
func TestReaperLeavesAppsWhoseTenantKeyIsNotTheirIAMName(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		const iamOrg = "wallet-evm-db4d55cb76473850"
		key := namespace.Sanitize(iamOrg)
		if key == iamOrg {
			t.Fatalf("%q is its own tenant key; this test needs an org that Sanitize rewrites", iamOrg)
		}
		fp := newFakeProjects()
		if _, err := fp.Create(ctx, iamOrg, "site", "Site", ""); err != nil {
			t.Fatalf("seed project: %v", err)
		}
		s := reaperService(t, fp)
		if err := s.State.store.CreateApplication(ctx, mkApp(key, "site", "web")); err != nil {
			t.Fatalf("seed app: %v", err)
		}

		reapOrphans(s, ctx)

		if _, err := s.State.store.GetApplication(ctx, key, "site", "web"); err != nil {
			t.Fatalf("an app of a live project was reaped because its tenant key is not the IAM org name: %v", err)
		}
	})
}
