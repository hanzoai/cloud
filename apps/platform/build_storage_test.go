package platform

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// A build's working set is disk — clone, module cache, 112 plugin binaries, the
// exported layer cache. Without a declared request the pod is best-effort for
// ephemeral-storage: the scheduler will place it on a node with no room and the
// kubelet evicts it first, which killed a release ten minutes into its build with
// "The node was low on resource: ephemeral-storage … request is 0".
func TestBuildDeclaresItsDiskNeed(t *testing.T) {
	k := fakeK8s()
	job := k.buildJobSpec("pf-runner-t", "hanzoai", "runner", "push-hanzoai", []any{"buildctl-daemonless.sh"})
	cs, _, err := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	if err != nil || len(cs) == 0 {
		t.Fatalf("no containers: %v", err)
	}
	res, ok := cs[0].(map[string]any)["resources"].(map[string]any)
	if !ok {
		t.Fatal("the build container declares no resources; it is best-effort for disk and evicted first")
	}
	req, _ := res["requests"].(map[string]any)
	if req["ephemeral-storage"] == nil {
		t.Error("no ephemeral-storage REQUEST: the scheduler cannot avoid a full node")
	}
	lim, _ := res["limits"].(map[string]any)
	if lim["ephemeral-storage"] == nil {
		t.Error("no ephemeral-storage LIMIT: one runaway build can fill the node for everything else")
	}
}

// The request has to reflect what a build actually uses. At 20Gi it was declared
// but still short: the kubelet evicted a build that had exceeded its request off
// a node already at its threshold, ten minutes in.
func TestTheDiskRequestMatchesARealBuild(t *testing.T) {
	k := fakeK8s()
	job := k.buildJobSpec("pf-runner-t", "hanzoai", "runner", "push-hanzoai", []any{"buildctl-daemonless.sh"})
	cs, _, _ := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	res := cs[0].(map[string]any)["resources"].(map[string]any)
	req, _ := res["requests"].(map[string]any)
	if req["ephemeral-storage"] == "20Gi" {
		t.Error("20Gi was measured too small — a build evicted after exceeding it")
	}
}

// A finished build holds its buildkitd emptyDir until the POD is deleted, so the
// TTL is disk time. An hour of it, with a build every few minutes, is what filled
// the nodes.
func TestAFinishedBuildReleasesItsDiskPromptly(t *testing.T) {
	k := fakeK8s()
	job := k.buildJobSpec("pf-runner-t", "hanzoai", "runner", "push-hanzoai", []any{"buildctl-daemonless.sh"})
	ttl, found, err := unstructured.NestedInt64(job.Object, "spec", "ttlSecondsAfterFinished")
	if err != nil || !found {
		t.Fatal("the build job must bound how long it holds a node's disk")
	}
	if ttl > 900 {
		t.Errorf("ttl %ds holds the build's disk long after the work is done", ttl)
	}
	// Still far longer than the terminal-state read, which polls every 5s.
	if ttl < 60 {
		t.Errorf("ttl %ds could delete the job before its result is read", ttl)
	}
}

// The cache backend is a DEPLOYMENT fact, not a code opinion. The object store is
// the right home — a registry cache is pulled whole onto the node before any of it
// can be read, so its size lands on the same 105GB disk that holds the images, the
// snapshots and the build's working set, which is what filled the runner pool. But
// the build namespace has no network path to the object store yet, so selecting it
// by default would point every build at a cache it cannot open.
func TestTheCacheBackendFollowsWhatIsReachable(t *testing.T) {
	// Unset: the backend that works today.
	t.Setenv("BUILD_CACHE_S3_ENDPOINT", "")
	got := strings.Join(cacheArgs("ghcr.io/hanzoai/cloud"), " ")
	if !strings.Contains(got, "type=registry") {
		t.Errorf("with no object store configured the cache must stay on the registry: %s", got)
	}

	// Set: the cache moves, with no code change.
	t.Setenv("BUILD_CACHE_S3_ENDPOINT", "s3.hanzo.svc:9000")
	got = strings.Join(cacheArgs("ghcr.io/hanzoai/cloud"), " ")
	for _, want := range []string{
		"type=s3", "bucket=buildcache",
		"endpoint_url=http://s3.hanzo.svc:9000",
		// SeaweedFS addresses buckets by path, not virtual host.
		"use_path_style=true",
		// Keyed per repository inside the shared bucket.
		"name=hanzoai-cloud",
		// min, not max: max re-compressed and re-uploaded the whole build stage
		// (221.3s on cloud) to cache four steps worth 4.6s.
		"mode=min",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("s3 cache missing %q: %s", want, got)
		}
	}
	if strings.Contains(got, "type=registry") {
		t.Error("both backends at once: the cache would be written twice")
	}
}

