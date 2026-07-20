package deploy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"

	"github.com/zap-proto/zip"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

// getJSON drives one guarded GET through the full router as a SuperAdmin and
// decodes the JSON body. It asserts a 200 so a route-not-registered (404) or a
// guard reject (403) fails loudly.
func getJSON(t *testing.T, s *cloud.Service[state], path string) map[string]any {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, s)
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("X-User-IsAdmin", "true")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s (admin) = %d, want 200", path, resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return body
}

// ── clusters endpoint ─────────────────────────────────────────────────────────

// TestDashClustersEndpoint: /clusters returns a ClusterList the SPA's Destination
// column reads (items[].server/name/connectionState), with the fleet's app count,
// and NO cluster credential.
func TestDashClustersEndpoint(t *testing.T) {
	s := fakeService(
		appCR("App", "hanzo", "cloud", "u1", "ghcr.io/hanzoai/cloud", "v1", "Running", 1, 1),
		appCR("App", "hanzo", "iam", "u2", "ghcr.io/hanzoai/iam", "v1", "Running", 1, 1),
	)
	body := getJSON(t, s, "/v1/deploy/clusters")

	items, ok := body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("clusters items = %v, want exactly one (in-cluster)", body["items"])
	}
	c0 := items[0].(map[string]any)
	if c0["server"] != inClusterServer || c0["name"] != inClusterName {
		t.Fatalf("cluster[0] = %v, want in-cluster", c0)
	}
	if cs, _ := c0["connectionState"].(map[string]any); cs["status"] != "Successful" {
		t.Fatalf("connectionState = %v, want status Successful", c0["connectionState"])
	}
	if info, _ := c0["info"].(map[string]any); info["applicationsCount"].(float64) != 2 {
		t.Fatalf("applicationsCount = %v, want 2", c0["info"])
	}
	// Credential-leak guard at the HTTP boundary.
	raw, _ := json.Marshal(body)
	for _, forbidden := range []string{"config", "bearerToken", "tlsClientConfig", "execProviderConfig"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("clusters response leaked %q: %s", forbidden, raw)
		}
	}
}

// ── projects endpoint ─────────────────────────────────────────────────────────

// TestDashProjectsEndpoint_Synthesizes: with no AppProject CRD served, /projects
// synthesizes the distinct App-CR project set (default always present).
func TestDashProjectsEndpoint_Synthesizes(t *testing.T) {
	s := fakeService(appCR("App", "hanzo", "cloud", "u1", "ghcr.io/hanzoai/cloud", "v1", "Running", 1, 1))
	body := getJSON(t, s, "/v1/deploy/projects")

	items, ok := body["items"].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("projects items = %v, want at least [default]", body["items"])
	}
	found := false
	for _, it := range items {
		if it.(map[string]any)["metadata"].(map[string]any)["name"] == "default" {
			found = true
		}
	}
	if !found {
		t.Fatalf("projects missing 'default': %v", items)
	}
}

// TestDashProjectsEndpoint_PrefersRealCRs: when real AppProject CRs are served,
// /projects lists THOSE (not synthesized ones) and surfaces only intended fields.
func TestDashProjectsEndpoint_PrefersRealCRs(t *testing.T) {
	s := fakeService(
		appCR("App", "hanzo", "cloud", "u1", "ghcr.io/hanzoai/cloud", "v1", "Running", 1, 1),
		appProjectCR("team-a", "https://git.hanzo.ai/team-a/*"),
	)
	body := getJSON(t, s, "/v1/deploy/projects")
	items := body["items"].([]any)
	names := map[string]bool{}
	for _, it := range items {
		names[it.(map[string]any)["metadata"].(map[string]any)["name"].(string)] = true
	}
	if !names["team-a"] {
		t.Fatalf("real AppProject 'team-a' not listed: %v", names)
	}
	// The synthesized 'default' must NOT appear when real projects are preferred.
	if names["default"] {
		t.Fatalf("synthesized 'default' present alongside real projects: %v", names)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), "secret-role") {
		t.Fatalf("real AppProject leaked role metadata: %s", raw)
	}
}

