package host

// WHAT THE SENSOR SEES OF A CALLER THAT WALKS THE CATALOGUE.
//
// Pattern.Paths is the approximate number of distinct paths one caller touched
// in the window — the spread signal, and the one that separates a client using a
// capability from one enumerating them. The gate builds its observation from
// routeFamily of the path the TRANSPORT carried, and a request that names its
// operation in the payload carries a path that names the door. So every
// operation in the fleet reached that way is one path.
//
// The fleet serves 2,400-odd operations, and an agent has a door to all of them.
// This drives the same eight operations over the two doors and reads the count
// back off the sensor.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	cloud "github.com/hanzoai/cloud/sdk"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

type walkIn struct {
	N int `json:"n"`
}

type walkOut struct {
	OK bool `json:"ok"`
}

// walkOps is more than one and fewer than the spread counter saturates at, so
// the count it produces is the number of operations and not a ceiling.
const walkOps = 8

type walkRig struct {
	base    string
	traffic *edge.Traffic
}

func walkApp(t *testing.T) *walkRig {
	t.Helper()
	tr := edge.NewTraffic()
	policy := staticModeStore(t, edge.ModeShadow)
	deps := cloud.Deps{}

	app := zip.New(zip.Config{ErrorHandler: cloud.ErrorHandler})
	app.Use(cloud.Bridge())
	app.Use(boundary())
	app.Use(AbuseGate(deps, policy, tr))
	app.Authorize(cloud.Rule(nil, nil))

	run := func(ctx context.Context, in *walkIn) (*walkOut, error) { return &walkOut{OK: true}, nil }
	for i := 1; i <= walkOps; i++ {
		app.Post(fmt.Sprintf("/v1/probe/step%d", i), run,
			zip.WithOperationID(fmt.Sprintf("probe_step%d", i)))
	}
	if err := app.Build(); err != nil {
		t.Fatalf("build: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fasthttp.Serve(ln, app.Fiber().Handler()) }()
	t.Cleanup(func() { _ = ln.Close() })
	return &walkRig{base: "http://" + ln.Addr().String(), traffic: tr}
}

// walkKey is the credential the walk presents. The sensor keys a caller on a
// VALIDATED credential and falls back to its address, and a request driven at a
// listener in-process has no address the framework surfaces — so an unkeyed walk
// lands in the unattributed lane and records no paths at all, whichever door it
// used. Presenting a key is what makes the two rows below comparable rather than
// both empty.
const walkKey = "sk-walk-catalogue"

func (r *walkRig) identify(h http.Header, org string) {
	h.Set("Content-Type", "application/json")
	h.Set("X-Org-Id", org)
	h.Set("X-User-Id", "alice")
	h.Set("X-Api-Key", walkKey)
}

// walkREST asks each operation at its own address.
func (r *walkRig) walkREST(t *testing.T, org string) {
	t.Helper()
	for i := 1; i <= walkOps; i++ {
		req, _ := http.NewRequest(http.MethodPost,
			fmt.Sprintf("%s/v1/probe/step%d", r.base, i), strings.NewReader(`{"n":1}`))
		r.identify(req.Header, org)
		res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("REST step%d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
}

// walkRESTOnce asks exactly one operation at its own address.
func (r *walkRig) walkRESTOnce(t *testing.T, org string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.base+"/v1/probe/step1", strings.NewReader(`{"n":1}`))
	r.identify(req.Header, org)
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("REST step1: %v", err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
}

// walkMCP asks the same operations by name, through one address.
func (r *walkRig) walkMCP(t *testing.T, org string) {
	t.Helper()
	for i := 1; i <= walkOps; i++ {
		frame, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": i, "method": "tools/call",
			"params": map[string]any{
				"name":      fmt.Sprintf("probe_step%d", i),
				"arguments": map[string]any{"n": 1},
			},
		})
		req, _ := http.NewRequest(http.MethodPost, r.base+"/mcp", strings.NewReader(string(frame)))
		r.identify(req.Header, org)
		res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatalf("MCP step%d: %v", i, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		var ans struct {
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &ans); err != nil {
			t.Fatalf("MCP step%d: reply is not a frame: %v (%s)", i, err, body)
		}
		if ans.Error != nil {
			t.Fatalf("MCP step%d: protocol error, so the op never ran: %d %s",
				i, ans.Error.Code, ans.Error.Message)
		}
	}
}

// spread reads the count back WITHOUT contributing to it: an empty Path adds
// nothing to the set, so the number is what the walk above put there. The caller
// is named the same way the gate named it — the validated credential's
// fingerprint — so this asks about the caller that walked, not a new one.
func (r *walkRig) spread(t *testing.T, org string) int {
	t.Helper()
	p := r.traffic.Observe(edge.Signal{Org: org, Cred: Fingerprint(walkKey)}, time.Now())
	if p.Requests < 2 {
		t.Fatalf("the sensor attributed %d requests to this caller — the probe is reading "+
			"a caller that never walked, and its count means nothing", p.Requests)
	}
	return p.Paths
}

// TWO DOORS, ONE CATALOGUE, ONE COUNT.
//
// The count is deliberately approximate: the spread word is 64 bits and each
// member sets one, so eight operations can register as seven when two of them
// land on the same bit. The seed is per-sensor and random, so an exact number is
// not a property to assert — asking for eight passes or fails on the seed.
//
// What IS a property is that the two doors agree. The same eight operations,
// hashed by the same sensor with the same seed, put the same bits in whichever
// caller's word they land in — so a walk over MCP has to read the same as a walk
// over REST. Before the operation reached the sensor, MCP read 1 against REST's
// 7 or 8.
func TestTheDoorsSeeTheSameSpread(t *testing.T) {
	r := walkApp(t)
	r.walkREST(t, "acme")
	r.walkMCP(t, "globex")

	rest, mcp := r.spread(t, "acme"), r.spread(t, "globex")
	if rest <= 1 {
		t.Fatalf("the REST walk registered %d distinct paths for %d operations — the "+
			"control is broken and the row below means nothing", rest, walkOps)
	}
	if mcp != rest {
		t.Fatalf("the same %d operations read as %d distinct paths over MCP and %d over "+
			"REST — the sensor is counting the door an agent knocked at, not what it "+
			"asked for", walkOps, mcp, rest)
	}
}

// The door itself is not one of them. Adding the door to the spread set would be
// a cheaper fix and a wrong one: every caller would carry one constant path it
// did not choose, which reads as spread that nobody produced. One call through a
// door contributes exactly the one operation it reached.
func TestADoorIsNotAPathTheCallerTouched(t *testing.T) {
	r := walkApp(t)
	r.walkMCP(t, "globex")
	one := r.spread(t, "globex")

	r2 := walkApp(t)
	r2.walkRESTOnce(t, "globex")
	if got := r2.spread(t, "globex"); got != 1 {
		t.Fatalf("one operation over REST registered %d distinct paths, want 1", got)
	}
	if one < 1 {
		t.Fatalf("a walk over MCP registered %d distinct paths", one)
	}
}
