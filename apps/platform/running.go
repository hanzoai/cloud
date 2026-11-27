package platform

// running.go — what the cluster runs for a declaration, and what the registry
// has published since. declare.go reads what git says; cd.go reads what CD did
// with it; this reads the two facts neither can: the image the pods are actually
// running, and the newest release of that image. Drift is the difference between
// the three.

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Running is what the cluster runs for one declaration, read off its pods.
type Running struct {
	// Image is the image the app's container runs, as the pod spec names it.
	// When pods disagree — a rollout in flight — it is the one most of them run.
	Image string `json:"image"`
	// Digest is the digest the kubelet resolved that image to, "" when no pod
	// has reported one yet.
	Digest string `json:"digest,omitempty"`
	// Pods is how many pods carry the app's selector.
	Pods int `json:"pods"`
	// Ready is how many of them report every container ready.
	Ready int `json:"ready"`
}

// podsIn lists the pods of the given namespaces once, keyed by namespace. A
// namespace that cannot be listed is an error: a board that renders an
// unreadable namespace as "nothing running" reports a healthy empty fleet during
// an outage.
func podsIn(fs *cloud.Service[fleetState], ctx context.Context, namespaces []string) (map[string][]unstructured.Unstructured, error) {
	if err := fleetReady(fs); err != nil {
		return nil, err
	}
	out := make(map[string][]unstructured.Unstructured, len(namespaces))
	for _, ns := range namespaces {
		list, err := fs.State.dyn.Resource(k8s.Pods).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		out[ns] = list.Items
	}
	return out, nil
}

// runningOf matches a declaration to its pods by the label set the chart selects
// them with, and reads the app's container off them. Nil when no running pod
// carries the selector — which is a fact about the cluster, and drift decides
// what it means.
//
// Only a pod in phase Running that no Job owns counts. The chart's CronJobs stamp
// the app's selector labels on their pods, and a finished pod of any kind keeps
// the image it ran, so either would out-vote the Deployment and read a rolled app
// as un-rolled.
func runningOf(d Declaration, pods []unstructured.Unstructured) *Running {
	if len(d.selector) == 0 {
		return nil
	}
	count := map[string]int{}
	digest := map[string]string{}
	r := Running{}
	for i := range pods {
		p := &pods[i]
		if !selects(d.selector, p.GetLabels()) || p.GetDeletionTimestamp() != nil || !serving(p) {
			continue
		}
		name, image := appContainer(p, d.Repository)
		if image == "" {
			continue
		}
		r.Pods++
		count[image]++
		if ok, id := containerReady(p, name); ok || id != "" {
			if ok {
				r.Ready++
			}
			if id != "" {
				digest[image] = id
			}
		}
	}
	if r.Pods == 0 {
		return nil
	}
	for image, n := range count {
		if n > count[r.Image] || (n == count[r.Image] && image < r.Image) {
			r.Image = image
		}
	}
	r.Digest = digest[r.Image]
	return &r
}

// serving reports whether a pod is one of the app's long-running pods: Running,
// and not a Job's.
func serving(p *unstructured.Unstructured) bool {
	if phase, _, _ := unstructured.NestedString(p.Object, "status", "phase"); phase != "Running" {
		return false
	}
	for _, o := range p.GetOwnerReferences() {
		if o.Kind == "Job" {
			return false
		}
	}
	return true
}

