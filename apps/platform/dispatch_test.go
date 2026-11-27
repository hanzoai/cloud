package platform

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	cloud "github.com/hanzoai/cloud/sdk"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// heldAndWaiting counts the unfinished build Jobs: those resumed, and those still suspended.
func heldAndWaiting(t *testing.T, k *k8sClient) (running, waiting int) {
	t.Helper()
	list, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range list.Items {
		if jobFinished(&list.Items[i]) {
			continue
		}
		if suspended, _, _ := unstructured.NestedBool(list.Items[i].Object, "spec", "suspend"); suspended {
			waiting++
		} else {
			running++
		}
	}
	return running, waiting
}

func gitApp(org, slug string) Application {
	a := mkApp(org, "proj_"+org, slug)
	a.Source, a.RepoURL, a.RepoBranch = "git", "https://github.com/hanzoai/app", "main"
	return a
}

// A BUILD PAST ITS ORG'S CONCURRENCY IS ACCEPTED AND WAITS. It is never refused for the others
// running, another org's build starts beside it, and the place a finished build gives up goes to
// the build that has waited longest.
func TestABuildPastTheOrgCeilingWaitsAndIsNeverRefused(t *testing.T) {
	ctx := context.Background()
	k := fakeK8s()
	a := gitApp("lux", "api")
	image := k.buildImageRef("lux", "api", "main")
	var first string
	for i := 0; i <= client.PlatformBuildsPerOrgSeed; i++ {
		job, err := k.launchBuildJob(ctx, estate, "lux", a, image, "main", "bld_"+itoa(i))
		if err != nil {
			t.Fatalf("build %d was refused: %v", i, err)
		}
		if i == 0 {
			first = job
		}
	}
	if running, waiting := heldAndWaiting(t, k); running != client.PlatformBuildsPerOrgSeed || waiting != 1 {
		t.Fatalf("%d running and %d waiting, want %d and 1", running, waiting, client.PlatformBuildsPerOrgSeed)
	}

	b := gitApp("acme", "api")
	if _, err := k.launchBuildJob(ctx, customer, "acme", b, k.buildImageRef("acme", "api", "main"), "main", "bld_acme"); err != nil {
		t.Fatalf("another org's build was refused: %v", err)
	}
	if running, _ := heldAndWaiting(t, k); running != client.PlatformBuildsPerOrgSeed+1 {
		t.Fatalf("acme's build did not start beside lux's full line: %d running", running)
	}

	jobSucceeded(t, k, first)
	if err := k.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, waiting := heldAndWaiting(t, k); waiting != 0 {
		t.Fatalf("a finished build's place did not go to the one waiting: %d still wait", waiting)
	}
}

// A FAN-OUT IS ONE BUILD. Its two Jobs take one place and start together, and a build past the
// ceiling waits with both of them suspended.
func TestAFanOutStartsAndWaitsAsOneBuild(t *testing.T) {
	ctx := context.Background()
	k := fakeK8s()
	both := []string{"linux/amd64", "linux/arm64"}
	for i := 0; i <= client.PlatformBuildsPerOrgSeed; i++ {
		if _, err := k.launchDirectBuild(ctx, estate, "lux", "", "https://github.com/hanzoai/app", "main",
			"ghcr.io/luxfi/app:v1."+itoa(i), "Dockerfile", "", "", "bld_fan"+itoa(i), nil, both); err != nil {
			t.Fatalf("fan-out %d was refused: %v", i, err)
		}
	}
	if running, waiting := heldAndWaiting(t, k); running != 2*client.PlatformBuildsPerOrgSeed || waiting != 2 {
		t.Fatalf("%d Jobs running and %d waiting, want %d and 2: a fan-out starts and waits whole", running, waiting, 2*client.PlatformBuildsPerOrgSeed)
	}
}

