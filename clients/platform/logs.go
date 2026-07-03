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
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// logTailLines bounds a single log read — enough context for a deploy/build without
// streaming an unbounded history into the response.
const logTailLines = int64(400)

// logMaxBytes caps the total bytes returned so a chatty container cannot balloon the
// response; the tail is what a user needs, so we keep the END when over the cap.
const logMaxBytes = 256 << 10

// logReadTimeout bounds the cluster read so a wedged apiserver/pod cannot hang the
// HTTP handler; on timeout the caller falls back to the recorded timeline.
const logReadTimeout = 8 * time.Second

// buildLogs streams the BuildKit Job's pod logs for jobName from the central build
// namespace. The batch controller stamps every Job pod with `job-name=<jobName>`, so
// that label selects exactly this build's pod. Returns ("", false) when the cluster
// is unreachable, no pod exists yet (build not scheduled / TTL-reaped), or the read
// fails — the caller then shows the recorded timeline, never a fabricated log.
func (k *k8sClient) buildLogs(ctx context.Context, jobName string) (string, bool) {
	if jobName == "" {
		return "", false
	}
	return k.podLogsBySelector(ctx, k.buildNS, "job-name="+jobName)
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
	return k.podLogsBySelector(ctx, tenantNamespace(org), "app.kubernetes.io/instance="+slug)
}

// podLogsBySelector finds the newest pod matching selector in ns and streams its
// logs (tail-bounded, byte-capped, time-boxed). It is the ONE cluster log path,
// shared by build + app logs so the read semantics never drift. Any failure (no
// typed client, no pod, stream error) returns ("", false) — the caller decides the
// honest fallback; this never invents content.
func (k *k8sClient) podLogsBySelector(ctx context.Context, ns, selector string) (string, bool) {
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

	req := k.clientset.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{TailLines: ptrInt64(logTailLines)})
	stream, err := req.Stream(rctx)
	if err != nil {
		return "", false
	}
	defer stream.Close()

	data, err := io.ReadAll(io.LimitReader(stream, logMaxBytes+1))
	if err != nil && len(data) == 0 {
		return "", false
	}
	out := string(data)
	if len(out) > logMaxBytes {
		// Keep the TAIL (most recent) and mark the truncation honestly.
		out = out[len(out)-logMaxBytes:]
		if nl := strings.IndexByte(out, '\n'); nl >= 0 && nl < len(out)-1 {
			out = out[nl+1:] // start at a line boundary
		}
		out = "… (truncated to the most recent " + byteCount(logMaxBytes) + ")\n" + out
	}
	if strings.TrimSpace(out) == "" {
		return "", false // an empty stream is not useful content
	}
	return out, true
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
func (s *svc) buildLogContext(ctx context.Context, d Deployment, b Build, lines []string) (out []string, streamed bool) {
	if b.JobName == "" {
		return lines, false
	}
	lines = append(lines, fmt.Sprintf("build %s status=%s job=%s", b.ID, b.Status, b.JobName))
	if logs, ok := s.k8s.buildLogs(ctx, b.JobName); ok {
		lines = append(lines, "── build logs "+"("+s.k8s.buildNS+"/"+b.JobName+") ──", logs)
		return lines, true
	}
	lines = append(lines, "(build pod logs are not available — the Job pod has not started yet or its TTL elapsed)")
	return lines, false
}

// scanLogLines is a small utility that normalizes a raw log blob into trimmed lines,
// used by tests to assert on individual lines without whitespace flakiness.
func scanLogLines(raw string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func ptrInt64(v int64) *int64 { return &v }

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
