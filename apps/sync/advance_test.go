package sync

import (
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// advance_test.go is the proof that a canonical ref is never overwritten.
//
// It exercises [work.advance] DIRECTLY, against two REAL git hosts served by
// git-http-backend (stand_test.go) — no app mounted, no store, no forge REST.
// That is deliberate: the safety property belongs to the advance, so it is
// proved where it lives, and it stays proved even if everything around it is
// rearranged.
//
// Nothing about a rejection is simulated. The refusals here are `git push` and
// `git receive-pack` refusing, which is the only thing that will refuse in
// production. And [TestAdvanceRefusesADivergence] does not stop at "Conflict":
// it then force-pushes the same commit by hand and watches the destination tip
// change, so the divergence it set up is demonstrably one that a forcing refspec
// would have destroyed.

const mainRef = "refs/heads/main"

// pair sets up a source and a destination host, each holding one repository
// seeded from the same commit, and a scratch repository to move objects through.
func pair(t *testing.T) (src, dst *host, srcWork, dstWork *tree, w *work) {
	t.Helper()
	src, dst = newHost(t), newHost(t)
	srcWork = src.seed("up", "widgets", "one")
	dstWork = dst.seed("forge", "widgets", "one")
	// Give the destination the source's history, so the two share a base commit —
	// which is what makes a later divergence a divergence rather than two
	// unrelated repositories.
	git(t, dstWork.dir, "fetch", "--quiet", srcWork.bare, "main")
	git(t, dstWork.dir, "reset", "--quiet", "--hard", "FETCH_HEAD")
	git(t, dstWork.dir, "push", "--quiet", "--force", "origin", "HEAD:"+mainRef)

	var err error
	w, err = openTransit(context.Background(), t.TempDir(), "org", "widgets")
	if err != nil {
		t.Fatalf("transit repo: %v", err)
	}
	t.Cleanup(w.close)
	return src, dst, srcWork, dstWork, w
}

// ends turns two hosts into the two ends of an advance.
func ends(src, dst *host) (from, to remote) {
	return remote{URL: src.URL + "/up/widgets.git"}, remote{URL: dst.URL + "/forge/widgets.git"}
}

// run reads both sides' tips and advances ref, failing the test on a transport
// error — which is never the answer any case here expects.
func run(t *testing.T, w *work, from, to remote, ref string) outcome {
	t.Helper()
	ctx := context.Background()
	srcTips, _, err := refs(ctx, from, ref)
	if err != nil {
		t.Fatalf("read source refs: %v", err)
	}
	dstTips, _, err := refs(ctx, to, ref)
	if err != nil {
		t.Fatalf("read destination refs: %v", err)
	}
	oc, err := w.advance(ctx, from, to, ref, dstTips[ref], srcTips[ref])
	if err != nil {
		t.Fatalf("advance %s: %v", ref, err)
	}
	return oc
}

// TestAdvanceCreatesAndFastForwards: a ref the destination does not have is
// created; a ref whose source has moved forward is fast-forwarded.
func TestAdvanceCreatesAndFastForwards(t *testing.T) {
	src, dst, srcWork, _, w := pair(t)
	from, to := ends(src, dst)
	dstBare := dst.bare("forge", "widgets")

	// A branch the destination has never seen.
	made := srcWork.branch("release/1")
	if oc := run(t, w, from, to, "refs/heads/release/1"); !oc.Applied || oc.Before != "" || oc.After != made {
		t.Fatalf("create: %+v, want applied ''→%s", oc, made)
	}
	if got := tip(t, dstBare, "refs/heads/release/1"); got != made {
		t.Fatalf("destination release/1 = %q, want %q", got, made)
	}

	// And a branch it has, moved forward.
	before := tip(t, dstBare, mainRef)
	after := srcWork.commit("two")
	if oc := run(t, w, from, to, mainRef); !oc.Applied || oc.Before != before || oc.After != after {
		t.Fatalf("fast-forward: %+v, want applied %s→%s", oc, before, after)
	}
	if got := tip(t, dstBare, mainRef); got != after {
		t.Fatalf("destination main = %q, want %q", got, after)
	}
}

// TestAdvanceRefusesADivergence is the whole point of this package.
func TestAdvanceRefusesADivergence(t *testing.T) {
	src, dst, srcWork, dstWork, w := pair(t)
	from, to := ends(src, dst)
	dstBare := dst.bare("forge", "widgets")

	// Both sides move on from the shared base: the destination has work the
	// source has never seen, and vice versa.
	kept := dstWork.commit("the destination's own work")
	lost := srcWork.commit("the source's work")
	if kept == lost {
		t.Fatal("the two sides did not diverge")
	}

	oc := run(t, w, from, to, mainRef)
	if !oc.Conflict {
		t.Fatalf("a divergence must be a Conflict, got %+v", oc)
	}
	if oc.Before != kept || oc.After != kept {
		t.Errorf("a conflict must report the destination unmoved, got %+v", oc)
	}
	if got := tip(t, dstBare, mainRef); got != kept {
		t.Fatalf("THE DESTINATION WAS OVERWRITTEN: main = %q, want %q", got, kept)
	}
	if reachable(dstBare, lost) {
		t.Errorf("the refused commit %s reached the destination anyway", lost)
	}
	if !strings.Contains(oc.Detail, mainRef) {
		t.Errorf("the conflict does not name the ref: %q", oc.Detail)
	}

	// AND NOW THE PROOF THAT IT MATTERED. The same commit, pushed by hand with a
	// forcing refspec, does destroy what the advance preserved.
	git(t, srcWork.dir, "push", "--quiet", "--force", to.URL, "HEAD:"+mainRef)
	if got := tip(t, dstBare, mainRef); got != lost {
		t.Fatalf("the forced push did not land (%q) — the case above was not one a force could destroy", got)
	}
	if reachable(dstBare, kept) {
		t.Log("note: the destination's commit survives as an unreferenced object; the REF is what was lost")
	}
}

// TestAdvanceEqualTipCostsNothing: the echo of a ref we just sent the other way
// is answered from the two advertisements, with no pack transferred at all.
func TestAdvanceEqualTipCostsNothing(t *testing.T) {
	src, dst, _, _, w := pair(t)
	from, to := ends(src, dst)

	packs := src.counts() + dst.counts()
	oc := run(t, w, from, to, mainRef)
	if !oc.NoOp {
		t.Fatalf("equal tips must be a NoOp, got %+v", oc)
	}
	if got := src.counts() + dst.counts(); got != packs {
		t.Errorf("a NoOp moved %d packs; the tips were already equal", got-packs)
	}
}

// TestAdvanceReportsAMissingSourceRef: a ref the source does not have is a
// no-op, not an error and certainly not a deletion. Nothing in this package can
// remove a ref from the canonical store, and this is the case that would most
// plausibly want to.
func TestAdvanceReportsAMissingSourceRef(t *testing.T) {
	src, dst, _, _, w := pair(t)
	from, to := ends(src, dst)
	dstBare := dst.bare("forge", "widgets")
	held := tip(t, dstBare, mainRef)

	oc := run(t, w, from, to, "refs/heads/never-existed")
	if !oc.NoOp {
		t.Fatalf("a ref the source lacks must be a NoOp, got %+v", oc)
	}
	if got := tip(t, dstBare, mainRef); got != held {
		t.Errorf("main changed to %q while syncing an absent ref", got)
	}
}

// TestAdvanceRefusesToMoveAnExistingTag: a published tag is never re-pointed, so
// a moved upstream tag cannot silently rename a release.
func TestAdvanceRefusesToMoveAnExistingTag(t *testing.T) {
	src, dst, srcWork, _, w := pair(t)
	from, to := ends(src, dst)
	dstBare := dst.bare("forge", "widgets")

	first := srcWork.tag("v1")
	if oc := run(t, w, from, to, "refs/tags/v1"); !oc.Applied {
		t.Fatalf("a new tag must land, got %+v", oc)
	}
	if got := tip(t, dstBare, "refs/tags/v1"); got != first {
		t.Fatalf("v1 = %q, want %q", got, first)
	}

	srcWork.commit("later")
	if moved := srcWork.tag("v1"); moved == first {
		t.Fatal("the tag did not move at the source")
	}
	oc := run(t, w, from, to, "refs/tags/v1")
	if !oc.Conflict {
		t.Fatalf("re-pointing a published tag must be refused, got %+v", oc)
	}
	if got := tip(t, dstBare, "refs/tags/v1"); got != first {
		t.Errorf("v1 = %q, want the published %q", got, first)
	}
}

// TestRefsDropsThePeeledTag: an annotated tag is advertised twice, once as the
// tag object and once peeled to the commit it wraps. Keeping the peeled entry
// would make the sync push a COMMIT to a ref the source holds a TAG at.
func TestRefsDropsThePeeledTag(t *testing.T) {
	src, dst, srcWork, _, _ := pair(t)
	from, _ := ends(src, dst)

	git(t, srcWork.dir, "tag", "-a", "v9", "-m", "annotated")
	git(t, srcWork.dir, "push", "--quiet", "origin", "refs/tags/v9")
	object := git(t, srcWork.dir, "rev-parse", "refs/tags/v9")
	commit := git(t, srcWork.dir, "rev-parse", "refs/tags/v9^{commit}")
	if object == commit {
		t.Fatal("the tag is not annotated, so this proves nothing")
	}
	tips, _, err := refs(context.Background(), from)
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	if tips["refs/tags/v9"] != object {
		t.Errorf("refs/tags/v9 = %q, want the tag object %q", tips["refs/tags/v9"], object)
	}
	for name := range tips {
		if strings.HasSuffix(name, "^{}") {
			t.Errorf("a peeled entry survived: %q", name)
		}
	}
}

// TestTheCredentialTravelsByHeader: a credential reaches the destination as a
// Basic header, and never as part of the URL — the two halves of "env-only,
// never argv, never a reflog".
func TestTheCredentialTravelsByHeader(t *testing.T) {
	src, dst, srcWork, _, w := pair(t)
	from, to := ends(src, dst)
	to.Cred = gitCred{User: "x-access-token", Token: "s3cret"}

	srcWork.commit("two")
	if oc := run(t, w, from, to, mainRef); !oc.Applied {
		t.Fatalf("want applied, got %+v", oc)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:s3cret"))
	if got := dst.auth("git-receive-pack"); got != want {
		t.Errorf("the push carried %q, want %q", got, want)
	}
	if strings.Contains(to.URL, "s3cret") || strings.Contains(from.URL, "@") {
		t.Error("a credential reached a URL")
	}
	// The other end holds nothing for us and is asked for nothing.
	if got := src.auth("git-upload-pack"); got != "" {
		t.Errorf("a credential was offered to the source: %q", got)
	}
}

// TestACredentialNeverRidesCleartext: a token is attached over https, and over
// http only to loopback — the same exemption the forge client makes, for the
// same reason, and bounded to an address nobody else can reach.
func TestACredentialNeverRidesCleartext(t *testing.T) {
	cred := gitCred{User: "x-access-token", Token: "s3cret"}
	for url, want := range map[string]bool{
		"https://github.com/a/b.git":     true,
		"http://127.0.0.1:9/a/b.git":     true,
		"http://localhost:9/a/b.git":     true,
		"http://github.com/a/b.git":      false,
		"http://10.0.0.5/a/b.git":        false,
		"git://github.com/a/b.git":       false,
		"http://[::1]:9/a/b.git":         true,
		"http://evil.example/a/b.git":    false,
		"https://user:pw@github.com/a/b": true,
	} {
		if got := credAuthHeader(url, cred) != ""; got != want {
			t.Errorf("%s: credential attached = %v, want %v", url, got, want)
		}
	}
}

// TestTheEnvTokenIsPresentedTheWayItsHostAccepts: with no explicit credential the
// token held FOR a host still has to be spelled the way that host reads it —
// GitLab refuses a personal or OAuth token offered under any username but oauth2.
// The explicit-credential path already asks mirrorBasicUser; so does this one.
func TestTheEnvTokenIsPresentedTheWayItsHostAccepts(t *testing.T) {
	t.Setenv("GIT_MIRROR_TOKEN_GITLAB_COM", "gl")
	t.Setenv("GIT_MIRROR_TOKEN_GITHUB_COM", "gh")
	for url, want := range map[string]string{
		"https://gitlab.com/g/x.git": "oauth2:gl",
		"https://github.com/a/b.git": "x-access-token:gh",
	} {
		got := credAuthHeader(url, gitCred{})
		if want := base64.StdEncoding.EncodeToString([]byte(want)); got != want {
			t.Errorf("%s: credential = %q, want %q", url, got, want)
		}
	}
}

// TestTheGitEnvKeepsEveryConfigKey: one environment, numbered ONCE.
//
// git reads its env config by COUNT — GIT_CONFIG_COUNT plus KEY_n/VALUE_n — and
// os/exec keeps the last of a duplicated variable, so two builders each writing
// their own count leaves only the last one's keys. That is not a visible
// failure: the subprocess runs, the sync works, and the bounds that were dropped
// are the ones that only matter on a repository big enough to exhaust the pod —
// core.packedGitLimit and core.packedGitWindowSize, whose absence arrives as a
// kernel OOM of the whole process rather than as a failed fetch.
func TestTheGitEnvKeepsEveryConfigKey(t *testing.T) {
	r := remote{URL: "https://github.com/a/b.git", Cred: gitCred{User: "x-access-token", Token: "s3cret"}}
	cmd, err := gitCmd(context.Background(), &r, []string{"http.lowSpeedLimit=1000"}, "push")
	if err != nil {
		t.Fatalf("gitCmd: %v", err)
	}

	counts, cfg := 0, map[string]string{}
	keys := map[string]string{}
	for _, kv := range cmd.Env {
		name, value, _ := strings.Cut(kv, "=")
		switch {
		case name == "GIT_CONFIG_COUNT":
			counts++
		case strings.HasPrefix(name, "GIT_CONFIG_KEY_"):
			keys[strings.TrimPrefix(name, "GIT_CONFIG_KEY_")] = value
		case strings.HasPrefix(name, "GIT_CONFIG_VALUE_"):
			cfg[strings.TrimPrefix(name, "GIT_CONFIG_VALUE_")] = value
		}
	}
	if counts != 1 {
		t.Fatalf("the environment carries %d GIT_CONFIG_COUNTs; git honours one and drops the rest", counts)
	}
	got := map[string]string{}
	for n, key := range keys {
		got[key] = cfg[n]
	}
	for _, want := range append(baseConfig(), "http.lowSpeedLimit=1000", "http.followRedirects=false") {
		k, v, _ := strings.Cut(want, "=")
		if have, ok := got[k]; !ok || have != v {
			t.Errorf("%s = %q (present %v), want %q", k, have, ok, v)
		}
	}
	if hdr := got["http.extraHeader"]; !strings.HasPrefix(hdr, "Authorization: Basic ") {
		t.Errorf("the credential did not survive the merge: %q", hdr)
	}
	// A local call reaches no remote, so it carries no protocol policy and no
	// credential — and still every memory bound.
	local, err := gitCmd(context.Background(), nil, nil, "rev-parse")
	if err != nil {
		t.Fatalf("gitCmd: %v", err)
	}
	for _, kv := range local.Env {
		if strings.Contains(kv, "extraHeader") || strings.HasPrefix(kv, "GIT_ALLOW_PROTOCOL") {
			t.Errorf("a local git call carries %q", kv)
		}
	}
}

// TestAdvanceReadsTheDestinationForItself: what an advance reports moving FROM
// is what the destination held when the objects were taken, not what its
// advertisement said when the caller read it.
//
// The caller reads one advertisement for a whole repository and then advances
// its refs one at a time, so by the time any given ref is pushed the value it
// holds may be minutes old. Both halves of the report are downstream of it:
// push-to-deploy diffs Before..After, and "applied" is what says a deployment
// has something to build.
func TestAdvanceReadsTheDestinationForItself(t *testing.T) {
	ctx := context.Background()
	src, dst, srcWork, dstWork, w := pair(t)
	from, to := ends(src, dst)
	stale := tip(t, dst.bare("forge", "widgets"), mainRef)

	// The destination moves on its own, and the source lands on top of THAT.
	theirs := dstWork.commit("the destination's own commit")
	git(t, srcWork.dir, "fetch", "--quiet", to.URL, "main")
	git(t, srcWork.dir, "reset", "--quiet", "--hard", "FETCH_HEAD")
	want := srcWork.commit("on top of it")

	oc, err := w.advance(ctx, from, to, mainRef, stale, want)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !oc.Applied || oc.Before != theirs || oc.After != want {
		t.Fatalf("want applied %s→%s — the tip the destination actually held — got %+v", theirs, want, oc)
	}

	// And now the two ends agree, however stale the caller's value is. Pushing
	// here earns git's "Everything up-to-date", which exits zero and moves
	// nothing; calling that an advance hands a deployment an empty diff.
	oc, err = w.advance(ctx, from, to, mainRef, stale, want)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if !oc.NoOp || oc.Applied || oc.Before != want || oc.After != want {
		t.Fatalf("want a no-op at %s, got %+v", want, oc)
	}
}

// TestNothingHereForces reads the source of the advance itself.
//
// The refusal this client depends on is the ABSENCE of a forcing refspec, and an
// absence is what a behavioural test cannot pin: a '+' added tomorrow would
// leave every case above passing and every conflict silently gone. So the
// property is asserted where it lives.
func TestNothingHereForces(t *testing.T) {
	body, err := os.ReadFile("advance.go")
	if err != nil {
		t.Fatal(err)
	}
	// Comments explain the rule and must be free to quote it; the code must not.
	code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(string(body), "")
	for _, forbidden := range []string{
		`"+"`, `'+'`, `"+refs/`, `+%s`, "--force", "--mirror", "--prune", "force-with-lease",
	} {
		if strings.Contains(code, forbidden) {
			t.Errorf("advance.go contains %q — the canonical store is only safe while it does not", forbidden)
		}
	}
	if !strings.Contains(code, `"push", r.URL, src+":"+ref`) {
		t.Error("the push refspec is not the bare src:ref this file's whole argument rests on")
	}
}

// reachable reports whether a bare repository holds a commit object at all — a
// stricter question than "does a ref point at it", and the one that says whether
// a refused push nevertheless left its objects behind.
func reachable(bare, oid string) bool {
	c := exec.Command("git", "--git-dir="+bare, "cat-file", "-e", oid+"^{commit}")
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	return c.Run() == nil
}

// TestTransitIsACacheAndOnlyACache: the transit repository keeps its objects
// between advances — that is what makes the next one a delta instead of a clone
// — and it keeps them under refs/transit, which no repository uses for anything.
//
// It also pins the ONE way two callers are kept off the same repository: the
// lock is taken before the work is handed out, so a second open blocks.
func TestTransitIsACacheAndOnlyACache(t *testing.T) {
	src, dst, srcWork, _, w := pair(t)
	from, to := ends(src, dst)

	srcWork.commit("two")
	if oc := run(t, w, from, to, mainRef); !oc.Applied {
		t.Fatalf("want applied, got %+v", oc)
	}
	if got := tip(t, w.dir, scratch("src", mainRef)); got == "" {
		t.Fatal("the transit repository kept nothing, so the next advance re-clones")
	}
	if got := tip(t, w.dir, mainRef); got != "" {
		t.Errorf("a scratch ref landed in the branch namespace: %s", mainRef)
	}

	// A second advance moves a DELTA: the source's whole history is already here,
	// so only the new commit crosses. Proved by cost — one more commit, and the
	// transfer stays a transfer rather than growing with the repository.
	held := w.dir
	before := src.counts() + dst.counts()
	srcWork.commit("three")
	if oc := run(t, w, from, to, mainRef); !oc.Applied {
		t.Fatalf("want applied, got %+v", oc)
	}
	if w.dir != held {
		t.Errorf("the transit repository moved to %q; it must be the same one", w.dir)
	}
	if got := src.counts() + dst.counts(); got-before > 3 {
		t.Errorf("a second advance cost %d transfers, want the fetch pair and the push", got-before)
	}

	// And the lock really is held: a second open of the same repository must wait.
	opened := make(chan struct{})
	go func() {
		second, err := openTransit(context.Background(), filepath.Dir(filepath.Dir(filepath.Dir(w.dir))), "org", "widgets")
		if err == nil {
			second.close()
		}
		close(opened)
	}()
	select {
	case <-opened:
		t.Error("a second caller opened the same transit repository while it was held")
	case <-time.After(150 * time.Millisecond):
	}
	w.close()
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Error("closing the transit repository did not release it")
	}
}
