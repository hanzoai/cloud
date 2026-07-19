// stream.go — GET /v1/deploy/stream/applications: the ArgoCD applications watch
// as Server-Sent Events. The applications view opens this the moment it loads and
// keeps it open for live fleet updates; a 404 here makes the SPA error-toast.
//
// The stream emits one ADDED event per current App CR — the SAME projection
// dashAppList serves (listAppCRs + runningVersions + projectApp: one source, one
// projection) — then watches the App CRs and forwards ADDED/MODIFIED/DELETED as
// they occur, holding the connection open with periodic keep-alives. Every watch +
// goroutine it starts is bound to the request and torn down on return, so a client
// disconnect leaks nothing. Read-only and SuperAdmin-gated by guard(); it fails
// closed (503) when no cluster client is configured, and degrades to keep-alive
// only (the initial state still renders) if the watch verb is not granted.
package deploy

import (
	"bufio"
	"context"
	"encoding/json"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

// streamKeepalive is the interval between keep-alive comments on an idle stream.
// The keep-alive holds the connection open AND is when a client disconnect is
// detected — a failing flush ends the stream — so it also bounds how long an idle
// disconnected client lingers before its watch is torn down.
const streamKeepalive = 25 * time.Second

// applicationWatchEvent is the ArgoCD v1alpha1 ApplicationWatchEvent — the object
// the SPA reads from each SSE frame's `.result`.
type applicationWatchEvent struct {
	Type        string  `json:"type"` // ADDED | MODIFIED | DELETED
	Application argoApp `json:"application"`
}

// streamResult is the `{"result": …}` envelope the SPA unwraps (JSON.parse(data).result).
type streamResult struct {
	Result applicationWatchEvent `json:"result"`
}

// dashStreamApps is GET /v1/deploy/stream/applications — the applications watch as
// SSE. SuperAdmin-gated by guard(); 503 when no cluster client is configured.
func dashStreamApps(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	// Capture the context BEFORE SendStreamWriter: its callback runs AFTER this
	// handler returns (fasthttp body writer) and must not touch c. c.Context() is
	// background-derived — it does NOT cancel on client disconnect; the disconnect
	// signal is a failing flush inside the loop.
	ctx := c.Context()
	setStreamHeaders(c)
	return c.SendStreamWriter(func(w *bufio.Writer) {
		streamApps(s, ctx, w)
	})
}

// setStreamHeaders writes the SSE response headers. Factored out so it is testable
// without spawning the body-stream goroutine (fasthttp starts the writer eagerly
// on SendStreamWriter).
func setStreamHeaders(c *zip.Ctx) {
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-cache")
	c.SetHeader("Connection", "keep-alive")
	c.SetHeader("X-Accel-Buffering", "no") // never buffer SSE at a proxy
}

// streamApps is the SSE core, separated from the handler so it is unit-testable
// over a bytes.Buffer + a cancelable context: emit the initial ADDED burst, then
// watch for live changes until the client disconnects or the context is canceled.
func streamApps(s *cloud.Service[state], ctx context.Context, w *bufio.Writer) {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel() // stops every watch + forwarder started below
	if !streamAppBurst(s, wctx, w) {
		return // client gone during the burst
	}
	streamAppWatch(s, wctx, w)
}

// streamAppBurst emits one ADDED event per current App CR, projected identically
// to dashAppList. Returns false if a write failed (client disconnected).
func streamAppBurst(s *cloud.Service[state], ctx context.Context, w *bufio.Writer) bool {
	for _, ns := range scanOrder() {
		crs, err := listAppCRs(s, ctx, ns)
		if err != nil {
			s.Log.Warn("deploy stream: initial list failed", "namespace", ns, "err", err)
			continue // best-effort burst; other namespaces still stream
		}
		running := runningVersions(s, ctx, ns)
		for i := range crs {
			if !writeAppEvent(w, string(watch.Added), projectApp(&crs[i], ns, running[crs[i].GetName()])) {
				return false
			}
		}
	}
	return true
}

// streamEvent is one forwarded watch event carrying the raw App CR (projected in
// the single-threaded main loop, which owns the running-tag map).
type streamEvent struct {
	typ string
	ns  string
	obj *unstructured.Unstructured
}

// streamAppWatch holds the stream open: it forwards live App-CR changes as ArgoCD
// watch events and writes a keep-alive on an idle interval. It returns when the
// context is canceled OR a write fails (client disconnected). Every watch +
// goroutine is bound to ctx and stopped on return — no leak.
func streamAppWatch(s *cloud.Service[state], ctx context.Context, w *bufio.Writer) {
	events := make(chan streamEvent, 16)
	started := 0
	for _, ns := range scanOrder() {
		watcher, err := s.State.dyn.Resource(appsCRGVR).Namespace(ns).Watch(ctx, metav1.ListOptions{})
		if err != nil {
			// Degrade, don't fail: the initial state stands and the keep-alive holds
			// the connection open. The operator can grant the `watch` verb to turn on
			// live updates without any code change.
			s.Log.Warn("deploy stream: watch unavailable; namespace will not stream live", "namespace", ns, "err", err)
			continue
		}
		started++
		go forwardWatch(ctx, ns, watcher, events)
	}
	if started > 0 {
		s.Log.Info("deploy stream: watching App CRs", "namespaces", started)
	}

	// Running-image tags refresh on the keep-alive tick, bounding LIST calls to once
	// per interval regardless of event rate; a live event projects with the last
	// known tag (nil-map reads are the empty string — safe).
	running := allRunningVersions(s, ctx)
	ticker := time.NewTicker(streamKeepalive)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			if !writeAppEvent(w, ev.typ, projectApp(ev.obj, ev.ns, running[ev.ns][ev.obj.GetName()])) {
				return
			}
		case <-ticker.C:
			running = allRunningVersions(s, ctx)
			if _, err := w.WriteString(": keep-alive\n\n"); err != nil {
				return
			}
			if err := w.Flush(); err != nil {
				return
			}
		}
	}
}

