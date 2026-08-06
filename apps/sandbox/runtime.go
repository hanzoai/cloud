// runtime.go — the Kubernetes half: what a sandbox IS in the cluster, and the
// one channel into it.
//
// A sandbox is a Pod, and its isolation boundary is the RUNTIME that pod names
// — one field, `SANDBOX_RUNTIME_CLASS`, holding `gvisor` or `kata-fc` or
// `kata-clh` or nothing. It is the ONLY boundary claimed here: no uid juggling,
// no process groups, no daemon inside the pod deciding what it is allowed to
// run. Everything the predecessor built to approximate that boundary in Go is
// deleted rather than kept "for defence in depth", because a second
// half-boundary is a second thing to keep true.
//
// Empty is honest, not a hole: it means the node's default runtime, which is
// the containment a normal pod gets. It ships that way because runsc has to be
// installed on the nodes first and that restarts containerd under 204 running
// pods — maintenance, not a release step. The field flips afterwards with no
// rebuild, and that is the whole reason the runtime is a string.
//
// There is no os/exec in this file and there must never be. It creates
// Kubernetes objects and streams bytes to the apiserver.
package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/k8s"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// workdir is where a dev sandbox's project lives and where its commands run. One
// path, named once, because both the pod spec and the path confinement have to
// mean the same directory.
const workdir = "/work"

// execdir is the same thing for an `exec` sandbox, and it is a DIFFERENT path
// because the code-interpreter contract already named one: the tool description the
// model reads says to persist artifacts in /mnt/data. See workdirFor.
const execdir = "/mnt/data"

// container is the one container in a sandbox's pod. Named so the exec
// subresource addresses it explicitly — defaulting to "the first container" is
// how a sidecar someone adds later silently starts receiving the commands.
const container = "sandbox"

// serviceAccount is the identity a sandbox pod runs as. A constant and not an
// env var: which account exists in the sandbox namespace is decided by the same
// manifest that creates the namespace, so a second knob here could only ever
// disagree with it. It is declared in infra/k8s/sandboxes/registry.yaml.
const serviceAccount = "sandbox"

// Labels a sandbox's objects carry, so an operator can find a tenant's sandbox
// with kubectl and without reading a database.
const (
	labSandbox = "hanzo.ai/sandbox"
	labOrg     = "hanzo.ai/sandbox-org"
	labClass   = "hanzo.ai/sandbox-class"
)

// defaultTTL is the lease a class gets when the caller names none. Unbounded is
// not an option for a pod running submitted code on our nodes.
var defaultTTL = map[string]int{"exec": 900, "dev": 14400, "desktop": 14400}

// maxLiveExec is how many `exec` sandboxes ONE org may hold at once.
//
// An exec sandbox has no project, so the single-attach rule that bounds `dev` and
// `desktop` does not reach it, and nothing else did either. Sixteen is written in
// node capacity rather than chosen for roundness: 16 x 512Mi requested memory is
// 8Gi and 16 x 250m is 4 cores, which is one worker node's worth for a single
// tenant — enough that a busy conversation never meets it, small enough that a
// runaway caller cannot take a node with it.
const maxLiveExec = 16

// maxTTL caps what a caller may ask for. A longer-lived sandbox is a Deployment
// an operator declares, not a lease a request can extend to forever.
const maxTTL = 86400

// ExecResult is what running a command produced. A non-zero ExitCode is data,
// not an error: the call succeeded and the program failed.
type ExecResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