// Whichever backend is chosen, the credential reaches buildkit through the
// ENVIRONMENT and never argv — a build command is logged and inspectable.
func TestTheCacheCredentialIsNeverOnArgv(t *testing.T) {
	t.Setenv("BUILD_CACHE_S3_ENDPOINT", "s3.hanzo.svc:9000")
	got := strings.Join(cacheArgs("ghcr.io/hanzoai/cloud"), " ")
	for _, leak := range []string{"access_key_id=", "secret_access_key="} {
		if strings.Contains(got, leak) {
			t.Errorf("the build command carries %q; it belongs in the env", leak)
		}
	}
	// And it IS supplied, from the same optional Secret the artifact publisher uses:
	// absent, buildkit reports a miss and the build runs uncached rather than the
	// Job being unschedulable. A cache accelerates; it never gates.
	k := fakeK8s()
	job := k.buildJobSpec("pf-runner-t", "hanzoai", "runner", "push-hanzoai", []any{"buildctl-daemonless.sh"})
	cs, _, _ := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	env, _ := cs[0].(map[string]any)["env"].([]any)
	found := map[string]bool{}
	for _, e := range env {
		em := e.(map[string]any)
		name, _ := em["name"].(string)
		found[name] = true
		if strings.HasPrefix(name, "AWS_") {
			ref := em["valueFrom"].(map[string]any)["secretKeyRef"].(map[string]any)
			if ref["optional"] != true {
				t.Errorf("%s is required; an absent cache credential would make the Job unschedulable", name)
			}
		}
	}
	for _, n := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
		if !found[n] {
			t.Errorf("%s is not supplied, so an object-store cache could never authenticate", n)
		}
	}
}

// The layer cache belongs in the object store, and hanzo-build's Cilium policy
// selects PODS: build-egress-deny-internal denies the namespace every internal
// CIDR, artifact-publish-egress reopens s3:9000 for hanzo.ai/publish=artifact.
// A Job copies only its POD TEMPLATE's labels onto the pod, so the four labels on
// the Job (which countActiveBuilds reads) do not reach it. The template carried
// none, the pod matched no policy, and every dial of the object store timed out —
// silently, since a cache miss is not an error. That left the cache on the node's
// disk, where it filled the runner pool and evicted builds mid-run.
func TestBuildPodCanReachTheObjectStore(t *testing.T) {
	k := fakeK8s()
	job := k.buildJobSpec("pf-runner-t", "hanzoai", "runner", "push-hanzoai", []any{"buildctl-daemonless.sh"})

	pod, _, err := unstructured.NestedStringMap(job.Object, "spec", "template", "metadata", "labels")
	if err != nil {
		t.Fatalf("pod template labels: %v", err)
	}
	if pod["hanzo.ai/publish"] != "artifact" {
		t.Errorf("pod template labels are %v, so the pod matches no egress policy and cannot reach s3:9000", pod)
	}

	// The Job's own labels are a separate concern — the build quota counts Jobs by
	// them. Labelling the pod must not have moved them.
	j, _, err := unstructured.NestedStringMap(job.Object, "metadata", "labels")
	if err != nil {
		t.Fatalf("job labels: %v", err)
	}
	if j["hanzo.ai/build"] != "true" || j["hanzo.ai/org"] != "hanzoai" {
		t.Errorf("job labels are %v; countActiveBuilds selects on hanzo.ai/build + hanzo.ai/org", j)
	}
}

// capOf returns a named volume's emptyDir sizeLimit, failing if the volume is
// absent or is not an emptyDir. It exists so the rule below is stated once per
// job rather than once per volume — the assertion is the same sentence three
// times, and the third one is the one somebody forgets.
func capOf(t *testing.T, job *unstructured.Unstructured, vol string) any {
	t.Helper()
	vols, _, err := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "volumes")
	if err != nil {
		t.Fatalf("volumes: %v", err)
	}
	for _, v := range vols {
		m, ok := v.(map[string]any)
		if !ok || m["name"] != vol {
			continue
		}
		ed, ok := m["emptyDir"].(map[string]any)
		if !ok {
			t.Fatalf("volume %q is not an emptyDir: %v", vol, m)
		}
		return ed["sizeLimit"]
	}
	t.Fatalf("no %q volume in the Job", vol)
	return nil
}

