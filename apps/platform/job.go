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
	// runnerRuntimeClass is the boundary each job runs in. The sandbox is the
	// boundary — steps run on the host executor inside it — so nothing nests.
	//
	// It says gvisor because kata-fc did not deliver a microVM. Kata derives its
	// config from the name the shim was invoked as, so containerd-shim-kata-fc-v2
	// looks for configuration-fc.toml; the package ships configuration-rs-fc.toml,
	// the lookup misses, and it falls back to configuration.toml, whose path is
	// qemu-system-x86_64. Sixteen runners on evo asked for Firecracker and got
	// sixteen QEMU q35 machines — 9,048M of them, beside 4,763M of virtiofsd and
	// 2,918M of shims, against 43M of runsc for the same fleet.
	runnerRuntimeClass = "gvisor"
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
// launch a runner, 500'd its webhook, and had the provider redeliver the same
// queued job forever. `dns1123` below is what bridges the two.
var safeLabel = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// dns1123 maps a segment onto the alphabet Kubernetes accepts for an object
// name. Mapping rather than refusing, because the name is an ADDRESS this code
// chooses, not a fact about the repository — and the job id that follows it is
// what keeps it unique, so two repos that map alike still get distinct names.
func dns1123(s string) string {
	b := make([]rune, 0, len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b = append(b, r)
		default:
			b = append(b, '-')
		}
	}
	return strings.Trim(string(b), "-.")
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
	name := truncate(fmt.Sprintf("runner-%s-%s-%d", ev.Provider, dns1123(repo), ev.ID), 63)
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
						// REQUESTS BELOW LIMITS, because this pod is no longer a
						// virtual machine. Equal requests were correct while it was:
						// a Kata guest is sized from the LIMIT at boot, so 6/12Gi
						// started `-smp 7 -m 12320M` and held it for the life of the
						// job, and asking for less described a smaller lie rather
						// than a smaller workload.
						//
						// gVisor pre-allocates neither. The sentry is a process, so
						// the request is what the job needs to be admitted and the
						// limit is the ceiling it may reach — the ordinary meaning,
						// which was the wrong one only while a hypervisor was
						// reading the limit at boot. Reserving six idle CPUs per
						// runner is what filled the node: seventeen of them held
						// seventeen of evo's twenty-nine, and the pods refused for
						// that capacity were ordinary workloads asking honestly.
						"resources": map[string]any{
							"requests": map[string]any{"cpu": "1", "memory": "2Gi"},
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