// streamer is the exec channel, behind an interface for exactly one reason: the
// real one opens an SPDY stream to the apiserver, and a test has no apiserver.
// It is NOT an abstraction over "ways to run a command" — there is one way.
type streamer interface {
	stream(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error
}

type runtime struct {
	ns           string
	image        string // oci.hanzo.ai/hanzoai/sandbox, without a tag
	tag          string
	runtimeClass string
	startTimeout time.Duration
	execTimeout  time.Duration
	dyn          dynamic.Interface
	str          streamer
	bound        Bound
	initErr      string
}

func newRuntime() *runtime {
	r := &runtime{
		// Machines do NOT live in `hanzo`. This is the namespace whose policy
		// denies them the cluster — ingress from nothing, egress a whitelist —
		// and a sandbox must not sit beside the datastores it is forbidden to
		// reach. One namespace, one policy, everything that runs submitted code.
		ns:           envOr("SANDBOX_NAMESPACE", "hanzo-sandboxes"),
		image:        envOr("SANDBOX_IMAGE_REPO", "oci.hanzo.ai/hanzoai/sandbox"),
		tag:          envOr("SANDBOX_IMAGE_TAG", ""),
		runtimeClass: strings.TrimSpace(os.Getenv("SANDBOX_RUNTIME_CLASS")),
		startTimeout: time.Duration(atoiOr(os.Getenv("SANDBOX_START_TIMEOUT_SEC"), 120)) * time.Second,
		execTimeout:  time.Duration(atoiOr(os.Getenv("SANDBOX_EXEC_TIMEOUT_SEC"), 900)) * time.Second,
	}
	// THE NAMESPACE IS CHECKED BEFORE ANYTHING ELSE IS BUILT. A sandbox namespace
	// whose name is a system namespace is not a misconfiguration to warn about — it
	// is a runtime that could delete DaemonSets, so it never gets a client at all and
	// every call through ready() fails closed with the reason. See bound.go.
	b, err := bindTo(r.ns)
	if err != nil {
		r.initErr = err.Error()
		return r
	}
	r.bound = b

	cfg, cerr := rest.InClusterConfig()
	if cerr != nil {
		// KUBECONFIG fallback for local development — identical to every other
		// subsystem that reaches the cluster. One way in, not two.
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		if cfg, cerr = cc.ClientConfig(); cerr != nil {
			r.initErr = fmt.Sprintf("no in-cluster config and no kubeconfig: %v", cerr)
			return r
		}
	}
	cfg.UserAgent = "hanzo-cloud/sandbox"
	dyn, derr := dynamic.NewForConfig(cfg)
	if derr != nil {
		r.initErr = fmt.Sprintf("dynamic client: %v", derr)
		return r
	}
	r.dyn, r.str = dyn, &spdy{cfg: cfg}
	return r
}

func (r *runtime) ready() error {
	if r == nil || r.dyn == nil {
		if r != nil && r.initErr != "" {
			return fmt.Errorf("%s", r.initErr)
		}
		return fmt.Errorf("kubernetes client not configured")
	}
	return nil
}

// imageFor is the tag chain: one image, three tags. The deployment pins the
// version; nothing here resolves `latest`, because an image decided by WHEN the
// pod started rather than by what was shipped is not a deployment.
func (r *runtime) imageFor(class string) string {
	if d := r.digestFor(class); d != "" {
		return r.image + "@" + d
	}
	tag := r.tag
	if tag == "" {
		tag = envOr("SANDBOX_IMAGE_TAG_"+strings.ToUpper(class), "")
	}
	if tag == "" {
		return r.image + ":" + class
	}
	// <version>-<class>, which is the order CI PUBLISHES. This read
	// `class + "-" + tag` and asked for `dev-2026.6.7` while the registry held
	// `2026.6.7-dev`, so the default path 404'd on an image that was sitting
	// right there. The publisher wins that argument: hanzoai/ci appends its
	// per-image `tag-suffix` to the version, so every image in the fleet is
	// <version>-<suffix> and a consumer that spells it the other way is simply
	// wrong.
	return r.image + ":" + tag + "-" + class
}

// DigestFor pins by CONTENT when the deployment names a digest, and it is the
// only pin that cannot move.
//
// A version tag looks immutable and is not: hanzoai/bot ships the sandbox image
// under bot's own package.json version, which was last bumped 2026-06-07, so a
// rebuild TODAY republished `2026.6.7-dev` from a commit two months newer. A tag
// that gets rewritten is not a pin, and one that LOOKS like a pin is worse than
// `latest`, which at least admits what it is.
//
// SANDBOX_IMAGE_DIGEST is therefore honoured ahead of any tag: `repo@sha256:…`
// names bytes, and bytes do not change under a running fleet.
// It is PER CLASS, because the three classes are three different images and one
// digest names one of them. A single SANDBOX_IMAGE_DIGEST would have quietly
// given every class the exec image — the same shape of bug as a tag that looks
// pinned and is not, which is what this function exists to end.
func (r *runtime) digestFor(class string) string {
	return strings.TrimSpace(os.Getenv("SANDBOX_IMAGE_DIGEST_" + strings.ToUpper(class)))
}

func (r *runtime) pods() dynamic.ResourceInterface {
	return r.dyn.Resource(k8s.Pods).Namespace(r.ns)
}

// start creates the sandbox's volume (if it has one) and its pod, and waits for
// the pod to be running. A create that returns before the sandbox can answer is
// a create that hands the caller a 502 on its very next call.
func (r *runtime) start(ctx context.Context, m Sandbox) error {
	if err := r.ready(); err != nil {
		return err
	}
	if m.Volume != "" {
		if err := r.ensureVolume(ctx, m); err != nil {
			return err
		}
	}
	if _, err := r.pods().Create(ctx, r.podSpec(m), metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create pod: %w", err)
		}
	}
	return r.waitRunning(ctx, m)
}

