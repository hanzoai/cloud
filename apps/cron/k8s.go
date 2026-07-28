// Copyright © 2026 Hanzo AI. MIT License.

package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
	luxlog "github.com/luxfi/log"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// enabledLabel marks a ConfigMap as a cron entry; nameLabel ties a
	// created Job back to its entry (Forbid-concurrency lookup).
	enabledLabel = "cron.hanzo.ai/enabled"
	nameLabel    = "cron.hanzo.ai/name"
	// defaultJobTTL self-reaps finished Jobs (the CronJobs' history limits
	// equivalent; the durable workflow run IS the history now).
	defaultJobTTL = int32(3600)
)

// kubeNamespace is the k8s namespace holding cron ConfigMaps and receiving
// Jobs. One namespace — platform cron is cluster-plane, not tenant-plane.
func kubeNamespace() string {
	if v := os.Getenv("CRON_NAMESPACE"); v != "" {
		return v
	}
	return "hanzo"
}

type entryKind string

const (
	kindJob  entryKind = "job"
	kindPoke entryKind = "poke"
)

// entry is one parsed cron ConfigMap.
type entry struct {
	Name     string
	Schedule string
	Kind     entryKind
	JobYAML  []byte   // kind=job: the batchv1 Job manifest
	Poke     pokeSpec // kind=poke
}

// kube is the k8s seam — an interface so the engine e2e tests drive the
// whole durable path with a fake cluster.
type kube interface {
	listEnabled(ctx context.Context) ([]entry, error)
	getEntry(ctx context.Context, name string) (entry, error)
	hasActiveJob(ctx context.Context, name string) (bool, error)
	createJob(ctx context.Context, name string, manifest []byte) (string, error)
	jobDone(ctx context.Context, jobName string) (done, succeeded bool, err error)
}

// ── package wiring (set once by start; test-overridable) ────────────────

var (
	wireMu  sync.RWMutex
	pkgKube kube
	pkgLog  luxlog.Logger
)

func setWiring(k kube, log luxlog.Logger) {
	wireMu.Lock()
	pkgKube, pkgLog = k, log
	wireMu.Unlock()
}

func activityKube() kube {
	wireMu.RLock()
	defer wireMu.RUnlock()
	if pkgKube == nil {
		return errKube{fmt.Errorf("cron kube not wired")}
	}
	return pkgKube
}

func activityLog() luxlog.Logger {
	wireMu.RLock()
	defer wireMu.RUnlock()
	if pkgLog == nil {
		return luxlog.NewNoOpLogger()
	}
	return pkgLog
}

// currentEngine resolves the shared engine for the reconcile activity.
var currentEngine = func() *tasksengine.Embedded { return cloud.EmbeddedTasks() }

type errKube struct{ err error }

func (e errKube) listEnabled(context.Context) ([]entry, error)      { return nil, e.err }
func (e errKube) getEntry(context.Context, string) (entry, error)   { return entry{}, e.err }
func (e errKube) hasActiveJob(context.Context, string) (bool, error) { return false, e.err }
func (e errKube) createJob(context.Context, string, []byte) (string, error) {
	return "", e.err
}
func (e errKube) jobDone(context.Context, string) (bool, bool, error) { return false, false, e.err }

// ── real cluster implementation ─────────────────────────────────────────

type clusterKube struct {
	cs *kubernetes.Clientset
	ns string
}

// defaultKube builds the in-cluster client (kubeconfig fallback for dev).
// Errors return errKube so every activity fails with the root cause instead
// of a nil panic.
func defaultKube() kube {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		if cfg, err = cc.ClientConfig(); err != nil {
			return errKube{fmt.Errorf("no in-cluster config and no kubeconfig: %w", err)}
		}
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return errKube{fmt.Errorf("kubernetes client: %w", err)}
	}
	return &clusterKube{cs: cs, ns: kubeNamespace()}
}

