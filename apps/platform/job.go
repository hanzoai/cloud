package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// job.go — a queued workflow job becomes one runner.
//
// The forge and GitHub each say when a job is queued, and that one delivery
// becomes one Job here: a runner that registers for that job, takes it, and
// exits. Capacity is therefore the cluster's, not a fixed pool's, and an idle
// fleet costs nothing. A delivery that starts no runner is picked up by the
// queue loop (queue.go).
//
// Platform holds the cluster and nothing about either provider. What a runner
// needs to register is minted by the receiver that verified the delivery — the
// forge's registration token by integrations, GitHub's just-in-time
// configuration likewise — and arrives here as a value in the event.

const (
	// runnerImageEnv names the runner image: act_runner and GitHub's runner on
	// the host executor, one entrypoint that reads which it was handed. Declared
	// in the universe beside the cloud's other images, never a floating tag.
	runnerImageEnv = "RUNNER_IMAGE"
	// runnerNamespaceEnv overrides where runner Jobs go; the default is this
	// process's own namespace, which is where its Role to create them lives.
	runnerNamespaceEnv = "CLOUD_PLATFORM_RUNNER_NS"
	// The runner image may live on either registry, so the Job carries both.
	ghcrPullSecret = "ghcr-secret"
	ociPullSecret  = "oci-hanzo-ai"
	// runnerPriorityClass ranks a runner below every serving workload, so a
	// build waits for capacity rather than taking it from a customer.
	runnerPriorityClass = "hanzo-ci"
	// deployPriorityClass is a job whose labels say deploy (xl-deploy-linux-amd64):
	// a site going live. It is still below every serving workload, and above every
	// other runner, so it is placed first when a node frees and may evict a test
	// shard for one (universe infra/aws/k8s/priority.yaml). hanzo.ai's deploy and
	// its gates share the xl pool, and a full gates run held both of its nodes
	// while the deploy waited 43 minutes (hanzo-inc/app run 41005619589).
	deployPriorityClass = "hanzo-deploy"
	deployPart          = "deploy"
	// runnerDeadline bounds one job in seconds: three hours, the limit the
	// runner holds (act_runner's default) and the forge holds ([actions]
	// ENDLESS_TASK_TIMEOUT in universe's hanzo-git values). A job past it is
	// dead, and the pod goes with it. cloud's gate, the longest job, runs 1h40m
	// on a node of its own.
	runnerDeadline = int64(3 * 60 * 60)
	// How a forge runner polls the forge. The forge's own limit on a request is Cloudflare's
	// 100 s; 30 s covers a forge answering slowly under load, which the runner's default 5 s did not.
	runnerFetchTimeout     = "30s"
	runnerFetchInterval    = "6s"
	runnerFetchIntervalMax = "15s"
	// runnerTTL keeps a finished Job for its log, then lets it go.
	runnerTTL = int64(600)
	// runnerDiskRequest is what an ordinary job writes; eight of them fit the
	// amd64 runner node's 91Gi. runnerDiskLimit is above the heaviest gate.
	runnerDiskRequest = "10Gi"
	runnerDiskLimit   = "80Gi"
	// runnerCPURequest and runnerMemoryRequest are what a job uses, measured
	// across 123 samples of 27 runners: CPU p50 1.12 and p90 1.87 cores, memory
	// p50 656Mi and p90 1.4Gi. At 1 CPU the scheduler packed more runners onto a
	// node than it had cores and every one of them ran starved; at 2Gi memory
	// filled a node's requests at 59% real use and refused runners it could run.
	runnerCPURequest    = "1500m"
	runnerMemoryRequest = "1536Mi"
	// runnerCPULimit caps every job but an xl one, and a heavy job requests it:
	// cloud's gate uses all six cores, and a job placed by less shares a node it
	// fills.
	runnerCPULimit = "6"
	// classLabel marks a heavy or xl runner's pod, which is what its spread
	// counts. On a node it names the class the node is for, as a label and as a
	// taint only that class's runners tolerate.
	classLabel = "hanzo.ai/class"
	heavyClass = "heavy"
	// xlClass is a job on universe's xl pool (infra/aws pool.tf), 32 vCPU /
	// 64 GiB nodes carrying classLabel=xl and its taint. It requests what an xl
	// job uses (measured 2026-11-08 across four hanzo.ai deploy and gate runners:
	// at most 9.2 CPU and 3.5 GiB) and is limited at the whole node, so four share
	// one and each may burst into the cores the others leave idle. The limits sit
	// under the 58.4 GiB the kubelet leaves of 64 (61274100Ki allocatable on a
	// c7a.8xlarge).
	xlClass = "xl"
	// xlPerNode is how many share one. The spread lets the class's pods differ by
	// that many from node to node: a node that cannot take another (the cloud builder
	// holds 30 of its 32 cores) is the least-loaded one the next job would otherwise
	// have to go to, and with a skew of one every other node stopped at a single job.
	xlPerNode       = 4
	xlCPURequest    = "7"
	xlCPULimit      = "32"
	xlMemoryRequest = "14Gi"
	xlMemoryLimit   = "56Gi"
	// runnerMemoryLimit is above the heaviest job, as the disk limit is. The
	// docs.hanzo.ai export compiles 2,950 MDX pages, and Turbopack's compile
	// holds 14-16G in one process at its peak however its parallelism, cache and
	// eviction are set; under visor the sentry takes another 1.4G beside it. At
	// 12Gi that job is killed mid-compile on every run.
	runnerMemoryLimit = "24Gi"
)

