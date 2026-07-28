package bot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// botTestCfg replaces fiber's Test() default of a 1s WALL-CLOCK deadline on an
// in-process request: under load a correct handler blows it and the test reports
// an i/o timeout, which teaches nothing. The generous bound still fails a hang.
var botTestCfg = fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}

// mountBot builds the surface over a registry the test controls, through the same
// routes() the binary calls — so what a test drives is the code that ships.
func mountBot(t *testing.T, reg *Registry) *zip.App {
	t.Helper()
	deps := cloud.Deps{Logger: luxlog.New("test"), Version: "test"}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "bot"),
		State: state{reg: reg},
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	routes(app, s, deps)
	return app
}

// botCall sends a request with the gateway-minted identity headers. A non-empty
// org also sets X-User-Id (the validated principal); an empty org sends neither,
// which is the off-gateway shape.
func botCall(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(req, botTestCfg)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// connect attaches a node that answers every invocation with reply.
func connect(t *testing.T, reg *Registry, org, node, platform string, commands []string, reply string) {
	t.Helper()
	s := &Session{
		Key:         NodeKey{Org: org, NodeID: node},
		ConnID:      "conn-" + org + "-" + node,
		Platform:    platform,
		Commands:    commands,
		ConnectedAt: time.Unix(1<<30, 0),
		send: func(_ context.Context, b []byte) error {
			req, err := decodeInvoke(b)
			if err != nil {
				return err
			}
			go reg.Answer(req.ID, InvokeResult{OK: true, Payload: []byte(reply)})
			return nil
		},
	}
	if err := reg.Register(s); err != nil {
		t.Fatalf("attach %s/%s: %v", org, node, err)
	}
}

func gated() *Registry { return NewRegistry(WithGate(gate(Mode{Auth: AuthIAM}))) }

// The whole reason for the port, at the HTTP face: two orgs may run a node with
// the SAME id and never see each other's.
func TestNodesListIsOrgScoped(t *testing.T) {
	reg := gated()
	connect(t, reg, "acme", "laptop-1", "darwin", []string{"canvas.snapshot"}, `{"a":1}`)
	connect(t, reg, "globex", "laptop-1", "darwin", []string{"canvas.snapshot"}, `{"g":1}`)
	app := mountBot(t, reg)

	for _, org := range []string{"acme", "globex"} {
		code, body := botCall(t, app, http.MethodGet, "/v1/bot/nodes", org, nil)
		if code != http.StatusOK {
			t.Fatalf("%s list: %d (%s)", org, code, body)
		}
		var v nodesView
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("decode: %v (%s)", err, body)
		}
		if len(v.Nodes) != 1 || v.Nodes[0].ID != "laptop-1" {
			t.Fatalf("%s sees %+v, want exactly its own laptop-1", org, v.Nodes)
		}
	}

	// A third org sees an empty list, never null.
	code, body := botCall(t, app, http.MethodGet, "/v1/bot/nodes", "initech", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"nodes":[]`) {
		t.Fatalf("an org with no nodes got %d %s", code, body)
	}
}

// The org is the gateway's verdict. Off-gateway the identity middleware restores
// a forged X-Org-Id but no user, and that request must be refused, not served.
func TestNodeRoutesRefuseAnUnvalidatedCaller(t *testing.T) {
	reg := gated()
	connect(t, reg, "acme", "n1", "darwin", []string{"canvas.snapshot"}, `{}`)
	app := mountBot(t, reg)

	for _, path := range []string{"/v1/bot/nodes", "/v1/bot/connect"} {
		if code, _ := botCall(t, app, http.MethodGet, path, "", nil); code != http.StatusForbidden {
			t.Fatalf("GET %s with no principal = %d, want 403", path, code)
		}
	}
	if code, _ := botCall(t, app, http.MethodPost, "/v1/bot/nodes/n1/invoke", "", map[string]any{"command": "canvas.snapshot"}); code != http.StatusForbidden {
		t.Fatalf("invoke with no principal = %d, want 403", code)
	}

	// A forged org with NO validated user is the same refusal.
	req := httptest.NewRequest(http.MethodGet, "/v1/bot/nodes", nil)
	req.Header.Set("X-Org-Id", "acme") // forged; no X-User-Id
	resp, err := app.Fiber().Test(req, botTestCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a forged org with no principal listed nodes: %d", resp.StatusCode)
	}
}

func TestInvokeReachesTheNodeAndPassesItsAnswerBack(t *testing.T) {
	reg := gated()
	connect(t, reg, "acme", "laptop-1", "darwin", []string{"canvas.snapshot"}, `{"shot":"ok"}`)
	app := mountBot(t, reg)

	code, body := botCall(t, app, http.MethodPost, "/v1/bot/nodes/laptop-1/invoke", "acme",
		map[string]any{"command": "canvas.snapshot", "params": map[string]any{"full": true}})
	if code != http.StatusOK {
		t.Fatalf("invoke: %d (%s)", code, body)
	}
	var v invokeView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if !v.OK || string(v.Payload) != `{"shot":"ok"}` {
		t.Fatalf("answer was %+v, want the node's own payload", v)
	}
}

// A node belonging to another tenant must answer EXACTLY what a node that does
// not exist answers — same status, same body — or the difference names it.
func TestForeignNodeAnswersLikeAnAbsentOne(t *testing.T) {
	reg := gated()
	connect(t, reg, "globex", "laptop-1", "darwin", []string{"canvas.snapshot"}, `{}`)
	app := mountBot(t, reg)

	call := map[string]any{"command": "canvas.snapshot"}
	foreignCode, foreignBody := botCall(t, app, http.MethodPost, "/v1/bot/nodes/laptop-1/invoke", "acme", call)
	absentCode, absentBody := botCall(t, app, http.MethodPost, "/v1/bot/nodes/no-such-machine/invoke", "acme", call)

	if foreignCode != http.StatusNotFound {
		t.Fatalf("another tenant's node answered %d (%s), want 404", foreignCode, foreignBody)
	}
	if foreignCode != absentCode || string(foreignBody) != string(absentBody) {
		t.Fatalf("a foreign node (%d %s) is distinguishable from an absent one (%d %s)",
			foreignCode, foreignBody, absentCode, absentBody)
	}

	// And the node in the other org was never asked.
	if code, _ := botCall(t, app, http.MethodGet, "/v1/bot/nodes", "acme", nil); code != http.StatusOK {
		t.Fatalf("list after the miss: %d", code)
	}
}

// The gate runs at the socket, so a command the node never declared is refused
// with its stable code and never written.
func TestInvokeIsPolicyChecked(t *testing.T) {
	reg := gated()
	connect(t, reg, "acme", "n1", "darwin", []string{"canvas.snapshot"}, `{}`)
	app := mountBot(t, reg)

	code, body := botCall(t, app, http.MethodPost, "/v1/bot/nodes/n1/invoke", "acme",
		map[string]any{"command": "system.run", "params": map[string]any{"command": []string{"rm", "-rf", "/"}}})
	if code != http.StatusForbidden {
		t.Fatalf("an undeclared command answered %d (%s), want 403", code, body)
	}
	var d deniedView
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if d.Code != CodeCommandNotDeclared {
		t.Fatalf("denial code %q, want %q", d.Code, CodeCommandNotDeclared)
	}
}

// A dangerous command is in no platform default: it is reachable only because an
// operator named it, and Deny takes it away again.
func TestDangerousCommandsNeedTheOperatorsOverlay(t *testing.T) {
	declared := []string{"camera.snap"}

	closed := gated()
	connect(t, closed, "acme", "n1", "darwin", declared, `{}`)
	code, body := botCall(t, mountBot(t, closed), http.MethodPost,
		"/v1/bot/nodes/n1/invoke", "acme", map[string]any{"command": "camera.snap"})
	if code != http.StatusForbidden || !strings.Contains(string(body), CodeCommandNotAllowlisted) {
		t.Fatalf("camera.snap was reachable by default: %d %s", code, body)
	}

	mode := Mode{Auth: AuthIAM, Allow: []string{"camera.snap"}}
	open := NewRegistry(WithGate(gate(mode)))
	connect(t, open, "acme", "n1", "darwin", declared, `{"jpg":"…"}`)
	if code, body := botCall(t, mountBot(t, open), http.MethodPost,
		"/v1/bot/nodes/n1/invoke", "acme", map[string]any{"command": "camera.snap"}); code != http.StatusOK {
		t.Fatalf("an operator-allowed command answered %d (%s), want 200", code, body)
	}

	denied := Mode{Auth: AuthIAM, Allow: []string{"camera.snap"}, Deny: []string{"camera.snap"}}
	last := NewRegistry(WithGate(gate(denied)))
	connect(t, last, "acme", "n1", "darwin", declared, `{}`)
	if code, _ := botCall(t, mountBot(t, last), http.MethodPost,
		"/v1/bot/nodes/n1/invoke", "acme", map[string]any{"command": "camera.snap"}); code != http.StatusForbidden {
		t.Fatalf("Deny did not win over Allow: %d", code)
	}
}

// A caller must not be able to approve its own shell command by asking to be
// approved. There is no approval registry, so the claim is refused outright —
// and the ordinary path is untouched.
func TestSystemRunCannotSelfApprove(t *testing.T) {
	reg := gated()
	var sent [][]byte
	s := &Session{
		Key: NodeKey{Org: "acme", NodeID: "n1"}, ConnID: "c1",
		Platform: "darwin", Commands: []string{CommandSystemRun},
		send: func(_ context.Context, b []byte) error {
			sent = append(sent, b)
			req, err := decodeInvoke(b)
			if err != nil {
				return err
			}
			go reg.Answer(req.ID, InvokeResult{OK: true, Payload: []byte(`{"exit":0}`)})
			return nil
		},
	}
	if err := reg.Register(s); err != nil {
		t.Fatal(err)
	}
	app := mountBot(t, reg)

	for _, tc := range []struct {
		name   string
		params map[string]any
		want   string
	}{
		{"no run id", map[string]any{"command": []string{"whoami"}, "approved": true}, CodeMissingRunID},
		{"named a run id", map[string]any{"command": []string{"whoami"}, "approved": true, "runId": "r-1"}, CodeApprovalsUnavail},
		{"claimed a decision", map[string]any{"command": []string{"whoami"}, "approvalDecision": "allow-always"}, CodeMissingRunID},
	} {
		code, body := botCall(t, app, http.MethodPost, "/v1/bot/nodes/n1/invoke", "acme",
			map[string]any{"command": CommandSystemRun, "params": tc.params})
		if code != http.StatusForbidden || !strings.Contains(string(body), tc.want) {
			t.Fatalf("%s: %d %s, want 403 %s", tc.name, code, body, tc.want)
		}
	}
	if len(sent) != 0 {
		t.Fatalf("a self-approved command reached the node: %s", sent[0])
	}

	// The same command without the claim runs, and what goes on the wire carries
	// no approval at all.
	code, body := botCall(t, app, http.MethodPost, "/v1/bot/nodes/n1/invoke", "acme",
		map[string]any{"command": CommandSystemRun, "params": map[string]any{"command": []string{"whoami"}}})
	if code != http.StatusOK {
		t.Fatalf("an ordinary system.run answered %d (%s)", code, body)
	}
	if len(sent) != 1 {
		t.Fatalf("the node was written %d times, want 1", len(sent))
	}
	if strings.Contains(string(sent[0]), "approved") {
		t.Fatalf("the forwarded params carry an approval claim: %s", sent[0])
	}
}

// The surface is /v1/bot/*. Never /api/, and nothing outside the subsystem's own
// prefix.
func TestRoutesLiveUnderV1Bot(t *testing.T) {
	app := mountBot(t, gated())
	want := map[string]bool{
		"GET /v1/bot/connect":           false,
		"GET /v1/bot/nodes":             false,
		"POST /v1/bot/nodes/:id/invoke": false,
		"POST " + PeerInvokePath:        false,
	}
	for _, r := range app.Fiber().GetRoutes() {
		if strings.Contains(r.Path, "/api/") {
			t.Fatalf("route %s %s carries an /api/ prefix", r.Method, r.Path)
		}
		if !strings.HasPrefix(r.Path, "/v1/bot") {
			continue
		}
		key := r.Method + " " + r.Path
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected route %s", key)
		}
		want[key] = true
	}
	for k, seen := range want {
		if !seen {
			t.Fatalf("route %s is not registered", k)
		}
	}
}
