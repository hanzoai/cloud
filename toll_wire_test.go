package cloud

import (
	"os"
	"strings"
	"testing"
)

// The four-door proof in toll_test.go builds its OWN app and calls
// app.Authorize(Toll(...)) itself. That proves Toll charges correctly wherever it
// is installed. It says nothing about whether the SERVER installs it — delete the
// two lines in serve.go and every one of those subtests still passes, measured.
//
// Two claims, and only one of them had a gate:
//
//	Toll charges once on every door        toll_test.go       proven
//	the server actually mounts Toll        nothing            <- this file
//
// That gap is the shape of the failure this whole client exists to stop. Money was
// gated by HTTP middleware and tested green for as long as the tests only spoke
// HTTP; the gate was real, the coverage was real, and the hole was real, all at
// once. A gate nothing mounts is worth exactly as much as no gate, and it is worse
// than no gate because the passing suite reads as assurance.
//
// So this reads the composition root as TEXT. That is deliberate and it is the
// point: an assertion against a live App would ask the object what it was
// configured with, and a hook can be installed and then overwritten by a later
// call — the last writer wins and the object cannot tell you there were two. The
// source says how many times the line appears.
func TestServerMountsTheToll(t *testing.T) {
	b, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatalf("read serve.go: %v", err)
	}
	src := string(b)

	for _, want := range []struct{ line, why string }{
		{"app.Authorize(Toll(deps.Metering, deps.Commerce))",
			"the main app — REST, MCP tools/call and CLI LocalInvoke all reach op.invoke through it"},
		{"app.Peer().Authorize(Toll(deps.Metering, deps.Commerce))",
			"the peer plane is a SEPARATE zip.App with its own hook, so it needs the gate explicitly"},
	} {
		if n := strings.Count(src, want.line); n != 1 {
			t.Errorf("serve.go has %d of\n\t%s\nwant exactly 1 — %s", n, want.line, want.why)
		}
	}
}