// safeLabel is what a runner label or a repository segment may be. It is an
// INPUT guard — it keeps anything a provider sends from reaching a shell or a
// Job name unquoted — and it deliberately admits `_`, because a repository may
// carry one and refusing the repo is not ours to do.
//
// It is NOT the Kubernetes alphabet, which is the mistake the old comment here
// invited by calling it "the same alphabet a hostname allows": a hostname has no
// underscore, and neither does an object name. Composing a name straight from
// this alphabet produced `runner-forge-hanzoai_extension-3903`, which the API
// server refuses outright — so every repository with `_` in its name could never
// launch a runner and 500'd its webhook. [cloud.RunnerName] maps the repository
// onto the object alphabet — mapping rather than refusing, because the name is an
// ADDRESS this code chooses, and the job id that follows keeps it unique.
var safeLabel = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// runnerNode is the architecture a job's labels name, linux-amd64 or
// linux-arm64. A job that names none runs on either node. macOS and Windows
// have no node here and are refused ([cloud.Podless]). Labels are read in any
// case, as GitHub matches them: its own runners answer to X64 and ARM64.
func runnerNode(labels []string) (string, error) {
	arch := ""
	for _, l := range labels {
		if cloud.Podless(l) {
			return "", fmt.Errorf("runner: label %q names a platform with no node in this cluster", l)
		}
		for part := range strings.SplitSeq(strings.ToLower(l), "-") {
			switch part {
			case "arm64", "aarch64":
				arch = "arm64"
			case "amd64", "x64", "x86_64":
				arch = "amd64"
			}
		}
	}
	return arch, nil
}

// runnerClass is the class a job's labels name: heavy, as heavy-linux-amd64
// does, xl, as xl-linux-amd64 does, or "" for an ordinary job. A heavy job requests its limit of cores
// and joins no node that runs another while a node without one exists; where
// none can hold it, it waits and the autoscaler adds one. An xl job does the
// same on the xl pool and only there. The class is read as the architecture
// is: a part between hyphens, in any case, never a substring. A job naming both
// is xl.
func runnerClass(labels []string) string {
	class := ""
	for _, l := range labels {
		for part := range strings.SplitSeq(strings.ToLower(l), "-") {
			switch part {
			case xlClass:
				return xlClass
			case heavyClass:
				class = heavyClass
			}
		}
	}
	return class
}

// runnerPriority is the priority class a job's labels name: a deploy job's
// above every other runner, the rest hanzo-ci. Read as the class is, a part
// between hyphens in any case.
func runnerPriority(labels []string) string {
	for _, l := range labels {
		for part := range strings.SplitSeq(strings.ToLower(l), "-") {
			if part == deployPart {
				return deployPriorityClass
			}
		}
	}
	return runnerPriorityClass
}

