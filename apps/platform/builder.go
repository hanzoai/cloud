// builder.go — the warm builder: a BuildKit daemon that outlives the builds of
// the one repository it serves.
//
// A private daemon (buildctl-daemonless.sh) starts empty and dies with its pod.
// Every `--mount=type=cache` goes with it, and the layer cache (cacheArgs)
// carries layers, never a cache mount, so cloud compiled all of its plugins and
// re-cloned its whole history on every build. A builder keeps Go's build cache,
// the module cache, the linked plugins, the git checkout and every layer, so a
// commit pays for what it changed.
//
// A builder is declared in universe (charts/app/values/hanzo-build/<name>.yaml)
// at zero replicas and named to the door by the repository it serves
// (CLOUD_PLATFORM_BUILDERS). The door wakes it for a build and the reconciler
// puts it back to sleep once no client has existed for builderIdle, so its node
// is paid for while builds run. The build Job becomes its client: it waits for
// the daemon, dials it over mTLS and streams the solve. The daemon holds the
// CPU, the disk and the cache.

package platform

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk/env"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

const (
	// builderPort is where a builder serves gRPC, behind mTLS.
	builderPort = "1234"
	// builderLabel admits a pod to one builder. The builder's NetworkPolicy lets
	// the pods carrying it — the daemon and its clients — reach each other on
	// builderPort, and no other pod reaches the daemon at all.
	builderLabel = "hanzo.ai/builder"
	// builderCerts is where a client mounts its builder's client identity:
	// cert-manager's Secret <builder>-client-tls, holding ca.crt, tls.crt and
	// tls.key. The leaf is issued for client auth only.
	builderCerts = "/certs"
)

// builderScript waits for the builder to answer, then hands it the solve.
//
// A builder the door has just woken needs a node to boot, its image pulled and
// its daemon started, and a new pod is admitted to a NetworkPolicy
// asynchronously, so the first dials are refused. Ten minutes without an answer
// is a builder that is not coming, and the build fails with a reason that names
// it and carries the dial error. The script is a constant and every value
// reaches it through "$@", as indexScript's does.
const builderScript = `set -eu
addr=$1; shift
tls="--tlscacert=` + builderCerts + `/ca.crt --tlscert=` + builderCerts + `/tls.crt --tlskey=` + builderCerts + `/tls.key"
i=0
until buildctl --addr "$addr" $tls debug workers >/dev/null 2>&1; do
  i=$((i+1))
  if [ "$i" -ge 120 ]; then
    why=$(buildctl --addr "$addr" $tls debug workers 2>&1 || true)
    echo "error: builder $addr did not answer within 600s: $why"
    exit 1
  fi
  sleep 5
done
exec buildctl --addr "$addr" $tls "$@"`

// repoKey is a clone URL as builders are matched on: host and path, lowercased,
// with no scheme, trailing slash or .git. The host is part of it, so the same
// owner/name on another provider is another repository and has no builder.
func repoKey(u string) string {
	u = strings.ToLower(strings.TrimSpace(u))
	u = strings.TrimPrefix(u, "https://")
	return strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
}

// builderName names the builder of one repository: builder-<owner>-<repo>. It is
// the name of its Deployment and Service, the value of builderLabel, and with
// -client-tls the Secret holding its client identity.
func builderName(repoURL string) string {
	return "builder-" + strings.ReplaceAll(repoSlug("https://"+repoKey(repoURL)), "/", "-")
}

// builderAddr is a builder's gRPC address, by its Service in ns.
func builderAddr(name, ns string) string {
	return "tcp://" + name + "." + ns + ".svc.cluster.local:" + builderPort
}

// builderIdle is how long a builder stays awake after its last build was
// asked for. Its state is an emptyDir, so a builder that sleeps wakes cold: an
// hour keeps a working day's commits warm and releases the node overnight.
const builderIdle = time.Hour

// builderUsed is the Deployment annotation wakeBuilder stamps with the time of
// each build it admits, and sleepBuilders reads.
const builderUsed = "hanzo.ai/used"