// selects reports whether labels carry every key=value of selector.
func selects(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// appContainer is the pod's container that runs the declared repository, else its
// first container — so a sidecar is never mistaken for the app.
func appContainer(p *unstructured.Unstructured, repository string) (name, image string) {
	raw, _, _ := unstructured.NestedSlice(p.Object, "spec", "containers")
	for i, c := range raw {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		n, _ := m["name"].(string)
		img, _ := m["image"].(string)
		if i == 0 {
			name, image = n, img
		}
		if repository != "" && repoFromImageRef(img) == repository {
			return n, img
		}
	}
	return name, image
}

// containerReady reads one container's status: whether it is ready, and the
// digest the kubelet pulled (the part of imageID after "@").
func containerReady(p *unstructured.Unstructured, name string) (bool, string) {
	raw, _, _ := unstructured.NestedSlice(p.Object, "status", "containerStatuses")
	for _, c := range raw {
		m, ok := c.(map[string]any)
		if !ok || m["name"] != name {
			continue
		}
		ready, _ := m["ready"].(bool)
		id, _ := m["imageID"].(string)
		if at := strings.LastIndex(id, "@"); at >= 0 {
			id = id[at+1:]
		} else {
			id = ""
		}
		return ready, id
	}
	return false, ""
}

// ── the newest release ───────────────────────────────────────────────────────

// latestTTL is how long a registry's answer is reused. A release is a
// human-paced event and a board polls.
const latestTTL = 10 * time.Minute

// latestPages bounds how far a tag listing is followed. ghcr pages at the n it is
// asked for, and a repository with more tags than this has its newest ones in
// the pages already read only when the registry sorts them — it does not promise
// to, so the bound is a cost cap, not a correctness claim.
const latestPages = 5

var latestCache = struct {
	sync.Mutex
	tags map[string]latestEntry
}{tags: map[string]latestEntry{}}

type latestEntry struct {
	tag string
	at  time.Time
}

// newestTag answers the newest semver tag the registry publishes for repository,
// or "" when it cannot tell — a registry other than ghcr, a private repository
// the anonymous pull token cannot list, or a repository with no semver tag. An
// unknown is "", never a guess.
func newestTag(ctx context.Context, repository string) string {
	latestCache.Lock()
	if e, ok := latestCache.tags[repository]; ok && time.Since(e.at) < latestTTL {
		latestCache.Unlock()
		return e.tag
	}
	latestCache.Unlock()

	tag := listNewest(ctx, repository)
	if ctx.Err() != nil {
		// A read the deadline cut short is not the registry's answer, so it is
		// answered and not remembered.
		return tag
	}
	latestCache.Lock()
	latestCache.tags[repository] = latestEntry{tag: tag, at: time.Now()}
	latestCache.Unlock()
	return tag
}

func listNewest(ctx context.Context, repository string) string {
	host, repo, ok := strings.Cut(repository, "/")
	if !ok || host != "ghcr.io" {
		return ""
	}
	token, err := registryPullToken(ctx, host, repo)
	if err != nil {
		return ""
	}
	var best string
	var bestV semver
	next := registryBase + "/v2/" + repo + "/tags/list?n=1000"
	for page := 0; page < latestPages && next != ""; page++ {
		var body struct {
			Tags []string `json:"tags"`
		}
		link, err := registryList(ctx, next, token, &body)
		if err != nil {
			return best
		}
		for _, t := range body.Tags {
			if v, ok := parseSemver(t); ok && (best == "" || bestV.less(v)) {
				best, bestV = t, v
			}
		}
		next = link
	}
	return best
}

// registryList reads one page of a tag listing and returns the next page's URL
// from the Link header, "" when there is none. A next page is followed only on
// the registry's own scheme and host: the pull token rides every page, and a
// Link naming anywhere else would hand it there.
func registryList(ctx context.Context, endpoint, token string, out any) (string, error) {
	var next string
	err := registryGetWith(ctx, endpoint, token, out, func(h http.Header) {
		base, err := url.Parse(registryBase)
		if err != nil {
			return
		}
		for _, l := range h.Values("Link") {
			ref, rel, _ := strings.Cut(l, ";")
			if !strings.Contains(rel, `rel="next"`) {
				continue
			}
			r, err := base.Parse(strings.Trim(strings.TrimSpace(ref), "<>"))
			if err == nil && r.Scheme == base.Scheme && r.Host == base.Host {
				next = r.String()
			}
		}
	})
	return next, err
}

// latestOf answers newestTag for every repository at once, bounded to eight
// registry reads in flight and to the context's deadline. A repository the
// deadline leaves unanswered is "".
func latestOf(ctx context.Context, repositories []string) map[string]string {
	out := make(map[string]string, len(repositories))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	seen := map[string]bool{}
	sorted := append([]string(nil), repositories...)
	sort.Strings(sorted)
	for _, r := range sorted {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		wg.Add(1)
		go func(repo string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			tag := newestTag(ctx, repo)
			mu.Lock()
			out[repo] = tag
			mu.Unlock()
		}(r)
	}
	wg.Wait()
	return out
}