// ownNamespace is the namespace this process runs in, from the service account
// the cluster mounted, or the override.
func ownNamespace() (string, error) {
	if ns := strings.TrimSpace(os.Getenv(runnerNamespaceEnv)); ns != "" {
		return ns, nil
	}
	b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", fmt.Errorf("runner namespace: not in a cluster and %s is unset", runnerNamespaceEnv)
	}
	return strings.TrimSpace(string(b)), nil
}

// exposeJob publishes the launcher on the plane, for the process that verified
// a delivery when platform is not the one that received it.
func exposeJob() {
	cloud.Plane().Post("/platform/job",
		func(ctx context.Context, in *client.JobIn) (*client.Launched, error) {
			who := cloud.Who(ctx)
			if who.Org == "" {
				return nil, zip.ErrForbidden("platform job: org required")
			}
			s := mounted
			if s == nil {
				return nil, zip.Errorf(503, "platform job: platform not mounted")
			}
			name, err := launch(s, ctx, cloud.JobEvent{
				Org: who.Org, Provider: in.Provider, Repo: in.Repo, ID: in.ID,
				Labels: in.Labels, URL: in.URL, Token: in.Token, Try: in.Try,
			})
			if err != nil {
				// A full cluster is 429; the queue loop starts the job once a slot frees.
				if errors.Is(err, errTooManyRunners) {
					return nil, zip.Errorf(429, "platform job: %v", err)
				}
				return nil, err
			}
			return &client.Launched{Job: name}, nil
		},
		zip.WithOperationID(client.PlatformJob),
		zip.WithSummary("Run one queued workflow job on a runner that lives for it"))
}

// runnerRuntime is the boundary each job runs in: visor, our fork of gVisor,
// and the boundary the sandbox app runs on — so a runner and a sandbox ask for
// the same thing. The sandbox is the boundary (steps run on the host executor
// inside it), so nothing nests. NOT kata-fc, whose shim resolves to
// qemu-system-x86_64: 9,048M of QEMU where runsc took 43M for the same fleet.
const runnerRuntime = "visor"

// visorInstalled refuses a launch on a cluster without visor. A Job naming a
// class the cluster lacks never gets a pod, and holds a runner slot until its
// deadline while every queued job waits behind it.
func visorInstalled(ctx context.Context, k *k8sClient) error {
	if _, err := k.dyn.Resource(k8s.RuntimeClasses).Get(ctx, runnerRuntime, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("runner: this cluster does not install %s: %w", runnerRuntime, err)
	}
	return nil
}

// The registration is a credential: whoever holds it can register a runner and
// take the org's jobs. So it rides a Secret named for the Job and owned by it —
// garbage-collected with the Job — and the pod reads it by reference. Nothing in
// the Job object carries it, so `get job` shows no credential.
func registration(name, secret string) map[string]any {
	return map[string]any{"name": name, "valueFrom": map[string]any{
		"secretKeyRef": map[string]any{"name": secret, "key": "token"},
	}}
}

// runnerSecret writes the registration beside its Job. It is created after the
// Job, because the owner reference needs the Job's uid; a pod that starts first
// waits in CreateContainerConfigError and the kubelet retries once it exists.
func runnerSecret(ctx context.Context, k *k8sClient, ns string, job *unstructured.Unstructured, token string) error {
	secret := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]any{
			"name":      job.GetName(),
			"namespace": ns,
			"labels":    map[string]any{"hanzo.ai/managed-by": "platform"},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "batch/v1", "kind": "Job",
				"name": job.GetName(), "uid": string(job.GetUID()),
				"blockOwnerDeletion": true,
			}},
		},
		"type":       "Opaque",
		"stringData": map[string]any{"token": token},
	}}
	_, err := k.dyn.Resource(secretsGVR).Namespace(ns).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = k.dyn.Resource(secretsGVR).Namespace(ns).Update(ctx, secret, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("runner: registration secret: %w", err)
	}
	return nil
}

