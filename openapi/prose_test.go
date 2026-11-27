package openapi

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/spec"
)

// A tag is the app that serves the operation (HIP-0139 §4), so its sentence is
// the app's own — never a sibling's that happens to hold a deeper route.
func TestProseIsTheAppsOwnSentence(t *testing.T) {
	parts := []Part{
		{App: "commerce", Doc: &spec.Document{
			Info:  spec.Info{Description: "Package commerce is selling: checkout, subscriptions, invoices."},
			Paths: map[string]spec.PathItem{"/v1/plans/entries": {"get": {Tags: []string{"commerce"}}}},
		}},
		{App: "plan", Doc: &spec.Document{
			Info:  spec.Info{Description: "Package plan is the plan catalog: every tier you can buy."},
			Paths: map[string]spec.PathItem{"/v1/plan": {"get": {Tags: []string{"plan"}}}},
		}},
	}
	said := prose(parts)
	if !strings.HasPrefix(said["plan"], "Package plan is the plan catalog") {
		t.Fatalf("plan took the wrong sentence: %q", said["plan"])
	}
	if !strings.HasPrefix(said["commerce"], "Package commerce is selling") {
		t.Fatalf("commerce took the wrong sentence: %q", said["commerce"])
	}
}

// An app with no package doc leaves its tag blank rather than taking the fleet's
// own sentence — an undescribed capability must stay distinguishable from a
// described one.
func TestProseIsSilentWhenTheAppSaysNothingAboutItself(t *testing.T) {
	parts := []Part{{App: "metrics", Doc: &spec.Document{
		Info:  fleetInfo,
		Paths: map[string]spec.PathItem{"/v1/metrics/query": {"get": {Tags: []string{"metrics"}}}},
	}}}
	if got, said := prose(parts)["metrics"]; said {
		t.Fatalf("the fleet's own sentence was published as a capability's description: %q", got)
	}
}
