package platform

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
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
		build:  func(context.Context) error { return errors.New("boom") },
		smoke:  func(context.Context) error { return nil },
		tag:    func(context.Context) error { tagged = true; return nil },
		notify: func(context.Context) error { return nil },
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

// Reading a release must never require MORE authority than starting one, or the
// 202 hands back an id the caller cannot ask about — the gap these routes close.
// Both sides read the same two predicates, so this holds them together.
func TestReadingAReleaseIsNotStricterThanCuttingOne(t *testing.T) {
	src, err := os.ReadFile("release.go")
	if err != nil {
		t.Fatalf("read release.go: %v", err)
	}
	gate, err := os.ReadFile("runner.go")
	if err != nil {
		t.Fatalf("read runner.go: %v", err)
	}
	// The cut admits platform sudo OR the owning org's admin; the read must admit
	// the same two, not sudo alone.
	for _, need := range []string{"principal.IsSuperAdmin", "principal.IsOrgAdmin", "imageInOrgRegistry"} {
		if !strings.Contains(string(src), need) {
			t.Errorf("the release read does not consider %s, which the cut does", need)
		}
		if !strings.Contains(string(gate), need) {
			t.Errorf("fixture: the cut no longer uses %s", need)
		}
	}
}