// launch creates the Job for one queued workflow job and answers its name.
func launch(s *cloud.Service[state], ctx context.Context, ev cloud.JobEvent) (string, error) {
	k := s.State.k8s
	if err := k.ready(); err != nil {
		return "", err
	}
	image := strings.TrimSpace(os.Getenv(runnerImageEnv))
	if image == "" {
		return "", fmt.Errorf("runner: %s is unset", runnerImageEnv)
	}
	ns, err := ownNamespace()
	if err != nil {
		return "", err
	}
	// One delivery becomes one runner, and a runner's memory is its node's — so a
	// busy afternoon on a big repo was seven runners for one repository and twelve
	// across the cluster, 9-10 GB apiece as kata microVMs (they run under visor
	// now), until the kernel began killing processes. Nothing bounded it: the
	// build path counts its Jobs before starting another and this path did not.
	//
	// A refused job stays queued, and the queue loop starts it once a slot frees.
	arch, err := runnerNode(ev.Labels)
	if err != nil {
		return "", err
	}
	// The xl pool is amd64. An xl job asking arm64 would wait for a node no
	// group makes, as a Pending pod nothing reports.
	class := runnerClass(ev.Labels)
	if class == xlClass && arch == "arm64" {
		return "", fmt.Errorf("runner: class %s has no arm64 node", xlClass)
	}
	if err := k.admitRunner(ctx, ns, ev.Provider, arch, class); err != nil {
		return "", err
	}
	if err := visorInstalled(ctx, k); err != nil {
		return "", err
	}
	owner, repo, ok := strings.Cut(ev.Repo, "/")
	if !ok || !safeLabel.MatchString(owner) || !safeLabel.MatchString(repo) || ev.ID <= 0 || ev.Try < 0 {
		return "", fmt.Errorf("runner: malformed job coordinate")
	}
	for _, l := range ev.Labels {
		if !safeLabel.MatchString(l) {
			return "", fmt.Errorf("runner: malformed label")
		}
	}
	if strings.TrimSpace(ev.Token) == "" {
		return "", fmt.Errorf("runner: no registration for the job")
	}
	name := cloud.RunnerName(ev.Provider, repo, ev.ID, ev.Try)
	var env []any
	switch ev.Provider {
	case "forge":
		if len(ev.Labels) == 0 || !strings.HasPrefix(ev.URL, "https://") {
			return "", fmt.Errorf("runner: a forge job needs labels and the forge's URL")
		}
		labels := make([]string, len(ev.Labels))
		for i, l := range ev.Labels {
			labels[i] = l + ":host"
		}
		env = []any{
			map[string]any{"name": "GIT_RUNNER_URL", "value": ev.URL},
			registration("GIT_RUNNER_TOKEN", name),
			map[string]any{"name": "RUNNER_LABELS", "value": strings.Join(labels, ",")},
			map[string]any{"name": "GIT_TERMINAL_PROMPT", "value": "0"},
			// A forge that answers a poll after the runner stopped waiting has already assigned
			// the task the runner then never hears of. These stop the runner giving up early
			// and polling a loaded forge every 2 s (git-runner config.go).
			map[string]any{"name": "RUNNER_FETCH_TIMEOUT", "value": runnerFetchTimeout},
			map[string]any{"name": "RUNNER_FETCH_INTERVAL", "value": runnerFetchInterval},
			map[string]any{"name": "RUNNER_FETCH_INTERVAL_MAX", "value": runnerFetchIntervalMax},
		}
	case "github":
		env = []any{
			registration("RUNNER_JITCONFIG", name),
			map[string]any{"name": "GIT_TERMINAL_PROMPT", "value": "0"},
		}
	default:
		return "", fmt.Errorf("runner: unknown provider %q", ev.Provider)
	}
	env = append(env, map[string]any{"name": "RUNNER_NAME", "value": name})
	// Runners spread across nodes by preference, which the autoscaler's
	// PreferNoSchedule on an idle node outweighs: right for a job that fits
	// beside others. A heavy or xl job's spread counts its class's pods and is
	// required. An xl job goes only to the xl pool, whose taint it alone
	// tolerates.
	//
	// evo carries hanzo.ai/no-ci. A runner tolerates it and yields to customer
	// work through the priority class.
	podLabels := map[string]any{"hanzo.ai/runner": ev.Provider}
	spread, among, skew := "ScheduleAnyway", map[string]any{"hanzo.ai/runner": ev.Provider}, int64(1)
	requests := map[string]any{"cpu": runnerCPURequest, "memory": runnerMemoryRequest, "ephemeral-storage": runnerDiskRequest}
	limits := map[string]any{"cpu": runnerCPULimit, "memory": runnerMemoryLimit, "ephemeral-storage": runnerDiskLimit}
	tolerations := []any{map[string]any{"key": "hanzo.ai/no-ci", "operator": "Exists", "effect": "NoSchedule"}}
	switch class {
	case heavyClass:
		requests["cpu"] = runnerCPULimit
	case xlClass:
		requests["cpu"], requests["memory"] = xlCPURequest, xlMemoryRequest
		limits["cpu"], limits["memory"] = xlCPULimit, xlMemoryLimit
		tolerations = append(tolerations, map[string]any{"key": classLabel, "operator": "Equal", "value": xlClass, "effect": "NoSchedule"})
	}
	if class != "" {
		podLabels[classLabel] = class
		spread, among = "DoNotSchedule", map[string]any{classLabel: class}
	}
	if class == xlClass {
		skew = xlPerNode
	}
	job := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"labels": map[string]any{
				"hanzo.ai/org":        ev.Org,
				"hanzo.ai/managed-by": "platform",
				"hanzo.ai/runner":     ev.Provider,
			},
		},
		"spec": map[string]any{
			// No retry. GitHub's just-in-time registration is single-use, so a
			// retried pod holds a slot it cannot use; a failed runner frees its
			// slot, and the queue loop starts a fresh one while its job is queued.
			"backoffLimit":            int64(0),
			"activeDeadlineSeconds":   runnerDeadline,
			"ttlSecondsAfterFinished": runnerTTL,
			"template": map[string]any{
				// The cluster autoscaler drains a node it reads as idle, runner and
				// all. A runner evicted mid-job leaves the forge holding a task
				// nobody will finish until ENDLESS_TASK_TIMEOUT, three hours later
				// (cloud's runs on 2026-10-30 lost their gate and unit jobs so, to
				// TaintManagerEviction). A runner keeps its node until it ends.
				"metadata": map[string]any{
					"labels":      podLabels,
					"annotations": map[string]any{"cluster-autoscaler.kubernetes.io/safe-to-evict": "false"},
				},
				"spec": map[string]any{
					"restartPolicy":    "Never",
					"runtimeClassName": runnerRuntime,
					"topologySpreadConstraints": []any{map[string]any{
						"maxSkew":           skew,
						"topologyKey":       "kubernetes.io/hostname",
						"whenUnsatisfiable": spread,
						"labelSelector":     map[string]any{"matchLabels": among},
					}},
					"priorityClassName":            runnerPriority(ev.Labels),
					"tolerations":                  tolerations,
					"automountServiceAccountToken": false,
					"imagePullSecrets": []any{
						map[string]any{"name": ghcrPullSecret},
						map[string]any{"name": ociPullSecret},
					},
					"containers": []any{map[string]any{
						"name":  "runner",
						"image": image,
						"env":   env,
						// The request admits the job; the limit caps it. gVisor reserves
						// neither at boot.
						//
						// Disk is declared for the reason the build Job declares it. A
						// runner with no ephemeral-storage request is best-effort for disk:
						// the scheduler places it on a node that is already full, and the
						// kubelet evicts it first, mid-run, with a log that simply stops.
						// cloud's gate writes ~29G. The request is what an ordinary job
						// needs, so a busy node still takes eight; the limit lets the gate
						// burst and stops a runaway from evicting its neighbours.
						"resources": map[string]any{"requests": requests, "limits": limits},
					}},
				},
			},
		},
	}}
	pod := podSpec(job.Object)
	selector := map[string]any{}
	if arch != "" {
		selector["kubernetes.io/arch"] = arch
	}
	if class == xlClass {
		selector[classLabel] = xlClass
	}
	if len(selector) > 0 {
		pod["nodeSelector"] = selector
	}
	k8s.AvoidPressure(pod)
	created, err := k.dyn.Resource(jobsGVR).Namespace(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	if err := runnerSecret(ctx, k, ns, created, ev.Token); err != nil {
		background := metav1.DeletePropagationBackground
		_ = k.dyn.Resource(jobsGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &background})
		return "", err
	}
	s.Log.Info("runner launched", "org", ev.Org, "provider", ev.Provider, "repo", ev.Repo, "job", ev.ID, "name", name)
	return name, nil
}
