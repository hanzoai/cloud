// logs.go — REAL deployment logs for /v1/platform (the deploy.go phase-2 gap).
//
// deploymentLogs previously returned only the recorded status timeline + a Job
// reference, ending with a "(live BuildKit Job logs stream in phase 2)" note. This
// closes that gap: it streams the ACTUAL pod logs from the cluster — the BuildKit
// Job's pod for a git build, and the running app's pod for a deployed app — via the
// typed CoreV1 client's Pods().GetLogs subresource. Everything stays operator-
// consistent and org-scoped: the app's pods are read from tenant-<org> (the same
// isolation namespace the Service CR lives in), the build pod from the central build
// namespace by the deterministic job-name label. It NEVER fabricates output — an
// unreachable cluster or an absent pod yields the honest recorded timeline instead,
// exactly as the rest of the subsystem degrades.

package platform

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/text"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// logTailLines bounds a single log read — enough context for a deploy/build without
// streaming an unbounded history into the response.
const logTailLines = int64(400)

// logMaxBytes caps the total bytes returned so a chatty container cannot balloon the
// response; the tail is what a user needs, so we keep the END when over the cap.
const logMaxBytes = 256 << 10

// logCut heads a log cut to its most recent logMaxBytes, so the log itself says it
// was cut, and a reader that needs to know asks for this prefix.
var logCut = "… (truncated to the most recent " + byteCount(logMaxBytes) + ")\n"

// logReadTimeout bounds the cluster read so a wedged apiserver/pod cannot hang the
// HTTP handler; on timeout the caller falls back to the recorded timeline.
const logReadTimeout = 8 * time.Second

// buildLogs streams the BuildKit Job's pod logs for jobName from the central build
// namespace. The batch controller stamps every Job pod with `job-name=<jobName>`, so
// that label selects exactly this build's pod. Returns ("", false) when the cluster
// is unreachable, no pod exists yet (build not scheduled / TTL-reaped), or the read
// fails — the caller then shows the recorded timeline, never a fabricated log.
//
// Every container of the pod is read, in the order it runs them: a customer
// build solves in an init container and pushes from the main one (customerJob),
// and the artifact lane runs its recipe in init containers, so the main
// container alone would show the step after the one that did the work.
func (k *k8sClient) buildLogs(ctx context.Context, jobName string) (string, bool) {
	if jobName == "" {
		return "", false
	}
	return k.podLogsBySelector(ctx, k.buildNS, "job-name="+jobName, true)
}

// appLogs streams the running app's pod logs from tenant-<org>. The operator labels
// the workload it renders for a Service CR with `app.kubernetes.io/instance=<slug>`
// (the SAME label serviceCR stamps on the CR), so that selects the app's pods; the
// most-recently-started pod is read (the current rollout). Returns ("", false) on an
// unreachable cluster / no pod / read failure so the caller degrades honestly.
func (k *k8sClient) appLogs(ctx context.Context, org, slug string) (string, bool) {
	if slug == "" {
		return "", false
	}
	return k.podLogsBySelector(ctx, tenantNamespace(org), "app.kubernetes.io/instance="+slug, false)
}

