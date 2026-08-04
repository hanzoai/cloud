package platform

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/zap-proto/zip"
)

// A release answers 202 and then runs detached, so everything the caller can
// learn about it has to be either refused up front or recorded. These cover both
// halves of the failure that made a release that launched nothing look like one
// in flight.

// The defect: {"repo":"cloud"} — a bare name where a clone URL belongs — parses
// without error but carries no scheme and no host, so it failed deep in the
// pipeline AFTER the 202 was returned with an image tag.
func TestBareRepoNameIsNotAURL(t *testing.T) {
	u, err := url.Parse("cloud")
	if err != nil {
		t.Fatalf("fixture: %q was expected to parse", "cloud")
	}
	if u.Scheme != "" || u.Host != "" {
		t.Fatalf("fixture: %q now has scheme=%q host=%q", "cloud", u.Scheme, u.Host)
	}
	// That is exactly the shape startRelease refuses, and why it cannot rely on
	// url.Parse returning an error.
	if !badRepoURL("cloud") {
		t.Error("a bare repo name must be refused")
	}
	for _, ok := range []string{
		"https://github.com/hanzoai/cloud",
		"https://git.hanzo.ai/hanzoai/cloud",
	} {
		if badRepoURL(ok) {
			t.Errorf("%q is a valid clone URL", ok)
		}
	}
}

// badRepoURL mirrors startRelease's guard so the rule is testable without an HTTP
// request; startRelease applies the same three conditions.
func badRepoURL(repo string) bool {
	if repo == "" {
		return false // omitted means the default repo
	}
	u, err := url.Parse(repo)
	return err != nil || u.Scheme == "" || u.Host == ""
}

func TestReleaseRecordIsQueryableByItsID(t *testing.T) {
	st := &ReleaseState{ID: "rel_test1", Image: "ghcr.io/hanzoai/cloud:v1.0.0", Version: "1.0.0", Status: "releasing"}
	recordRelease(st)
	got, ok := ReleaseByID("rel_test1")
	if !ok {
		t.Fatal("the id returned by a 202 must be answerable")
	}
	if got.Status != "releasing" || got.Image != st.Image {
		t.Errorf("recorded %+v", got)
	}
}

// The whole point: a release that dies in the detached pipeline reports FAILED
// with the step it reached, rather than staying "releasing" forever.
func TestAFailedReleaseSaysSoAndNamesTheStep(t *testing.T) {
	recordRelease(&ReleaseState{ID: "rel_test2", Status: "releasing"})
	recordRelease(&ReleaseState{
		ID: "rel_test2", Status: "failed", Reached: "none",
		Error: "release stopped before built: launch build: invalid build input",
	})
	got, ok := ReleaseByID("rel_test2")
	if !ok {
		t.Fatal("missing")
	}
	if got.Status != "failed" {
		t.Fatalf("status %q — a dead release must not read as in flight", got.Status)
	}
	if got.Reached != "none" || got.Error == "" {
		t.Errorf("a failure must say where it stopped and why: %+v", got)
	}
}

func TestUnknownReleaseIDIsNotFound(t *testing.T) {
	if _, ok := ReleaseByID("rel_nope"); ok {
		t.Error("an unknown id must not resolve")
	}
}

// The record is bounded, so a long-lived process cannot grow it without limit.
func TestOnlyTheLastReleasesAreKept(t *testing.T) {
	for i := range releasesKept + 5 {
		recordRelease(&ReleaseState{ID: "rel_bulk_" + string(rune('a'+i%26)) + string(rune('0'+i/26)), Status: "released"})
	}
	if n := len(Releases()); n > releasesKept {
		t.Errorf("kept %d releases, cap is %d", n, releasesKept)
	}
}

// Newest first, so a caller reads the current release without scanning.
func TestReleasesAreNewestFirst(t *testing.T) {
	releases.Lock()
	releases.byID, releases.order = map[string]*ReleaseState{}, nil
	releases.Unlock()
	for _, id := range []string{"rel_one", "rel_two", "rel_three"} {
		recordRelease(&ReleaseState{ID: id, Status: "released"})
	}
	got := Releases()
	if len(got) != 3 || got[0].ID != "rel_three" {
		t.Fatalf("want newest first, got %v", got)
	}
}

