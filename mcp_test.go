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
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud/internal/planetest"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/fleet"
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

// subsystem is one plugin process's worth of machinery: the identity boundary, one
// typed op, its EDGE endpoint on its own socket (zip's /mcp, what cloud.Serve
// leaves where the framework puts it), and its PLANE endpoint (cloud.UseMCP). It
// returns the two addresses a host can reach it at.
func subsystem(t *testing.T) (edge, plane string) {
	t.Helper()
	ResetPlane()
	t.Cleanup(ResetPlane)

	app := zip.New(zip.Config{AppName: "websearch", DisableStartupMessage: true})
	Identify(app, &Config{})
	zip.Post(app, "/v1/websearch/probe", func(ctx context.Context, _ *struct{}) (*seen, error) {
		org, ok := principal.OrgFrom(ctx)
		return &seen{Org: org, OrgOK: ok, Validated: principal.ValidatedFrom(ctx)}, nil
	}, zip.WithOperationID(tenantOp), zip.WithSummary("what this op resolves about its caller"))

	UseMCP(app)

	dir := planetest.Dir(t)
	edge, plane = filepath.Join(dir, "edge.sock"), filepath.Join(dir, "plane.sock")
	go func() { _ = app.Listen(edge) }()
	go func() { _ = Plane().Listen(plane) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	accepts(t, edge)
	accepts(t, plane)
	return edge, plane
}

// endpoints composes the fleet's MCP server at BOTH of its addresses over that one
// subsystem: the edge's, forwarding into the subsystem's edge endpoint, and the
// fleet's own internal socket, forwarding into its plane endpoint. Exactly the
// pair cmd/cloud registers (locate / inside).
func endpoints(t *testing.T) (fromEdge, fromInside *zip.App) {
	t.Helper()
	edge, plane := subsystem(t)

	fromEdge = zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})
	fromInside = zip.New(zip.Config{AppName: "plane", DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})

	d := fleet.Use(fromEdge, manifest.MCPPath, []string{"websearch"},
		func(string) (string, string, error) { return edge, manifest.FrameworkMCPPath, nil })
	// The MCP server lists from the build-time catalog and asks nothing, so a
	// child this binary did not build publishes nothing and every route below is
	// "unknown tool". Saying what it publishes is what fleet.MCP.Catalog is for;
	// the Doc is the sentence its own WithSummary carries, so what the MCP server
	// lists and what the op says about itself cannot drift apart here.
	d.Catalog = func(string) []fleet.Op {
		return []fleet.Op{{ID: tenantOp, Doc: "what this op resolves about its caller"}}
	}
	d.Serve(fromInside, manifest.MCPPath,
		func(string) (string, string, error) { return plane, manifest.MCPPath, nil })
	return fromEdge, fromInside
}

// TestInsideTheFleetTheCallerReachesTheOp is the fix. A sibling states the
// principal it resolved server-side, reaches the endpoint on the fleet's own
// socket, and the op resolves the tenant — which is what every tool the agent
// calls needs and what none of them got.
func TestInsideTheFleetTheCallerReachesTheOp(t *testing.T) {
	_, inside := endpoints(t)

	got := tool(t, inside, "hanzo", "hanzo/z@hanzo.ai")

	if !got.Validated {
		t.Error("ValidatedFrom = false for a caller the fleet resolved server-side; " +
			"every op that gates on it answers `sign in` and no tool is reachable")
	}
	if !got.OrgOK || got.Org != "hanzo" {
		t.Errorf("OrgFrom = (%q, %v), want (\"hanzo\", true) — the op cannot scope a query "+
			"to the tenant the run is billed to", got.Org, got.OrgOK)
	}
}

// TestForgedIdentityStillDiesAtTheEdge is the half that must NOT move. The SAME
// headers, from outside, are a caller naming its own tenant with nothing behind it
// — the F1 forge — and the boundary in front of the subsystem's edge endpoint is
// what refuses them. It refuses them EARLIER than it once did: a tools/call
// carrying no credential is answered 401 at the edge with a challenge naming where
// to sign in, so the forged headers never reach an op at all. A fix that made the
// endpoint work by trusting what it was handed would pass the test above and fail
// this one.
func TestForgedIdentityStillDiesAtTheEdge(t *testing.T) {
	edge, _ := endpoints(t)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tenantOp + `","arguments":{}}}`
	req, err := http.NewRequest("POST", "http://cloud"+manifest.MCPPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(zip.HeaderOrg, "victim-corp")
	req.Header.Set(zip.HeaderUser, "victim-corp/ceo@victim.test")
	resp, err := edge.Test(req, zip.TestConfig{Timeout: 60 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST %s: %v", manifest.MCPPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a forged identity with no credential got %d from the public endpoint; "+
			"the endpoint challenges before it routes, so nothing it was handed is ever read", resp.StatusCode)
	}
	if h := resp.Header.Get("WWW-Authenticate"); !strings.Contains(h, "oauth-protected-resource") {
		t.Errorf("WWW-Authenticate = %q; the challenge names the resource metadata an MCP client signs in from", h)
	}
}

// TestInsideTheFleetAnOrgAloneIsStillRefused pins that the fix carried the trust
// rule across rather than dropping it. principal.OrgOf refuses an org that arrived
// without a validated user, and it refuses one here for the same reason: a caller
// that names a tenant and nobody has named its own authority.
func TestInsideTheFleetAnOrgAloneIsStillRefused(t *testing.T) {
	_, inside := endpoints(t)

	got := tool(t, inside, "hanzo", "")

	if got.Validated || got.OrgOK {
		t.Errorf("OrgFrom = (%q, %v) validated=%v for an org with no user; the org that "+
			"rode along is untrusted on every endpoint", got.Org, got.OrgOK, got.Validated)
	}
}

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
