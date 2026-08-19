package lineage

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// run executes a git command in dir with a deterministic identity, so the commits
// these tests build are reproducible and depend on nothing about the machine.
func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"LC_ALL=C",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit adds one commit to dir's current branch and returns its full name.
func commit(t *testing.T, dir, msg string) string {
	t.Helper()
	if err := os.WriteFile(dir+"/f", []byte(msg), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "add", "f")
	run(t, dir, "commit", "--quiet", "-m", msg)
	return run(t, dir, "rev-parse", "HEAD")
}

// repo builds a repository with `n` commits on main and returns its path plus every
// commit name in order. `name` distinguishes one history from another: a commit name
// is a hash of its content and its ancestors, so two repositories built from the same
// content, messages and fixed dates are not two histories — they are the same one
// with two checkouts, and would be reachable from each other's branch.
func repo(t *testing.T, name string, n int) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "--quiet", "--initial-branch=main")
	var shas []string
	for i := range n {
		shas = append(shas, commit(t, dir, fmt.Sprintf("%s-%d", name, i)))
	}
	return dir, shas
}

// TestVerify covers the whole predicate: what an arbiter's release branch reaches,
// and every shape of thing it does not.
func TestVerify(t *testing.T) {
	// The arbiter: three commits on main, then one more on a side branch that main
	// never merges. The side commit is real, and in this very repository, and still
	// not part of the sequence main numbers.
	arb, main := repo(t, "arbiter", 3)
	run(t, arb, "checkout", "--quiet", "-b", "side")
	side := commit(t, arb, "side")
	run(t, arb, "checkout", "--quiet", "main")

	// A second repository sharing no history at all — the shape of two projects that
	// publish the same image name from different lines of development.
	_, other := repo(t, "elsewhere", 2)

	a := Arbiter{Remote: arb, Branch: "main"}

	admits := map[string]string{
		"first commit on the branch":  main[0],
		"middle commit on the branch": main[1],
		"the branch tip itself":       main[2],
	}
	for name, sha := range admits {
		t.Run("admits "+name, func(t *testing.T) {
			if err := a.Verify(t.Context(), "", sha); err != nil {
				t.Fatalf("a commit on the release branch must be admitted, got: %v", err)
			}
		})
	}

	refuses := map[string]string{
		// The defect this exists to stop: a real commit, from a real history, that
		// the arbiter's branch cannot reach.
		"a commit from an unrelated history": other[1],
		"the tip of an unrelated history":    other[0],
		// Present in the arbiter, absent from the branch that numbers the sequence.
		"a commit on a branch main never merged": side,
		// Nothing holds it.
		"a commit that exists nowhere": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		"the all-zero name":            strings.Repeat("0", 40),
		// A name that is not one commit exactly.
		"an abbreviated name":  main[0][:12],
		"an empty name":        "",
		"a branch name":        "main",
		"a name with a suffix": main[0] + "^",
	}
	for name, sha := range refuses {
		t.Run("refuses "+name, func(t *testing.T) {
			err := a.Verify(t.Context(), "", sha)
			if err == nil {
				t.Fatalf("%s must be refused, it was admitted", name)
			}
			if !errors.Is(err, ErrForeign) {
				t.Fatalf("%s must be refused AS a lineage refusal, got: %v", name, err)
			}
			// A refusal that does not say which commit and which arbiter sends the
			// reader nowhere.
			if sha != "" && !strings.Contains(err.Error(), sha) {
				t.Errorf("refusal must name the commit %q: %v", sha, err)
			}
		})
	}
}

