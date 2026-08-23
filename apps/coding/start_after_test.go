package coding

import (
	"strings"
	"testing"
)

// A follow-up run starts from the earlier run's branch.
//
// This is the whole of "continue the work": the engine already clones at the
// base, and a run's branch is a pure function of its session, so building on an
// earlier run needs no second path through the engine — only a way to say WHICH
// run, without the caller having to know how a branch name is built.
//
// It drives BaseOf, which is the one definition Start calls; a test that restated
// the rule beside it would stay green through exactly the change that matters.
func TestBaseOf(t *testing.T) {
	const earlier = "sess_0123456789abcdef"
	for name, c := range map[string]struct{ base, after, want string }{
		"a session names the branch to build on": {"", earlier, BranchFor(earlier)},
		"a given base is taken as it is":         {"release/2", "", "release/2"},
		"a given base is not overruled":          {"release/2", earlier, "release/2"},
		"neither leaves the repo's default":      {"", "", ""},
		"blank is not a session":                 {"", "   ", ""},
	} {
		if got := BaseOf(c.base, c.after); got != c.want {
			t.Errorf("%s: BaseOf(%q, %q) = %q, want %q", name, c.base, c.after, got, c.want)
		}
	}
}

// The derived base is inside the one namespace a run may write, and passes the
// same shape check a caller-given base does.
//
// Both matter. The namespace is what lets the forge state its ref rule
// structurally rather than checking it; the shape check is what stops a base
// reaching `git clone -b <base>` as a FLAG rather than a branch. A session id is
// not a branch name, so turning one into a branch here — instead of accepting a
// branch — is what makes the first property hold for this path too.
func TestADerivedBaseIsStillABranch(t *testing.T) {
	for _, session := range []string{
		"sess_0123456789abcdef",
		"01HQ8F5ZK9",
		"a",
		"sess_-----------------",
	} {
		got := BaseOf("", session)
		if !strings.HasPrefix(got, "agent/") {
			t.Errorf("BaseOf(\"\", %q) = %q, outside the namespace a run may write", session, got)
		}
		if !BaseRE.MatchString(got) {
			t.Errorf("BaseOf(\"\", %q) = %q, which the base shape check would refuse", session, got)
		}
	}
}