// builderFor is the builder that runs one build, or "" for a private daemon.
//
// Only a trusted commit in the release shape reaches a builder. The shape: an
// estate build of a repository CLOUD_PLATFORM_BUILDERS names, from a
// Dockerfile, for the builder's architecture, with no target, context directory
// or declared build-arg, so a caller-named BASE never runs there. The commit: a
// full SHA that GitHub, the repository's home, says one of its published
// branches contains (refTrusted). A builder is shared by its builds — its cache
// mounts are writable by each, and with no process sandbox a step can read a
// concurrent step's secrets — so only code the repository published runs on it.
// Everything else keeps a daemon of its own.
func (k *k8sClient) builderFor(ctx context.Context, l lane, org, repoURL, ref, dockerfile, target, dir string, args map[string]string, platforms []string) string {
	if l != estate || dockerfile == "" || target != "" || dir != "" || len(args) > 0 {
		return ""
	}
	if len(platforms) > 1 || len(platforms) == 1 && nativeArch[platforms[0]] != defaultArch {
		return ""
	}
	key := repoKey(repoURL)
	if !slices.ContainsFunc(builders(), func(b string) bool { return repoKey(b) == key }) {
		return ""
	}
	if !refTrusted(ctx, org, repoURL, ref) {
		return ""
	}
	name := builderName(repoURL)
	if !k.wakeBuilder(ctx, name) {
		return ""
	}
	return name
}

// builders is the repositories CLOUD_PLATFORM_BUILDERS names, by clone URL.
func builders() []string {
	return strings.FieldsFunc(env.Or("CLOUD_PLATFORM_BUILDERS", ""), func(r rune) bool { return r == ',' || r == ' ' })
}

// refTrusted reports whether ref is a commit the repository's maintainers
// published: a full SHA, on a host that holds the estate's repositories under
// their own path (mirrored), which GitHub — the home both spellings mirror —
// says its default branch, a protected branch or a tag contains. It is asked of
// GitHub through the platform App's installation (githubTrusts), and every doubt
// is a no.
func refTrusted(ctx context.Context, org, repoURL, ref string) bool {
	if !env.IsCommit(ref) || !mirrored(gitHost("https://"+repoKey(repoURL))) {
		return false
	}
	owner, name, ok := strings.Cut(repoSlug("https://"+repoKey(repoURL)), "/")
	return ok && githubTrusts(ctx, org, owner, name, ref)
}

// wakeBuilder stamps builder's Deployment as used, makes it run one pod, and
// reports whether a client may be sent to it: it is Ready, or it was asleep and
// is now waking, or its pod is younger than builderWake and still starting. A
// builder whose pod has been up that long and is not Ready is broken, and the
// build runs in a private daemon. Any read or write the door may not make is a
// no.
func (k *k8sClient) wakeBuilder(ctx context.Context, name string) bool {
	if k.dyn == nil {
		return false
	}
	deps := k.dyn.Resource(k8s.Deployments).Namespace(k.buildNS)
	d, err := deps.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false
	}
	asleep := false
	if n, _, _ := unstructured.NestedInt64(d.Object, "spec", "replicas"); n == 0 {
		asleep = true
	}
	patch := `{"metadata":{"annotations":{"` + builderUsed + `":"` + time.Now().UTC().Format(time.RFC3339) + `"}},"spec":{"replicas":1}}`
	if _, err := deps.Patch(ctx, name, k8stypes.MergePatchType, []byte(patch), metav1.PatchOptions{FieldManager: "cloud-platform"}); err != nil {
		return false
	}
	if asleep {
		return true
	}
	pods, ok := k.builderPods(ctx, name)
	if !ok {
		return false
	}
	return len(pods) == 0 || slices.ContainsFunc(pods, func(p unstructured.Unstructured) bool {
		return ready(p) || time.Since(p.GetCreationTimestamp().Time) < builderWake
	})
}

// builderWake bounds a builder's start: a node to boot, the image pulled, the
// daemon up. It is builderScript's wait.
const builderWake = 10 * time.Minute

// sleepBuilders puts back to zero replicas every awake builder that has no
// client Job and was last used more than builderIdle ago, so its node goes back
// to the autoscaler. The reconciler calls it each tick.
func (k *k8sClient) sleepBuilders(ctx context.Context) {
	if k.dyn == nil {
		return
	}
	deps := k.dyn.Resource(k8s.Deployments).Namespace(k.buildNS)
	for _, repo := range builders() {
		name := builderName(repo)
		d, err := deps.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		if n, _, _ := unstructured.NestedInt64(d.Object, "spec", "replicas"); n == 0 {
			continue
		}
		if used, err := time.Parse(time.RFC3339, d.GetAnnotations()[builderUsed]); err == nil && time.Since(used) < builderIdle {
			continue
		}
		jobs, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(ctx, metav1.ListOptions{LabelSelector: builderLabel + "=" + name})
		if err != nil || slices.ContainsFunc(jobs.Items, func(j unstructured.Unstructured) bool { return !jobFinished(&j) }) {
			continue
		}
		_, _ = deps.Patch(ctx, name, k8stypes.MergePatchType, []byte(`{"spec":{"replicas":0}}`), metav1.PatchOptions{FieldManager: "cloud-platform"})
	}
}