// EVERY emptyDir this package creates must be capped, not just the build cache.
//
// An uncapped emptyDir does not fail its own pod — it is charged to the NODE's
// ephemeral storage, so it fills the runner rootfs, trips DiskPressure, and the
// kubelet evicts the pod's NEIGHBOURS. A job whose own resource limits are never
// exceeded can take down every other job on the node, which is why the cap has
// to be on the volume and not only on the container. Not hypothetical: three of
// eight runners sat under DiskPressure with 70 finished build Jobs still holding
// their pods and emptyDirs, and a release was Evicted mid-flight for "node was
// low on resource: ephemeral-storage".
//
// The build cache was capped first, because it is the one that grew. This is the
// same assertion for all three, as a table, because the other two were left as
// `emptyDir: {}` — and "the one nobody got to yet" is the entire shape of the
// incident. A fourth job without a cap fails here rather than on a runner at 3am.
func TestEveryJobEmptyDirIsCapped(t *testing.T) {
	k := fakeK8s()
	for _, c := range []struct {
		name string
		job  *unstructured.Unstructured
		vol  string
		want string
	}{
		{"build", k.buildJobSpec("pf-runner-t", "hanzoai", "runner", "push-hanzoai", []any{"buildctl-daemonless.sh"}), "buildkitd", buildCacheLimit},
		{"artifact", k.artifactJobSpec("pf-art-t", "https://github.com/hanzoai/runner", "main", "v1", "base", "put", nil), "w", artifactWorkspaceLimit},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := capOf(t, c.job, c.vol); got != c.want {
				t.Fatalf("%s job's %q emptyDir sizeLimit is %v, want %q — uncapped, this job "+
					"evicts its neighbours off the node while staying inside its own limits",
					c.name, c.vol, got, c.want)
			}
		})
	}
}

// The fabric's own package registry sits behind the forge's REQUIRE_SIGNIN_VIEW,
// so an anonymous `npm ci` for an @hanzoteam/* dependency 401s even though the
// repo is public. The build therefore carries a registry credential — separate
// from GIT_AUTH_TOKEN, which authenticates a git fetch and rotates on its own
// schedule. They differ in whether the mount is optional, and that difference is
// the point: one has a working degraded mode and the other has none.
func TestBuildCarriesAPackageRegistryCredentialApartFromTheForgeOne(t *testing.T) {
	k := fakeK8s()
	job := k.buildJobSpec("pf-runner-t", "hanzoai", "runner", "push-hanzoai", []any{"buildctl-daemonless.sh"})
	cs, _, err := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	if err != nil || len(cs) == 0 {
		t.Fatalf("no containers: %v", err)
	}
	env, _ := cs[0].(map[string]any)["env"].([]any)

	from := map[string]string{}
	optional := map[string]bool{}
	for _, e := range env {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		vf, ok := m["valueFrom"].(map[string]any)
		if !ok {
			continue
		}
		skr, ok := vf["secretKeyRef"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		from[name], _ = skr["name"].(string)
		optional[name], _ = skr["optional"].(bool)
	}

	if from["REGISTRY_TOKEN"] != registryTokenSecret {
		t.Fatalf("REGISTRY_TOKEN comes from %q, want %q", from["REGISTRY_TOKEN"], registryTokenSecret)
	}
	if from["GIT_AUTH_TOKEN"] != forgeTokenSecret {
		t.Fatalf("GIT_AUTH_TOKEN comes from %q, want %q", from["GIT_AUTH_TOKEN"], forgeTokenSecret)
	}
	if registryTokenSecret == forgeTokenSecret {
		t.Fatal("the two credentials share one Secret; a package read now depends on a git-fetch rotation")
	}

	// A package credential is optional because losing it DEGRADES — the build
	// installs from public registries. A forge credential is not, because losing
	// it does not degrade anything: the forge serves nothing anonymously, so the
	// build fetches no source and says only that it wanted a username. Optional
	// there buys a fetch that cannot work in exchange for the kubelet's error
	// naming the Secret and the key.
	if !optional["REGISTRY_TOKEN"] {
		t.Error("REGISTRY_TOKEN must stay optional; without the Secret a build should install from public registries, not fail to schedule")
	}
	if optional["GIT_AUTH_TOKEN"] {
		t.Error("GIT_AUTH_TOKEN must not be optional; an absent forge credential has to name itself at the pod, not surface as a clone asking for a username")
	}
}

// A secret the solve never receives is a secret the Dockerfile cannot mount, so
// the env wiring above is only half the contract.
func TestTheRegistryCredentialReachesTheSolve(t *testing.T) {
	cmd := buildFrontendCmd("git://x", "Dockerfile", "oci.hanzo.ai/ns/app:1")
	var joined string
	for _, a := range cmd {
		s, _ := a.(string)
		joined += s + " "
	}
	for _, want := range []string{"id=REGISTRY_TOKEN,env=REGISTRY_TOKEN", "id=GIT_AUTH_TOKEN,env=GIT_AUTH_TOKEN"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("the solve never receives %q, so no Dockerfile can mount it", want)
		}
	}
}
