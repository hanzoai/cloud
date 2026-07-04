package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/audit"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	tasksengine "github.com/hanzoai/tasks/pkg/tasks"
	"github.com/zap-proto/zip"
)

// ── MED-1: exactly-once run bookkeeping across every entrypoint ──────────────

// TestScheduledRunMeteredExactlyOnce proves the durable path is the single owner of
// run bookkeeping: a scheduled tick (a FlowRunWorkflow started by workflow id, as the
// cron scheduler does) meters+audits the run EXACTLY once and the run shows up in
// listRuns — keyed on the per-tick workflow id, so distinct ticks are distinct runs
// even though a schedule embeds one fixed FlowRunInput.RunID.
func TestScheduledRunMeteredExactlyOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	app, rec := newAppWithAudit(t) // Mount sets the package `mounted` used by the bookkeeping activity

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

	cli, err := tasksclient.Dial(tasksclient.Options{HostPort: fmt.Sprintf("127.0.0.1:%d", port), Namespace: "acme", DialTimeout: 5 * time.Second, CallTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()

	w := tasksworker.New(cli, automationsTaskQueue, tasksworker.Options{})
	w.RegisterWorkflow(FlowRunWorkflow)
	w.RegisterActivity(ExecuteStepActivity)
	w.RegisterActivity(RecordRunStartActivity)
	w.RegisterActivity(RecordRunEndActivity)
	if err := w.Start(); err != nil {
		t.Fatalf("worker start: %v", err)
	}
	defer w.Stop()

	// A tick starts FlowRunWorkflow with a per-tick workflow id; in.RunID is the
	// schedule's FIXED id (same for every tick) — proving the run key is the workflow id.
	tick := func(wfID string) {
		in := FlowRunInput{Owner: "acme", FlowID: "f1", FlowVersionID: "v1", RunID: "sched-f1",
			Steps: []FlowStep{{Name: "s1", PieceName: corePiece, ActionName: "code", Input: map[string]any{"k": "v"}}}}
		run, err := cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{ID: wfID, TaskQueue: automationsTaskQueue}, FlowRunWorkflow, in)
		if err != nil {
			t.Fatalf("tick %s: %v", wfID, err)
		}
		if err := run.Get(ctx, nil); err != nil {
			t.Fatalf("tick %s get: %v", wfID, err)
		}
	}

	tick("tick-1")
	// Exactly one flow.run audit record ⇒ exactly one meter (same won-guard).
	if n := auditCount(t, rec, "acme", "automations.flow.run"); n != 1 {
		t.Fatalf("first tick metered %d times, want 1", n)
	}
	runs := listRunsHTTP(t, app, "acme")
	if len(runs) != 1 || runs[0].ID != "tick-1" {
		t.Fatalf("listRuns want [tick-1], got %+v", runs)
	}

	tick("tick-2")
	if n := auditCount(t, rec, "acme", "automations.flow.run"); n != 2 {
		t.Fatalf("after two ticks metered %d total, want 2", n)
	}
	runs = listRunsHTTP(t, app, "acme")
	ids := map[string]bool{}
	for _, r := range runs {
		ids[r.ID] = true
	}
	if len(runs) != 2 || !ids["tick-1"] || !ids["tick-2"] {
		t.Fatalf("two ticks must be two distinct runs, got %+v", runs)
	}
}

// TestRunStartBookkeepingIdempotent proves recordRunStart is idempotent by run id:
// calling it twice for the same run (the manual handler's row-create + the durable
// activity, or an activity retry) meters + audits ONCE and lands ONE run row.
func TestRunStartBookkeepingIdempotent(t *testing.T) {
	_, rec := newAppWithAudit(t)
	ctx := context.Background()
	in := RunStartInput{RunID: "r1", Owner: "acme", FlowID: "f1", FlowVersionID: "v1"}

	if err := mounted.recordRunStart(ctx, in); err != nil {
		t.Fatalf("recordRunStart 1: %v", err)
	}
	if err := mounted.recordRunStart(ctx, in); err != nil {
		t.Fatalf("recordRunStart 2 (retry): %v", err)
	}
	if n := auditCount(t, rec, "acme", "automations.flow.run"); n != 1 {
		t.Fatalf("recordRunStart double-billed: %d flow.run records, want 1", n)
	}
	runs, _ := mounted.store.ListRuns(ctx, "acme", "", 10)
	if len(runs) != 1 || runs[0].ID != "r1" {
		t.Fatalf("want exactly one run row r1, got %+v", runs)
	}
}

