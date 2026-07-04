package automations

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
)

// probe is a TEST-ONLY connector (registered only in _test builds) whose record
// action captures the resolved input it received — so a durable flow can PROVE, end
// to end, that a later step saw an earlier step's threaded output.
var (
	probeMu   sync.Mutex
	probeSeen []string
)

func init() {
	register(&Connector{
		Name:        "probe",
		DisplayName: "Probe",
		AuthType:    "none",
		Actions: map[string]*Action{
			"record": {
				Name: "record", DisplayName: "Record", Description: "test probe: record resolved input",
				Run: func(_ context.Context, rc RunContext) (any, error) {
					probeMu.Lock()
					probeSeen = append(probeSeen, strInput(rc.Input, "seen"))
					probeMu.Unlock()
					return map[string]any{"recorded": strInput(rc.Input, "seen")}, nil
				},
			},
		},
	})
}

func probeReset() { probeMu.Lock(); probeSeen = nil; probeMu.Unlock() }
func probeSnapshot() []string {
	probeMu.Lock()
	defer probeMu.Unlock()
	return append([]string(nil), probeSeen...)
}

// TestDurableFlowRunSucceeds proves a flow runs DURABLY on hanzoai/tasks: it embeds a
// real tasks engine (like examples/embed.go), registers FlowRunWorkflow +
// ExecuteStepActivity on a worker, runs a flow of core steps (code + delay + a test
// probe), and asserts the run reaches SUCCEEDED with the step outputs THREADED (the
// probe saw the code step's output resolved through {{step1.greeting}}).
func TestDurableFlowRunSucceeds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	srv, err := tasksengine.Embed(ctx, tasksengine.EmbedConfig{ZAPPort: port, Namespace: "acme", DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	defer func() { _ = srv.Stop(context.Background()) }()

	cli, err := tasksclient.Dial(tasksclient.Options{
		HostPort: fmt.Sprintf("127.0.0.1:%d", port), Namespace: "acme",
		DialTimeout: 5 * time.Second, CallTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()

	w := tasksworker.New(cli, automationsTaskQueue, tasksworker.Options{})
	w.RegisterWorkflow(FlowRunWorkflow)
	w.RegisterActivity(ExecuteStepActivity)
	if err := w.Start(); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	defer w.Stop()

	probeReset()
	in := FlowRunInput{
		Owner: "acme", FlowID: "f1", FlowVersionID: "v1", RunID: "run_durable_1",
		Steps: []FlowStep{
			{Name: "step1", PieceName: corePiece, ActionName: "code", Input: map[string]any{"greeting": "hi"}},
			{Name: "delay1", PieceName: corePiece, ActionName: "delay", Input: map[string]any{"seconds": 0.01}},
			{Name: "probe1", PieceName: "probe", ActionName: "record", Input: map[string]any{"seen": "{{step1.greeting}} there"}},
		},
	}
	run, err := cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{ID: in.RunID, TaskQueue: automationsTaskQueue}, FlowRunWorkflow, in)
	if err != nil {
		t.Fatalf("execute workflow: %v", err)
	}
	// Get(nil) blocks until a terminal state; a non-Completed terminal returns an error.
	if err := run.Get(ctx, nil); err != nil {
		t.Fatalf("flow did not reach SUCCEEDED: %v", err)
	}

	info, err := cli.DescribeWorkflow(ctx, in.RunID, "")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if got := mapWorkflowStatus(info.Status); got != RunSucceeded {
		t.Fatalf("run status want SUCCEEDED, got %q (engine status %d)", got, info.Status)
	}

	// Threading proven end-to-end: the probe step saw step1's output.
	seen := probeSnapshot()
	if len(seen) != 1 || seen[0] != "hi there" {
		t.Fatalf("step outputs not threaded through the durable run: probe saw %v", seen)
	}
}

// TestTokenIsolation is the cross-tenant credential-leak test RED must scrutinize: an
// activity's ONLY credential scope is in.Owner (the VALIDATED org). A FORGED
// in.Input.owner cannot widen it. The step's connector id is the ONLY provider.
func TestTokenIsolation(t *testing.T) {
	type call struct{ org, provider string }
	var (
		mu    sync.Mutex
		calls []call
	)
	orig := tokenSource
	tokenSource = func(_ context.Context, org, provider, _ string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, call{org, provider})
		mu.Unlock()
		return nil, fmt.Errorf("not connected")
	}
	t.Cleanup(func() { tokenSource = orig })

	// Org A runs a slack step; a hostile Input carries owner=globex to try to smuggle
	// another tenant's scope. The activity must tokenize ONLY as (acme, slack).
	in := StepInput{
		Owner: "acme", PieceName: "slack", ActionName: "send_message",
		Input: map[string]any{"owner": "globex", "org": "globex", "channel": "C1", "text": "hi"},
	}
	if _, err := ExecuteStepActivity(context.Background(), in); err == nil {
		t.Fatal("slack step must fail closed when not connected")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one credential fetch, got %d: %+v", len(calls), calls)
	}
	if calls[0].org != "acme" {
		t.Fatalf("credential scope MUST be in.Owner=acme; forged input widened it to %q", calls[0].org)
	}
	if calls[0].provider != "slack" {
		t.Fatalf("credential provider MUST be the step's connector=slack, got %q", calls[0].provider)
	}
}

// TestStepOutputsThreaded proves the reference-resolution mechanism directly: an
// activity resolves {{step.path}} against the threaded prior outputs, preserving type
// for a whole-string reference.
func TestStepOutputsThreaded(t *testing.T) {
	// String-embedded reference → string substitution.
	out, err := ExecuteStepActivity(context.Background(), StepInput{
		Owner: "acme", PieceName: corePiece, ActionName: "code", Name: "step2",
		Input:       map[string]any{"echo": "{{step1.greeting}} world"},
		PrevOutputs: map[string]any{"step1": map[string]any{"greeting": "hello"}},
	})
	if err != nil {
		t.Fatalf("code activity: %v", err)
	}
	m, ok := out.Output.(map[string]any)
	if !ok || m["echo"] != "hello world" {
		t.Fatalf("embedded reference not threaded, got %+v", out.Output)
	}

	// Whole-string reference → value with type preserved (number stays number).
	out2, err := ExecuteStepActivity(context.Background(), StepInput{
		Owner: "acme", PieceName: corePiece, ActionName: "code", Name: "step3",
		Input:       map[string]any{"n": "{{step1.count}}"},
		PrevOutputs: map[string]any{"step1": map[string]any{"count": float64(5)}},
	})
	if err != nil {
		t.Fatalf("code activity 2: %v", err)
	}
	m2 := out2.Output.(map[string]any)
	if m2["n"] != float64(5) {
		t.Fatalf("whole-string reference must preserve type, got %T %v", m2["n"], m2["n"])
	}
}

// TestHTTPRequestSSRFBlocked proves the http_request SSRF guard refuses a non-public
// target by default (the DNS-rebind TOCTOU window is closed by the dialer Control hook).
func TestHTTPRequestSSRFBlocked(t *testing.T) {
	_, err := runHTTPRequest(context.Background(), RunContext{Input: map[string]any{"url": "http://127.0.0.1:9/x"}})
	if err == nil || !strings.Contains(err.Error(), "ssrf") {
		t.Fatalf("loopback target must be SSRF-refused, got err=%v", err)
	}
}

// TestHTTPRequestHappyPath proves the success path (with the private-allow override a
// test uses to hit a loopback stub): status + body are returned.
func TestHTTPRequestHappyPath(t *testing.T) {
	t.Setenv(httpAllowPrivateEnv, "1")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	out, err := runHTTPRequest(context.Background(), RunContext{Input: map[string]any{"url": ts.URL}})
	if err != nil {
		t.Fatalf("http_request: %v", err)
	}
	m := out.(map[string]any)
	if m["status"] != http.StatusOK {
		t.Fatalf("status want 200, got %v", m["status"])
	}
	if !strings.Contains(m["body"].(string), "ok") {
		t.Fatalf("body want ok, got %v", m["body"])
	}
}
