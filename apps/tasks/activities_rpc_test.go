package tasks

// The two refusals that make the plane read safe to depend on. Both are the shape
// of the bug it fixes: visor asked its OWN engine for a namespace only the tasks
// app ever wrote, got back an empty page and no error, and reported a GPU that was
// heartbeating every 30 seconds as no fleet at all.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
)

// A caller with no org cannot page activities. The org rides the CALLER — the
// gateway's assertion, or what a background job stated with cloud.For — and is
// absent from ActivitiesIn entirely, so naming another tenant's shard is not
// something an input here can express. This pins the one case where that could
// still leak: an unattributed call must be refused rather than defaulted.
func TestActivitiesRefusesACallWithNoOrg(t *testing.T) {
	if _, err := planeActivities(context.Background(), &client.ActivitiesIn{Namespace: "fleet"}); err == nil {
		t.Fatal("an unattributed call was answered; it must be refused")
	}
}

// An engine this process does not have is an ERROR, never an empty page.
//
// This is the whole defect, one layer down: an empty page is indistinguishable
// from a genuinely empty namespace, so every reader above renders "no workers, no jobs"
// and nothing anywhere reports a fault. The caller (visor's allActivitiesForOrg)
// is deliberately fail-soft, so if this answered an empty page instead of an
// error, the fleet would silently read as empty exactly like before.
func TestActivitiesWithNoEngineIsAnErrorNotAnEmptyPage(t *testing.T) {
	if cloud.EmbeddedTasks() != nil {
		t.Skip("this process has an engine; the no-engine path is what is under test")
	}
	out, err := planeActivities(cloud.For(context.Background(), "hanzo"),
		&client.ActivitiesIn{Namespace: "fleet"})
	if err == nil {
		t.Fatalf("no engine answered %+v with no error; an unreadable engine must fault", out)
	}
}
