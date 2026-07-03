package projectsvc

import (
	"context"
	"testing"
)

// TestProjectOwnership proves the cross-org ownership SQL that the identity trust
// boundary relies on: mine iff the org owns the id/slug, other iff a DIFFERENT org
// owns it, matched by both slug and opaque id.
func TestProjectOwnership(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, p := range []Project{
		mkProject("acme", "site-a", "Acme Site A"),
		mkProject("beta", "secret", "Beta Secret"),
		mkProject("acme", "shared", "Acme Shared"),
		mkProject("beta", "shared", "Beta Shared"),
	} {
		if err := s.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject %s/%s: %v", p.Org, p.Slug, err)
		}
	}

	cases := []struct {
		name, org, id     string
		wantMine, wantOth bool
	}{
		{"own by slug", "acme", "site-a", true, false},
		{"own by id", "acme", "proj_acme_site-a", true, false},
		{"another org's project", "acme", "secret", false, true},
		{"another org's project by id", "acme", "proj_beta_secret", false, true},
		{"unregistered", "acme", "ghost", false, false},
		{"same slug in both orgs → mine AND other", "acme", "shared", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mine, other, err := s.ProjectOwnership(ctx, tc.org, tc.id)
			if err != nil {
				t.Fatalf("ProjectOwnership: %v", err)
			}
			if mine != tc.wantMine || other != tc.wantOth {
				t.Fatalf("ProjectOwnership(%q,%q)=(mine=%v,other=%v) want (mine=%v,other=%v)",
					tc.org, tc.id, mine, other, tc.wantMine, tc.wantOth)
			}
		})
	}
}
