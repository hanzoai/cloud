package sync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// importer_test.go proves the client around the advance: which repository, which
// refs, which credential, and what is remembered afterwards.
//
// The advance itself is proved in advance_test.go against bare hosts. Here the
// whole app is mounted against a stand-in forge that serves BOTH its REST
// endpoints and its repositories, so an import really creates a repository and
// really pushes into it.

const (
	// forgeOrg is the only IAM org with a namespace on the forge — forge.Owner is
	// a closed table — so it is the org every case here acts for.
	forgeOrg   = "hanzo"
	forgeOwner = "hanzoai"
	upOwner    = "acme-gh"
	repoName   = "widgets"
)

// widgets is the repository every case here syncs: upOwner's `widgets`. It is a
// PAIR, because a repository name means one repository only within an account,
// and `widgets.flat()` is the one name it takes on the forge.
var widgets = repo{account: upOwner, name: repoName}

// imported mounts the app against a fresh upstream and forge, seeds the upstream
// with one commit, and imports it — the state every inbound case starts from.
func imported(t *testing.T) (up *host, fg *stand, src *tree, cloneURL string) {
	t.Helper()
	up = newHost(t)
	fg = newStand(t, forgeOwner)
	mountForge(t)
	src = up.seed(upOwner, repoName, "one")
	cloneURL = up.URL + "/" + upOwner + "/" + repoName + ".git"
	if err := (importer{}).ImportRepo(context.Background(), cloud.GitImportReq{
		Org: forgeOrg, Project: upOwner, Repo: repoName, CloneURL: cloneURL,
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	return up, fg, src, cloneURL
}

// inbound runs one inbound advance of main.
func inbound(t *testing.T, cloneURL string) cloud.GitSyncResult {
	t.Helper()
	res, err := (importer{}).InboundSync(context.Background(), cloud.GitInboundReq{
		Org: forgeOrg, Project: upOwner, Repo: repoName, Ref: mainRef,
		CloneURL: cloneURL, Origin: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	return res
}

// diverge gives the forge repository a commit the upstream does not have —
// somebody pushing to the canonical store directly, which is the whole reason
// the canonical store must not be overwritten.
func diverge(t *testing.T, url, content string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, "", "clone", "--quiet", url, dir)
	if err := os.WriteFile(filepath.Join(dir, "forge-only"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "forge-only")
	git(t, dir, "push", "--quiet", "origin", "HEAD:"+mainRef)
	return git(t, dir, "rev-parse", "HEAD")
}

// TestImportLandsTheRepository: a first import creates the forge repository
// EMPTY — the stand refuses auto_init, because a repository born with a commit
// is one born in conflict with its upstream — advances every branch and tag onto
// it, and points HEAD where the upstream points its own. A second import moves
// nothing.
func TestImportLandsTheRepository(t *testing.T) {
	up := newHost(t)
	fg := newStand(t, forgeOwner)
	mountForge(t)

	src := up.seed(upOwner, repoName, "one")
	src.branch("release/1")
	src.tag("v1")
	head := git(t, src.dir, "rev-parse", "HEAD")
	cloneURL := up.URL + "/" + upOwner + "/" + repoName + ".git"

	if err := (importer{}).ImportRepo(context.Background(), cloud.GitImportReq{
		Org: forgeOrg, Project: upOwner, Repo: repoName, CloneURL: cloneURL,
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	bare := fg.bare(forgeOwner, widgets.flat())
	for _, ref := range []string{mainRef, "refs/heads/release/1", "refs/tags/v1"} {
		if got := tip(t, bare, ref); got != head {
			t.Errorf("%s = %q, want %q", ref, got, head)
		}
	}
	if got := git(t, "", "--git-dir="+bare, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("forge HEAD = %q, want main", got)
	}

	// Idempotent, and cheap: nothing has moved, so nothing is transferred.
	packs := up.counts() + fg.counts()
	if err := (importer{}).ImportRepo(context.Background(), cloud.GitImportReq{
		Org: forgeOrg, Project: upOwner, Repo: repoName, CloneURL: cloneURL,
	}); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if got := up.counts() + fg.counts(); got != packs {
		t.Errorf("re-import moved %d packs; an unchanged upstream must move none", got-packs)
	}
	if got := tip(t, bare, mainRef); got != head {
		t.Errorf("re-import changed main to %q", got)
	}
}

// TestInboundAdvanceApplies: the upstream moves forward, the forge follows, and
// the advance is announced with the source host so the echo can be suppressed.
func TestInboundAdvanceApplies(t *testing.T) {
	_, fg, src, cloneURL := imported(t)
	before := tip(t, fg.bare(forgeOwner, widgets.flat()), mainRef)

	after := src.commit("two")
	landed := make(chan cloud.LifecycleEvent, 4)
	cloud.RegisterLifecycleSubscriber(func(_ context.Context, ev cloud.LifecycleEvent) {
		if ev.Kind == cloud.LifecyclePushLanded && ev.Repo == repoName && ev.Before != "" {
			select {
			case landed <- ev:
			default:
			}
		}
	})

	res := inbound(t, cloneURL)
	if !res.Applied || res.After != after || res.Before != before {
		t.Fatalf("want applied %s→%s, got %+v", before, after, res)
	}
	if now := tip(t, fg.bare(forgeOwner, widgets.flat()), mainRef); now != after {
		t.Fatalf("forge main = %q, want %q", now, after)
	}
	select {
	case ev := <-landed:
		if ev.After != after || ev.Origin != "127.0.0.1" || ev.Branch != mainRef {
			t.Errorf("push.landed = %+v", ev)
		}
	default:
		t.Error("an applied advance announced nothing, so push-to-deploy never fires")
	}
}

// TestInboundConflictIsRecorded: the forge is preserved, and the divergence is
// written down — because a refused push leaves no trace on the receiving end,
// which is exactly what makes it safe and exactly why somebody has to remember.
func TestInboundConflictIsRecorded(t *testing.T) {
	_, fg, src, cloneURL := imported(t)
	bare := fg.bare(forgeOwner, widgets.flat())

	forgeTip := diverge(t, fg.URL+"/"+forgeOwner+"/"+widgets.flat()+".git", "somebody's work")
	src.commit("upstream's work")

	res := inbound(t, cloneURL)
	if !res.Conflict {
		t.Fatalf("a divergence must be a Conflict, got %+v", res)
	}
	if now := tip(t, bare, mainRef); now != forgeTip {
		t.Fatalf("THE FORGE WAS OVERWRITTEN: main = %q, want %q", now, forgeTip)
	}
	st, err := storeFor(mounted.Load(), forgeOrg)
	if err != nil {
		t.Fatal(err)
	}
	states, err := st.States(context.Background(), forgeOrg)
	if err != nil {
		t.Fatal(err)
	}
	if !states[widgets].Conflict || states[widgets].At == 0 {
		t.Fatalf("conflict not recorded: %+v", states[widgets])
	}

	// Resolving is the same write: the upstream takes the forge's commit on, and
	// the next advance is a fast-forward that clears the record.
	dir := t.TempDir()
	git(t, "", "clone", "--quiet", fg.URL+"/"+forgeOwner+"/"+widgets.flat()+".git", dir)
	git(t, dir, "push", "--quiet", "--force", cloneURL, "HEAD:"+mainRef)
	if res := inbound(t, cloneURL); !res.NoOp {
		t.Fatalf("want a no-op once the two agree, got %+v", res)
	}
	states, _ = st.States(context.Background(), forgeOrg)
	if states[widgets].Conflict {
		t.Error("the conflict survived a clean advance")
	}
}

// TestInboundNeverProvisions: a push naming a repository nobody imported is a
// no-op. A webhook does not get to create repositories.
func TestInboundNeverProvisions(t *testing.T) {
	up := newHost(t)
	fg := newStand(t, forgeOwner)
	mountForge(t)
	up.seed(upOwner, "ghost", "one")

	res, err := (importer{}).InboundSync(context.Background(), cloud.GitInboundReq{
		Org: forgeOrg, Project: upOwner, Repo: "ghost", Ref: mainRef,
		CloneURL: up.URL + "/" + upOwner + "/ghost.git",
	})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if !res.NoOp || res.Detail != "repo not imported" {
		t.Fatalf("want a no-op for an un-imported repo, got %+v", res)
	}
	if _, err := os.Stat(fg.bare(forgeOwner, upOwner+"_ghost")); err == nil {
		t.Error("the webhook created a repository on the forge")
	}
}

// TestTheMachineNamespaceIsNeverSynced: a run's branch namespace is not
// something an upstream may write into, by webhook or by import.
func TestTheMachineNamespaceIsNeverSynced(t *testing.T) {
	_, fg, src, cloneURL := imported(t)
	src.branch("agent/session-1")
	agent := "refs/heads/agent/session-1"

	res, err := (importer{}).InboundSync(context.Background(), cloud.GitInboundReq{
		Org: forgeOrg, Project: upOwner, Repo: repoName, Ref: agent, CloneURL: cloneURL,
	})
	if err != nil {
		t.Fatalf("inbound: %v", err)
	}
	if !res.NoOp {
		t.Fatalf("the machine namespace must not be synced, got %+v", res)
	}
	if err := (importer{}).ImportRepo(context.Background(), cloud.GitImportReq{
		Org: forgeOrg, Project: upOwner, Repo: repoName, CloneURL: cloneURL,
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if tip(t, fg.bare(forgeOwner, widgets.flat()), agent) != "" {
		t.Error("an import carried a machine-namespace branch")
	}
}

// TestImportConflictSparesTheOtherBranches: one diverged branch is refused and
// recorded; every other branch still lands.
func TestImportConflictSparesTheOtherBranches(t *testing.T) {
	_, fg, src, cloneURL := imported(t)
	bare := fg.bare(forgeOwner, widgets.flat())

	forgeTip := diverge(t, fg.URL+"/"+forgeOwner+"/"+widgets.flat()+".git", "somebody's work")
	src.commit("upstream's work")
	side := src.branch("release/2")

	if err := (importer{}).ImportRepo(context.Background(), cloud.GitImportReq{
		Org: forgeOrg, Project: upOwner, Repo: repoName, CloneURL: cloneURL,
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if now := tip(t, bare, mainRef); now != forgeTip {
		t.Fatalf("the diverged branch was overwritten: %q, want %q", now, forgeTip)
	}
	if now := tip(t, bare, "refs/heads/release/2"); now != side {
		t.Errorf("release/2 = %q, want %q — a conflict on one branch must not stop the rest", now, side)
	}
}

// TestStatusReadsBothSides: "imported" is a read of the forge, "conflict" and
// "last synced" are reads of what we recorded — and a name the forge does not
// hold comes back zero rather than missing.
func TestStatusReadsBothSides(t *testing.T) {
	_, fg, src, cloneURL := imported(t)

	st, err := (importer{}).RepoStatus(context.Background(), forgeOrg, upOwner, []string{repoName, "absent"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st[repoName].Imported || st[repoName].LastSyncedAt == 0 || st[repoName].Conflict {
		t.Errorf("%s status = %+v, want imported and clean", repoName, st[repoName])
	}
	if st["absent"] != (cloud.GitRepoStatus{}) {
		t.Errorf("a repo the forge does not hold = %+v, want zero", st["absent"])
	}

	diverge(t, fg.URL+"/"+forgeOwner+"/"+widgets.flat()+".git", "somebody's work")
	src.commit("upstream's work")
	if res := inbound(t, cloneURL); !res.Conflict {
		t.Fatalf("want conflict, got %+v", res)
	}
	got, err := (importer{}).RepoStatus(context.Background(), forgeOrg, upOwner, []string{repoName})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !got[repoName].Conflict {
		t.Error("a conflict is not surfaced in the status the console renders")
	}
}

// TestTheForgeCredentialTravelsByHeader: the machine credential from KMS reaches
// the forge's push endpoint as a Basic header, and the public upstream is
// fetched anonymously rather than offered somebody else's token.
func TestTheForgeCredentialTravelsByHeader(t *testing.T) {
	up, fg, src, cloneURL := imported(t)
	src.commit("two")
	if res := inbound(t, cloneURL); !res.Applied {
		t.Fatalf("want applied, got %+v", res)
	}
	if got := fg.auth("git-receive-pack"); got != basic() {
		t.Errorf("the forge push carried %q, want the machine credential", got)
	}
	if got := up.auth("git-upload-pack"); got != "" {
		t.Errorf("a credential was offered to the upstream: %q", got)
	}
}

// TestPushOutIsAlsoFastForwardOnly: a declared downstream is advanced, not
// force-fed — because a bidirectional sync's "downstream" is its upstream, which
// is a place people push.
func TestPushOutIsAlsoFastForwardOnly(t *testing.T) {
	up, fg, src, cloneURL := imported(t)
	upBare := up.bare(upOwner, repoName)
	forgeURL := fg.URL + "/" + forgeOwner + "/" + widgets.flat() + ".git"

	// The target is declared straight into the store: validateMirrorTarget
	// requires https and an allowlisted host, which the loopback stand is not, and
	// what this case pins is the PUSH rather than the declaration gate
	// (TestMirrorTargetsMustBeAllowed pins that).
	st, err := storeFor(mounted.Load(), forgeOrg)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMirror(context.Background(), forgeOrg, widgets, "127.0.0.1", cloneURL); err != nil {
		t.Fatal(err)
	}

	// The forge moves ahead: the downstream follows.
	forgeTip := diverge(t, forgeURL, "canonical work")
	if err := pushOut(context.Background(), forgeOrg, upOwner, repoName); err != nil {
		t.Fatalf("push out: %v", err)
	}
	if got := tip(t, upBare, mainRef); got != forgeTip {
		t.Fatalf("downstream main = %q, want %q", got, forgeTip)
	}

	// Now both move on from THAT commit, independently. The downstream must
	// survive: it is a place people push, not a replica to be overwritten — and
	// the caller must be TOLD, because a push that landed nothing is not a sync.
	git(t, src.dir, "fetch", "--quiet", "origin", "main")
	git(t, src.dir, "reset", "--quiet", "--hard", "FETCH_HEAD")
	downTip := src.commit("the downstream's own work")
	diverge(t, forgeURL, "more canonical work")
	err = pushOut(context.Background(), forgeOrg, upOwner, repoName)
	if err == nil {
		t.Fatal("a push nothing accepted reported success; the engine stamps 'last synced' on that")
	}
	if !strings.Contains(err.Error(), "127.0.0.1") || !strings.Contains(err.Error(), mainRef) {
		t.Errorf("the failure does not name the target and the ref: %v", err)
	}
	if got := tip(t, upBare, mainRef); got != downTip {
		t.Fatalf("THE DOWNSTREAM WAS OVERWRITTEN: main = %q, want %q", got, downTip)
	}
	// And it is written down against THAT host, so the console says this
	// repository is not in step rather than only a log line saying so.
	states, err := st.States(context.Background(), forgeOrg)
	if err != nil {
		t.Fatal(err)
	}
	if !states[widgets].Conflict {
		t.Error("a refused outbound push left no conflict for the console to render")
	}
}

// ── one name, two accounts ───────────────────────────────────────────────────

// otherOwner is a SECOND upstream account holding a repository of the same name.
// hanzoai/ai, hanzo-apps/ai and hanzo-docs/ai are the real instance of this.
const otherOwner = "other-gh"

// other is that second repository — same name, different account.
var other = repo{account: otherOwner, name: repoName}

// twoAccounts seeds ONE name under TWO upstream accounts, sharing a base commit,
// and imports both.
//
// The shared base is deliberate: two repositories with unrelated histories would
// refuse each other's pushes anyway, so a mix-up between them would bounce off
// git and prove nothing. Sharing a commit is the case where one's refs LAND in
// the other, which is what the account in the coordinate has to prevent.
func twoAccounts(t *testing.T) (up *host, fg *stand, base, urlA, urlB string) {
	t.Helper()
	up = newHost(t)
	fg = newStand(t, forgeOwner)
	mountForge(t)

	a := up.seed(upOwner, repoName, "shared")
	base = git(t, a.dir, "rev-parse", "HEAD")
	up.init(otherOwner, repoName)
	urlA = up.URL + "/" + upOwner + "/" + repoName + ".git"
	urlB = up.URL + "/" + otherOwner + "/" + repoName + ".git"
	git(t, a.dir, "push", "--quiet", urlB, "HEAD:"+mainRef)

	for _, im := range []struct{ account, url string }{{upOwner, urlA}, {otherOwner, urlB}} {
		if err := (importer{}).ImportRepo(context.Background(), cloud.GitImportReq{
			Org: forgeOrg, Project: im.account, Repo: repoName, CloneURL: im.url,
		}); err != nil {
			t.Fatalf("import %s: %v", im.account, err)
		}
	}
	return up, fg, base, urlA, urlB
}

// TestTwoAccountsAreTwoRepositories: a repository name means one repository only
// within one account, so the same name from two accounts must be two
// repositories on the forge — separate refs, separate HEAD, separate status.
//
// Sharing one, each import overwrites the last one's branches and re-points the
// HEAD they share, and the console draws one row for both.
func TestTwoAccountsAreTwoRepositories(t *testing.T) {
	up, fg, base, urlA, _ := twoAccounts(t)

	if _, err := os.Stat(fg.bare(forgeOwner, repoName)); err == nil {
		t.Fatalf("the forge holds a repository at the bare name %q, so the account is not in the coordinate", repoName)
	}
	for _, r := range []repo{widgets, other} {
		if got := tip(t, fg.bare(forgeOwner, r.flat()), mainRef); got != base {
			t.Fatalf("%s main = %q, want %q", r.flat(), got, base)
		}
	}

	// One of them moves. The other must not.
	srcA := &tree{t: t, dir: t.TempDir(), bare: up.bare(upOwner, repoName)}
	git(t, "", "clone", "--quiet", urlA, srcA.dir)
	moved := srcA.commit("only A's work")
	if res := inbound(t, urlA); !res.Applied {
		t.Fatalf("want applied, got %+v", res)
	}
	if got := tip(t, fg.bare(forgeOwner, widgets.flat()), mainRef); got != moved {
		t.Fatalf("A's forge repo = %q, want %q", got, moved)
	}
	if got := tip(t, fg.bare(forgeOwner, other.flat()), mainRef); got != base {
		t.Fatalf("B's forge repo followed A's push: %q, want %q", got, base)
	}

	// And the console reads them apart: both imported, and the one that synced is
	// the one with a sync time.
	st, err := (importer{}).RepoStatus(context.Background(), forgeOrg, upOwner, []string{repoName})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	stB, err := (importer{}).RepoStatus(context.Background(), forgeOrg, otherOwner, []string{repoName})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st[repoName].Imported || !stB[repoName].Imported {
		t.Errorf("both accounts' repositories are imported, got %+v and %+v", st[repoName], stB[repoName])
	}
	if st[repoName].LastSyncedAt == 0 {
		t.Error("A synced and its status says never")
	}
}

// TestAnAccountsRefsStayInItsOwnReplica: each repository's declared outbound
// target is ITS OWN, so one account's commits can never be pushed into another
// account's repository — which is what happens when the target is remembered
// against a bare name: two same-named repositories replicating to the same HOST
// share one row, the second declaration takes the first's place, and the first's
// refs are then pushed to the second's address under the org's own credential.
func TestAnAccountsRefsStayInItsOwnReplica(t *testing.T) {
	up, fg, base, urlA, urlB := twoAccounts(t)

	st, err := storeFor(mounted.Load(), forgeOrg)
	if err != nil {
		t.Fatal(err)
	}
	// Both declare a target on the SAME host — the collision, stated as data.
	for _, m := range []struct {
		r   repo
		url string
	}{{widgets, urlA}, {other, urlB}} {
		if err := st.SetMirror(context.Background(), forgeOrg, m.r, "127.0.0.1", m.url); err != nil {
			t.Fatal(err)
		}
	}

	// A's canonical copy moves ahead, and is pushed out.
	ahead := diverge(t, fg.URL+"/"+forgeOwner+"/"+widgets.flat()+".git", "A's own work")
	if err := pushOut(context.Background(), forgeOrg, upOwner, repoName); err != nil {
		t.Fatalf("push out: %v", err)
	}
	if got := tip(t, up.bare(upOwner, repoName), mainRef); got != ahead {
		t.Fatalf("A's own replica = %q, want %q", got, ahead)
	}
	if got := tip(t, up.bare(otherOwner, repoName), mainRef); got != base {
		t.Fatalf("ACCOUNT A'S WORK LANDED IN ACCOUNT B'S REPOSITORY: %q, want %q", got, base)
	}
	// And B still has its own target rather than A's.
	urls, err := st.Mirrors(context.Background(), forgeOrg, other)
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || urls[0] != urlB {
		t.Fatalf("B's declared targets = %v, want [%s]", urls, urlB)
	}
}

// ── a namespace that nests ───────────────────────────────────────────────────

// TestNestedNamespacesDoNotCollapse: a GitLab namespace nests, and the forge's
// does not. group/sub1/widgets and group/sub2/widgets are two repositories that
// the flat forge — one owner, one name — cannot spell apart, so both are REFUSED
// and nothing is written.
//
// It drives the provider rather than the importer, because the defect was in
// deriving the coordinate FROM THE URL, not in checking one handed over: reading
// only the namespace's top segment called both of them group's `widgets`, so
// they took one name on the forge and the second import walked into the first's
// refs and its HEAD — the account collision again, one level down.
//
// Refusing is the answer this client already gives an account carrying the
// separator [repo.flat] joins on. The case it must NOT refuse — a flat group,
// with a repository directly in it — goes down the same path here, and lands.
func TestNestedNamespacesDoNotCollapse(t *testing.T) {
	up := newHost(t)
	fg := newStand(t, forgeOwner)
	mountForge(t)

	// One name under two subgroups of one group, sharing a base commit — so a
	// mix-up between them LANDS rather than bouncing off git's own refusal, which
	// is the only version of this that could lose anything.
	a := up.seed("group/sub1", repoName, "sub1's work")
	up.init("group/sub2", repoName)
	nested := []string{
		up.URL + "/group/sub1/" + repoName + ".git",
		up.URL + "/group/sub2/" + repoName + ".git",
	}
	git(t, a.dir, "push", "--quiet", nested[1], "HEAD:"+mainRef)

	for _, src := range nested {
		changed, err := (gitProvider{}).Reconcile(context.Background(), gitlabSync(src, repoName), Event{
			Manual: true, Provider: provGitLab, Org: forgeOrg,
		})
		if err == nil {
			t.Fatalf("%s was imported; a nested namespace has no forge coordinate to import it to", src)
		}
		if changed {
			t.Error("a refused import reported a change, which stamps 'last synced' on it")
		}
		if !strings.Contains(err.Error(), "group/sub") {
			t.Errorf("the refusal does not name the namespace it refused: %v", err)
		}
	}

	// NOTHING was written: not under the name the truncated namespace produced,
	// not under either subgroup's own, not under the bare name.
	for _, name := range []string{"group_" + repoName, repoName, "sub1_" + repoName, "sub2_" + repoName} {
		if _, err := os.Stat(fg.bare(forgeOwner, name)); err == nil {
			t.Errorf("the forge holds %q; a refused import writes nothing", name)
		}
	}

	// And a FLAT group still syncs — the refusal is of what cannot be spelled, not
	// of GitLab.
	flat := up.seed("group", "gadgets", "the group's own work")
	if _, err := (gitProvider{}).Reconcile(context.Background(),
		gitlabSync(up.URL+"/group/gadgets.git", "gadgets"), Event{
			Manual: true, Provider: provGitLab, Org: forgeOrg,
		}); err != nil {
		t.Fatalf("a flat group is a coordinate the forge can hold: %v", err)
	}
	if got := tip(t, fg.bare(forgeOwner, "group_gadgets"), mainRef); got != git(t, flat.dir, "rev-parse", "HEAD") {
		t.Errorf("group/gadgets landed at %q on the forge", got)
	}
}

// gitlabSync is a pulling sync from one GitLab URL onto native, which is the
// target the API derives for it — the source's own short name.
func gitlabSync(src, native string) Sync {
	return Sync{
		Kind: "git", Direction: dirPull, Actor: "hanzo-sync", Org: forgeOrg,
		Source: Endpoint{Provider: provGitLab, Locator: src},
		Target: Endpoint{Provider: provNative, Locator: native},
	}
}

// TestAnImportThatTookNothingInIsNotASync: every ref refused means the canonical
// store did not move. The reconcile stamps "last synced, just now" on this
// returning nil, so a fully-refused import that reports success is a green
// console over a repository taking nothing in.
func TestAnImportThatTookNothingInIsNotASync(t *testing.T) {
	_, fg, src, cloneURL := imported(t)
	bare := fg.bare(forgeOwner, widgets.flat())

	forgeTip := diverge(t, fg.URL+"/"+forgeOwner+"/"+widgets.flat()+".git", "somebody's work")
	src.commit("upstream's work")

	err := (importer{}).ImportRepo(context.Background(), cloud.GitImportReq{
		Org: forgeOrg, Project: upOwner, Repo: repoName, CloneURL: cloneURL,
	})
	if err == nil {
		t.Fatal("an import that took nothing in reported success; the reconcile stamps 'last synced' on that")
	}
	if !strings.Contains(err.Error(), widgets.flat()) {
		t.Errorf("the failure does not name the repository: %v", err)
	}
	if now := tip(t, bare, mainRef); now != forgeTip {
		t.Fatalf("THE FORGE WAS OVERWRITTEN: main = %q, want %q", now, forgeTip)
	}
}

// TestAPushWithNoTargetIsNotASync: a push-only reconcile whose repository has no
// declared target moved no bytes, and says so. Reporting success there is what
// stamps "synced, just now" on a repository nothing is being pushed from.
func TestAPushWithNoTargetIsNotASync(t *testing.T) {
	imported(t)
	err := pushOut(context.Background(), forgeOrg, upOwner, repoName)
	if err == nil {
		t.Fatal("a push with nowhere to push reported success")
	}
	if !strings.Contains(err.Error(), "no declared outbound target") {
		t.Errorf("the failure does not say what is missing: %v", err)
	}
}

// TestSyncedRepositoriesAreBornAdvanceOnly: the forge is asked to refuse a force
// push and a deletion on every branch of a repository this client creates.
//
// The advance never sends a force and never deletes, but that is one client's
// discipline — a colleague with a clone, or a future caller, reaches the same
// repository with neither. The rule is the forge's, so it holds for all of them.
func TestSyncedRepositoriesAreBornAdvanceOnly(t *testing.T) {
	_, fg, _, _ := imported(t)

	rule := fg.rule(widgets.flat())
	if rule == nil {
		t.Fatal("no branch rule was asked for; the repository is only as safe as its clients")
	}
	if rule["rule_name"] != "**" {
		t.Errorf("rule covers %q, want every branch", rule["rule_name"])
	}
	if rule["enable_push"] != true {
		t.Error("the rule refuses the push the repository exists to receive")
	}
	if rule["enable_force_push"] != false {
		t.Error("the rule permits a force push, which is the one thing this client exists to prevent")
	}
}

// TestACredentialGoesOnlyToASourceWeMintFor: the token a caller hands in is
// offered only to a host this deployment mints credentials for, and a call that
// would send it elsewhere is REFUSED rather than quietly downgraded.
//
// The outbound half has always been allowlisted. The inbound half carries a live
// installation token to a URL that is an argument on the internal plane, which
// is the same question with a worse answer.
func TestACredentialGoesOnlyToASourceWeMintFor(t *testing.T) {
	// The SSRF gate is not what is under test here, so the invented hosts are
	// allowed past it deliberately.
	t.Setenv("GIT_MIRROR_ALLOW_PRIVATE_HOSTS", "evil.example,github.com")

	if _, err := upstream("https://evil.example/a/b.git", "installation-token"); err == nil {
		t.Error("a credential was offered to a host we mint nothing for")
	}
	from, err := upstream("https://github.com/a/b.git", "installation-token")
	if err != nil {
		t.Fatalf("github is a source we mint for: %v", err)
	}
	if from.Cred.Token != "installation-token" {
		t.Error("the credential did not reach the source it was minted for")
	}
	// Without a token there is nothing to leak, so any source is fetchable.
	if _, err := upstream("https://evil.example/a/b.git", ""); err != nil {
		t.Errorf("an anonymous fetch needs no allowlist: %v", err)
	}
	// The plane door says the same thing with a status, so a caller learns it
	// asked for something it may not have.
	if err := sourceOK("https://evil.example/a/b.git", "installation-token"); err == nil {
		t.Error("the plane door accepted a credential for a host the advance refuses")
	}
	if err := sourceOK("https://github.com/a/b.git", "installation-token"); err != nil {
		t.Errorf("the plane door refused a source we mint for: %v", err)
	}
}

// TestADeletedRefDoesNotResolveADivergence: an upstream that DELETES a branch it
// had diverged on has not resolved anything — the forge still holds the split
// history — so the record must survive. Writing "in step" for a ref nobody
// offered is a green console over an unresolved divergence.
func TestADeletedRefDoesNotResolveADivergence(t *testing.T) {
	up, fg, src, cloneURL := imported(t)

	diverge(t, fg.URL+"/"+forgeOwner+"/"+widgets.flat()+".git", "somebody's work")
	src.commit("upstream's work")
	if res := inbound(t, cloneURL); !res.Conflict {
		t.Fatalf("want a conflict to start from, got %+v", res)
	}

	// The upstream drops the branch.
	git(t, "", "--git-dir="+up.bare(upOwner, repoName), "update-ref", "-d", mainRef)
	if res := inbound(t, cloneURL); !res.NoOp {
		t.Fatalf("a ref the source no longer has is a no-op, got %+v", res)
	}
	st, err := storeFor(mounted.Load(), forgeOrg)
	if err != nil {
		t.Fatal(err)
	}
	states, err := st.States(context.Background(), forgeOrg)
	if err != nil {
		t.Fatal(err)
	}
	if !states[widgets].Conflict {
		t.Error("deleting the branch cleared the conflict it never resolved")
	}
}

// TestOneSpellingOfAnOrg: an org is folded at the door, so a caller that spells
// it differently reaches the same store and the same namespace.
//
// [forge.Owner] folds on its own, so a variant spelling already reached the same
// forge namespace — while the store name it took was a different SQLite file.
// One import, two records, and a console that reads whichever it asked for.
func TestOneSpellingOfAnOrg(t *testing.T) {
	up := newHost(t)
	newStand(t, forgeOwner)
	mountForge(t)
	up.seed(upOwner, repoName, "one")
	cloneURL := up.URL + "/" + upOwner + "/" + repoName + ".git"

	if err := (importer{}).ImportRepo(context.Background(), cloud.GitImportReq{
		Org: " HANZO ", Project: upOwner, Repo: repoName, CloneURL: cloneURL,
	}); err != nil {
		t.Fatalf("import: %v", err)
	}
	st, err := (importer{}).RepoStatus(context.Background(), forgeOrg, upOwner, []string{repoName})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !st[repoName].Imported || st[repoName].LastSyncedAt == 0 {
		t.Fatalf("a differently-spelled org wrote somewhere else: %+v", st[repoName])
	}
}

// TestMirrorTargetsMustBeAllowed: a declaration is refused unless the host is an
// approved outbound target over https — so nothing can point a repository's
// pushes at an internal service, or at the forge itself.
func TestMirrorTargetsMustBeAllowed(t *testing.T) {
	newStand(t, forgeOwner)
	mountForge(t)
	ctx := context.Background()
	for _, bad := range []string{
		"http://github.com/acme/x.git",       // not https
		"https://git.hanzo.ai/hanzoai/x.git", // the forge is never a target
		"https://10.0.0.5/x.git",             // internal
		"ssh://git@github.com/a/b.git",       // not a git-over-https URL
	} {
		if err := (mirrorControl{}).EnsureMirror(ctx, forgeOrg, upOwner, repoName, bad, true); err == nil {
			t.Errorf("%s was accepted as an outbound target", bad)
		}
	}
	// Userinfo is STRIPPED rather than refused; what must never happen is that a
	// credential is written down.
	if err := (mirrorControl{}).EnsureMirror(ctx, forgeOrg, upOwner, repoName,
		"https://user:pw@github.com/a/b.git", true); err != nil {
		t.Fatalf("a userinfo URL should be accepted with the credential stripped: %v", err)
	}
	st, err := storeFor(mounted.Load(), forgeOrg)
	if err != nil {
		t.Fatal(err)
	}
	urls, err := st.Mirrors(ctx, forgeOrg, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || strings.Contains(urls[0], "pw@") {
		t.Fatalf("stored targets = %v; a credential must never be written down", urls)
	}
	// And withdrawing is idempotent both ways.
	if err := (mirrorControl{}).EnsureMirror(ctx, forgeOrg, upOwner, repoName,
		"https://github.com/a/b.git", false); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if urls, _ := st.Mirrors(ctx, forgeOrg, widgets); len(urls) != 0 {
		t.Errorf("withdraw left %v", urls)
	}
}