// The ordering invariant the whole pipeline exists to enforce: a tag is a receipt
// for a proven image, so a build failure stops before the tag is ever minted.
func TestAFailedBuildNeverReachesTheTag(t *testing.T) {
	tagged := false
	plan := releasePlan{
		build: func(context.Context) error { return errors.New("boom") },
		smoke: func(context.Context) error { return nil },
		tag:   func(context.Context) error { tagged = true; return nil },
		pin:   func(context.Context) error { return nil },
	}
	reached, err := plan.run(context.Background())
	if err == nil {
		t.Fatal("a failing build must fail the release")
	}
	if tagged {
		t.Error("a tag was minted for an image that never built")
	}
	if reached != stepNone {
		t.Errorf("reached %s, want none", reached)
	}
}

// getRunnerAs GETs a path as a VALIDATED IAM principal, setting the identity
// headers SanitizeIdentity mints from a signature-verified JWT. It is the GET
// counterpart of postRunnerAs (runner_test.go) and sets exactly the same four.
func getRunnerAs(t *testing.T, app *zip.App, path, user, org string, orgAdmin, superAdmin bool) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if orgAdmin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	if superAdmin {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// ONE AUTHORITY, THREE DOORS. Cutting a release, listing releases and reading one
// by id ask the same question — may this caller act on the platform's own release?
// — so they take one answer from one function (mayRelease). This drives all three
// over the role axis and asserts they agree principal for principal, which is what
// makes "the 202 hands back an id the caller can ask about" true by construction
// rather than by a comment asking two files to stay in step.
//
// It replaces a test that read runner.go and release.go as TEXT and asserted both
// mentioned the same predicate NAMES. Spelling was all it could ever see: it was
// green while both surfaces contradicted the published contract, and it would have
// stayed green had the two admitted different callers under the same names.
//
// The seams are stubbed to 500, so an admitted cut fails INSIDE the pipeline (502)
// and a refused one never makes an outbound call — every case is hermetic, and
// "not 403" is exactly "the gate let it through".
func TestReleaseSurfacesTakeOneAuthority(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	defer swapAPIBase(srv.URL)()
	defer swapRegistryBase(srv.URL)()

	for _, tc := range []struct {
		name                 string
		user, org            string
		orgAdmin, superAdmin bool
		admitted             bool
	}{
		// The legitimate actor, pinned on every door: a release must stay CUTTABLE
		// and readable, or this is a change that merely disables the path.
		{"SuperAdmin", "root-uuid", "admin", false, true, true},
		// The brand org owns `hanzoai` and IS the deployment's own org — the caller
		// the old gate admitted, and the whole point of the change.
		{"brand-org admin", "e7d7-uuid", "hanzo", true, false, false},
		{"foreign-org admin", "lux-uuid", "lux", true, false, false},
		{"plain member of the brand org", "member-uuid", "hanzo", false, false, false},
		{"no principal at all", "", "", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := runnerApp(t)
			cut, cutBody := postRunnerAs(t, app, tc.user, tc.org, tc.orgAdmin, tc.superAdmin, map[string]any{
				"repo": "https://github.com/hanzoai/cloud", "release": true,
				"image": "ghcr.io/hanzoai/cloud:v1"})
			releasing.Store(false)
			list, listBody := getRunnerAs(t, app, "/v1/runner/releases", tc.user, tc.org, tc.orgAdmin, tc.superAdmin)
			one, oneBody := getRunnerAs(t, app, "/v1/runner/releases/no-such-id", tc.user, tc.org, tc.orgAdmin, tc.superAdmin)

			for _, door := range []struct {
				what string
				code int
				body []byte
			}{
				{"cutting a release", cut, cutBody},
				{"listing releases", list, listBody},
				{"reading one release", one, oneBody},
			} {
				if admitted := door.code != http.StatusForbidden; admitted != tc.admitted {
					t.Errorf("%s: admitted=%v, want %v (HTTP %d — %s)",
						door.what, admitted, tc.admitted, door.code, door.body)
				}
			}
		})
	}
}