// podLogsBySelector finds the newest pod matching selector in ns and streams its
// logs (tail-bounded, byte-capped, time-boxed). It is the ONE cluster log path,
// shared by build + app logs so the read semantics never drift. Any failure (no
// typed client, no pod, stream error) returns ("", false) — the caller decides the
// honest fallback; this never invents content.
//
// every reads each of the pod's containers, init containers first, under a line
// naming it; a pod with one container reads exactly as it would without. A
// container that has not started has no log yet and is left out.
func (k *k8sClient) podLogsBySelector(ctx context.Context, ns, selector string, every bool) (string, bool) {
	if k == nil || k.clientset == nil {
		return "", false
	}
	rctx, cancel := context.WithTimeout(ctx, logReadTimeout)
	defer cancel()

	pods, err := k.clientset.CoreV1().Pods(ns).List(rctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil || len(pods.Items) == 0 {
		return "", false
	}
	pod := newestPod(pods.Items)
	if pod == "" {
		return "", false
	}
	var containers []string
	if every {
		// newestPod sorted the list newest first.
		for _, c := range pods.Items[0].Spec.InitContainers {
			containers = append(containers, c.Name)
		}
		for _, c := range pods.Items[0].Spec.Containers {
			containers = append(containers, c.Name)
		}
	}

	var out string
	if len(containers) < 2 {
		data, ok := k.containerLogs(rctx, ns, pod, "")
		if !ok {
			return "", false
		}
		out = data
	} else {
		var b strings.Builder
		for _, c := range containers {
			data, ok := k.containerLogs(rctx, ns, pod, c)
			if !ok {
				continue
			}
			b.WriteString("── " + c + " ──\n")
			b.WriteString(data)
			if !strings.HasSuffix(data, "\n") {
				b.WriteByte('\n')
			}
		}
		out = b.String()
	}
	if len(out) > logMaxBytes {
		// Keep the TAIL (most recent) and mark the truncation honestly.
		out = out[len(out)-logMaxBytes:]
		if nl := strings.IndexByte(out, '\n'); nl >= 0 && nl < len(out)-1 {
			out = out[nl+1:] // start at a line boundary
		}
		out = logCut + out
	}
	if strings.TrimSpace(out) == "" {
		return "", false // an empty stream is not useful content
	}
	return out, true
}

// containerLogs reads one container's log tail, or the pod's only container's
// when container is empty. It reports false when the log cannot be read at all.
func (k *k8sClient) containerLogs(ctx context.Context, ns, pod, container string) (string, bool) {
	req := k.clientset.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container: container, TailLines: new(logTailLines)})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", false
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, logMaxBytes+1))
	if err != nil && len(data) == 0 {
		return "", false
	}
	return string(data), true
}

// newestPod returns the name of the most-recently-started pod (by startTime, then
// creationTimestamp) — the current rollout's pod for an app, or the live attempt for
// a build. Deterministic: ties break on name so the choice is stable.
func newestPod(pods []corev1.Pod) string {
	sort.Slice(pods, func(i, j int) bool {
		ti, tj := podTime(pods[i]), podTime(pods[j])
		if ti.Equal(tj) {
			return pods[i].Name > pods[j].Name
		}
		return ti.After(tj)
	})
	return pods[0].Name
}

// podTime is a pod's start time (falls back to creation time when not yet started).
func podTime(p corev1.Pod) time.Time {
	if p.Status.StartTime != nil {
		return p.Status.StartTime.Time
	}
	return p.CreationTimestamp.Time
}

// buildLogContext appends the recorded build summary line plus, when the cluster
// yields them, the REAL build-pod logs. It returns the extended lines and whether
// LIVE log content was actually streamed (streamed=true only when a pod's logs were
// read) — the caller uses that to set the response `source`, so `source` reflects
// real content, not merely the existence of a build row. The summary line is always
// real recorded state; a missing live tail is stated honestly, never faked.
func buildLogContext(s *cloud.Service[state], ctx context.Context, d Deployment, b Build, lines []string) (out []string, streamed bool) {
	if b.JobName == "" {
		return lines, false
	}
	lines = append(lines, fmt.Sprintf("build %s status=%s job=%s", b.ID, b.Status, b.JobName))
	if logs, ok := s.State.k8s.buildLogs(ctx, b.JobName); ok {
		lines = append(lines, "── build logs "+"("+s.State.k8s.buildNS+"/"+b.JobName+") ──", logs)
		return lines, true
	}
	lines = append(lines, "(build pod logs are not available — the Job pod has not started yet or its TTL elapsed)")
	return lines, false
}

//go:fix inline
func ptrInt64(v int64) *int64 { return new(v) }