// TestAMergeAdmitsNothingItAbsorbed is the reason the predicate is first-parent and
// not ancestry.
//
// A merge makes everything it absorbed an ancestor of the branch, permanently, and it
// needs no force and no rewriting — an ordinary merge that fast-forwards, which a
// branch protected against force-pushes accepts by design. So under an ancestor test
// one merge hands a whole foreign history the right to take numbers out of this
// sequence, and nothing about the branch's protection stands in the way.
//
// The test asserts BOTH halves, because only the pair says anything: that git really
// does consider those commits ancestors, and that Verify refuses them anyway. Drop
// the first and the test would still pass against a predicate that never looks at the
// graph at all.
func TestAMergeAdmitsNothingItAbsorbed(t *testing.T) {
	arb, main := repo(t, "arbiter", 3)
	elsewhere, foreign := repo(t, "elsewhere", 2)

	// Exactly the operation that is available without force: fetch the other history
	// and merge it. `-X ours` settles the file both sides happen to write; the
	// content is not what this measures.
	run(t, arb, "fetch", "--quiet", elsewhere, "main:refs/foreign")
	run(t, arb, "merge", "--quiet", "--no-ff", "--allow-unrelated-histories", "-X", "ours",
		"-m", "merge", "refs/foreign")
	merge := run(t, arb, "rev-parse", "HEAD")

	a := Arbiter{Remote: arb, Branch: "main"}

	// The states the branch has been: everything before the merge, and the merge.
	for _, sha := range append(append([]string{}, main...), merge) {
		if err := a.Verify(t.Context(), "", sha); err != nil {
			t.Fatalf("a commit the branch has been must be admitted, %s got: %v", sha, err)
		}
	}

	for i, sha := range foreign {
		t.Run(fmt.Sprintf("absorbed commit %d", i), func(t *testing.T) {
			// An ancestor test would say yes here. That is the whole point.
			if err := exec.Command("git", "-C", arb, "merge-base", "--is-ancestor", sha, "main").Run(); err != nil {
				t.Fatalf("the merge was supposed to make %s an ancestor; without that this test proves nothing: %v", sha, err)
			}
			err := a.Verify(t.Context(), "", sha)
			if err == nil {
				t.Fatalf("%s was merged in, never a state of the branch, and must not be able to hold a number", sha)
			}
			if !errors.Is(err, ErrForeign) {
				t.Fatalf("a merged-in commit must be refused AS a lineage refusal, got: %v", err)
			}
			if !strings.Contains(err.Error(), sha) {
				t.Errorf("the refusal must name the commit %q: %v", sha, err)
			}
		})
	}
}

// TestVerifyReportsAnUnreadableArbiter separates the two ways a claim stops. Both
// refuse, and they are not the same fact: one says the commit does not belong, the
// other says the arbiter could not be asked. Reporting the second as the first would
// send someone hunting a lineage problem that is really a broken remote.
func TestVerifyReportsAnUnreadableArbiter(t *testing.T) {
	_, shas := repo(t, "any", 1)
	a := Arbiter{Remote: t.TempDir() + "/absent", Branch: "main"}

	err := a.Verify(t.Context(), "", shas[0])
	if err == nil {
		t.Fatal("an arbiter that cannot be read must stop the claim")
	}
	if errors.Is(err, ErrForeign) {
		t.Fatalf("an unreadable arbiter is not a lineage refusal: %v", err)
	}
	if !strings.Contains(err.Error(), a.Remote) {
		t.Errorf("the refusal must name the arbiter it could not read: %v", err)
	}
}

// TestVerifyRefusesWithNoArbiter fails closed on an unconfigured arbiter rather than
// treating "nothing to check against" as "nothing to object to".
func TestVerifyRefusesWithNoArbiter(t *testing.T) {
	_, shas := repo(t, "any", 1)
	for _, a := range []Arbiter{{}, {Remote: "x"}, {Branch: "main"}} {
		if err := (a).Verify(t.Context(), "", shas[0]); err == nil {
			t.Fatalf("%+v must refuse: an unconfigured arbiter cannot admit anything", a)
		}
	}
}

// TestARefusalCarriesNoCredential. Every refusal names the arbiter so a reader can
// act on it, and the arbiter is reached with a token — so the refusal is the place a
// credential would surface if one ever reached the text. Nothing here puts it there:
// it travels as a base64 header rather than as URL userinfo, and git prints the URL.
//
// The remote is a closed local port, so this asks the question with no network and no
// forge: what matters is that a credential was set and an https fetch failed.
func TestARefusalCarriesNoCredential(t *testing.T) {
	const token = "s3cret-forge-token"
	err := Arbiter{Remote: "https://127.0.0.1:1/x/y", Branch: "main"}.
		Verify(t.Context(), token, strings.Repeat("a", 40))
	if err == nil {
		t.Fatal("a remote that cannot be reached must stop the claim")
	}
	header := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	for _, secret := range []string{token, header} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the refusal carries the credential: %v", err)
		}
	}
}

