package coding

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
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

// A follow-up starts from the earlier run's branch only where the remote holds
// it. A run that stopped before it pushed left none there, and a clone of it
// failed ("Remote branch agent/… not found in upstream origin"): such a follow-up
// starts from the earlier run's own base, else the repository's default.
func TestFollowBase(t *testing.T) {
	branch := BranchFor("sess_0123456789abcdef")
	for name, c := range map[string]struct {
		held              []string
		earlierBase, home string
		want              string
	}{
		"the pushed branch is built on":                   {[]string{branch, "dev"}, "dev", "main", branch},
		"an unpushed one gives way to the earlier base":   {[]string{"dev", "main"}, "dev", "main", "dev"},
		"and that one, gone too, to the default":          {[]string{"main"}, "dev", "main", "main"},
		"an earlier run with no base goes to the default": {nil, "", "main", "main"},
		"a base that names the branch is not taken":       {nil, branch, "main", "main"},
		"with no default named, the clone's own":          {nil, "dev", "", ""},
	} {
		on := func(b string) bool {
			for _, h := range c.held {
				if h == b {
					return true
				}
			}
			return false
		}
		if got := followBase(on, branch, c.earlierBase, c.home); got != c.want {
			t.Errorf("%s: followBase(%v, %q, %q, %q) = %q, want %q", name, c.held, branch, c.earlierBase, c.home, got, c.want)
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

func TestSandboxRun_CloneFallsBackWhenAgentBranchNotOnOrigin(t *testing.T) {
	clones := 0
	p := &pod{answer: func(argv []string) client.Ran {
		if slices.Contains(argv, "clone") {
			clones++
			if slices.Contains(argv, "-b") && slices.Contains(argv, "agent/e0b41308e66d") {
				return client.Ran{
					ExitCode: 128,
					Stderr:   "fatal: Remote branch agent/e0b41308e66d not found in upstream origin\n",
				}
			}
			return client.Ran{}
		}
		if slices.Contains(argv, "rev-parse") {
			return client.Ran{Stdout: theSHA + "\n"}
		}
		return client.Ran{}
	}}
	servePod(t, p)

	req := aRun()
	req.BaseBranch = "agent/e0b41308e66d"
	res, err := (sandboxRunner{}).Run(context.Background(), "acme", "u_1", req, nil)
	if err != nil {
		t.Fatalf("run failed instead of falling back to default branch: %v", err)
	}
	if !res.OK {
		t.Fatalf("run did not succeed: %+v", res)
	}
	if clones != 2 {
		t.Fatalf("expected 2 clone attempts (agent branch then default), got %d", clones)
	}
}
