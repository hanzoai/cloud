package cloud

import (
	"testing"

	"github.com/hanzoai/cloud/manifest"
)

// TestProcNameIsTheRoutedApp pins what a program calls itself when it carries a
// CO-RESIDENT passenger — the plugin/ai case, and the only one in the fleet.
//
// The name is not cosmetic. It becomes the zip AppName, which is both the
// `service` every log record ships under and the ZAP node identity the exporter
// connects with. When this answered "cloud" for the ai process, console's Models
// logs view (service `ai`) was empty while ai served every inference request, and
// the records were refused outright for claiming the front door's identity.
func TestProcNameIsTheRoutedApp(t *testing.T) {
	// The test cannot mean anything if zen stopped being a passenger, so ask the
	// manifest rather than assuming the arrangement this pins.
	if !manifest.Coresident("zen") {
		t.Fatal("zen is no longer Coresident — this test pins the passenger case and must be restated over whatever is one now")
	}
	if manifest.Coresident("ai") {
		t.Fatal("ai is Coresident — it routes the /v1 inference surface, so nothing here holds")
	}

	// plugin/ai/main.go's composition, in ITS order: zen mounts first so its Claim
	// precedes the catch-all ai registers.
	if got := procName([]Plugin{{Name: "zen"}, {Name: "ai"}}); got != "ai" {
		t.Errorf("procName(zen,ai) = %q, want \"ai\" — zen routes no prefix of its own, so the process is the app it rides on", got)
	}
	// Order must not decide it either.
	if got := procName([]Plugin{{Name: "ai"}, {Name: "zen"}}); got != "ai" {
		t.Errorf("procName(ai,zen) = %q, want \"ai\" — a passenger does not become the program by being listed second", got)
	}
}

// TestProcNameOfOneRoutedApp is the twenty-odd siblings that were always right:
// one plugin, its own name, its own service on the plane.
func TestProcNameOfOneRoutedApp(t *testing.T) {
	if got := procName([]Plugin{{Name: "tasks"}}); got != "tasks" {
		t.Errorf("procName(tasks) = %q, want \"tasks\"", got)
	}
}

// TestProcNameOfTheFusedHost keeps the answer that was right for the reason it
// was right: two apps that BOTH route is the fused binary, and no single app's
// name is honest for it.
func TestProcNameOfTheFusedHost(t *testing.T) {
	if got := procName([]Plugin{{Name: "iam"}, {Name: "kms"}}); got != "cloud" {
		t.Errorf("procName(iam,kms) = %q, want \"cloud\" — two routed apps in one process is the host", got)
	}
	if got := procName(nil); got != "cloud" {
		t.Errorf("procName(nil) = %q, want \"cloud\"", got)
	}
}