func (k *clusterKube) listEnabled(ctx context.Context) ([]entry, error) {
	cms, err := k.cs.CoreV1().ConfigMaps(k.ns).List(ctx, metav1.ListOptions{
		LabelSelector: enabledLabel + "=true",
	})
	if err != nil {
		return nil, err
	}
	out := make([]entry, 0, len(cms.Items))
	for _, cm := range cms.Items {
		e, err := parseEntry(cm.Name, cm.Data)
		if err != nil {
			activityLog().Warn("cron: skipping invalid entry", "configmap", cm.Name, "err", err)
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

func (k *clusterKube) getEntry(ctx context.Context, name string) (entry, error) {
	cm, err := k.cs.CoreV1().ConfigMaps(k.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return entry{}, err
	}
	return parseEntry(name, cm.Data)
}

// parseEntry validates one ConfigMap's data into an entry. Exactly one of
// job.yaml / poke.json must be present.
func parseEntry(name string, data map[string]string) (entry, error) {
	e := entry{Name: name, Schedule: data["schedule"]}
	if e.Schedule == "" {
		return entry{}, fmt.Errorf("missing data.schedule")
	}
	jobYAML, hasJob := data["job.yaml"]
	pokeJSON, hasPoke := data["poke.json"]
	switch {
	case hasJob && hasPoke:
		return entry{}, fmt.Errorf("both job.yaml and poke.json set — pick one")
	case hasJob:
		e.Kind, e.JobYAML = kindJob, []byte(jobYAML)
	case hasPoke:
		e.Kind = kindPoke
		if err := json.Unmarshal([]byte(pokeJSON), &e.Poke); err != nil {
			return entry{}, fmt.Errorf("poke.json: %w", err)
		}
		if e.Poke.URL == "" {
			return entry{}, fmt.Errorf("poke.json: url required")
		}
	default:
		return entry{}, fmt.Errorf("neither job.yaml nor poke.json set")
	}
	return e, nil
}

func (k *clusterKube) hasActiveJob(ctx context.Context, name string) (bool, error) {
	jobs, err := k.cs.BatchV1().Jobs(k.ns).List(ctx, metav1.ListOptions{
		LabelSelector: nameLabel + "=" + name,
	})
	if err != nil {
		return false, err
	}
	for _, j := range jobs.Items {
		if j.Status.CompletionTime == nil && !jobFailed(&j) {
			return true, nil
		}
	}
	return false, nil
}

// createJob decodes the manifest, stamps the run identity (unique name,
// entry label, TTL self-reaping), and submits it.
func (k *clusterKube) createJob(ctx context.Context, name string, manifest []byte) (string, error) {
	jsonBytes, err := utilyaml.ToJSON(manifest)
	if err != nil {
		return "", fmt.Errorf("job.yaml: %w", err)
	}
	var job batchv1.Job
	if err := json.Unmarshal(jsonBytes, &job); err != nil {
		return "", fmt.Errorf("job.yaml: %w", err)
	}
	job.Namespace = k.ns
	job.Name = fmt.Sprintf("%s-%d", name, time.Now().UTC().Unix())
	if job.Labels == nil {
		job.Labels = map[string]string{}
	}
	job.Labels[nameLabel] = name
	if job.Spec.TTLSecondsAfterFinished == nil {
		ttl := defaultJobTTL
		job.Spec.TTLSecondsAfterFinished = &ttl
	}
	created, err := k.cs.BatchV1().Jobs(k.ns).Create(ctx, &job, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	return created.Name, nil
}

func (k *clusterKube) jobDone(ctx context.Context, jobName string) (bool, bool, error) {
	j, err := k.cs.BatchV1().Jobs(k.ns).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return false, false, err
	}
	if j.Status.CompletionTime != nil {
		return true, true, nil
	}
	if jobFailed(j) {
		return true, false, nil
	}
	return false, false, nil
}

// jobFailed reports the terminal Failed condition (backoffLimit exhausted).
func jobFailed(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == "True" {
			return true
		}
	}
	return false
}
