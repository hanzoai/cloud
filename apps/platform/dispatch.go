// dispatch.go — a build waits its turn; it is never refused for the others.
//
// Every build Job is created suspended, and a suspended Job is the queue entry. It lives in
// the apiserver, so it survives a restart of every cloud replica; it carries its org and its
// age, so the line is read from the cluster and kept nowhere else; and resuming it is the
// whole of "start". dispatchBuilds resumes the builds the cluster has room for, whenever a
// build is launched and on every reconcile tick, which is also when a finished build frees
// its place.
//
// Room is two numbers an operator sets in admin.hanzo.ai: how many builds one org may run at
// once, and how many the cluster runs at once. Between orgs the next place goes to the org
// running the fewest, so one org's release train never holds the others' builds back; within
// an org, an admin's build starts before a member's, then the oldest first.
//
// The ceilings are soft: two dispatchers that read the line together can resume past them by
// the builds in flight, and the leader's tick is the one dispatcher that is always there.
//
// A build is one or more Jobs asked for once: a fan-out is a Job per architecture and then
// the index that joins them. A build counts once, and once any of its Jobs has started the
// rest of it starts with it, so the index of a build admitted long ago never waits behind
// builds that have not begun.

package platform

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/hanzoai/cloud/client"
	flagsplane "github.com/hanzoai/cloud/client/flag"
	"github.com/hanzoai/cloud/internal/cluster"
	"github.com/hanzoai/cloud/sdk"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

// queueFactor is how many times its concurrency an org may have waiting. The line is the
// apiserver's, so an org that asks without end is a cost to every other org and not only to itself.
const queueFactor = 10

// buildLine is one build in the line: the Jobs it is made of and where they stand.
type buildLine struct {
	id      string
	org     string
	member  bool      // a member's build, which starts after the org's admins'
	created time.Time // its oldest Job's
	waiting []string  // its Jobs not yet resumed
	begun   bool      // one of its Jobs has been resumed, and is running or finished
	live    bool      // it holds a place: a resumed Job of it has not finished
}

// buildLines reads the line from the cluster: every unfinished build, of one org or, when
// org is empty, of all of them.
func (k *k8sClient) buildLines(ctx context.Context, org string) ([]buildLine, error) {
	sel := "hanzo.ai/build=true"
	if org != "" {
		sel += ",hanzo.ai/org=" + org
	}
	list, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, err
	}
	byID := map[string]*buildLine{}
	for i := range list.Items {
		j := &list.Items[i]
		labels := j.GetLabels()
		id := labels["hanzo.ai/build-id"]
		if id == "" {
			id = "job/" + j.GetName()
		}
		l := byID[id]
		if l == nil {
			l = &buildLine{id: id, org: labels["hanzo.ai/org"], member: labels[memberLabel] != "", created: j.GetCreationTimestamp().Time}
			byID[id] = l
		}
		if t := j.GetCreationTimestamp().Time; t.Before(l.created) {
			l.created = t
		}
		switch suspended, _, _ := unstructured.NestedBool(j.Object, "spec", "suspend"); {
		case jobFinished(j):
			l.begun = true
		case suspended:
			l.waiting = append(l.waiting, j.GetName())
		default:
			l.begun, l.live = true, true
		}
	}
	lines := make([]buildLine, 0, len(byID))
	for _, l := range byID {
		if l.live || len(l.waiting) > 0 {
			lines = append(lines, *l)
		}
	}
	return lines, nil
}

