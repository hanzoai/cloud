package host

// THE CEILING, ON EVERY DOOR THE OPERATION HAS.
//
// defaultServiceRPM caps /v1/audio/* at 60 requests a minute for an org nobody
// configured, and middleware_ratelimit_default_test.go proves it binds. It
// proves it over REST, which is the door where the path a request carries IS
// the operation. It is not that door anywhere else: over MCP the path is /mcp
// and over the call plane it is /.well-known/zip/op/<name>, and
// canonicalService wants a /v1/<service>/... shape, so both resolve to the empty
// service and defaultServiceRPM[""] is zero.
//
// So the same operation, asked for the same way by the same org, is bounded
// through one door and unbounded through the two an agent uses. Measured here
// rather than argued.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/manifest"
	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

type speakIn struct {
	N int `json:"n"`
}

type speakOut struct {
	OK bool `json:"ok"`
}

// ceilingRig is the UNCONFIGURED deployment — no commerce, no gateway store —
// which is where the static floor is the only thing that says stop.
type ceilingRig struct {
	base string
	door string // where this rig's MCP door answers.
	ran  func() int
}

// ceilingApp serves the MCP door where zip puts it by default. That is where a
// plugin serves its own, and it is NOT where the fleet's agent door answers —
// see ceilingHost.
func ceilingApp(t *testing.T) *ceilingRig { return ceilingAppAt(t, "") }

// ceilingHost serves the MCP door where the fleet actually serves it. The
// address matters to a ceiling that reads paths: /mcp resolves to no service at
// all, while /v1/mcp resolves to the service "mcp" — an answer, and the wrong
// one. A predicate written against the framework default alone passes every row
// below and ships a bypass, so the door that ships gets its own rig.
func ceilingHost(t *testing.T) *ceilingRig { return ceilingAppAt(t, manifest.MCPPath) }

func ceilingAppAt(t *testing.T, mcpPath string) *ceilingRig {
	t.Helper()
	var ran int
	cfg := zip.Config{ErrorHandler: cloud.ErrorHandler}
	if mcpPath != "" {
		cfg.MCP = zip.MCPConfig{Path: mcpPath}
	}
	app := zip.New(cfg)
	// Bridge first, as serve.go composes it: it is what puts the request on the
	// context, and without it the op-invoke leg sees no request and stands down —
	// which reads exactly like the gap it is there to close.
	app.Use(cloud.Bridge())
	app.Use(ScopeRateLimit(nil, nil))
	app.Authorize(cloud.Rule(nil, nil))
	app.Post("/v1/audio/transcriptions",
		func(ctx context.Context, in *speakIn) (*speakOut, error) {
			ran++
			return &speakOut{OK: true}, nil
		}, zip.WithOperationID("audio_transcribe"))
	// A surface nobody priced a rate for — the control the rows above need.
	app.Post("/v1/probe/run",
		func(ctx context.Context, in *speakIn) (*speakOut, error) { return &speakOut{OK: true}, nil },
		zip.WithOperationID("probe_run"))
	if err := app.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fasthttp.Serve(ln, app.Fiber().Handler()) }()
	t.Cleanup(func() { _ = ln.Close() })
	door := mcpPath
	if door == "" {
		door = manifest.FrameworkMCPPath
	}
	return &ceilingRig{base: "http://" + ln.Addr().String(), door: door, ran: func() int { return ran }}
}

// The floor binds over REST. This is the control: it is the property
// middleware_ratelimit_default_test.go already holds, restated against a TYPED
// op so the two doors below differ in nothing except the door.
func TestTheAudioFloorBindsOverREST(t *testing.T) {
	rpm := audioRPM(t)
	r := ceilingApp(t)
	for i := range rpm {
		if got := r.rest(t, "acme"); got != http.StatusOK {
			t.Fatalf("request %d of %d answered %d, want 200 — the floor bound early", i+1, rpm, got)
		}
	}
	if got := r.rest(t, "acme"); got != http.StatusTooManyRequests {
		t.Fatalf("request %d answered %d, want 429 — the floor never bound", rpm+1, got)
	}
}

