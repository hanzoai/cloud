// Copyright © 2026 Hanzo AI. MIT License.

package cron

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/env"

	"github.com/hanzoai/cloud/sdk"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
	luxlog "github.com/luxfi/log"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
	// jobTTL self-reaps finished Jobs (the CronJobs' history limits equivalent;
	// the durable workflow run IS the history now). It outlasts every attempt a
	// firing can make, so a retry always finds the Job its firing made — reaped
	// sooner, a retry would make it again — and a manifest that names a shorter
	// one is raised to it.
	jobTTL = int32((jobAttempts*jobTimeout + 10*time.Minute) / time.Second)
)

// kubeNamespace is the k8s namespace holding cron ConfigMaps and receiving
// Jobs. One namespace — platform cron is cluster-plane, not tenant-plane.
func kubeNamespace() string {
	if v := env.Or("CRON_NAMESPACE", ""); v != "" {
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

// kube is the k8s client — an interface so the engine e2e tests drive the
// whole durable path with a fake cluster.
type kube interface {
	listEnabled(ctx context.Context) ([]entry, error)
	getEntry(ctx context.Context, name string) (entry, error)
	// hasActiveJob reports whether a Job of entry name other than except is running.
	hasActiveJob(ctx context.Context, name, except string) (bool, error)
	// createJob creates the Job jobName for entry name, and answers nil when a Job of
	// that name already exists: one firing makes one Job however often it is asked.
	createJob(ctx context.Context, name, jobName string, manifest []byte) error
	jobDone(ctx context.Context, jobName string) (done, succeeded bool, err error)
	// jobFailure says why the failed Job jobName failed, in one line ("" if
	// nothing can be read).
	jobFailure(ctx context.Context, jobName string) string
	// recordRun sets these keys of the cron-status ConfigMap (statusMap) and
	// leaves every other key as it is.
	recordRun(ctx context.Context, data map[string]string) error
}

// ── package wiring (set once by start; test-overridable) ────────────────

var (
	wireMu  sync.RWMutex
	pkgKube kube
	pkgLog  luxlog.Logger
	pkgPace = steady
)

// pace is how long cron waits: between two looks at a Job it made, and before the
// first retry of a firing's activity (each later retry waits twice as long, up to a
// minute).
type pace struct{ watch, retry time.Duration }

// steady is production's pace, and what start wires.
var steady = pace{watch: 10 * time.Second, retry: 10 * time.Second}

func setWiring(k kube, log luxlog.Logger, p pace) {
	wireMu.Lock()
	pkgKube, pkgLog, pkgPace = k, log, p
	wireMu.Unlock()
}

func activityPace() pace {
	wireMu.RLock()
	defer wireMu.RUnlock()
	return pkgPace
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
		return luxlog.Default()
	}
	return pkgLog
}

// currentEngine resolves the shared engine for the reconcile activity.
var currentEngine = func() *tasksengine.Embedded { return cloud.EmbeddedTasks() }

type errKube struct{ err error }

func (e errKube) listEnabled(context.Context) ([]entry, error)               { return nil, e.err }
func (e errKube) getEntry(context.Context, string) (entry, error)            { return entry{}, e.err }
func (e errKube) hasActiveJob(context.Context, string, string) (bool, error) { return false, e.err }
func (e errKube) createJob(context.Context, string, string, []byte) error    { return e.err }
func (e errKube) jobDone(context.Context, string) (bool, bool, error)        { return false, false, e.err }
func (e errKube) jobFailure(context.Context, string) string                  { return "" }
func (e errKube) recordRun(context.Context, map[string]string) error         { return e.err }

// ── real cluster implementation ─────────────────────────────────────────

type clusterKube struct {
	cs kubernetes.Interface
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

// recordRun merge-patches the given keys into cron-status: one entry's run
// replaces that entry's keys and no other, so two entries finishing at once
// cannot undo each other, and nothing is read first.
func (k *clusterKube) recordRun(ctx context.Context, data map[string]string) error {
	patch, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		return err
	}
	_, err = k.cs.CoreV1().ConfigMaps(k.ns).Patch(ctx, statusMap, types.MergePatchType, patch,
		metav1.PatchOptions{FieldManager: "cloud-cron"})
	return err
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
		// A poke asks an op by name (app and op) or calls a URL, never both.
		op := e.Poke.App != "" || e.Poke.Op != ""
		switch {
		case op && e.Poke.URL != "":
			return entry{}, fmt.Errorf("poke.json: an op (app, op) or a url, not both")
		case op && (e.Poke.App == "" || e.Poke.Op == ""):
			return entry{}, fmt.Errorf("poke.json: an op names both app and op")
		case !op && e.Poke.URL == "":
			return entry{}, fmt.Errorf("poke.json: needs an op (app, op) or a url")
		}
	default:
		return entry{}, fmt.Errorf("neither job.yaml nor poke.json set")
	}
	return e, nil
}

