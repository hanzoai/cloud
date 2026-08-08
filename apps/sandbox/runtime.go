// runtime.go — the Kubernetes half: what a sandbox IS in the cluster, and the
// one channel into it.
//
// A sandbox is a Pod, and its isolation boundary is the RUNTIME that pod names
// — one field, whose value comes from `runtimes` and nowhere else. It is the
// ONLY boundary claimed here: no uid juggling, no process groups, no daemon
// inside the pod deciding what it is allowed to run. Everything the predecessor
// built to approximate that boundary in Go is deleted rather than kept "for
// defence in depth", because a second half-boundary is a second thing to keep
// true.
//
// WHICH boundary is a derivation and not a setting — see runtimeFor, which is
// the one place that decides it, over two facts a sandbox already states about
// itself: whose code it runs, and whether it keeps anything.
// `SANDBOX_RUNTIME_CLASS` states the deployment's preference among them and is
// checked against the table at startup.
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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud/apps/k8s"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
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
	labProject = "hanzo.ai/sandbox-project"
)

// annLeased is the DATE a disk was last leased, UTC, on the disk itself.
//
// A disk outlives every object that could account for it — the pod by an hour,
// the row by the lease — so "is this still wanted?" has no other place to be
// asked. Without it the answer has to come from a hash: the name ends in
// sha256(org, project), which identifies a disk only to whoever already knows
// the project that made it, and that is nobody a month later.
//
// A DATE and not a timestamp, so the write is idempotent within a day: a disk
// leased forty times before lunch is patched once. The question it answers is
// counted in days, so the precision that would cost a write per lease would buy
// nothing.
const annLeased = "hanzo.ai/sandbox-leased"

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
// It is NOT an abstraction over "ways to run a command" — there is one way, and
// its two shapes are the two things a caller can want from it: collect what a
// command produced, or hand a person a terminal.
type streamer interface {
	stream(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error
	tty(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout io.Writer, size remotecommand.TerminalSizeQueue) error
}

type runtime struct {
	ns           string
	image        string // oci.hanzo.ai/hanzoai/sandbox, without a tag
	tag          string
	runtimeClass string
	// bare is the boundary our OWN code takes — the one with no kernel of its
	// own — and it is empty until the cluster keeps that boundary to nodes of its
	// own. Resolved once, against the cluster, because containment is the
	// cluster's fact and a setting that claimed it would be the one thing here
	// worth lying about. See confine.
	bare         string
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

	// SANDBOX_RUNTIME_CLASS IS CHECKED AGAINST THE TABLE, AT STARTUP.
	//
	// It was read straight through to the pod spec, so a value the table had
	// never heard of arrived at the apiserver as a runtimeClassName and the
	// sandbox sat Pending with nothing said — the same silence the caller-facing
	// refusal exists to end, one level up and with nobody watching for it. Worse,
	// it only did that for a volumeless sandbox: the old derivation short-
	// circuited on `m.Volume == ""` and never consulted the table at all, so the
	// one path that skipped validation was also the common one.
	//
	// A deployment's runtime is decided once, so it is checked once, here, where
	// the reason has somewhere to go. Everything downstream may then take a
	// non-empty r.runtimeClass as a name we run.
	if _, ok := runtimes[r.runtimeClass]; r.runtimeClass != "" && !ok {
		r.initErr = fmt.Sprintf("SANDBOX_RUNTIME_CLASS=%q is not one we run (%s)",
			r.runtimeClass, runtimeNames())
		return r
	}

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
	// Asked once, at startup, and only ever able to REMOVE a boundary from what
	// this deployment offers — so a cluster that cannot answer costs a little
	// speed and never an outage.
	if n := bare(); r.confine(context.Background(), n) {
		r.bare = n
	}
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

// imageFor is the tag chain: one image, four tags. The deployment pins the
// version; nothing here resolves `latest`, because an image decided by WHEN the
// pod started rather than by what was shipped is not a deployment.
//
// `super` IS THE ONE PLACE AN IDENTITY REACHES THE IMAGE, and it reaches it for
// exactly one class. A SuperAdmin's `dev` sandbox runs the `admin` image — dev
// plus zsh, kubectl and doctl (hanzoai/bot Dockerfile.box) — and every other
// caller's `dev` sandbox runs `dev`, byte for byte what it ran before.
//
// IT IS A SUBSTITUTION AND NOT A FOURTH CLASS. A class is a word in the request:
// `classes` is the closed set a caller may ask for, and it stays three. Which
// bytes a caller is handed is a fact about the CALLER, so it is answered where
// the caller is known and never offered as a field. The row still says class
// `dev`, the pod still carries the `dev` label, and only Image differs — which
// is the honest record of what happened.
//
// ONLY `dev`, because `admin` is BUILT from dev and a substitute has to be a
// superset of what it replaces or the swap quietly changes what the class means.
// `exec` stays the throwaway a tool call spends fifteen minutes in — swapping it
// would put a bigger image behind every function invocation this identity makes
// — and `desktop` keeps the screen `admin` has no X server for.
//
// THE ADMIN IMAGE CARRIES NO CREDENTIAL, which is what keeps this one line
// rather than a gate. kubectl with no kubeconfig and doctl with no token are
// argument parsers; they reach nothing until a POD is handed something, and what
// a pod is handed is decided by the identity that leased it, not by the bytes it
// booted. So there is nothing here to defend against a caller who names the
// admin image by hand — which checkImage already permits for every platform
// image, for precisely this reason.
//
// THAT INVARIANT IS NOW LOAD-BEARING RATHER THAN ANTICIPATED. The kubeconfig and
// the DigitalOcean token exist, and they arrive at the POD from the identity that
// leased it: the env of one pod, and one file written through the exec channel,
// both built by cred.go on the SAME predicate this line reads. So naming the
// admin image by hand still gets a caller exactly what it always did — kubectl
// and doctl with nothing to reach — because nothing a caller can spell makes them
// a SuperAdmin, and the credential was never in the layer to begin with.
func (r *runtime) imageFor(class string, super bool) string {
	if admin(class, super) {
		class = "admin"
	}
	if d := r.digestFor(class); d != "" {
		return r.image + "@" + d
	}
	tag := r.tag
	if tag == "" {
		tag = envOr("SANDBOX_IMAGE_TAG_"+strings.ToUpper(class), "")
	}
	if tag == "" {
		// FALL BACK TO A NAME NOTHING PUBLISHES, on purpose.
		//
		// The bare `exec`/`dev`/`desktop` tags exist in the registry and all
		// three point at stock node:22 — no toolchain, no agent, and running as
		// ROOT. So this fallback, which fires on one empty env var, quietly
		// swapped a hardened sandbox for a root shell. It looked fine: the pod
		// runs, the API answers, and the only tell is a `whoami`.
		//
		// `-unset` is published by nothing, so an unset tag now fails at the
		// image pull with a name that says why, instead of succeeding into the
		// wrong image. A loud stop beats a silent downgrade.
		return r.image + ":" + class + "-unset"
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
// It is PER IMAGE — SANDBOX_IMAGE_DIGEST_EXEC, _DEV, _DESKTOP, _ADMIN — because
// those are four different images and one digest names one of them. A single
// SANDBOX_IMAGE_DIGEST would have quietly given every class the exec image: the
// same shape of bug as a tag that looks pinned and is not, which is what this
// function exists to end.
func (r *runtime) digestFor(class string) string {
	return strings.TrimSpace(os.Getenv("SANDBOX_IMAGE_DIGEST_" + strings.ToUpper(class)))
}

// runtimes are the isolation boundaries we run, and for each the TWO facts that
// decide which sandboxes may take it.
//
//	kernel — the sandbox gets a kernel of its own, so a process that breaks out
//	         of its container has not broken out onto the node.
//	shares — the sandbox can back a PERSISTENT VOLUME.
//
// Firecracker has no virtio-fs. Read it on the node rather than take it on
// faith: `configuration-fc.toml` carries no `shared_fs` key at all, where
// `configuration-clh.toml:130` sets `shared_fs = "virtio-fs"`. A kata-fc guest
// therefore cannot mount a directory from the host — and Kubernetes does not
// fail that mount. The sandbox gets a ~599M tmpfs standing exactly where the
// volume should be, every write SUCCEEDS into it, and the VM takes the bytes
// with it when it exits. A `dev` sandbox would look perfect and lose the
// checkout.
//
// runc has no kernel of its own because it IS the node's kernel, which is both
// why it is the fastest thing here and why only code of ours may take it. See
// runtimeFor for who that is, and confine for the topology that has to hold
// before it is offered at all.
//
// That is why this is a table and not a comment: neither failure has a symptom
// at the point it happens — a lost volume looks like a successful write, and a
// shared kernel looks like a fast sandbox — so both are refused here, where the
// facts are written down once.
var runtimes = map[string]struct{ kernel, shares bool }{
	"runc":     {shares: true},
	"gvisor":   {kernel: true, shares: true},
	"kata-clh": {kernel: true, shares: true},
	"kata-fc":  {kernel: true},
}

// shared is the boundary that meets BOTH facts at once, and so the answer
// whenever the one a deployment stated does not. gVisor isolates and shares a
// filesystem, and it is already what the fleet runs, so this names the existing
// behaviour rather than adding one.
const shared = "gvisor"

// fits reports whether this deployment may put a sandbox on a boundary — the
// ONE predicate, asked by all three paths into runtimeFor.
//
// It is one and not three because it was two, briefly, and the third path was
// the bug: the derivation consulted the topology, the by-name request did not,
// and the deployment's own setting did not either — so `SANDBOX_RUNTIME_CLASS=runc`
// put our sandboxes on the node's kernel wherever the scheduler felt like,
// which is the entire thing confine exists to prevent. A fact that only some
// callers consult is a fact that has already drifted.
//
// An unknown name fits nothing, which is what makes an answer fall to `shared`
// rather than to a string the apiserver has never heard of.
func (r *runtime) fits(name string, kernel, shares bool) bool {
	b, ok := runtimes[name]
	if !ok || (shares && !b.shares) {
		return false
	}
	// A boundary with a kernel of its own suits anybody. The one without suits
	// only code of ours, and only where the cluster keeps it to nodes of its own.
	return b.kernel || (!kernel && name == r.bare)
}

// bare names the boundary with NO KERNEL OF ITS OWN — a container on the node's
// kernel, which is the fastest thing we run and the only one reserved to code of
// ours. Derived from the table rather than written down a second time, so adding
// or removing a boundary stays a table entry.
func bare() string {
	for _, name := range sorted() {
		if !runtimes[name].kernel {
			return name
		}
	}
	return ""
}

// runtimeFor is the ONE place that decides a sandbox's isolation boundary. Not
// a fork in the code and not a knob per class — ONE LOOKUP over two facts the
// sandbox already states about itself:
//
//	WHO OWNS THE CODE decides how much isolation is needed. Ours runs on our own
//	kernel; everybody else's gets a kernel of its own.
//	WHETHER IT KEEPS ANYTHING decides which boundaries can serve it. A project
//	volume needs a boundary that can share a filesystem.
//
// NEITHER FACT CAN BE HANDED IN, and that is the whole security of it. The org
// is the caller's identity — api.go takes it from principal.Org, which refuses
// an org that arrived without a validated principal, and no body field reaches
// it — while the volume was set two lines earlier from the project. A caller
// that could name its own org could name its own kernel.
//
// Keying on m.Volume and not on m.Class is the same point made about the other
// fact. CLASS DOES NOT DECIDE THIS — `project` does (api.go). `dev` and
// `desktop` are refused without one so they always carry a volume, but `exec`
// is optional: an exec sandbox NAMING A PROJECT gets a volume too. A table of
// class→runtime would have read `exec → the fast one` and silently thrown that
// org's project away. The volume is the thing that matters, so the volume is
// what is asked.
//
// The two callers are answered differently ON PURPOSE:
//
//   - The DEPLOYMENT states a preference, so it is derived down. A fleet set to
//     the fast runtime still has to run dev sandboxes, and refusing them would
//     make the setting unusable.
//   - A CALLER states a request, so a contradiction is refused rather than
//     corrected. Handing back a runtime nobody asked for is the same silence
//     this table exists to end, one level up.
func (r *runtime) runtimeFor(m Sandbox, want string) (string, error) {
	// THE TWO FACTS. authz.AdminOrg is the reserved platform org — the issuer's
	// own constant, the same predicate admin-guard and the audit trail read, so
	// there is no second notion of "ours" here to drift from IAM's.
	kernel, shares := m.Org != authz.AdminOrg, m.Volume != ""

	if want = strings.TrimSpace(want); want != "" {
		b, known := runtimes[want]
		switch {
		case !known:
			// A runtimeClassName the cluster has never heard of is a pod that
			// waits Pending with no explanation, so a typo stops here instead.
			return "", fmt.Errorf("runtime %q is not one we run (%s)", want, runtimeNames())
		case kernel && !b.kernel:
			return "", fmt.Errorf(
				"runtime %q is the node's own kernel, which is a boundary only for code of ours — "+
					"ask for %s", want, shared)
		case !b.kernel && want != r.bare:
			// EVEN FOR US. Asking by name must go through the same topology the
			// derivation does, or the one caller allowed to name runc is the one
			// caller who can put it on a pool it does not own — and on a cluster
			// with no such class at all, on a pod that waits Pending forever.
			return "", fmt.Errorf(
				"runtime %q is the node's own kernel and this cluster does not keep it to a pool "+
					"of its own, so nothing may take it — ask for %s", want, shared)
		case shares && !b.shares:
			return "", fmt.Errorf(
				"runtime %q has no shared filesystem, so it cannot mount project volume %q — "+
					"the write would succeed into a tmpfs and be lost when the sandbox ends; "+
					"ask for %s, or drop the project for a sandbox that keeps nothing",
				want, m.Volume, shared)
		}
		return want, nil
	}
	// OUR OWN CODE takes the boundary the cluster keeps to itself. There is no
	// test for who the caller is here, and there does not need to be: the
	// boundary has no kernel of its own, so it fits nothing that needs one and
	// the table refuses it to everybody else. r.bare is empty until the topology
	// holds (confine), and an empty name fits nothing at all.
	if r.fits(r.bare, kernel, shares) {
		return r.bare, nil
	}
	// Empty stays empty: that is the node's default runtime, which is a
	// different request from any named class, and a cluster with no gVisor
	// installed must not be handed one. Every named value has been through the
	// table at startup, so what reaches a pod spec here is a name we run.
	if r.runtimeClass == "" || r.fits(r.runtimeClass, kernel, shares) {
		return r.runtimeClass, nil
	}
	return shared, nil
}

// sorted is the closed set, in one order. A Go map range would give a different
// one every time, and both an error message and a derived choice that change on
// their own are ones nobody can grep for or reproduce.
func sorted() []string {
	n := make([]string, 0, len(runtimes))
	for k := range runtimes {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func runtimeNames() string { return strings.Join(sorted(), ", ") }

// confine asks the CLUSTER whether it keeps a boundary to NODES OF ITS OWN, and
// it is what stands between "runc is the fastest thing we run" and "somebody's
// model output is on the node's kernel next to another tenant".
//
// Our own agent is not our own binary. Its commands are written by a model that
// just read a repository, a web page or a user's prompt, so the realistic threat
// is that somebody else wrote them — and the node's kernel is one bug away. That
// is survivable only if the blast radius is drawn by TOPOLOGY rather than by the
// runtime: nothing else on the pool, and nothing on the pool worth taking.
//
// A RuntimeClass already draws it. `scheduling.nodeSelector` and
// `scheduling.tolerations` are merged into every pod that names the class, so
// the selector says which pool our sandboxes go to and the toleration says that
// pool is tainted against everything else. BOTH are required, because either
// alone is half a pool: a selector with no taint puts our sandboxes on nodes
// anything may join, and a taint with no selector leaves them free to land
// anywhere. And the pool must be the boundary's OWN — sharing one with a
// boundary other tenants take would put a kernel-sharing sandbox on the same
// node as their gVisor ones, which is the radius we just went to the trouble of
// drawing.
//
// It is READ and never configured. A boundary that merely says it is contained
// is not, and this is precisely the fact an attacker would like taken on faith.
// Every no-answer is a NO — no such class, no client, no permission — so the
// boundary is simply not offered and our code takes the same one as everybody
// else's. That is the failure this can afford to have.
func (r *runtime) confine(ctx context.Context, name string) bool {
	if r.dyn == nil || name == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	all, err := r.dyn.Resource(k8s.RuntimeClasses).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false
	}
	pool := map[string]any{}
	for _, rc := range all.Items {
		if rc.GetName() != name {
			continue
		}
		sel, _, _ := unstructured.NestedMap(rc.Object, "scheduling", "nodeSelector")
		tol, _, _ := unstructured.NestedSlice(rc.Object, "scheduling", "tolerations")
		if len(sel) == 0 || len(tol) == 0 {
			return false
		}
		pool = sel
	}
	if len(pool) == 0 {
		return false
	}
	for _, rc := range all.Items {
		sel, _, _ := unstructured.NestedMap(rc.Object, "scheduling", "nodeSelector")
		if rc.GetName() != name && samePool(sel, pool) {
			return false
		}
	}
	return true
}

// samePool reports whether two node selectors choose the same nodes. An empty
// selector chooses every node, so it is never "the same pool" as one that
// chooses some — it is a superset, and the caller has already refused it.
func samePool(a, b map[string]any) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (r *runtime) pods() dynamic.ResourceInterface {
	return r.dyn.Resource(k8s.Pods).Namespace(r.ns)
}

// start creates the sandbox's volume (if it has one) and its pod, and waits for
// the pod to be running. A create that returns before the sandbox can answer is
// a create that hands the caller a 502 on its very next call.
func (r *runtime) start(ctx context.Context, m Sandbox, cr cred) error {
	if err := r.ready(); err != nil {
		return err
	}
	if m.Volume != "" {
		if err := r.ensureVolume(ctx, m); err != nil {
			return err
		}
	}
	if _, err := r.pods().Create(ctx, r.podSpec(m, cr), metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create pod: %w", err)
		}
	}
	if err := r.waitRunning(ctx, m); err != nil {
		return err
	}
	// THE KUBECONFIG ARRIVES LAST AND THROUGH THE EXEC CHANNEL, so it exists in
	// the pod and in no Kubernetes object — see cred.go for why the bigger
	// credential takes this route and the DO token does not. It is empty for every
	// lease but a SuperAdmin's own, so this is one comparison for everybody else.
	//
	// A failure here FAILS THE LEASE. The caller gets the 503 and the row records
	// why, which is the same shape a failed pod create already has; the
	// alternative is a shell whose kubectl reaches nothing and only says so the
	// first time somebody trusts it.
	if len(cr.kube) == 0 {
		return nil
	}
	res, err := r.put(ctx, m, kubePath, cr.kube)
	if err != nil {
		return fmt.Errorf("write kubeconfig: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("write kubeconfig: %s", strings.TrimSpace(res.Stderr))
	}
	return nil
}

// put writes one file into the pod. It is the mechanism UNDER the fs write verb —
// `cat >` over the exec subresource, which is how `kubectl cp` has always worked —
// stated once at the level where the path may be ours rather than a caller's.
//
// umask 077 because one of its two callers writes a credential and the other
// cannot tell the difference: every process in a sandbox is uid 1000, so 0600 and
// 0644 are indistinguishable from inside one, and only the stricter of the two is
// also right for a kubeconfig.
//
// It answers the RESULT and not just an error, because its callers need different
// halves of it: a transport failure is a bad gateway, a non-zero exit is a bad
// path, and collapsing them would make the fs API blame the wrong side.
func (r *runtime) put(ctx context.Context, m Sandbox, path string, data []byte) (ExecResult, error) {
	q := shellQuote(path)
	return r.exec(ctx, m, []string{"sh", "-c",
		"umask 077 && mkdir -p -- \"$(dirname -- " + q + ")\" && cat > " + q},
		bytes.NewReader(data), 0, nil)
}

// ensureVolume creates the project disk if it is not already there. A volume
// OUTLIVES the sandbox that mounts it — it holds the checkout and the dependency
// caches, which is the whole reason a second session is cheap — so this creates
// and never deletes. Only an explicit purge does that.
//
// Because it outlives everything else, it is also the only place its own
// accounting can live, so every lease stamps the disk with the day it was
// wanted (annLeased) and every disk is born saying which project it belongs to.
// Nothing here reclaims anything — this writes the facts a reclaim would need,
// which is the part that was missing: a disk whose project is a hash and whose
// last use is unrecorded cannot be shown to be dead, so it is kept forever by
// default. Measured 2026-08-07: 15 disks, 300GiB, every one of them in exactly
// that state.
func (r *runtime) ensureVolume(ctx context.Context, m Sandbox) error {
	vols := r.dyn.Resource(k8s.Volumes).Namespace(r.ns)
	today := time.Now().UTC().Format(time.DateOnly)
	if have, err := vols.Get(ctx, m.Volume, metav1.GetOptions{}); err == nil {
		if have.GetAnnotations()[annLeased] == today {
			return nil
		}
		// A merge patch of the one key, so dating the disk cannot clobber a
		// concurrent write of anything else on it. The error is dropped on purpose:
		// a tenant's sandbox does not owe its existence to our bookkeeping, and a
		// lease that failed because a disk could not be dated would be the rare
		// outage caused entirely by the thing meant to save money.
		_, _ = vols.Patch(ctx, m.Volume, types.MergePatchType,
			[]byte(`{"metadata":{"annotations":{"`+annLeased+`":"`+today+`"}}}`), metav1.PatchOptions{})
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get volume: %w", err)
	}
	pvc := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "PersistentVolumeClaim",
		"metadata": map[string]any{
			"name":        m.Volume,
			"namespace":   r.ns,
			"labels":      map[string]any{labOrg: slug(m.Org), labProject: slug(m.Project)},
			"annotations": map[string]any{annLeased: today},
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
func (r *runtime) podSpec(m Sandbox, cr cred) *unstructured.Unstructured {
	c := map[string]any{
		"name":       container,
		"image":      m.Image,
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
	// THE ONLY THING AN IDENTITY EVER PUTS IN A POD, and it is empty for every
	// lease but one. cr.env is non-empty only on the `admin` branch — a
	// SuperAdmin's own dev sandbox, cred.go — so this key is ABSENT, not empty,
	// from every other sandbox's spec. Absent is the point: "a sandbox is handed
	// nothing" stays a readable fact about the OBJECT rather than a claim about
	// the code that built it.
	//
	// Sorted, because a Go map ranges in random order and a pod spec that differs
	// run to run is one nothing can diff.
	if len(cr.env) > 0 {
		names := make([]string, 0, len(cr.env))
		for k := range cr.env {
			names = append(names, k)
		}
		sort.Strings(names)
		env := make([]any, 0, len(names))
		for _, k := range names {
			env = append(env, map[string]any{"name": k, "value": cr.env[k]})
		}
		c["env"] = env
	}
	// `sleep infinity` and nothing else — the pod is a place to run commands, not
	// a program. Every lifetime, from a one-shot invoke to a week-long session, is
	// the same pod entered through the same channel.
	//
	// EXCEPT A DESKTOP, WHOSE SCREEN IS ITS PROCESS. The desktop image's CMD
	// starts Xvfb, a window manager and the VNC/noVNC pair and only then becomes
	// the same `sleep infinity`; stating a command here replaced that script
	// outright, so the class that exists to have a display came up with no X
	// server at all — byte-identical to `dev` but for a label, and silent about
	// it, because a pod that sleeps looks perfectly healthy.
	//
	// Deferring to the image is not a second way to start a sandbox. Work still
	// arrives only through the exec subresource, for all three classes; the
	// desktop simply also has something of its own to run first.
	if m.Class != "desktop" {
		c["command"] = []any{"sleep", "infinity"}
	} else {
		// Declared so the screen is addressable by name rather than by a number
		// somebody has to look up. Ports are how a reader learns a desktop has a
		// display; they do not open anything the entrypoint has not bound, and it
		// binds loopback.
		c["ports"] = []any{
			map[string]any{"name": "vnc", "containerPort": int64(5900)},
			map[string]any{"name": "novnc", "containerPort": int64(6080)},
		}
	}
	spec := map[string]any{
		// No token, ever. A sandbox runs somebody else's code; a projected
		// service-account token in its filesystem is an API credential handed to
		// that code. This is also why nothing else needs to strip credentials on
		// the way in — there are none to strip.
		//
		// THIS STAYS TRUE NOW THAT ONE POD IS HANDED SOMETHING. What this line
		// refuses is a token minted by KUBERNETES, for THIS namespace's service
		// account, which the cluster would honour and which every sandbox would
		// carry — an ambient credential nobody chose. A SuperAdmin's own shell
		// carries that SuperAdmin's own DigitalOcean credentials (cred.go): a
		// credential the person already holds, in a pod running only what they
		// type, chosen by their identity at the moment they leased it. Different
		// thing, and this one is still refused for every pod including that one.
		//
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
	// PER SANDBOX, and read from the sandbox rather than passed beside it. It was
	// deployment-wide, which meant the same task could not be run on two runtimes
	// and compared without a rollout — and a caller could not choose.
	//
	// m.Runtime is what runtimeFor ANSWERED, resolved once in Lease. There is no
	// second fallback here on purpose: a `rc == "" then use r.runtimeClass` line
	// stood here, and it is precisely how the row and the pod come to disagree —
	// the row would say "the node default" while the pod ran gvisor, and the
	// person comparing two runtimes would be reading the wrong label on the right
	// experiment. One derivation, one place, and the field the row reports is the
	// same field the pod is built from.
	if m.Runtime != "" {
		spec["runtimeClassName"] = m.Runtime
	}
	// THE WORKDIR IS MOUNTED EITHER WAY, and until now only one of the two ways
	// existed. A `dev` sandbox gets its project PVC at /work; an `exec` sandbox has
	// no project and got NOTHING, so /mnt/data was whatever the image shipped —
	// root:root 0755 — against a pod this file pins to runAsUser 1000. The
	// interpreter's own contract tells the model to "persist handoff artifacts in
	// /mnt/data", and every such write failed with EACCES: the one directory the
	// tool exists to fill was the one directory it could not write.
	//
	// It survived because nothing checks. A run that prints its answer looks
	// perfectly successful; only a run that saves a plot notices, and it notices as
	// a traceback the model apologises for rather than as an error anyone sees.
	// (Measured 2026-08-06 in a live exec sandbox: `mkdir /mnt/data/.x` →
	// Permission denied, `id` → uid=1000(sandbox).)
	//
	// emptyDir, not a PVC: a code-interpreter session is exactly as long-lived as
	// its pod, which is what emptyDir already means. And it is what makes the mount
	// WRITABLE — the kubelet chowns an emptyDir to the pod's fsGroup, which
	// securityContext above already sets to 1000, so the fix is the mount itself
	// rather than a chown in the image.
	switch {
	case m.Volume != "":
		c["volumeMounts"] = []any{map[string]any{"name": "project", "mountPath": workdirFor(m.Class)}}
		spec["volumes"] = []any{map[string]any{
			"name":                  "project",
			"persistentVolumeClaim": map[string]any{"claimName": m.Volume},
		}}
	default:
		c["volumeMounts"] = []any{map[string]any{"name": "work", "mountPath": workdirFor(m.Class)}}
		spec["volumes"] = []any{map[string]any{
			"name": "work",
			// Bounded like everything else the pod can fill. The container's own
			// ephemeral-storage limit does NOT cover an emptyDir's usage on every
			// runtime, so the volume states its own ceiling and the kubelet evicts
			// the pod that exceeds it — which is the sandbox's problem to have,
			// not the node's.
			"emptyDir": map[string]any{"sizeLimit": envOr("SANDBOX_WORKDIR_SIZE", "2Gi")},
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
// say, when a caller named a session, is handed THE SAME BYTES on their way to
// the buffers, so a watcher reads the program's output while it is still being
// written. It is a pass-through and never a second read: a tap that re-ran the
// command to observe it would be observing a different command.
func (r *runtime) exec(ctx context.Context, m Sandbox, argv []string, stdin io.Reader, timeoutSec int, say *tell) (ExecResult, error) {
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
	so, se := io.Writer(&out), io.Writer(&errb)
	if say != nil {
		// BOTH streams, through one tell. A failing command says why on stderr, and
		// a watcher that only saw stdout would watch the silence.
		so, se = io.MultiWriter(&out, say), io.MultiWriter(&errb, say)
	}
	err := r.str.stream(ctx, r.ns, m.Pod, argv, stdin, so, se)
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

// tty runs argv on a PSEUDO-TERMINAL inside the sandbox and stays for as long as
// the person on the other end does.
//
// It is a different call from exec and not a flag on it, because almost nothing
// they do is the same. exec bounds the run with a timeout, buffers both streams
// to a ceiling and reports an exit code; a terminal has no timeout that is not an
// insult to whoever is typing, keeps nothing (the bytes go straight to the
// socket), and ends when the shell does. What they share is the channel, which is
// the one thing that is stated once.
//
// STDERR IS NOT REQUESTED. A pty has one stream by construction — the kernel
// merges them onto the same device — and asking the apiserver for a second one on
// a TTY session is rejected outright.
func (r *runtime) tty(ctx context.Context, m Sandbox, argv []string, stdin io.Reader, stdout io.Writer, size remotecommand.TerminalSizeQueue) error {
	if err := r.ready(); err != nil {
		return err
	}
	if m.Status != "running" || m.Pod == "" {
		return fmt.Errorf("sandbox is %s", firstNonEmpty(m.Status, "unknown"))
	}
	return r.str.tty(ctx, r.ns, m.Pod, argv, stdin, stdout, size)
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

func (s *spdy) tty(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout io.Writer, size remotecommand.TerminalSizeQueue) error {
	cl, err := rest.RESTClientFor(coreConfig(s.cfg))
	if err != nil {
		return err
	}
	req := cl.Post().Resource("pods").Namespace(ns).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   argv,
			Stdin:     true,
			Stdout:    true,
			Stderr:    false,
			TTY:       true,
		}, scheme.ParameterCodec)
	ex, err := remotecommand.NewSPDYExecutor(s.cfg, "POST", req.URL())
	if err != nil {
		return err
	}
	return ex.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin: stdin, Stdout: stdout, Tty: true, TerminalSizeQueue: size,
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
