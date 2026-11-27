package compute

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// doComputer is do() with the one extra header this surface reads. The org and
// user headers are still the gateway-minted pair do() injects, so a caller
// cannot name another tenant by awarding itself an X-Org-Id.
func doComputer(t *testing.T, app *zip.App, path, org string, body any, approve string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(http.MethodPost, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u-"+org)
	}
	if approve != "" {
		req.Header.Set(computerApprovalHeader, approve)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// computerApp mounts the compute surface against a Visor fake that owns ONE
// machine ("drop-a", org acme) with a bound agent and serves the proposed
// computer relay address. That is the machine a bound agent drives.
func computerApp(t *testing.T) (*zip.App, *botVM) {
	t.Helper()
	f := newBotVM()
	f.bots["drop-a"] = map[string]any{"owner": "acme", "name": "drop-a", "id": "drop-a"}
	f.bindings["drop-a"] = agentBinding{
		Owner: "acme", Name: "drop-a", MachineId: "acme/drop-a", AgentName: "gui-agent", Status: "running",
	}
	return mountBots(t, f, ""), f
}

func TestComputerActionOrgScopedAndUnknownMachine(t *testing.T) {
	app, f := computerApp(t)
	const path = "/v1/compute/machines/drop-a/computer"

	// No validated principal: 401 before anything reaches the machine.
	if code, _ := doComputer(t, app, path, "", map[string]any{"action": "screenshot"}, ""); code != http.StatusUnauthorized {
		t.Errorf("no principal = %d, want 401", code)
	}

	// Another org's call to the same id is a 404, not the relay — the machine is
	// addressed under the caller's org exactly like every other machine route.
	if code, _ := doComputer(t, app, path, "globex", map[string]any{"action": "screenshot"}, ""); code != http.StatusNotFound {
		t.Errorf("foreign org = %d, want 404", code)
	}

	// A machine that does not exist at all is the same clean 404.
	if code, _ := doComputer(t, app, "/v1/compute/machines/no-such/computer", "acme",
		map[string]any{"action": "screenshot"}, ""); code != http.StatusNotFound {
		t.Errorf("unknown machine = %d, want 404", code)
	}
	if f.computerCalls != 0 {
		t.Fatalf("a refused call reached the relay %d time(s)", f.computerCalls)
	}

	// A machine with no bound agent has nothing to drive through.
	delete(f.bindings, "drop-a")
	if code, _ := doComputer(t, app, path, "acme", map[string]any{"action": "screenshot"}, ""); code != http.StatusConflict {
		t.Errorf("unbound machine = %d, want 409", code)
	}
	if f.computerCalls != 0 {
		t.Fatalf("an unbound machine reached the relay")
	}
}

func TestComputerActionGatesActsAndNotReads(t *testing.T) {
	app, f := computerApp(t)
	const path = "/v1/compute/machines/drop-a/computer"

	// A read is open: no approval, yet the action reaches the machine.
	code, body := doComputer(t, app, path, "acme", map[string]any{"action": "screenshot"}, "")
	if code != http.StatusOK {
		t.Fatalf("read without approval = %d (%s), want 200", code, body)
	}
	if f.computerCalls != 1 || f.computerBody["action"] != "screenshot" {
		t.Fatalf("read not forwarded: calls=%d body=%+v", f.computerCalls, f.computerBody)
	}
	if f.computerBody["approval"] != false {
		t.Fatalf("read must not assert approval: %+v", f.computerBody)
	}

	// An act without evidence is refused BEFORE the relay is touched.
	before := f.computerCalls
	code, body = doComputer(t, app, path, "acme", map[string]any{
		"action": "click", "args": map[string]any{"x": 10, "y": 20},
	}, "")
	if code != http.StatusForbidden || !strings.Contains(string(body), "approval_required") {
		t.Fatalf("act without approval = %d (%s), want 403 approval_required", code, body)
	}
	if f.computerCalls != before {
		t.Fatalf("an unapproved act reached the relay")
	}

	// The same act with the body flag is forwarded with evidence in BOTH
	// spellings hanzod accepts.
	code, body = doComputer(t, app, path, "acme", map[string]any{
		"action": "computer.click", "args": map[string]any{"x": 10, "y": 20}, "approval": true,
	}, "")
	if code != http.StatusOK {
		t.Fatalf("approved act = %d (%s), want 200", code, body)
	}
	if f.computerBody["action"] != "click" || f.computerBody["approval"] != true {
		t.Fatalf("act body not normalized/forwarded: %+v", f.computerBody)
	}
	if f.computerApproveHdr != "true" {
		t.Fatalf("approval header not forwarded: %q", f.computerApproveHdr)
	}
	args, _ := json.Marshal(f.computerBody["args"])
	if !strings.Contains(string(args), `"x":10`) {
		t.Fatalf("args not forwarded verbatim: %s", args)
	}

	// The header alone is evidence too.
	code, _ = doComputer(t, app, path, "acme", map[string]any{"action": "type"}, "true")
	if code != http.StatusOK {
		t.Fatalf("header-approved act = %d, want 200", code)
	}
	if f.computerApproveHdr != "true" || f.computerBody["approval"] != true {
		t.Fatalf("header approval not forwarded: hdr=%q body=%+v", f.computerApproveHdr, f.computerBody)
	}

	// An action this Cloud does not know is an act, not a free read.
	before = f.computerCalls
	code, _ = doComputer(t, app, path, "acme", map[string]any{"action": "future_op"}, "")
	if code != http.StatusForbidden {
		t.Fatalf("unknown action without approval = %d, want 403", code)
	}
	if f.computerCalls != before {
		t.Fatalf("unknown action reached the relay without approval")
	}
	code, _ = doComputer(t, app, path, "acme", map[string]any{"action": "future_op", "approval": true}, "")
	if code != http.StatusOK || f.computerBody["action"] != "future_op" {
		t.Fatalf("approved unknown action = %d, body=%+v", code, f.computerBody)
	}
}

func TestComputerActionUpstreamStatusAndBodyPassThrough(t *testing.T) {
	app, f := computerApp(t)
	const path = "/v1/compute/machines/drop-a/computer"

	// A hanzod-shaped non-2xx keeps its status, content type and EXACT bytes.
	reply := []byte(`{"error":"approval_required","detail":"an operator must click OK"}`)
	f.computerStatus = http.StatusTeapot
	f.computerCT = "application/vnd.hanzo.computer+json"
	f.computerReply = reply
	code, body := doComputer(t, app, path, "acme",
		map[string]any{"action": "click", "approval": true}, "")
	if code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418", code)
	}
	if !bytes.Equal(body, reply) {
		t.Fatalf("body = %q, want %q", body, reply)
	}

	// A missing Visor endpoint is honest 503, never its HTML error page.
	f.computerStatus = http.StatusNotFound
	f.computerCT = "text/html"
	f.computerReply = []byte("<html><body>404 Not Found</body></html>")
	code, body = doComputer(t, app, path, "acme",
		map[string]any{"action": "screenshot"}, "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("visor 404 = %d, want 503", code)
	}
	if bytes.Contains(body, []byte("404 Not Found")) {
		t.Fatalf("upstream HTML leaked: %s", body)
	}
}

func TestComputerActionClassificationMatchesVocabulary(t *testing.T) {
	for _, act := range computerActActions {
		if computerNeedsApproval(act) {
			continue
		}
		t.Errorf("%q is a known act but is treated as a read", act)
	}
	for read := range computerReadActions {
		if !computerNeedsApproval(read) {
			continue
		}
		t.Errorf("%q is a known read but needs approval", read)
	}
	// The node plane's declared computer commands are the same vocabulary: a
	// prefix-normalised name must land in exactly one of the two sets.
	for _, cmd := range []string{"computer.click", "computer.screenshot", "computer.batch", "computer.screen_size"} {
		name := computerActionName(cmd)
		if name == "" || slices.Contains([]string{"click", "screenshot", "batch", "screen_size"}, name) == false {
			t.Errorf("normalizing %q gave %q", cmd, name)
		}
	}
}