// forwardWatch relays one namespace's App-CR watch onto events until ctx is
// canceled or the watch closes (server timeout / Stop). It stops its watcher on
// return and never blocks past ctx — the send to events selects on ctx.Done.
func forwardWatch(ctx context.Context, ns string, watcher watch.Interface, events chan<- streamEvent) {
	defer watcher.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-watcher.ResultChan():
			if !ok {
				return // watch closed
			}
			typ := watchType(e.Type)
			if typ == "" {
				continue // skip BOOKMARK / ERROR — not an application change
			}
			obj, ok := e.Object.(*unstructured.Unstructured)
			if !ok || obj.GetName() == "" {
				continue
			}
			if _, platform := nsEnv[obj.GetNamespace()]; !platform {
				continue // only the platform namespaces this plane reads
			}
			select {
			case events <- streamEvent{typ: typ, ns: ns, obj: obj}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// watchType maps a k8s watch event type to the ArgoCD WatchType the UI parses.
// BOOKMARK and ERROR are not application changes → "" (skipped).
func watchType(t watch.EventType) string {
	switch t {
	case watch.Added, watch.Modified, watch.Deleted:
		return string(t) // "ADDED" | "MODIFIED" | "DELETED"
	default:
		return ""
	}
}

// allRunningVersions collects running image tags per platform namespace
// (best-effort — a list error yields an empty inner map).
func allRunningVersions(s *cloud.Service[state], ctx context.Context) map[string]map[string]string {
	out := make(map[string]map[string]string, len(nsEnv))
	for _, ns := range scanOrder() {
		out[ns] = runningVersions(s, ctx, ns)
	}
	return out
}

// writeAppEvent writes one SSE frame — `data: {"result":{"type":…,"application":…}}`
// — and flushes. Returns false when the write/flush fails (client disconnected).
func writeAppEvent(w *bufio.Writer, typ string, app argoApp) bool {
	payload, err := json.Marshal(streamResult{Result: applicationWatchEvent{Type: typ, Application: app}})
	if err != nil {
		return true // unreachable for this shape; skip the frame rather than kill the stream
	}
	if _, err := w.WriteString("data: "); err != nil {
		return false
	}
	if _, err := w.Write(payload); err != nil {
		return false
	}
	if _, err := w.WriteString("\n\n"); err != nil {
		return false
	}
	return w.Flush() == nil
}
