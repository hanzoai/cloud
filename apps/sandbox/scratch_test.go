package sandbox

import "testing"

// A coding run leases a dev box with no project: its work leaves by push, and
// its disk should die with the pod. The lease used to refuse that, so every run
// from a chat surface stopped at "project required for class dev".
func TestScratchIsAnyExecSizedClass(t *testing.T) {
	for _, name := range []string{"exec", "dev", "desktop"} {
		if !classes[name].scratch() {
			t.Errorf("%s is exec-sized and must be leasable with no project", name)
		}
	}
	if classes["android"].scratch() {
		t.Error("android asks for a device and more than the exec box; it must name a project")
	}
}
