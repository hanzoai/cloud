package projects

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// TestSetProjectDefaults pins the pure default-application logic — the ONE place
// a new project's wired-by-default settings are decided: analytics ON unless the
// caller opted out, and the Base data-space namespace "<org>/<slug>".
func TestSetProjectDefaults(t *testing.T) {
	f := false
	tr := true
	cases := []struct {
		name     string
		optOut   *bool
		wantAnal bool
	}{
		{"absent defaults ON", nil, true},
		{"explicit true stays ON", &tr, true},
		{"explicit false opts OUT", &f, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Project{Org: "acme", Slug: "landing"}
			setProjectDefaults(&p, tc.optOut)
			if p.Analytics != tc.wantAnal {
				t.Fatalf("analytics=%v want %v", p.Analytics, tc.wantAnal)
			}
			if p.SpaceId != "acme/landing" {
				t.Fatalf("space=%q want acme/landing", p.SpaceId)
			}
		})
	}
}

// TestCreateProject_AnalyticsDefaultOn is the wire proof that a freshly created
// project is analytics-ON and space-wired with NO opt-in: POST /v1/projects with
// just a name returns a project whose analytics is true and whose Base data space
// is "<org>/<slug>". The default base embed is OFF, so this also proves the
// fail-soft path — space provisioning returns ErrNotEmbedded yet create is 201.
func TestCreateProject_AnalyticsDefaultOn(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/projects", "acme", map[string]any{"name": "Landing"})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var p projectView
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	if !p.Analytics {
		t.Fatalf("analytics want true by default, got false")
	}
	if p.Space != "acme/"+p.Slug {
		t.Fatalf("space want acme/%s, got %q", p.Slug, p.Space)
	}
	// The default persists: a fresh GET reports the same wired defaults.
	_, gb := do(t, app, http.MethodGet, "/v1/projects/"+p.Slug, "acme", nil)
	var got projectView
	_ = json.Unmarshal(gb, &got)
	if !got.Analytics || got.Space != p.Space {
		t.Fatalf("persisted defaults drift: analytics=%v space=%q", got.Analytics, got.Space)
	}
}

// TestCreateProject_AnalyticsOptOut proves the escape hatch: analytics:false at
// create opts the project out. Default-ON, but overridable.
func TestCreateProject_AnalyticsOptOut(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Private", "analytics": false})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var p projectView
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	if p.Analytics {
		t.Fatalf("analytics want false after opt-out, got true")
	}
	if p.Space != "acme/"+p.Slug {
		t.Fatalf("space still wired: want acme/%s, got %q", p.Slug, p.Space)
	}
}

// TestCreateProject_ProvisionsSpace proves the Base data space is provisioned on
// create through the ONE default-application path: the wired provisioner is
// called exactly once with the project's org, and the persisted project carries
// its space namespace.
func TestCreateProject_ProvisionsSpace(t *testing.T) {
	app := mountApp(t)
	var gotOrg string
	calls := 0
	mounted.State.ensureSpace = func(_ context.Context, org string) error {
		calls++
		gotOrg = org
		return nil
	}
	code, body := do(t, app, http.MethodPost, "/v1/projects", "acme", map[string]any{"name": "Shop"})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	if calls != 1 || gotOrg != "acme" {
		t.Fatalf("ensureSpace calls=%d org=%q want 1/acme", calls, gotOrg)
	}
	var p projectView
	_ = json.Unmarshal(body, &p)
	if p.Space != "acme/"+p.Slug {
		t.Fatalf("space want acme/%s, got %q", p.Slug, p.Space)
	}
}

// TestCreateProject_ProvisionFailureIsFailSoft locks the graceful-degradation
// contract: a Base provisioning error must NOT fail project creation. The project
// is still created (201) and persisted with its wired defaults; the side effect
// is logged and swallowed, exactly like the edge cache purge.
func TestCreateProject_ProvisionFailureIsFailSoft(t *testing.T) {
	app := mountApp(t)
	mounted.State.ensureSpace = func(_ context.Context, _ string) error {
		return errors.New("base down")
	}
	code, body := do(t, app, http.MethodPost, "/v1/projects", "acme", map[string]any{"name": "Resilient"})
	if code != http.StatusCreated {
		t.Fatalf("provisioning failure must not fail create: want 201, got %d (%s)", code, body)
	}
	var p projectView
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	if !p.Analytics || p.Space == "" {
		t.Fatalf("defaults must still apply on fail-soft: analytics=%v space=%q", p.Analytics, p.Space)
	}
	// The project really persisted despite the provisioning error.
	if gc, _ := do(t, app, http.MethodGet, "/v1/projects/"+p.Slug, "acme", nil); gc != http.StatusOK {
		t.Fatalf("project must persist on fail-soft, GET got %d", gc)
	}
}
