package cloud

import (
	"reflect"
	"testing"
)

// TestControlPlaneConfigParsing pins the Stage-0 control-plane config contract:
// the four topology knobs read from their EXACT env keys with the EXACT parsers
// LoadConfig uses, and — the Stage-0 guarantee — every one is INERT by default:
// unset ⇒ zero value (no node id, no peers, no role, quorum 0). Nothing consumes
// these yet; this locks the env-key names and the inert defaults so a later stage
// can wire the Quasar-PQ engine without silently shifting the operator surface.
//
// Mirrors the helper-level style of TestGetenvIntReadBuffer (config_readbuffer_test.go):
// the suite deliberately avoids LoadConfig(), which calls the process-global
// flag.Parse and cannot be re-entered across tests.
func TestControlPlaneConfigParsing(t *testing.T) {
	t.Run("unset yields inert zero values", func(t *testing.T) {
		for _, k := range []string{"NODE_ID", "PEERS", "ROLE", "CONTROL_PLANE_QUORUM"} {
			t.Setenv(k, "")
		}
		if got := getenv("NODE_ID", ""); got != "" {
			t.Errorf("NODE_ID unset = %q, want empty", got)
		}
		if got := splitTrim(getenv("PEERS", "")); got != nil {
			t.Errorf("PEERS unset = %v, want nil", got)
		}
		if got := getenv("ROLE", ""); got != "" {
			t.Errorf("ROLE unset = %q, want empty", got)
		}
		if got := getenvInt("CONTROL_PLANE_QUORUM", 0); got != 0 {
			t.Errorf("CONTROL_PLANE_QUORUM unset = %d, want 0", got)
		}
	})

	t.Run("set values parse", func(t *testing.T) {
		t.Setenv("NODE_ID", "cloud-0")
		t.Setenv("PEERS", "cloud-1:9653, cloud-2:9653 ,") // trailing/space entries dropped
		t.Setenv("ROLE", "voter")
		t.Setenv("CONTROL_PLANE_QUORUM", "3")

		if got := getenv("NODE_ID", ""); got != "cloud-0" {
			t.Errorf("NODE_ID = %q, want cloud-0", got)
		}
		if got, want := splitTrim(getenv("PEERS", "")), []string{"cloud-1:9653", "cloud-2:9653"}; !reflect.DeepEqual(got, want) {
			t.Errorf("PEERS = %v, want %v", got, want)
		}
		if got := getenv("ROLE", ""); got != "voter" {
			t.Errorf("ROLE = %q, want voter", got)
		}
		if got := getenvInt("CONTROL_PLANE_QUORUM", 0); got != 3 {
			t.Errorf("CONTROL_PLANE_QUORUM = %d, want 3", got)
		}
	})
}
