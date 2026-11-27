package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// skillsApp is a skills app that answers the given skills and counts its asks.
func skillsApp(asks *int, entries ...client.SkillEntry) func(context.Context, *client.SkillCatalogIn) (*client.SkillCatalog, error) {
	return func(context.Context, *client.SkillCatalogIn) (*client.SkillCatalog, error) {
		*asks++
		return &client.SkillCatalog{Brand: "hanzo", Skills: entries}, nil
	}
}

// The brand's skills are listed in the tool plane's own process, and an org
// activates one exactly as it activates any tool.
func TestTheBrandsSkillsAreListedAndActivatable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		asks := 0
		r := freshRegistry(t)
		r.Register(&brandSkills{ask: skillsApp(&asks,
			client.SkillEntry{Name: "git_repos", Description: "List repos"},
			client.SkillEntry{Name: "ai_models", Description: "List models"},
		)})

		got := r.List(ctx, Scope{Org: "acme"})
		if len(got) != 2 || got[0].Name != "skill_ai_models" || got[1].Name != "skill_git_repos" {
			t.Fatalf("listing: %+v", got)
		}
		for _, tl := range got {
			if tl.Source != SourceSkill || tl.Dispatchable || tl.Activated {
				t.Fatalf("a brand skill is a listed, undispatchable, inactive skill: %+v", tl)
			}
		}

		if err := r.Activate(ctx, "acme", "", "skill_git_repos", "u", false); err != nil {
			t.Fatalf("activate: %v", err)
		}
		for _, tl := range r.List(ctx, Scope{Org: "acme"}) {
			if tl.Activated != (tl.Name == "skill_git_repos") {
				t.Fatalf("activation for acme: %+v", tl)
			}
		}
		for _, tl := range r.List(ctx, Scope{Org: "other"}) {
			if tl.Activated {
				t.Fatalf("another org's activation leaked: %+v", tl)
			}
		}
		if _, err := r.Dispatch(ctx, Principal{Org: "acme"}, "skill_git_repos", nil); !errors.Is(err, ErrNotDispatchable) {
			t.Fatalf("a skill is attached, never called: %v", err)
		}
		if asks != 1 {
			t.Fatalf("the catalogue is asked once per window, asked %d times", asks)
		}
	})
}

// A brand skill wins a name collision against an org's own skill of that name.
func TestABrandSkillIsNotShadowedByAnOrgs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		asks := 0
		r := freshRegistry(t)
		r.Register(&brandSkills{ask: skillsApp(&asks, client.SkillEntry{Name: "deploy", Description: "brand"})})
		r.Register(&fakeProvider{src: SourceSkill, tools: []Tool{{Name: "skill_deploy", Source: SourceSkill, Description: "org"}}})
		got := r.List(context.Background(), Scope{Org: "acme"})
		if len(got) != 1 || got[0].Description != "brand" {
			t.Fatalf("brand must win: %+v", got)
		}
	})
}

// A deployment with no skills app lists no brand skills; a skills app that fails
// is asked again on the next listing rather than remembered as empty.
func TestAnAbsentOrFailingSkillsApp(t *testing.T) {
	ctx := context.Background()
	absent := &brandSkills{ask: func(context.Context, *client.SkillCatalogIn) (*client.SkillCatalog, error) {
		return nil, cloud.ErrNoPeer
	}}
	if got, err := absent.List(ctx, Scope{Org: "acme"}); err != nil || len(got) != 0 {
		t.Fatalf("no skills app: %v %+v", err, got)
	}

	fail := true
	asks := 0
	flaky := &brandSkills{ask: func(context.Context, *client.SkillCatalogIn) (*client.SkillCatalog, error) {
		asks++
		if fail {
			return nil, errors.New("skills: down")
		}
		return &client.SkillCatalog{Skills: []client.SkillEntry{{Name: "a"}}}, nil
	}}
	if _, err := flaky.List(ctx, Scope{Org: "acme"}); err == nil {
		t.Fatal("an outage must be an error, not an empty catalogue")
	}
	fail = false
	if got, err := flaky.List(ctx, Scope{Org: "acme"}); err != nil || len(got) != 1 || asks != 2 {
		t.Fatalf("recovery: %v %+v asks=%d", err, got, asks)
	}
}
