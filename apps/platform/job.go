package platform

import (
	"context"
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
)

// safeLabel is what a runner label or a repository segment may be: the same
// alphabet a hostname allows, so nothing a provider sends reaches a shell or a
// Job name unquoted.
var safeLabel = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

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
	owner, repo, ok := strings.Cut(ev.Repo, "/")
	if !ok || !safeLabel.MatchString(owner) || !safeLabel.MatchString(repo) || ev.ID <= 0 {
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
		}
	case "github":
		env = []any{map[string]any{"name": "RUNNER_JITCONFIG", "value": ev.Token}}
	default:
		return "", fmt.Errorf("runner: unknown provider %q", ev.Provider)
	}
	name := truncate(fmt.Sprintf("runner-%s-%s-%d", ev.Provider, strings.ToLower(repo), ev.ID), 63)
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
			"backoffLimit":            int64(0),
			"activeDeadlineSeconds":   runnerDeadline,
			"ttlSecondsAfterFinished": runnerTTL,
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"hanzo.ai/runner": ev.Provider}},
				"spec": map[string]any{
					"restartPolicy":                "Never",
					"runtimeClassName":             runnerRuntimeClass,
					"nodeSelector":                 map[string]any{"kubernetes.io/arch": "amd64"},
					"automountServiceAccountToken": false,
					"imagePullSecrets":             []any{map[string]any{"name": runnerPullSecret}},
					"containers": []any{map[string]any{
						"name":  "runner",
						"image": image,
						"env":   env,
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