func (k *clusterKube) hasActiveJob(ctx context.Context, name, except string) (bool, error) {
	jobs, err := k.cs.BatchV1().Jobs(k.ns).List(ctx, metav1.ListOptions{
		LabelSelector: nameLabel + "=" + name,
	})
	if err != nil {
		return false, err
	}
	for _, j := range jobs.Items {
		if j.Name != except && j.Status.CompletionTime == nil && !jobFailed(&j) {
			return true, nil
		}
	}
	return false, nil
}

// createJob decodes the manifest, stamps the run identity (the firing's name,
// entry label, TTL self-reaping), and submits it. A Job of that name already there
// is this firing's, made by an earlier attempt whose reply never arrived.
func (k *clusterKube) createJob(ctx context.Context, name, jobName string, manifest []byte) error {
	jsonBytes, err := utilyaml.ToJSON(manifest)
	if err != nil {
		return fmt.Errorf("job.yaml: %w", err)
	}
	var job batchv1.Job
	if err := json.Unmarshal(jsonBytes, &job); err != nil {
		return fmt.Errorf("job.yaml: %w", err)
	}
	job.Namespace = k.ns
	job.Name = jobName
	if job.Labels == nil {
		job.Labels = map[string]string{}
	}
	job.Labels[nameLabel] = name
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished < jobTTL {
		ttl := jobTTL
		job.Spec.TTLSecondsAfterFinished = &ttl
	}
	if _, err := k.cs.BatchV1().Jobs(k.ns).Create(ctx, &job, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

// jobNameFor names the Job one firing of entry makes: the entry, and a digest of the
// firing (the tick it fired for), so every attempt of one firing — and the same
// tick fired by another pod's engine — names the same Job, and the next tick names
// another. A Job name is a DNS label: 63 characters.
func jobNameFor(entry, firing string) string {
	sum := sha256.Sum256([]byte(firing))
	if len(entry) > 52 {
		entry = entry[:52]
	}
	return entry + "-" + hex.EncodeToString(sum[:5])
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

// THE RUN SAYS WHY ITS JOB FAILED. "job X failed" was all a failed run said, so
// the nightly backups that failed every night for over a week (hanzo-inc/cloud#828)
// said nothing about why to anyone without a shell in the cluster: the Job and
// its pods are reaped, and the reason went with them. The reason is read once,
// when the failure is, and rides the run's error into the engine's record
// (FailureStreak.LastError, the Tasks console) and the "cron: run failed" line.
const (
	// tailLines is how much of the failed container's log the reason carries:
	// its last words, which for a script that exits on an error are that error.
	tailLines = 5
	// tailBytes and whyBytes bound what one failure can put in a record.
	tailBytes = 2048
	whyBytes  = 1500
)

// jobNameLabel is the label the Job controller puts on each pod of a Job.
const jobNameLabel = "batch.kubernetes.io/job-name"

func (k *clusterKube) jobFailure(ctx context.Context, jobName string) string {
	j, err := k.cs.BatchV1().Jobs(k.ns).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return boundedFailure("could not read Job failure details: " + oneLine(err.Error()))
	}
	pods, err := k.cs.CoreV1().Pods(k.ns).List(ctx, metav1.ListOptions{LabelSelector: jobNameLabel + "=" + jobName})
	if err != nil {
		return boundedFailure(strings.Join(append(jobVerdict(j), "could not list Job pods: "+oneLine(err.Error())), "; "))
	}
	return failureOf(j, pods.Items, func(pod, container string) string {
		n, lim := int64(tailLines), int64(tailBytes)
		b, err := k.cs.CoreV1().Pods(k.ns).GetLogs(pod, &corev1.PodLogOptions{
			Container: container, TailLines: &n, LimitBytes: &lim,
		}).DoRaw(ctx)
		if err != nil {
			return "could not read container logs: " + oneLine(err.Error())
		}
		return string(b)
	})
}

// failureOf is why a failed Job failed, in one line: the Job's own verdict
// (BackoffLimitExceeded, DeadlineExceeded), the newest pod with actionable
// failure evidence (an older retry may have the only recorded exit), and the
// last lines that container wrote, read by tail.
func failureOf(j *batchv1.Job, pods []corev1.Pod, tail func(pod, container string) string) string {
	why := jobVerdict(j)
	if len(pods) > 0 {
		sort.Slice(pods, func(a, b int) bool {
			return pods[b].CreationTimestamp.Before(&pods[a].CreationTimestamp)
		})
		found := false
		for _, p := range pods {
			var evidence []string
			if p.Status.Reason != "" {
				evidence = append(evidence, "pod "+p.Name+" "+said(p.Status.Reason, p.Status.Message))
			}
			for _, c := range p.Status.Conditions {
				if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
					evidence = append(evidence, "pod "+p.Name+" not scheduled: "+said(c.Reason, c.Message))
				}
			}
			statuses := append(append([]corev1.ContainerStatus(nil), p.Status.InitContainerStatuses...), p.Status.ContainerStatuses...)
			for _, s := range statuses {
				t := s.State.Terminated
				previous := false
				if t == nil || t.ExitCode == 0 {
					t = s.LastTerminationState.Terminated
					previous = true
				}
				if t != nil && t.ExitCode != 0 {
					label := "container " + s.Name
					if previous {
						label += " (previous attempt)"
					}
					evidence = append(evidence, fmt.Sprintf("%s: %s, exit %d", label, said(t.Reason, t.Message), t.ExitCode))
					// Current logs may belong to a different container attempt.
					if !previous && tail != nil {
						if words := oneLine(tail(p.Name, s.Name)); words != "" {
							evidence = append(evidence, "its last lines: "+words)
						}
					}
					break
				}
				if w := s.State.Waiting; w != nil && w.Reason != "" && w.Reason != "PodInitializing" && w.Reason != "ContainerCreating" {
					evidence = append(evidence, "container "+s.Name+" never started: "+said(w.Reason, w.Message))
					break
				}
			}
			if len(evidence) > 0 {
				why = append(why, evidence...)
				found = true
				break
			}
		}
		if !found {
			why = append(why, "Job pods have no recorded failure details")
		}
	} else {
		why = append(why, "no Job pods found")
	}
	return boundedFailure(strings.Join(why, "; "))
}

func jobVerdict(j *batchv1.Job) []string {
	var why []string
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			why = append(why, said(c.Reason, c.Message))
		}
	}
	return why
}

func boundedFailure(out string) string {
	if len(out) > whyBytes {
		out = strings.ToValidUTF8(out[:whyBytes], "") + "…"
	}
	return out
}

// said is a reason and its message, whichever are there.
func said(reason, msg string) string {
	reason, msg = strings.TrimSpace(reason), oneLine(msg)
	switch {
	case reason == "":
		return msg
	case msg == "":
		return reason
	}
	return reason + ": " + msg
}

// oneLine folds text onto one line, its lines kept apart by " | ".
func oneLine(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, " | ")
}