// The same operation, the same org, one minute, over the door an agent uses —
// and at the SAME boundary. Asserting only that something was eventually refused
// would pass a ceiling that binds at half the allowance, which is a different
// bug wearing the same green tick: over REST an org gets 60 and over MCP it
// would get 30, because one request spent two tokens.
func TestTheAudioFloorBindsOverMCP(t *testing.T) {
	rpm := audioRPM(t)
	r := ceilingApp(t)
	for i := range rpm {
		if r.mcp(t, "globex") {
			t.Fatalf("call %d of %d was refused over MCP — the ceiling binds early, "+
				"so one call is spending more than one token", i+1, rpm)
		}
	}
	if !r.mcp(t, "globex") {
		t.Fatalf("call %d of a %d/min operation was served over MCP — the ceiling reads "+
			"the path the transport carried, and over MCP that path names no service",
			rpm+1, rpm)
	}
}

// AN ORG'S ALLOWANCE IS ONE ALLOWANCE, not one per door. The two halves of the
// ceiling share one set of counters, so spending half the minute over REST
// leaves exactly half of it for MCP. Two counters would let an agent double an
// org's rate by alternating doors, which is the failure the single-door tests
// above cannot see.
func TestTheDoorsShareOneAllowance(t *testing.T) {
	rpm := audioRPM(t)
	r := ceilingApp(t)
	for i := 0; i < rpm/2; i++ {
		if got := r.rest(t, "initech"); got != http.StatusOK {
			t.Fatalf("REST call %d answered %d, want 200", i+1, got)
		}
	}
	for i := 0; i < rpm-rpm/2; i++ {
		if r.mcp(t, "initech") {
			t.Fatalf("MCP call %d was refused with %d of the minute spent over REST — "+
				"the doors are counting the same org twice", i+1, rpm/2)
		}
	}
	if !r.mcp(t, "initech") {
		t.Fatal("the minute was spent across both doors and the next call was still served — " +
			"each door is keeping its own count")
	}
}

// A SERVICE WITH NO FLOOR STAYS UNBOUNDED, over every door. The leg reads the
// same three sources the edge reads and invents nothing: an operation nobody
// configured and no default covers is served, which is what makes the rows above
// a ceiling rather than a blanket.
func TestAnUnfloodedServiceIsNotCapped(t *testing.T) {
	r := ceilingApp(t)
	for i := 0; i < audioRPM(t)*2; i++ {
		if r.mcpOp(t, "hooli", "probe_run") {
			t.Fatalf("call %d of an operation with no configured rate was refused", i+1)
		}
	}
}

// THE DOOR THAT SHIPS. The fleet serves its agent MCP address at
// manifest.MCPPath, not at zip's framework default, and the two differ in
// exactly the way a path-reading ceiling cares about: canonicalService says
// nothing about /mcp and says "mcp" about /v1/mcp. The second is the more
// dangerous of the two, because it looks like a resolved service and buckets
// every operation in the fleet together under a name no floor covers.
func TestTheAudioFloorBindsOnTheFleetsOwnMCPAddress(t *testing.T) {
	rpm := audioRPM(t)
	r := ceilingHost(t)
	for i := range rpm {
		if r.mcp(t, "umbrella") {
			t.Fatalf("call %d of %d was refused at %s", i+1, rpm, r.door)
		}
	}
	if !r.mcp(t, "umbrella") {
		t.Fatalf("call %d of a %d/min operation was served at %s — the ceiling read the "+
			"door's own name as a service", rpm+1, rpm, r.door)
	}
}

// rest asks over the door whose path is the operation.
func (r *ceilingRig) rest(t *testing.T, org string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.base+"/v1/audio/transcriptions", strings.NewReader(`{"n":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "alice")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("REST: %v", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

// mcp asks over the door an agent uses. A tools/call answers 200 whether the op
// ran or was refused, so the refusal is read out of the frame, not the status.
func (r *ceilingRig) mcp(t *testing.T, org string) bool {
	t.Helper()
	return r.mcpOp(t, org, "audio_transcribe")
}

func (r *ceilingRig) mcpOp(t *testing.T, org, op string) bool {
	t.Helper()
	frame, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": op, "arguments": map[string]any{"n": 1}},
	})
	req, _ := http.NewRequest(http.MethodPost, r.base+r.door, strings.NewReader(string(frame)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "alice")
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("MCP: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	var ans struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &ans); err != nil {
		t.Fatalf("MCP: reply is not a frame: %v (%s)", err, body)
	}
	if ans.Error != nil {
		t.Fatalf("MCP: protocol error, so the op never ran: %d %s", ans.Error.Code, ans.Error.Message)
	}
	return ans.Result.IsError
}