// TestCloudNamesOneArbiter holds the value the claim reads. One sequence numbered
// by two histories is the defect this package exists to prevent, so which
// repository, which branch and which image is asserted rather than assumed.
//
// The image is asserted HERE, beside the repository, because that pairing is the
// property: these bytes are published by that branch and by nothing else. Read from
// somewhere else it is a coincidence, and a coincidence between an image named
// hanzoai/cloud and a repository named hanzoai/cloud is exactly what a release lane
// mistook for an identity.
func TestCloudNamesOneArbiter(t *testing.T) {
	if Cloud.Remote != "https://git.hanzo.ai/hanzo-inc/cloud" {
		t.Errorf("the cloud sequence is numbered by one repository, got %q", Cloud.Remote)
	}
	if Cloud.Branch != "main" {
		t.Errorf("the cloud sequence is numbered along one branch, got %q", Cloud.Branch)
	}
	if Cloud.Image != "ghcr.io/hanzoai/cloud" {
		t.Errorf("the cloud sequence names one image, got %q", Cloud.Image)
	}
}

// TestAPIAddressesTheRepositoryItVerifies is the reason API is a method and not a
// field: the write goes to the object the read just judged, so the two cannot be
// pointed at different repositories.
func TestAPIAddressesTheRepositoryItVerifies(t *testing.T) {
	if got, want := Cloud.API(), "https://git.hanzo.ai/v1/repos/hanzo-inc/cloud"; got != want {
		t.Errorf("the claim is written to %q, want %q", got, want)
	}
	// Move the arbiter and the claim address follows in the same step. Two fields
	// would let this pair come apart, which is how a lane verifies one repository
	// and allocates numbers in another.
	moved := Arbiter{Remote: "https://git.hanzo.ai/somewhere/else", Branch: "main"}
	if got, want := moved.API(), "https://git.hanzo.ai/v1/repos/somewhere/else"; got != want {
		t.Errorf("the claim address must follow the arbiter, got %q, want %q", got, want)
	}
	// Nothing to write to, so nothing is offered. A caller that addressed the empty
	// string would post to a relative path, and the tests below run against local
	// paths precisely because there is no forge behind them.
	for _, a := range []Arbiter{{}, {Remote: t.TempDir()}, {Remote: "https://git.hanzo.ai"}} {
		if got := a.API(); got != "" {
			t.Errorf("%+v has no forge to claim in, got %q", a, got)
		}
	}
}

// TestEnvKeepsTheCredentialOutOfArgvAndOffRedirects states the two properties the
// fetch environment exists for: the token is presented as a header rather than on a
// command line, and a redirect is refused so the name in Remote is the repository
// that answers.
// The two are asserted separately because they hold under different conditions: the
// header is presented only when there is a credential and an https remote, while the
// redirect refusal holds always. Asserting both only where a token is set would let
// the second quietly become conditional on the first.
func TestEnvKeepsTheCredentialOutOfArgvAndOffRedirects(t *testing.T) {
	remote := "https://git.hanzo.ai/hanzo-inc/cloud"

	// Whether a credential is set decides nothing about redirects. An arbiter that
	// grants anonymous reads is read anonymously, and a former name still has to fail
	// rather than resolve to whatever holds it now.
	for _, token := range []string{"s3cret", ""} {
		a := Arbiter{Remote: remote, Branch: "main"}
		env := strings.Join(a.env(t.TempDir(), token), "\n")
		if !strings.Contains(env, "http.followRedirects") {
			t.Errorf("redirects must be refused with token=%q: a former name answers with a different repository", token)
		}
	}

	a := Arbiter{Remote: remote, Branch: "main"}
	env := strings.Join(a.env(t.TempDir(), "s3cret"), "\n")
	// base64("x-access-token:s3cret")
	if !strings.Contains(env, "eC1hY2Nlc3MtdG9rZW46czNjcmV0") {
		t.Error("the credential must travel as a header")
	}
	if strings.Contains(env, "s3cret") {
		t.Error("the credential must not appear in plain text in the child environment")
	}

	// A local arbiter has no host to authenticate to, so nothing is presented.
	local := Arbiter{Remote: t.TempDir(), Branch: "main"}
	if strings.Contains(strings.Join(local.env(t.TempDir(), "s3cret"), "\n"), "extraHeader") {
		t.Error("a credential must not be presented to a remote that is not https")
	}
}