// ── auth: every new route is SuperAdmin-gated ─────────────────────────────────

// TestNewRoutesRequireAdmin: clusters, projects, and the stream must 403 without
// the SuperAdmin claim (fail-closed, no fleet/cluster data to an anonymous caller).
func TestNewRoutesRequireAdmin(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	routes(app, fakeService())
	for _, path := range []string{"/v1/deploy/clusters", "/v1/deploy/projects", "/v1/deploy/stream/applications"} {
		// EventSource sends Accept: text/event-stream + Sec-Fetch-Dest: empty, so a
		// non-admin gets a 403 (not the browser-document redirect) — assert both.
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Sec-Fetch-Dest", "empty")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		code := resp.StatusCode
		_ = resp.Body.Close()
		if code != http.StatusForbidden {
			t.Errorf("GET %s WITHOUT admin = %d, want 403", path, code)
		}
	}
}

// ── stream: SSE headers ───────────────────────────────────────────────────────

// TestStreamSetsSSEHeaders: the handler sets the SSE response headers. Tested via
// the pure header helper so no body-stream goroutine is spawned (fasthttp starts
// the writer eagerly on SendStreamWriter — see setStreamHeaders).
func TestStreamSetsSSEHeaders(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	c := app.TestCtx("GET", "/v1/deploy/stream/applications")
	setStreamHeaders(c)
	resp := c.Fiber().Response()
	if ct := string(resp.Header.Peek("Content-Type")); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := string(resp.Header.Peek("Cache-Control")); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	if conn := string(resp.Header.Peek("Connection")); conn != "keep-alive" {
		t.Errorf("Connection = %q, want keep-alive", conn)
	}
	if b := string(resp.Header.Peek("X-Accel-Buffering")); b != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no (defeat proxy buffering)", b)
	}
}

// TestStreamFailsClosedWithoutCluster: no k8s client → the handler 503s BEFORE
// any streaming (fail-closed), never a fabricated 200 stream.
func TestStreamFailsClosedWithoutCluster(t *testing.T) {
	noK8s := &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{initErr: "no kubeconfig"}}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	c := app.TestCtx("GET", "/v1/deploy/stream/applications")
	err := dashStreamApps(noK8s, c)
	if err == nil {
		t.Fatal("dashStreamApps with no cluster client = nil error, want 503")
	}
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusServiceUnavailable {
		t.Fatalf("stream no-cluster error = %v, want 503 HTTPError", err)
	}
}

// ── stream: burst core ────────────────────────────────────────────────────────

// TestStreamBurstEmitsAddedPerApp: the burst emits one ArgoCD ADDED watch event
// per current App CR — the ApplicationWatchEvent envelope the SPA parses — with
// the argo Application shape (status.health/sync), projected identically to
// dashAppList.
func TestStreamBurstEmitsAddedPerApp(t *testing.T) {
	s := fakeService(
		appCR("App", "hanzo", "cloud", "u1", "ghcr.io/hanzoai/cloud", "v1", "Running", 1, 1),
		appCR("App", "hanzo", "iam", "u2", "ghcr.io/hanzoai/iam", "v1", "Running", 1, 1),
	)
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if ok := streamAppBurst(s, superScope(), context.Background(), w); !ok {
		t.Fatal("streamAppBurst returned false (write failed) on a live buffer")
	}
	_ = w.Flush()

	frames := parseSSE(buf.String())
	if len(frames) != 2 {
		t.Fatalf("burst emitted %d frames, want 2 (one per app): %q", len(frames), buf.String())
	}
	names := map[string]bool{}
	for _, f := range frames {
		var env struct {
			Result applicationWatchEvent `json:"result"`
		}
		if err := json.Unmarshal([]byte(f), &env); err != nil {
			t.Fatalf("frame is not the {result:{...}} envelope: %q (%v)", f, err)
		}
		if env.Result.Type != "ADDED" {
			t.Fatalf("event type = %q, want ADDED", env.Result.Type)
		}
		if env.Result.Application.APIVersion != "argoproj.io/v1alpha1" || env.Result.Application.Kind != "Application" {
			t.Fatalf("application is not an argo Application: %+v", env.Result.Application)
		}
		if env.Result.Application.Status.Health.Status == "" || env.Result.Application.Status.Sync.Status == "" {
			t.Fatalf("application missing health/sync: %+v", env.Result.Application.Status)
		}
		names[env.Result.Application.Metadata.Name] = true
	}
	if !names["cloud"] || !names["iam"] {
		t.Fatalf("burst missing an app: got %v", names)
	}
}

