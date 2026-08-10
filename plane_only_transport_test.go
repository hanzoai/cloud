package cloud_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A dependency that is not in this process is reached over the PEER PLANE, or it
// is disabled. There is no third transport, and this is the test that keeps it
// that way.
//
// It exists because there WAS a third one, and it was invisible for as long as it
// existed. clients/rpc.go declared a "ZAP RPC" client for nine subsystems, wired
// live in BuildDeps behind CLOUD_<X>_ZAP_ADDR, and every one of its methods
// returned:
//
//	cloud: ZAP RPC client for iam@iam.hanzo.svc:9653 not yet wired (zapc-gen pending)
//
// Nothing caught it. It compiled, it was covered by tests (which asserted the
// stub's error, and passed), and setting the address logged "deps.IAM → ZAP RPC"
// at boot — so the one signal an operator had said the wire was up while nothing
// crossed it. Setting CLOUD_VFS_ZAP_ADDR swapped a working S3-backed blob client
// for one that failed every Put and Get.
//
// The failure mode is not "someone writes a bad client". It is that a transport
// which never carries a byte looks exactly like one that does, from every angle
// except a running fleet. So the check is lexical and blunt on purpose: a
// deferred wire announces itself in the words it uses.
func TestNoDeferredTransportInClients(t *testing.T) {
	// The vocabulary of a wire that does not exist yet. Each of these appeared
	// verbatim in the thing this test replaces.
	banned := []struct{ needle, why string }{
		{"not yet wired", "a client that reports itself unwired is a transport that does not exist; delete it or finish it"},
		{"zapc-gen", "zapc-gen never landed; a marker for it is a promise the fleet cannot keep"},
		{"TODO", "a TODO in a transport is a wire someone is trusting and nobody has built"},
	}

	var findings []string
	err := filepath.WalkDir("clients", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		// The package doc explains the deleted stub on purpose — history is how
		// the next reader learns why the shape is what it is. Only doc.go may.
		if filepath.Base(path) == "doc.go" {
			return nil
		}
		src := string(b)
		for _, ban := range banned {
			if strings.Contains(src, ban.needle) {
				findings = append(findings, path+": contains "+ban.needle+" — "+ban.why)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk clients/: %v", err)
	}
	for _, f := range findings {
		t.Errorf("deferred transport in the clients package: %s", f)
	}
	if len(findings) > 0 {
		t.Log("A subsystem that is not in this process has exactly two honest shapes: " +
			"reached over the peer plane (plane.Ask / plane/<app>, addressed by NAME, no endpoint " +
			"to configure), or Disabled<Subsystem>() which says so. Anything in between is a " +
			"client that looks configured and fails every call, which is strictly worse than " +
			"having none — a missing client is diagnosed in seconds and a lying one in an incident.")
	}
}

// A peer is addressed by NAME, so there is no endpoint for a deployment to set.
//
// The eight CLOUD_<X>_ZAP_ADDR knobs that used to select the stub above are gone,
// and this keeps them gone. A knob is the visible half of the defect: it is what
// let a deployment believe it had chosen a transport. plane.Ask resolves a peer
// through zip.SocketPath(name), which is why removing the knobs removed nothing —
// there was never an address to supply.
//
// This is a lexical check on config.go alone, because that is the ONE place cloud
// reads its environment; a knob that does not appear there cannot reach BuildDeps.
func TestNoPeerEndpointKnobs(t *testing.T) {
	b, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	// Match the SHAPE, not a list of names — a list would have to be kept in step
	// with the subsystems, and the ninth one added would be the one that slips
	// through. Note the digits: O11Y is why this is not [A-Z_]+.
	const suffix = "_ZAP_ADDR"
	src := string(b)
	if !strings.Contains(src, suffix) {
		return
	}
	for line := range strings.SplitSeq(src, "\n") {
		if !strings.Contains(line, suffix) {
			continue
		}
		t.Errorf("config.go reads a peer endpoint knob: %s\n"+
			"A peer is reached by NAME over its own socket (plane.Ask → zip.SocketPath), so an "+
			"address is not something a deployment supplies. The last set of these selected a "+
			"client that failed every call while logging that the transport was up.",
			strings.TrimSpace(line))
	}
}