// ensureVolume creates the project disk if it is not already there. A volume
// OUTLIVES the sandbox that mounts it — it holds the checkout and the dependency
// caches, which is the whole reason a second session is cheap — so this creates
// and never deletes. Only an explicit purge does that.
func (r *runtime) ensureVolume(ctx context.Context, m Sandbox) error {
	vols := r.dyn.Resource(k8s.Volumes).Namespace(r.ns)
	if _, err := vols.Get(ctx, m.Volume, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get volume: %w", err)
	}
	pvc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name":      m.Volume,
			"namespace": r.ns,
			"labels":    map[string]any{labOrg: slug(m.Org)},
		},
		"spec": map[string]any{
			"accessModes": []any{"ReadWriteOnce"},
			"resources":   map[string]any{"requests": map[string]any{"storage": envOr("SANDBOX_VOLUME_SIZE", "20Gi")}},
		},
	}}
	if sc := envOr("SANDBOX_STORAGE_CLASS", ""); sc != "" {
		pvc.Object["spec"].(map[string]any)["storageClassName"] = sc
	}
	if _, err := vols.Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create volume: %w", err)
	}
	return nil
}

// podSpec is the sandbox, stated once.
func (r *runtime) podSpec(m Sandbox) *unstructured.Unstructured {
	c := map[string]any{
		"name":  container,
		"image": m.Image,
		// `sleep infinity` and nothing else. The pod is a place to run commands,
		// not a program — every lifetime, from a one-shot invoke to a week-long
		// session, is the same pod entered through the same channel.
		"command":    []any{"sleep", "infinity"},
		"workingDir": workdirFor(m.Class),
		// EPHEMERAL STORAGE IS REQUESTED AND LIMITED, both, and it is not
		// optional. A pod that requests less than it uses is permanently first in
		// line for node-pressure eviction, and node-pressure eviction ignores
		// PodDisruptionBudgets. An npm install writes ~30,000 files into the
		// container rootfs, which on our nodes shares one disk with every image
		// layer and every build; a handful of unbounded sandbox take the node
		// into DiskPressure and evict their own neighbours. Measured, not feared.
		"resources": map[string]any{
			"requests": map[string]any{
				"cpu":               envOr("MACHINE_CPU_REQUEST", "250m"),
				"memory":            envOr("MACHINE_MEM_REQUEST", "512Mi"),
				"ephemeral-storage": envOr("MACHINE_DISK_REQUEST", "2Gi"),
			},
			"limits": map[string]any{
				"cpu":               envOr("MACHINE_CPU_LIMIT", "2"),
				"memory":            envOr("MACHINE_MEM_LIMIT", "4Gi"),
				"ephemeral-storage": envOr("MACHINE_DISK_LIMIT", "8Gi"),
			},
		},
		"securityContext": map[string]any{
			"allowPrivilegeEscalation": false,
			"capabilities":             map[string]any{"drop": []any{"ALL"}},
		},
	}
	spec := map[string]any{
		// No token, ever. A sandbox runs somebody else's code; a projected
		// service-account token in its filesystem is an API credential handed to
		// that code. This is also why nothing else needs to strip credentials on
		// the way in — there are none to strip.
		// THE POD RUNS AS uid 1000, and fsGroup is why it can WRITE.
		//
		// The image ends `USER sandbox` (uid 1000). A mounted volume — the emptyDir
		// at the exec class's workdir, or a project PVC — arrives owned by root:root
		// mode 0755, so uid 1000 gets EPERM on the very first write and the sandbox
		// is a read-only box that looks healthy. A Dockerfile `chown` cannot fix it:
		// the mount happens after the image layer and shadows it.
		//
		// fsGroup makes the kubelet chown the volume to that GID and adds it as a
		// supplemental group, which is the only mechanism that reaches a volume.
		// runAsUser/runAsGroup are stated rather than inherited so the pod does not
		// depend on the image's USER line staying 1000 — the two must agree, and the
		// one that is checkable from outside the image should say so.
		//
		// This is also what the image's own comments already ASSUMED was here and
		// was not: "the pod runs runAsNonRoot with runAsUser 1000, so the uid is
		// fixed by the securityContext". It was not fixed by anything until now, and
		// the live proof missed it only because a stock node:22 runs as root.
		"securityContext": map[string]any{
			"runAsUser":    int64(1000),
			"runAsGroup":   int64(1000),
			"fsGroup":      int64(1000),
			"runAsNonRoot": true,
		},
		"automountServiceAccountToken": false,
		// The account is NAMED, and naming it is the fix for a real outage rather
		// than tidiness. Unnamed means `default`, and DOKS's registry integration
		// re-attaches its own DigitalOcean pull secrets to every namespace's
		// `default` account whenever it reconciles — so a sandbox inherited a
		// credential for a registry that is not ours and died asking
		// oci.hanzo.ai for its image ANONYMOUSLY, with a 401 that reads like a
		// bad password and was in fact no password at all. `sandbox` is ours, DOKS
		// does not manage it, and it carries exactly one thing: the pull secret.
		// See infra/k8s/sandboxes/registry.yaml. It grants nothing — it is bound to
		// no Role, and the line above still refuses it a token.
		"serviceAccountName": serviceAccount,
		// No service environment variables either. Kubernetes injects the address
		// of every Service in the namespace as env vars by default, which is a
		// free map of the neighbourhood for anything running inside.
		"enableServiceLinks": false,
		// A sandbox that dies is finished, not restarted. Its lease belongs to a
		// caller who is waiting on an answer, and a silent restart would hand that
		// caller a fresh empty sandbox wearing the same id.
		"restartPolicy": "Never",
		"containers":    []any{c},
	}
	// THE ISOLATION BOUNDARY, and the one field that picks it. OMITTED when empty
	// rather than sent as "", because those are different requests: an absent
	// runtimeClassName means the node's default runtime, while an empty string is
	// a named class that does not exist.
	//
	// That distinction is what makes the runtime a deployment decision instead of
	// a code change. gVisor cannot schedule until runsc is installed on the nodes,
	// and installing it restarts containerd on 8 worker nodes carrying 204 pods —
	// scheduled maintenance, not something a release does on its way past. So the
	// sandbox ships first under the default runtime and MACHINE_RUNTIME_CLASS is
	// set to `gvisor` afterwards, with no rebuild. The same field takes `kata-fc`
	// or `kata-clh` the day either is installed, which is the whole reason the
	// runtime is a string here and not a fork in the code.
	//
	// NO nodeSelector and no tolerations, on purpose. Where a sandboxed pod may
	// land is the RuntimeClass's business — `scheduling.nodeSelector` and
	// `scheduling.tolerations` on the RuntimeClass are merged into every pod that
	// names it — so stating it again here would be a second copy that goes stale
	// the day the runsc node pool moves, and a wrong copy pins pods Pending
	// forever with a message that blames the wrong object.
	if r.runtimeClass != "" {
		spec["runtimeClassName"] = r.runtimeClass
	}
	if m.Volume != "" {
		c["volumeMounts"] = []any{map[string]any{"name": "project", "mountPath": workdirFor(m.Class)}}
		spec["volumes"] = []any{map[string]any{
			"name":                  "project",
			"persistentVolumeClaim": map[string]any{"claimName": m.Volume},
		}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      m.Pod,
			"namespace": r.ns,
			"labels": map[string]any{
				labSandbox: m.ID,
				labOrg:     slug(m.Org),
				labClass:   m.Class,
			},
		},
		"spec": spec,
	}}
}

