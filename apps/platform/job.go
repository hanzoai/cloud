package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// job.go — a queued workflow job becomes one runner.
//
// Nothing polls. The forge and GitHub each say when a job is queued, and that
// one delivery becomes one Job here: a runner that registers for that job,
// takes it, and exits. Capacity is therefore the cluster's, not a fixed pool's,
// and an idle fleet costs nothing.
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
	// runnerRuntimeClass is the guest each job runs in. The VM is the sandbox —
	// steps run on the host executor inside it — so no runtime nests in it.
	runnerRuntimeClass = "kata-fc"
	// runnerPullSecret is the registry credential the runner image pulls with.
	runnerPullSecret = "oci-hanzo-ai"
	// runnerDeadline bounds one job in seconds. The forge's own runner timeout
	// is four hours; a job past it is dead, and the pod goes with it.
	runnerDeadline = int64(4 * 60 * 60)
	// runnerTTL keeps a finished Job for its log, then lets it go.
	runnerTTL = int64(600)
	// runnerTries is how many pods a Job may spend before it is failed. A runner
	// that dies before it takes a job holds nothing, and the forge refuses a
	// registration outright while its database is busy, so the whole pod is
	// the right unit to retry.
	runnerTries = int64(3)
)

// safeLabel is what a runner label or a repository segment may be: the same
// alphabet a hostname allows, so nothing a provider sends reaches a shell or a
// Job name unquoted.
var safeLabel = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// objectName renders a repository as an object name. What a repository may be
// called is wider than what the API accepts: `hanzoai_extension` is a legal
// repository and an illegal Job, and the API refuses the whole create on it, so
// every job from every underscored repository -- the mirrors, which is most of
// them -- never got a runner at all. Anything outside an RFC 1123 subdomain
// becomes a hyphen.
var notObjectName = regexp.MustCompile(`[^a-z0-9.-]+`)

func objectName(s string) string {
	return strings.Trim(notObjectName.ReplaceAllString(strings.ToLower(s), "-"), "-.")
}

// runnerNode is the node architecture a job's labels ask for. A label names the
// platform it wants -- linux-amd64, linux-arm64 -- so the architecture is read
// from the label rather than assumed, and a job that names none (ubuntu-latest,
// and every plain workflow) runs on amd64. macOS and Windows are named the same
// way and have no node in this cluster, so they are refused: a Linux runner
// would answer such a job with an artifact for the wrong machine and nothing
// would say so.
func runnerNode(labels []string) (string, error) {
	arch := "amd64"
	for _, l := range labels {
		for _, part := range strings.Split(l, "-") {
			switch part {
			case "macos", "darwin", "windows", "win":
				return "", fmt.Errorf("runner: label %q names a platform with no node in this cluster", l)
			case "arm64", "aarch64":
				arch = "arm64"
			case "amd64", "x64", "x86_64":
				arch = "amd64"
			}
		}
	}
	return arch, nil
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
	zip.Post[client.JobIn, client.Launched](cloud.Plane(), "/platform/job",
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
				Labels: in.Labels, URL: in.URL, Token: in.Token,
			})
			if err != nil {
				// A full cluster is a RETRY, not a fault: the provider redelivers a
				// queued job whose runner never registered, so 429 is the honest
				// answer and the build is not lost. Every other failure here is the
				// caller's or ours and keeps its own status.
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
	// One delivery becomes one runner, and a runner is a kata microVM whose guest
	// RAM comes out of this node's /dev/shm — so a busy afternoon on a big repo
	// was seven runners for one repository and twelve across the cluster, 9-10 GB
	// apiece, until the kernel began killing processes. Nothing bounded it: the
	// build path counts its Jobs before starting another and this path did not.
	//
	// Refusing is safe here in a way it would not be for a build: the provider
	// redelivers a queued job whose runner never registered, so declining costs a
	// redelivery rather than a lost build.
	if err := k.admitRunner(ctx, ns); err != nil {
		return "", err
	}
	owner, repo, ok := strings.Cut(ev.Repo, "/")
	if !ok || !safeLabel.MatchString(owner) || !safeLabel.MatchString(repo) || ev.ID <= 0 {
		return "", fmt.Errorf("runner: malformed job coordinate")
	}
	for _, l := range ev.Labels {
		if !safeLabel.MatchString(l) {
			return "", fmt.Errorf("runner: malformed label")
		}
	}
	arch, err := runnerNode(ev.Labels)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(ev.Token) == "" {
		return "", fmt.Errorf("runner: no registration for the job")
	}
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
			map[string]any{"name": "GITEA_INSTANCE_URL", "value": ev.URL},
			map[string]any{"name": "GITEA_RUNNER_REGISTRATION_TOKEN", "value": ev.Token},
			map[string]any{"name": "RUNNER_LABELS", "value": strings.Join(labels, ",")},
			map[string]any{"name": "GIT_TERMINAL_PROMPT", "value": "0"},
		}
	case "github":
		env = []any{
			map[string]any{"name": "RUNNER_JITCONFIG", "value": ev.Token},
			map[string]any{"name": "GIT_TERMINAL_PROMPT", "value": "0"},
		}
	default:
		return "", fmt.Errorf("runner: unknown provider %q", ev.Provider)
	}
	name := truncate(fmt.Sprintf("runner-%s-%s-%d", ev.Provider, objectName(repo), ev.ID), 63)
	env = append(env, map[string]any{"name": "RUNNER_NAME", "value": name})
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
			"backoffLimit":            runnerTries,
			"activeDeadlineSeconds":   runnerDeadline,
			"ttlSecondsAfterFinished": runnerTTL,
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"hanzo.ai/runner": ev.Provider}},
				"spec": map[string]any{
					"restartPolicy":                "Never",
					"runtimeClassName":             runnerRuntimeClass,
					"nodeSelector":                 map[string]any{"kubernetes.io/arch": arch},
					"automountServiceAccountToken": false,
					"imagePullSecrets":             []any{map[string]any{"name": runnerPullSecret}},
					"containers": []any{map[string]any{
						"name":  "runner",
						"image": image,
						"env":   env,
						// REQUESTS EQUAL LIMITS, because this pod is a virtual
						// machine. runtimeClassName is kata-fc, and a Kata guest is
						// sized from the LIMIT at boot: with 6/12Gi declared, the
						// hypervisor starts `-smp 7 -m 12320M` and holds it for the
						// life of the job whether a step uses it or not.
						//
						// Requesting 1/2Gi therefore did not describe a smaller
						// workload, it described a smaller LIE. The scheduler priced
						// eight runners at 8 CPU and 16Gi while they had claimed 56
						// vCPU and 96Gi, so it kept admitting more onto a node that
						// was already full — and the pods that could not fit were
						// ordinary workloads with honest requests, refused for
						// capacity that had already been spent.
						//
						// Equal also makes the QoS class Guaranteed, which is the
						// truth for a VM: guest memory is not reclaimable, so there
						// is nothing for the kubelet to take back under pressure.
						"resources": map[string]any{
							"requests": map[string]any{"cpu": "6", "memory": "12Gi"},
							"limits":   map[string]any{"cpu": "6", "memory": "12Gi"},
						},
					}},
				},
			},
		},
	}}
	if _, err := k.dyn.Resource(jobsGVR).Namespace(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return "", err
	}
	s.Log.Info("runner launched", "org", ev.Org, "provider", ev.Provider, "repo", ev.Repo, "job", ev.ID, "name", name)
	return name, nil
}