// TestStreamBurstZeroAppsNoPanic: an empty fleet emits nothing, does not panic,
// and reports the client is still connected.
func TestStreamBurstZeroAppsNoPanic(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if ok := streamAppBurst(fakeService(), superScope(), context.Background(), w); !ok {
		t.Fatal("streamAppBurst on zero apps returned false, want true")
	}
	_ = w.Flush()
	if buf.Len() != 0 {
		t.Fatalf("zero-app burst wrote %q, want nothing", buf.String())
	}
}

// TestStreamHonorsContextCancel: the watch loop returns promptly when the request
// context is canceled — no hung goroutine, no leaked watch. Cancel drives the
// return directly (the keep-alive interval is irrelevant), so this needs no global
// tuning and cannot race a concurrent stream.
func TestStreamHonorsContextCancel(t *testing.T) {
	s := fakeService(appCR("App", "hanzo", "cloud", "u1", "ghcr.io/hanzoai/cloud", "v1", "Running", 1, 1))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		streamApps(s, superScope(), ctx, bufio.NewWriter(io.Discard))
		close(done)
	}()
	// Let the burst + watch establish, then cancel.
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamApps did not return within 2s of context cancel (goroutine/watch leak)")
	}
}

// TestForwardWatchSkipsTypedNilObjectNotFatal proves a malformed watch event
// carrying a typed-nil *unstructured.Unstructured is skipped, not fatal: the
// forwardWatch goroutine must not nil-deref on GetName() and a following valid
// event must still forward. A watch event is a system boundary and the read
// plane installs no panic recovery — a crash here would take down the process.
func TestForwardWatchSkipsTypedNilObjectNotFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw := watch.NewFake()
	events := make(chan streamEvent, 4)
	go forwardWatch(ctx, superScope(), "hanzo", fw, events)

	go func() {
		// a typed-nil object as ADDED: e.Object.(*unstructured.Unstructured) is
		// (nil, true), so the pre-guard code nil-derefs on GetName().
		fw.Action(watch.Added, (*unstructured.Unstructured)(nil))
		// then a valid App CR in a platform namespace — must still come through.
		fw.Action(watch.Added, appCR("App", "hanzo", "cloud", "u1", "ghcr.io/hanzoai/cloud", "v1", "Running", 1, 1))
	}()

	select {
	case ev := <-events:
		if ev.obj == nil || ev.obj.GetName() != "cloud" {
			t.Fatalf("expected the valid App CR to forward; got %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("valid event never arrived — forwardWatch died on the typed-nil object")
	}
}

// ── watchType mapping ─────────────────────────────────────────────────────────

func TestWatchTypeMapsArgoVocab(t *testing.T) {
	cases := map[string]string{"ADDED": "ADDED", "MODIFIED": "MODIFIED", "DELETED": "DELETED", "BOOKMARK": "", "ERROR": ""}
	for in, want := range cases {
		if got := watchType(watch.EventType(in)); got != want {
			t.Errorf("watchType(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── tiny local helpers (no new deps) ─────────────────────────────────────────

// parseSSE extracts the JSON payload of each `data: …` frame, ignoring keep-alive
// comment lines (`: …`).
func parseSSE(s string) []string {
	var out []string
	for _, block := range strings.Split(s, "\n\n") {
		line := strings.TrimSpace(block)
		if strings.HasPrefix(line, "data: ") {
			out = append(out, strings.TrimPrefix(line, "data: "))
		}
	}
	return out
}
