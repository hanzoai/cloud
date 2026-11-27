package cloud

// The agent MCP server, from both directions, against ONE subsystem.
//
// WHAT IT CAUGHT. Every tool the @hanzo Slack agent called answered `X-Org-Id
// required` or `sign in to use`, while tools/list and describe worked. The run
// holds a principal it resolved SERVER-SIDE and no bearer to replay for it, and
// the MCP server forwarded it into the subsystem's EDGE endpoint — where the
// identity boundary deletes every authority header and re-mints one only from a
// credential it verified. So X-User-Id was gone by the time the op read it,
// principal.OrgOf refused the org that had ridden along, and the op refused the
// caller.
//
// The plugin's own request log said `org=hanzo user=hanzo/z@hanzo.ai` for that
// exact request, which is what made it look impossible. It is not: zip reports the
// caller as a request BEGINS (telemetry.go, describe before Continue), so the line
// says what ARRIVED, and the op reads what SURVIVED. Both halves are pinned below.
//
// The two cases differ in ONE value — which endpoint the hop goes to — and that
// is the whole fix: an internal caller reaches the subsystem's plane endpoint,
// where its statement is worth what the socket is worth; a stranger reaches the
// edge endpoint, where the boundary judges it.
// TestForgedIdentityStillDiesAtTheEdge is the half that must never move.

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// seen is what a typed op resolves about its caller: the tenant it would scope a
// query by, and whether there is a validated principal behind it at all. These are
// the two facts every org-scoped op in the fleet gates on.
type seen struct {
	Org       string `json:"org"`
	OrgOK     bool   `json:"org_ok"`
	Validated bool   `json:"validated"`
}

// tenantOp is the one op under test, and it resolves its tenant the way every
// org-scoped op does — principal.OrgFrom / ValidatedFrom, never a header.
const tenantOp = "probe_tenant"

// accepts blocks until an address has a listener behind it, so "the subsystem is
// up" is a fact rather than an intention — the same wait cloud.ServePlane makes
// before it reports a plane bound.
//
// The network is read off the address the way zip reads it (transport.go
// `networkOf`): a path is a unix socket, anything else is tcp. One waiter for
// both, because "is it listening" is one question and a second copy of it is
// how the two come to disagree about what listening means.
func accepts(t *testing.T, addr string) {
	t.Helper()
	network := "tcp"
	if strings.HasPrefix(addr, "/") {
		network = "unix"
	}
	for range 400 {
		if c, err := net.Dial(network, addr); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never began listening", addr)
}

// tool runs tenantOp through one endpoint as (org, user) and reads back what the
// op resolved.
func tool(t *testing.T, endpoint *zip.App, org, user string) seen {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tenantOp + `","arguments":{}}}`
	req, err := http.NewRequest("POST", "http://cloud"+manifest.MCPPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(zip.HeaderOrg, org)
	if user != "" {
		req.Header.Set(zip.HeaderUser, user)
	}
	resp, err := endpoint.Test(req, zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST %s: %v", manifest.MCPPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("the endpoint answered something that is not JSON-RPC: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("the endpoint refused to route %s: %s", tenantOp, env.Error.Message)
	}
	if env.Result.IsError || len(env.Result.Content) == 0 {
		t.Fatalf("tools/call returned no result (isError=%v)", env.Result.IsError)
	}
	var got seen
	if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &got); err != nil {
		t.Fatalf("the content is not the op's output: %v (%s)", err, env.Result.Content[0].Text)
	}
	return got
}