// waitRunning polls until the pod is running or the deadline passes. Polling and
// not a Watch: this is one object for a bounded wait, and a watch would be a
// long-lived connection per in-flight create for no better answer.
func (r *runtime) waitRunning(ctx context.Context, m Sandbox) error {
	deadline := time.Now().Add(r.startTimeout)
	for {
		obj, err := r.pods().Get(ctx, m.Pod, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("get pod: %w", err)
		}
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		switch phase {
		case "Running":
			return nil
		case "Failed", "Succeeded":
			return fmt.Errorf("pod %s is %s: %s", m.Pod, phase, podMessage(obj))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pod %s not running after %s: %s", m.Pod, r.startTimeout, podMessage(obj))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// podMessage lifts whatever the cluster said about why a pod is not running —
// an image pull failure, a missing RuntimeClass, an unschedulable node
// selector. Without it the caller gets a timeout and no reason, which is the
// single most common way a sandbox outage becomes a two-hour investigation.
func podMessage(obj *unstructured.Unstructured) string {
	if msg, ok, _ := unstructured.NestedString(obj.Object, "status", "message"); ok && msg != "" {
		return msg
	}
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conds {
		cond, _ := raw.(map[string]any)
		if cond["status"] == "False" {
			if msg, _ := cond["message"].(string); msg != "" {
				return msg
			}
		}
	}
	statuses, _, _ := unstructured.NestedSlice(obj.Object, "status", "containerStatuses")
	for _, raw := range statuses {
		st, _ := raw.(map[string]any)
		wait, _ := st["state"].(map[string]any)["waiting"].(map[string]any)
		if msg, _ := wait["message"].(string); msg != "" {
			return msg
		}
		if reason, _ := wait["reason"].(string); reason != "" {
			return reason
		}
	}
	return "no reason reported"
}

// stop ends the lease. The pod is DELETED, never returned to anything: a pod
// that ran submitted code is not handed to the next tenant, and there is no pool
// for it to be handed back to.
func (r *runtime) stop(ctx context.Context, m Sandbox) error {
	if err := r.ready(); err != nil {
		return err
	}
	// READ, CHECK, THEN DELETE — three steps where one used to do, and the two extra
	// are the blast radius. The row says which pod this is; the OBJECT says whether
	// it is one of ours. A name alone is a claim, and a delete that acts on a claim
	// is how a sweep removes something that merely shares a name.
	obj, err := r.pods().Get(ctx, m.Pod, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get pod: %w", err)
	}
	if !r.bound.covers(obj) {
		return fmt.Errorf("refusing to delete %s/%s: it does not carry %s, so it is not a sandbox",
			obj.GetNamespace(), obj.GetName(), labSandbox)
	}
	err = r.pods().Delete(ctx, m.Pod, precondition(obj))
	if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
		// Conflict means the UID moved between the read and the delete: the pod we
		// inspected is already gone and a different object holds the name. Nothing to
		// do, and emphatically nothing to retry without the precondition.
		return nil
	}
	return err
}

// purge deletes the project VOLUME. Separate from stop, and opt-in, because the
// volume holds the only copy of the checkout and the caches.
func (r *runtime) purge(ctx context.Context, m Sandbox) error {
	if err := r.ready(); err != nil {
		return err
	}
	if m.Volume == "" {
		return nil
	}
	err := r.dyn.Resource(k8s.Volumes).Namespace(r.ns).Delete(ctx, m.Volume, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// exec runs argv in the sandbox and collects what it produced.
//
// The sandbox is addressed by POD NAME through the apiserver. There is no
// address to go stale, no shared key to present and no way for this call to
// arrive at a pod belonging to somebody else — a name is minted once per sandbox
// and never reused, so the recycled-address break the predecessor defended
// against with a header cannot be spelled here.
func (r *runtime) exec(ctx context.Context, m Sandbox, argv []string, stdin io.Reader, timeoutSec int) (ExecResult, error) {
	if err := r.ready(); err != nil {
		return ExecResult{}, err
	}
	if m.Status != "running" || m.Pod == "" {
		return ExecResult{}, fmt.Errorf("sandbox is %s", firstNonEmpty(m.Status, "unknown"))
	}
	d := r.execTimeout
	if timeoutSec > 0 && time.Duration(timeoutSec)*time.Second < d {
		d = time.Duration(timeoutSec) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	var out, errb capped
	err := r.str.stream(ctx, r.ns, m.Pod, argv, stdin, &out, &errb)
	res := ExecResult{Stdout: out.String(), Stderr: errb.String()}
	if err == nil {
		return res, nil
	}
	// A non-zero exit is the PROGRAM's answer, not a transport failure, so it
	// comes back as data with a 0-error. Everything else is a real failure of the
	// channel and is reported as one.
	var code utilexec.CodeExitError
	if ok := asCodeExit(err, &code); ok {
		res.ExitCode = code.Code
		return res, nil
	}
	return res, err
}

func asCodeExit(err error, out *utilexec.CodeExitError) bool {
	for err != nil {
		if c, ok := err.(utilexec.CodeExitError); ok {
			*out = c
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// capped is an output buffer with a ceiling. A sandbox's whole job is running
// something that might print forever, and an unbounded buffer on the cloud side
// turns that into our memory problem.
type capped struct {
	b   strings.Builder
	cut bool
}

const maxOutput = 1 << 20 // 1 MiB per stream

func (w *capped) Write(p []byte) (int, error) {
	if room := maxOutput - w.b.Len(); room > 0 {
		if len(p) > room {
			p, w.cut = p[:room], true
		}
		w.b.Write(p)
	} else {
		w.cut = true
	}
	return len(p), nil // the writer consumed it; a short write would abort the stream
}

func (w *capped) String() string {
	if w.cut {
		return w.b.String() + "\n[truncated at 1MiB]"
	}
	return w.b.String()
}

// spdy is the real channel: the Kubernetes exec subresource.
type spdy struct{ cfg *rest.Config }

func (s *spdy) stream(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cl, err := rest.RESTClientFor(coreConfig(s.cfg))
	if err != nil {
		return err
	}
	req := cl.Post().Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   argv,
			Stdin:     stdin != nil,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)
	ex, err := remotecommand.NewSPDYExecutor(s.cfg, "POST", req.URL())
	if err != nil {
		return err
	}
	return ex.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
	})
}

// coreConfig points a REST client at the core/v1 group. A copy, because the
// shared config is also the one the SPDY dialer reads.
func coreConfig(in *rest.Config) *rest.Config {
	out := rest.CopyConfig(in)
	out.GroupVersion = &corev1.SchemeGroupVersion
	out.APIPath = "/api"
	out.NegotiatedSerializer = scheme.Codecs.WithoutConversion()
	return out
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}