// listRunsHTTP exercises the real GET /v1/automations/runs endpoint (org-gated) and
// returns the rows — proving a run "shows up in listRuns", not just in the store.
func listRunsHTTP(t *testing.T, app *zip.App, org string) []FlowRun {
	t.Helper()
	r := req(t, app, http.MethodGet, "/v1/automations/runs", org, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("listRuns want 200, got %d (%s)", r.Code, r.Body)
	}
	var out struct {
		Data []FlowRun `json:"data"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("listRuns body: %v (%s)", err, r.Body)
	}
	return out.Data
}

// ── MED-2: SSRF blocklist covers IANA special-use ranges ────────────────────

func TestIsPublicIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", // loopback + RFC1918
		"169.254.169.254",    // AWS/GCP/Azure metadata (link-local)
		"100.100.100.200",    // Alibaba metadata (CGNAT)
		"100.64.0.1",         // CGNAT
		"192.0.0.1",          // IETF protocol
		"192.0.2.5",          // TEST-NET-1
		"192.88.99.1",        // 6to4 relay anycast
		"198.18.0.1",         // benchmarking
		"198.51.100.7",       // TEST-NET-2
		"203.0.113.9",        // TEST-NET-3
		"240.0.0.1",          // reserved / class E
		"0.0.0.0", "0.1.2.3", // this-network
		"::1", "fe80::1", "fc00::1", // v6 loopback/link-local/ULA
		"64:ff9b::808:808",       // NAT64 of 8.8.8.8
		"::ffff:10.0.0.1",        // v4-mapped private
		"::ffff:169.254.169.254", // v4-mapped metadata
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if isPublicIP(ip) {
			t.Fatalf("%s must be blocked (non-public)", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if !isPublicIP(net.ParseIP(s)) {
			t.Fatalf("%s must be allowed (public)", s)
		}
	}
}

// ── MED-3 + LOW-4: noisy-neighbor caps ──────────────────────────────────────

func TestFlowStepCapRejected(t *testing.T) {
	// A chain past the step cap is rejected at validation.
	var head *FlowAction
	for i := 0; i <= maxSteps; i++ {
		head = &FlowAction{Name: fmt.Sprintf("a%d", i), Type: ActionTypeCode, NextAction: head}
	}
	over := &FlowTrigger{Name: "t", Type: TriggerTypePiece, NextAction: head}
	if err := validateTrigger(over); err == nil {
		t.Fatalf("a flow of %d steps must be rejected", maxSteps+1)
	}
	// And over HTTP the create is an honest 422.
	app := newApp(t)
	r := req(t, app, http.MethodPost, "/v1/automations/flows", "acme", map[string]any{"displayName": "big", "trigger": over})
	if r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized flow create want 422, got %d (%s)", r.Code, r.Body)
	}

	// A within-cap flow passes.
	small := &FlowTrigger{Name: "t", Type: TriggerTypePiece, NextAction: &FlowAction{Name: "a", Type: ActionTypeCode}}
	if err := validateTrigger(small); err != nil {
		t.Fatalf("a small flow must pass: %v", err)
	}

	// A giant serialized tree (huge input blob) is rejected on size.
	big := &FlowTrigger{Name: "t", Type: TriggerTypePiece, NextAction: &FlowAction{
		Name: "a", Type: ActionTypeCode, Settings: StepSettings{Input: map[string]any{"blob": strings.Repeat("Z", maxTriggerBytes+1)}},
	}}
	if err := validateTrigger(big); err == nil {
		t.Fatal("an oversized flow tree must be rejected on size")
	}
}

func TestResumePayloadBounded(t *testing.T) {
	app := newApp(t)
	ctx := context.Background()
	if _, err := mounted.store.CreateRun(ctx, FlowRun{ID: "r1", Org: "acme", FlowID: "f1", WorkflowID: "r1", Status: RunPaused, Created: 1, Updated: 1}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	body := `{"note":"` + strings.Repeat("x", maxResumePayload+1) + `"}`
	r := reqRaw(t, app, "/v1/automations/runs/r1/resume", "acme", body)
	if r.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized resume want 413, got %d (%s)", r.Code, r.Body)
	}
}

// ── LOW-2: per-org concurrency cap ──────────────────────────────────────────

func TestConcurrencyLimiter(t *testing.T) {
	l := newConcurrencyLimiter(2)
	if !l.acquire("a") || !l.acquire("a") {
		t.Fatal("first two acquisitions must succeed")
	}
	if l.acquire("a") {
		t.Fatal("third acquisition must be refused at the cap")
	}
	if !l.acquire("b") {
		t.Fatal("a different org must be independent of a's cap")
	}
	l.release("a")
	if !l.acquire("a") {
		t.Fatal("after a release a slot must free")
	}
}

// ── LOW-1: MCP outcome is derived from the real result, after Run ───────────

func TestMCPAuditOutcome(t *testing.T) {
	app, rec := newAppWithAudit(t)
	// Success: core_code runs and returns.
	reqRaw(t, app, "/v1/automations/mcp", "acme",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"core_code","arguments":{"k":"v"}}}`)
	// Failure: slack is not connected → Run errors.
	reqRaw(t, app, "/v1/automations/mcp", "acme",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"slack_send_message","arguments":{"channel":"C","text":"hi"}}}`)

	rows, _, err := rec.Query(context.Background(), audit.Filter{Org: "acme", Action: "automations.mcp.call", Limit: 100})
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	var ok, bad int
	for _, r := range rows {
		switch r.Outcome.Result {
		case "ok":
			ok++
		case "error":
			bad++
		}
	}
	if ok != 1 || bad != 1 {
		t.Fatalf("want 1 ok + 1 error mcp.call audit (outcome from real result), got ok=%d error=%d (total %d)", ok, bad, len(rows))
	}
}

// ── LOW-3: publishedVersionId must name an in-org version of the flow ───────

func TestUpdateFlowPublishedVersionValidated(t *testing.T) {
	app := newApp(t)
	mk := func() populatedFlow {
		r := req(t, app, http.MethodPost, "/v1/automations/flows", "acme", map[string]any{
			"displayName": "F", "trigger": map[string]any{"name": "trigger", "type": TriggerTypePiece, "strategy": string(StrategyManual)},
		})
		var pf populatedFlow
		_ = json.Unmarshal(r.Body, &pf)
		return pf
	}
	a := mk()
	b := mk()

	// Valid: a's own version → 200.
	if r := req(t, app, http.MethodPatch, "/v1/automations/flows/"+a.ID, "acme", map[string]any{"publishedVersionId": a.Version.ID}); r.Code != http.StatusOK {
		t.Fatalf("valid publishedVersionId want 200, got %d (%s)", r.Code, r.Body)
	}
	// Bogus id → 422.
	if r := req(t, app, http.MethodPatch, "/v1/automations/flows/"+a.ID, "acme", map[string]any{"publishedVersionId": "ver_bogus"}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bogus publishedVersionId want 422, got %d", r.Code)
	}
	// A version that belongs to ANOTHER flow (same org) → 422.
	if r := req(t, app, http.MethodPatch, "/v1/automations/flows/"+a.ID, "acme", map[string]any{"publishedVersionId": b.Version.ID}); r.Code != http.StatusUnprocessableEntity {
		t.Fatalf("cross-flow publishedVersionId want 422, got %d", r.Code)
	}
}

// ── INF-1: tool-name collision refused at registration ──────────────────────

func TestToolNameCollisionPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a connector whose tool name collides with an existing one must panic at register")
		}
	}()
	// "core" already owns action "http_request" ⇒ tool "core_http_request". A
	// connector "core_http" with action "request" would collide into the SAME tool.
	register(&Connector{Name: "core_http", Actions: map[string]*Action{
		"request": {Name: "request", Run: func(context.Context, RunContext) (any, error) { return nil, nil }},
	}})
}
