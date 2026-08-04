package cloud

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// probeSHA is a syntactically real object name that is not any commit in this
// repo, so a binary reporting it can only have got it from the linker.
const probeSHA = "0123456789abcdef0123456789abcdef01234567"

// An ordinary `go test` build passes no -X, and that is the case that has to be
// LOUD rather than plausible: v1.801.426 shipped from older source and nothing
// noticed, so a build which cannot name its commit must say exactly that.
//
// Empty is the specific wrong answer. An empty string renders as `"revision":""`
// — a field that is present, looks like a real payload, and reads to a human
// skimming a rollout check as "no revision recorded" rather than "this process
// cannot tell you what it is".
func TestRevisionOfAnUnstampedBuildIsUnknown(t *testing.T) {
	if revision != "" {
		t.Fatalf("this test binary was stamped (%q); it must run unstamped to assert the default", revision)
	}
	got := Revision()
	if got != "unknown" {
		t.Fatalf("Revision() = %q, want %q", got, "unknown")
	}
	if got == "" {
		t.Fatal(`Revision() = "" — an unknown revision must be the word "unknown", never a blank that reads as a populated field`)
	}
}

// Everything that is NOT a commit reports "unknown", including the values most
// likely to arrive by accident: an unexpanded ${REVISION} when a build-arg was
// never passed, a branch name from a non-git build context, and a short sha from
// a `git rev-parse --short` somebody thought was equivalent.
//
// A near-miss is worse than no answer, because someone acts on it.
func TestRevisionReportsOnlyAFullCommitName(t *testing.T) {
	restore := revision
	t.Cleanup(func() { revision = restore })

	for _, bad := range []string{
		"",
		"unknown",
		"${REVISION}",
		"main",
		"dev",
		"v1.801.426",
		"0123456789ab", // short sha
		"0123456789abcdef0123456789abcdef0123456",    // 39
		"0123456789abcdef0123456789abcdef012345678",  // 41
		"0123456789ABCDEF0123456789ABCDEF01234567",   // uppercase
		"0123456789abcdef0123456789abcdef0123456g",   // non-hex
		" 0123456789abcdef0123456789abcdef01234567",  // leading space
		"0123456789abcdef0123456789abcdef01234567\n", // trailing newline
		"0123456789abcdef0123456789abcdef01234567-dirty",
	} {
		revision = bad
		if got := Revision(); got != "unknown" {
			t.Errorf("revision=%q: Revision() = %q, want %q", bad, got, "unknown")
		}
		if IsCommit(bad) {
			t.Errorf("IsCommit(%q) = true, want false", bad)
		}
	}

	revision = probeSHA
	if got := Revision(); got != probeSHA {
		t.Fatalf("revision=%q: Revision() = %q, want it reported verbatim", probeSHA, got)
	}
	if !IsCommit(probeSHA) {
		t.Fatalf("IsCommit(%q) = false, want true", probeSHA)
	}
}

// THE ONE THAT MATTERS. It links a real binary with the flag the Dockerfile
// passes and asks the PROCESS what it is.
//
// A test that merely assigned `revision` itself would pass forever while the
// real stamp reached nothing: `-X` naming an import path or symbol the linker
// cannot resolve is not an error, it is dropped silently, and the binary then
// reports the perfectly legitimate "unknown". So renaming this package, moving
// the variable or mistyping the path in the Dockerfile all fail in the one way
// nobody looks at — unless something links and runs the result. This does.
func TestRevisionStampReachesTheLinker(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "revision.test")

	build := exec.Command("go", "test", "-c", "-o", bin,
		"-ldflags", "-X github.com/hanzoai/cloud.revision="+probeSHA, ".")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("linking a stamped test binary failed: %v\n%s", err, out)
	}

	run := exec.Command(bin, "-test.run", "^TestRevisionIsTheStampedCommit$", "-test.v")
	out, err := run.CombinedOutput()

	// Read the ANSWER out of the output, never the exit code. A skipped test and a
	// passing test exit 0 alike, so an inner test that skips itself when it finds
	// no stamp reports success in precisely the case this is here to catch. The
	// first draft of this test did that, and a deliberately mis-named symbol —
	// a dropped stamp, the real failure — sailed through it green.
	want := "REVISION=" + probeSHA
	if err != nil || !bytes.Contains(out, []byte(want)) {
		t.Fatalf("the stamped binary never printed %q: the -X flag named an import path or "+
			"symbol the linker could not resolve, so it was silently dropped and every "+
			"health response this fleet serves would report \"unknown\".\nerr: %v\n%s",
			want, err, out)
	}
	t.Logf("the process was asked and answered:\n%s", out)
}

// TestRevisionIsTheStampedCommit is the inner half of the test above — the
// assertion that runs INSIDE the stamped binary. An ordinary run of this package
// links no -X, so it skips there and means nothing on its own; the outer test is
// what gives it a stamp and reads its verdict.
func TestRevisionIsTheStampedCommit(t *testing.T) {
	// Printed unconditionally, and printed BEFORE the skip, because this line is
	// the outer test's evidence. Whatever this binary believes it is, it says so.
	t.Logf("REVISION=%s", Revision())
	if revision == "" {
		t.Skip("unstamped binary; TestRevisionStampReachesTheLinker links a stamped one and runs this")
	}
	if got := Revision(); got != probeSHA {
		t.Fatalf("Revision() = %q, want the linked %q", got, probeSHA)
	}
}

// The ops listener is the unauthenticated in-cluster read — what a rollout check
// asks, on the health port, with no token and no product API in the way. Every
// route it serves carries the revision, including the 503 a draining pod answers,
// because "which commit is refusing me" is exactly when it is worth knowing.
func TestOpsListenerReportsTheRevision(t *testing.T) {
	srv := httptest.NewServer(healthMux())
	t.Cleanup(srv.Close)

	for _, path := range []string{"/healthz", "/readyz", "/health"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		var body map[string]any
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("GET %s: decode: %v", path, err)
		}
		rev, ok := body["revision"]
		if !ok {
			t.Fatalf("GET %s: %v has no revision field — this listener is how a rollout "+
				"check asks what is serving it", path, body)
		}
		if rev != Revision() || rev == "" {
			t.Fatalf("GET %s: revision = %q, want %q", path, rev, Revision())
		}
	}
}

// One payload, every status. The bodies on the ops listener used to be fixed byte
// strings and the product API built its own map, which is how a payload comes to
// carry a field on one surface and not another — so the field is asserted for
// every status the one builder produces, not just the happy one.
func TestHealthBodyAlwaysCarriesTheRevision(t *testing.T) {
	for _, status := range []string{"ok", "degraded", "draining"} {
		body := healthBody(status)
		if body["status"] != status {
			t.Errorf("healthBody(%q) status = %v", status, body["status"])
		}
		if body["revision"] != Revision() || body["revision"] == "" {
			t.Errorf("healthBody(%q) revision = %v, want %q", status, body["revision"], Revision())
		}
	}
}