// startOrder is the order the builds that have not begun start in, were nothing to finish
// meanwhile: the org running the fewest goes next, ties to the org whose next build is the
// oldest. running is each org's live builds.
func startOrder(lines []buildLine, running map[string]int) []buildLine {
	next := map[string][]buildLine{}
	for _, l := range lines {
		if !l.begun {
			next[l.org] = append(next[l.org], l)
		}
	}
	for _, q := range next {
		slices.SortFunc(q, func(a, b buildLine) int {
			return cmp.Or(boolInt(a.member)-boolInt(b.member), a.created.Compare(b.created), cmp.Compare(a.id, b.id))
		})
	}
	var out []buildLine
	for {
		best := ""
		for org, q := range next {
			if len(q) == 0 {
				continue
			}
			if best == "" || cmp.Or(
				cmp.Compare(running[org], running[best]),
				next[org][0].created.Compare(next[best][0].created),
				cmp.Compare(org, best),
			) < 0 {
				best = org
			}
		}
		if best == "" {
			return out
		}
		out = append(out, next[best][0])
		next[best] = next[best][1:]
		running[best]++
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// liveByOrg counts the builds holding a place, per org and in all.
func liveByOrg(lines []buildLine) (map[string]int, int) {
	running, all := map[string]int{}, 0
	for _, l := range lines {
		if l.live {
			running[l.org]++
			all++
		}
	}
	return running, all
}

// resumable is the builds to resume now: those already begun, which take what they still
// need, then those that have not, in start order, while the org and the cluster have room.
func resumable(lines []buildLine, perOrg, total int) []buildLine {
	running, all := liveByOrg(lines)
	var out []buildLine
	for _, l := range lines {
		if l.begun && len(l.waiting) > 0 {
			out = append(out, l)
			if !l.live {
				running[l.org]++
				all++
			}
		}
	}
	for _, l := range startOrder(lines, maps.Clone(running)) {
		if all >= total {
			break
		}
		if running[l.org] >= perOrg {
			continue
		}
		out = append(out, l)
		running[l.org]++
		all++
	}
	return out
}

// dispatchBuilds resumes the builds that have room to run.
func (k *k8sClient) dispatchBuilds(ctx context.Context, perOrg, total int) error {
	lines, err := k.buildLines(ctx, "")
	if err != nil {
		return fmt.Errorf("read the build line: %w", err)
	}
	var errs []error
	for _, l := range resumable(lines, perOrg, total) {
		for _, name := range l.waiting {
			_, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Patch(ctx, name, k8stypes.MergePatchType,
				[]byte(`{"spec":{"suspend":false}}`), metav1.PatchOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("start %s: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// ceilings are the two numbers an operator sets, read as the platform's own: a build has
// no request to name an org, and a ceiling belongs to no org.
func ceilings(ctx context.Context) (perOrg, total int) {
	ctx = cloud.For(ctx, cluster.Platform)
	return flagsplane.Int(ctx, client.PlatformBuildsPerOrg, client.PlatformBuildsPerOrgSeed), flagsplane.Int(ctx, client.PlatformBuilds, client.PlatformBuildsSeed)
}

// dispatch is dispatchBuilds under the ceilings an operator has set.
func (k *k8sClient) dispatch(ctx context.Context) error {
	perOrg, total := ceilings(ctx)
	return k.dispatchBuilds(ctx, perOrg, total)
}

// errBuildQueueFull is the one refusal a build meets: its org already has as many waiting as
// its line may hold.
var errBuildQueueFull = errors.New("platform: this org's build queue is full")

// admitBuild accepts a build for its org, whatever is running. It refuses only an org whose
// line is already queueFactor times its concurrency, and that bound is what keeps an org
// that asks without end from filling the apiserver.
func (k *k8sClient) admitBuild(ctx context.Context, org string) error {
	lines, err := k.buildLines(ctx, org)
	if err != nil {
		return fmt.Errorf("read the build line: %w", err)
	}
	perOrg, _ := ceilings(ctx)
	if len(lines) >= queueFactor*perOrg {
		return errBuildQueueFull
	}
	return nil
}

// createBuildJob puts a build Job in the line and starts the line.
//
// It is the one place a build Job is created. The Job is created suspended (buildJobSpec,
// artifactJobSpec), so this is where it joins; and dispatching here, and not only on the
// next tick, is what lets a build that finds the cluster idle start with no wait. A
// dispatch that fails here is the next tick's to retry: the build is already in the line.
func (k *k8sClient) createBuildJob(ctx context.Context, job *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	created, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	_ = k.dispatch(ctx)
	return created, nil
}

// queuePosition is a waiting build's place in the line, from 1, and 0 when it is not waiting.
func (k *k8sClient) queuePosition(ctx context.Context, buildID string) (int, error) {
	lines, err := k.buildLines(ctx, "")
	if err != nil {
		return 0, err
	}
	running, _ := liveByOrg(lines)
	id := jobIDSuffix(buildID)
	for i, l := range startOrder(lines, running) {
		if l.id == id {
			return i + 1, nil
		}
	}
	return 0, nil
}

// buildStarted is when a build's first Job started, and whether it is still waiting.
func (k *k8sClient) buildStarted(ctx context.Context, buildID string) (waiting bool, started time.Time, err error) {
	list, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(ctx, metav1.ListOptions{LabelSelector: "hanzo.ai/build-id=" + jobIDSuffix(buildID)})
	if err != nil {
		return false, time.Time{}, err
	}
	waiting = true
	for i := range list.Items {
		if suspended, _, _ := unstructured.NestedBool(list.Items[i].Object, "spec", "suspend"); suspended && !jobFinished(&list.Items[i]) {
			continue
		}
		waiting = false
		if s, ok, _ := unstructured.NestedString(list.Items[i].Object, "status", "startTime"); ok {
			if t, err := time.Parse(time.RFC3339, s); err == nil && (started.IsZero() || t.Before(started)) {
				started = t
			}
		}
	}
	return waiting && len(list.Items) > 0, started, nil
}
