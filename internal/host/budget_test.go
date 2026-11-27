package host

import (
	"testing"
	"time"

	"github.com/hanzoai/cloud/manifest"
)

// A process drains for its app's grace less the margin zip needs to kill it after; an app that
// states none keeps the 15s it always had.
func TestAChildDrainsForItsAppsGraceLessTheKillMargin(t *testing.T) {
	if got := childBudget("kms"); got != 15*time.Second {
		t.Fatalf("a default app drains for %v, want 15s", got)
	}
	if got, want := childBudget("agent"), manifest.GraceOf("agent")-killMargin; got != want {
		t.Fatalf("the agent drains for %v, want %v", got, want)
	}
}