// The ceilings are an operator's: dispatchBuilds takes them as numbers, and starts no more than
// the cluster's total or an org's share.
func TestDispatchHonoursBothCeilings(t *testing.T) {
	ctx := context.Background()
	k := fakeK8s()
	for _, org := range []string{"a", "b"} {
		for i := range 3 {
			job := k.buildJobSpec(estate, "pf-"+org+itoa(i), "bld_"+org+itoa(i), org, "runner", "push-hanzoai", "", []any{"x"}, defaultArch, nil)
			if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(ctx, job, metav1.CreateOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := k.dispatchBuilds(ctx, 2, 3); err != nil {
		t.Fatal(err)
	}
	running, waiting := heldAndWaiting(t, k)
	if running != 3 || waiting != 3 {
		t.Fatalf("%d running and %d waiting, want the cluster's 3 and the rest waiting", running, waiting)
	}
	lines, _ := k.buildLines(ctx, "")
	byOrg, _ := liveByOrg(lines)
	if byOrg["a"]+byOrg["b"] != 3 || byOrg["a"] == 0 || byOrg["b"] == 0 || byOrg["a"] > 2 || byOrg["b"] > 2 {
		t.Fatalf("running by org %v: neither org may take more than 2, and orgs take turns", byOrg)
	}
}

func line(id, org string, age int, flags ...string) buildLine {
	l := buildLine{id: id, org: org, created: time.Unix(1_000+int64(age), 0), waiting: []string{id}}
	for _, f := range flags {
		switch f {
		case "member":
			l.member = true
		case "live":
			l.live, l.begun, l.waiting = true, true, nil
		}
	}
	return l
}

func ids(lines []buildLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.id)
	}
	return out
}

// ORGS TAKE TURNS. Org a has a long train waiting and two builds running; org b has one build
// waiting and none running, and it starts first.
func TestOrgsTakeTurns(t *testing.T) {
	lines := []buildLine{
		line("a1", "a", 1, "live"), line("a2", "a", 2, "live"),
		line("a3", "a", 3), line("a4", "a", 4), line("a5", "a", 5),
		line("b1", "b", 10),
	}
	got := ids(resumable(lines, 20, 4))
	if want := []string{"b1", "a3"}; !slices.Equal(got, want) {
		t.Fatalf("started %v, want %v: the org running none goes first, then the longest waiting", got, want)
	}
}

// WITHIN AN ORG AN ADMIN'S BUILD STARTS BEFORE A MEMBER'S, however much older the member's is.
func TestAnAdminsBuildStartsBeforeAMembers(t *testing.T) {
	lines := []buildLine{line("m1", "a", 1, "member"), line("m2", "a", 2, "member"), line("adm", "a", 9)}
	if got := ids(resumable(lines, 1, 1)); !slices.Equal(got, []string{"adm"}) {
		t.Fatalf("started %v, want the admin's build", got)
	}
}

// THE INDEX OF A BUILD ALREADY ADMITTED STARTS AT ONCE. Its architectures have finished, it
// executes nothing, and making it wait behind builds that have not begun would hold a finished
// image back from its tag.
func TestTheIndexOfAnAdmittedBuildStartsWithoutWaiting(t *testing.T) {
	index := buildLine{id: "fan", org: "a", created: time.Unix(1, 0), waiting: []string{"pf-fan-index"}, begun: true}
	lines := []buildLine{index, line("other", "b", 2), line("busy", "c", 3, "live")}
	if got := ids(resumable(lines, 1, 1)); !slices.Equal(got, []string{"fan"}) {
		t.Fatalf("started %v, want the index alone: the cluster is full", got)
	}
}

// A WAITING BUILD IS TOLD ITS PLACE, in the order the line starts builds.
func TestAWaitingBuildKnowsItsPlace(t *testing.T) {
	ctx := context.Background()
	k := fakeK8s()
	for _, j := range []struct{ org, id string }{{"a", "bld_a1"}, {"a", "bld_a2"}, {"b", "bld_b1"}} {
		job := k.buildJobSpec(estate, "pf-"+j.id, j.id, j.org, "runner", "push-hanzoai", "", []any{"x"}, defaultArch, nil)
		if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(ctx, job, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]int{}
	for _, id := range []string{"bld_a1", "bld_a2", "bld_b1", "bld_none"} {
		pos, err := k.queuePosition(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		got[id] = pos
	}
	// The two orgs have nothing running, so they alternate: a's oldest, then b's, then a's next.
	if got["bld_none"] != 0 || got["bld_a1"] == 0 || got["bld_b1"] == 0 || got["bld_a2"] != 3 {
		t.Fatalf("places %v, want the three waiting builds at 1 to 3 and a build not in line at 0", got)
	}
	if got["bld_b1"] > 2 {
		t.Fatalf("places %v: org b has nothing running and must not wait behind org a's second build", got)
	}
}

// AN ORG THAT ASKS WITHOUT END IS REFUSED, and no other org is. Its line holds queueFactor times
// its concurrency, because the line is the apiserver's.
func TestAnOrgsLineIsBounded(t *testing.T) {
	ctx := context.Background()
	k := fakeK8s()
	for i := range queueFactor * client.PlatformBuildsPerOrgSeed {
		job := k.buildJobSpec(estate, "pf-q"+itoa(i), "bld_q"+itoa(i), "flood", "runner", "push-hanzoai", "", []any{"x"}, defaultArch, nil)
		if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(ctx, job, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := k.admitBuild(ctx, "flood"); err != errBuildQueueFull {
		t.Fatalf("a full line: want errBuildQueueFull, got %v", err)
	}
	if err := k.admitBuild(ctx, "calm"); err != nil {
		t.Fatalf("another org was refused for flood's line: %v", err)
	}
}

// THE DEADLINE IS TIME RUN, NOT TIME WAITED. A build still waiting its turn is never overdue, and
// one that started long ago is.
func TestTheBuildDeadlineStartsWhenTheBuildDoes(t *testing.T) {
	ctx := context.Background()
	k := fakeK8s()
	job := k.buildJobSpec(estate, "pf-wait", "bld_wait", "hanzo", "runner", "push-hanzoai", "", []any{"x"}, defaultArch, nil)
	if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	s := &cloud.Service[state]{State: state{k8s: k}}
	b := Build{ID: "bld_wait", Org: "hanzo", CreatedAt: time.Now().Add(-3 * buildDeadline).Unix()}

	if overdue, queued := buildOverdue(s, ctx, b, buildDeadline); overdue || !queued {
		t.Fatalf("a build waiting its turn for %s: overdue=%v queued=%v, want not overdue and queued", 3*buildDeadline, overdue, queued)
	}

	if err := k.dispatchBuilds(ctx, 1, 1); err != nil {
		t.Fatal(err)
	}
	j, _ := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(ctx, "pf-wait", metav1.GetOptions{})
	_ = unstructured.SetNestedField(j.Object, time.Now().Add(-buildDeadline-time.Minute).UTC().Format(time.RFC3339), "status", "startTime")
	if _, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Update(ctx, j, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if overdue, queued := buildOverdue(s, ctx, b, buildDeadline); !overdue || queued {
		t.Fatalf("a build that started %s ago: overdue=%v queued=%v, want overdue", buildDeadline+time.Minute, overdue, queued)
	}
}

// TEN BUILDS FROM ONE ORG, ALL AT ONCE, ALL START. None is refused and none waits: ten is under
// the org's concurrency.
func TestTenConcurrentBuildsFromOneOrgAllStart(t *testing.T) {
	ctx := context.Background()
	k := fakeK8s()
	a := gitApp("lux", "api")
	image := k.buildImageRef("lux", "api", "main")
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := k.launchBuildJob(ctx, estate, "lux", a, image, "main", "bld_c"+itoa(i))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent build was refused: %v", err)
		}
	}
	if running, waiting := heldAndWaiting(t, k); running != 10 || waiting != 0 {
		t.Fatalf("%d running and %d waiting, want all 10 running", running, waiting)
	}
}