// builderPods is a builder's pods that are not being deleted.
func (k *k8sClient) builderPods(ctx context.Context, name string) ([]unstructured.Unstructured, bool) {
	list, err := k.dyn.Resource(k8s.Pods).Namespace(k.buildNS).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=" + name})
	if err != nil {
		return nil, false
	}
	return slices.DeleteFunc(list.Items, func(p unstructured.Unstructured) bool { return p.GetDeletionTimestamp() != nil }), true
}

// ready is a pod whose Ready condition is True: for a builder, its daemon
// answers on the unix socket and its accept probe got in over mTLS with a
// client certificate, the path every client takes.
func ready(p unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(p.Object, "status", "conditions")
	return slices.ContainsFunc(conds, func(c any) bool {
		m, _ := c.(map[string]any)
		return m["type"] == "Ready" && m["status"] == "True"
	})
}

// onBuilder is cmd, the private-daemon argv buildFrontendCmdRev wrote, sent to
// builder instead: builderScript replaces buildctl-daemonless.sh, and the layer
// cache flags are dropped, because the builder's own store is the cache.
func onBuilder(cmd []any, builder, ns string) []any {
	out := []any{"/bin/sh", "-c", builderScript, "builder", builderAddr(builder, ns)}
	for i := 1; i < len(cmd); i++ {
		if s, _ := cmd[i].(string); s == "--import-cache" || s == "--export-cache" {
			i++
			continue
		}
		out = append(out, cmd[i])
	}
	return out
}

// asClient makes an estate build Job the client of builder.
//
// What a solve needs from its pod stays: the argv, the registry credential and
// the build secrets, each still read here and served to the daemon over the
// session. What a daemon needs goes: the rootless sandbox and its unconfined
// profiles, the state volume, the object-store key (a builder keeps its own
// cache) and the CPU, which the builder holds. The pod carries builderLabel and
// nothing else, so it reaches the builder and not the object store.
func asClient(job *unstructured.Unstructured, builder string) {
	job.Object["metadata"].(map[string]any)["labels"].(map[string]any)[builderLabel] = builder
	tpl := job.Object["spec"].(map[string]any)["template"].(map[string]any)
	tpl["metadata"] = map[string]any{"labels": map[string]any{builderLabel: builder}}
	pod := podSpec(job.Object)
	pod["securityContext"] = map[string]any{
		"runAsUser":      int64(1000),
		"runAsGroup":     int64(1000),
		"runAsNonRoot":   true,
		"fsGroup":        int64(1000),
		"seccompProfile": map[string]any{"type": "RuntimeDefault"},
	}
	pod["volumes"] = append(slices.DeleteFunc(pod["volumes"].([]any), named("buildkitd")), map[string]any{
		"name": "certs",
		// 0440 with fsGroup 1000: the files are root's, and the group bit is what
		// lets uid 1000 open the key.
		"secret": map[string]any{"secretName": builder + "-client-tls", "defaultMode": int64(0o440)},
	})
	c := pod["containers"].([]any)[0].(map[string]any)
	c["env"] = slices.DeleteFunc(c["env"].([]any), func(e any) bool {
		switch e.(map[string]any)["name"] {
		case "BUILDKITD_FLAGS", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY":
			return true
		}
		return false
	})
	c["volumeMounts"] = append(slices.DeleteFunc(c["volumeMounts"].([]any), named("buildkitd")),
		map[string]any{"name": "certs", "mountPath": builderCerts, "readOnly": true})
	// A client streams a context and a log: a fraction of a core and little memory.
	c["resources"] = map[string]any{
		"requests": map[string]any{"cpu": "100m", "memory": "128Mi", "ephemeral-storage": "64Mi"},
		"limits":   map[string]any{"cpu": "1", "memory": "512Mi", "ephemeral-storage": "1Gi"},
	}
	// It runs no daemon and unshares nothing, so it takes every restriction the
	// rootless daemon cannot. buildctl writes no file (measured under a read-only
	// root with strace: no open for write).
	c["securityContext"] = map[string]any{
		"privileged":               false,
		"runAsUser":                int64(1000),
		"runAsGroup":               int64(1000),
		"runAsNonRoot":             true,
		"allowPrivilegeEscalation": false,
		"readOnlyRootFilesystem":   true,
		"capabilities":             map[string]any{"drop": []any{"ALL"}},
		"seccompProfile":           map[string]any{"type": "RuntimeDefault"},
	}
}

// named matches a volume or a mount by name.
func named(name string) func(any) bool {
	return func(v any) bool { return v.(map[string]any)["name"] == name }
}