// byteCount renders a byte size compactly (e.g. 262144 → "256 KiB").
func byteCount(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// reasonMax bounds a stored failure reason: one line of a build log, not the log.
const reasonMax = 1024

// jobReason is why one build Job failed, in one line: the solve's own error, read
// from the log tail its container left as a termination message
// (FallbackToLogsOnError); else the state a container is stuck in, which names a
// missing Secret or an image that will not pull; else the Job's Failed
// condition. "" when the cluster has nothing to say, which is never a guess.
func (k *k8sClient) jobReason(ctx context.Context, jobName string) string {
	if k == nil || k.clientset == nil || jobName == "" {
		return ""
	}
	rctx, cancel := context.WithTimeout(ctx, logReadTimeout)
	defer cancel()
	if pods, err := k.clientset.CoreV1().Pods(k.buildNS).List(rctx, metav1.ListOptions{LabelSelector: "job-name=" + jobName}); err == nil {
		for _, p := range pods.Items {
			if r := podReason(p); r != "" {
				return r
			}
		}
	}
	if k.dyn == nil {
		return ""
	}
	job, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(rctx, jobName, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return failedCondition(job)
}

// podReason is why one build pod did not finish: a container that exited
// non-zero, one waiting with a message, or the pod's own reason (an eviction).
// Init containers first: a customer build's solve is one.
func podReason(p corev1.Pod) string {
	for _, cs := range slices.Concat(p.Status.InitContainerStatuses, p.Status.ContainerStatuses) {
		if t := cs.State.Terminated; t != nil && t.ExitCode != 0 {
			return cmp.Or(solveError(t.Message), fmt.Sprintf("%s: %s, exit %d", cs.Name, t.Reason, t.ExitCode))
		}
		if w := cs.State.Waiting; w != nil && w.Message != "" {
			return text.Cut(fmt.Sprintf("%s: %s: %s", cs.Name, w.Reason, w.Message), reasonMax)
		}
	}
	if p.Status.Reason != "" {
		return text.Cut(p.Status.Reason+": "+p.Status.Message, reasonMax)
	}
	return ""
}

// solveError is what a build log says about why it failed: buildctl's last words
// (`error: failed to solve: ...`) with the lines they run on to, since an error
// quoting a tool's stderr ends its first line at `git stderr:`; else a step's
// `#N ERROR: ...`; else the last line. One line, whitespace folded.
func solveError(log string) string {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	from := func(match func(string) bool) string {
		for i := len(lines) - 1; i >= 0; i-- {
			if l := strings.TrimSpace(lines[i]); l != "" && match(l) {
				return strings.Join(strings.Fields(strings.Join(lines[i:], " ")), " ")
			}
		}
		return ""
	}
	return text.Cut(cmp.Or(
		from(func(l string) bool { return strings.HasPrefix(strings.ToLower(l), "error:") }),
		from(func(l string) bool { return strings.HasPrefix(l, "#") && strings.Contains(l, " ERROR: ") }),
		from(func(string) bool { return true }),
	), reasonMax)
}

// failedCondition is a Job's Failed condition as `reason: message`, or "".
func failedCondition(job *unstructured.Unstructured) string {
	conds, _, _ := unstructured.NestedSlice(job.Object, "status", "conditions")
	for _, c := range conds {
		cm, _ := c.(map[string]any)
		if cm["type"] == "Failed" && cm["status"] == "True" {
			reason, _ := cm["reason"].(string)
			msg, _ := cm["message"].(string)
			return text.Cut(strings.TrimSuffix(reason+": "+msg, ": "), reasonMax)
		}
	}
	return ""
}

// buildPod is the newest pod of one build Job, or false when there is none to read.
func (k *k8sClient) buildPod(ctx context.Context, jobName string) (corev1.Pod, bool) {
	if k == nil || k.clientset == nil || jobName == "" {
		return corev1.Pod{}, false
	}
	rctx, cancel := context.WithTimeout(ctx, logReadTimeout)
	defer cancel()
	pods, err := k.clientset.CoreV1().Pods(k.buildNS).List(rctx, metav1.ListOptions{LabelSelector: "job-name=" + jobName})
	if err != nil || len(pods.Items) == 0 {
		return corev1.Pod{}, false
	}
	name := newestPod(slices.Clone(pods.Items))
	i := slices.IndexFunc(pods.Items, func(p corev1.Pod) bool { return p.Name == name })
	return pods.Items[i], true
}

// jobState is what an unfinished build Job is doing, in one line: waiting for a
// node with the scheduler's own reason, or running, for how long and at which
// step of its log. "" when there is no pod to read.
func (k *k8sClient) jobState(ctx context.Context, jobName string, now time.Time) string {
	p, ok := k.buildPod(ctx, jobName)
	if !ok {
		return ""
	}
	if p.Spec.NodeName == "" {
		why := "no scheduling decision yet"
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Message != "" {
				why = c.Message
			}
		}
		return text.Cut(fmt.Sprintf("pending %s, never placed: %s", now.Sub(p.CreationTimestamp.Time).Round(time.Second), why), reasonMax)
	}
	// No node name: the reason is read by the build's own org, and which machine
	// ran it is the fabric's business.
	state := "running"
	if p.Status.StartTime != nil {
		state = fmt.Sprintf("waited %s for a node, running %s", p.Status.StartTime.Sub(p.CreationTimestamp.Time).Round(time.Second), now.Sub(p.Status.StartTime.Time).Round(time.Second))
	}
	if log, ok := k.buildLogs(ctx, jobName); ok {
		if step := lastStep(log); step != "" {
			state += ", at " + step
		}
	}
	return text.Cut(state, reasonMax)
}

// jobTimes is how long one finished build Job waited for a node and how long its
// solve ran, from its pod's own timestamps, as "waited <d> ran <d>". "" when the
// pod is gone or never ran.
func (k *k8sClient) jobTimes(ctx context.Context, jobName string) string {
	p, ok := k.buildPod(ctx, jobName)
	if !ok || p.Status.StartTime == nil {
		return ""
	}
	out := fmt.Sprintf("waited %s", p.Status.StartTime.Sub(p.CreationTimestamp.Time).Round(time.Second))
	for _, cs := range slices.Concat(p.Status.InitContainerStatuses, p.Status.ContainerStatuses) {
		if t := cs.State.Terminated; t != nil && !t.FinishedAt.IsZero() {
			out += fmt.Sprintf(" %s ran %s", cs.Name, t.FinishedAt.Sub(t.StartedAt.Time).Round(time.Second))
		}
	}
	return out
}

// lastStep is the last BuildKit step a build log names — `#21 [build 7/11] RUN
// ...` — as one bounded line, or "".
func lastStep(log string) string {
	lines := strings.Split(log, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if n, rest, ok := strings.Cut(l, " ["); ok && strings.HasPrefix(n, "#") && strings.Contains(rest, "]") {
			return text.Cut(strings.Join(strings.Fields(l), " "), 160)
		}
	}
	return ""
}

var (
	sha256DigestRE     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	manifestDigestLine = regexp.MustCompile(`(?:exporting manifest(?: list)? |@)(sha256:[0-9a-f]{64})\b`)
)

// jobDigest reads the pushed OCI manifest or index digest from a succeeded
// build Job's pod: first from a container termination message (e.g. JSON
// `containerimage.digest` or push container output), then from the pod's logs
// (BuildKit's `exporting manifest` / `pushing manifest for ...@sha256:...` or
// `crane push` `@sha256:...`). Returns "" if unavailable.
func (k *k8sClient) jobDigest(ctx context.Context, jobName string) string {
	p, ok := k.buildPod(ctx, jobName)
	if ok {
		if d := podDigest(p); d != "" {
			return d
		}
	}
	if log, ok := k.buildLogs(ctx, jobName); ok {
		return parseBuildDigest(log)
	}
	return ""
}

// podDigest extracts a validated `sha256:...` digest from a succeeded container's
// termination message (BuildKit's `--metadata-file /dev/termination-log` JSON or
// a push container's output).
func podDigest(p corev1.Pod) string {
	for _, cs := range p.Status.ContainerStatuses {
		if t := cs.State.Terminated; t != nil && t.ExitCode == 0 && strings.TrimSpace(t.Message) != "" {
			if d := parseBuildDigest(t.Message); d != "" {
				return d
			}
		}
	}
	return ""
}

// parseBuildDigest extracts a validated OCI sha256 digest from BuildKit
// metadata JSON or build/push log lines.
func parseBuildDigest(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(s), &meta); err == nil {
		if v, ok := meta["containerimage.digest"].(string); ok {
			v = strings.TrimSpace(v)
			if sha256DigestRE.MatchString(v) {
				return v
			}
		}
	}
	if sha256DigestRE.MatchString(s) {
		return s
	}
	matches := manifestDigestLine.FindAllStringSubmatch(s, -1)
	if len(matches) > 0 {
		return matches[len(matches)-1][1]
	}
	return ""
}
