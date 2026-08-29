package agents

// door_test.go — the tool plane, over the wire it actually uses.
//
// Nothing is stubbed at the client under test. Every test here brings up real
// subsystem apps on real ZAP sockets, composes the REAL fleet.Door over them,
// publishes it on the router's socket exactly as cmd/cloud/wake.go does, and
// then drives doorTools — so what is asserted is what a deployed agent gets.
//
// A fake MCP server would have proved nothing: the two properties worth
// having are that the agent inherits the MCP server's CURATION and that the
// run's org reaches the owning subsystem, and both live in code a stub would
// have replaced.

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/fleet"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

type echoIn struct {
	Say string `json:"say"`
}

type echoOut struct {
	App string `json:"app"`
	Say string `json:"say"`
	Org string `json:"org"`
}

// declared is what the children below PUBLISH, which is a different fact from
// what they serve and is now the only one the MCP server reads.
//
// The MCP server used to ask a running subsystem what it had; it lists from the
// build-time catalog and asks nothing, because one question about names was
// costing a process per subsystem. These children are built here rather than by
// the fleet's generator, so nothing embeds their operations and the harness has
// to state them — which is exactly the case fleet.Door.Catalog documents.
//
// Written by subsystem, read by fleetDoor. Both run on the test's own goroutine
// and the calls to subsystem are ARGUMENTS to fleetDoor, so they are complete
// before the map is read; these tests do not run in parallel.
var declared = map[string][]fleet.Op{}

// subsystem starts one app serving the named ops on its own socket, the shape
// cloud.Serve gives every plugin binary. Each op echoes its input AND the org it
// was reached as, so a test can prove the run's tenant travelled the whole way.
func subsystem(t *testing.T, name string, ops ...string) string {
	t.Helper()
	pub := make([]fleet.Op, 0, len(ops))
	for _, id := range ops {
		// The same sentence WithSummary puts on the live op, so what the MCP
		// server lists and what the child would have answered cannot drift
		// apart here.
		pub = append(pub, fleet.Op{ID: id, Doc: "what " + name + " does at " + id})
	}
	declared[name] = pub
	t.Cleanup(func() { delete(declared, name) })
	sock := shortDir(t) + "/" + name + ".sock"
	app := zip.New(zip.Config{AppName: name, DisableStartupMessage: true})
	for _, id := range ops {
		zip.Post(app, "/v1/"+name+"/"+id, func(ctx context.Context, in *echoIn) (*echoOut, error) {
			return &echoOut{App: name, Say: in.Say, Org: zip.CallerOf(ctx).Org}, nil
		}, zip.WithOperationID(id), zip.WithSummary("what "+name+" does at "+id))
	}
	go func() { _ = app.Listen(sock) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	accepts(t, sock)
	return sock
}

// fleetDoor composes the real MCP server over those apps and puts it where a
// child looks for it — plane.HostApp's socket, at manifest.MCPPath. This is
// serveWake's two lines, not a reimplementation of them.
//
// at names each app's EDGE address and inside names the PLANE address of the
// ones that have one, which is the pair cmd/cloud registers (locate / inside).
// An app with no entry in inside is reached at its edge address — production's
// remotely-mounted case, where there is no local plane socket to reach.
func fleetDoor(t *testing.T, at map[string]string, inside map[string]string) {
	t.Helper()
	run := shortDir(t)
	t.Setenv("ZIP_RUNTIME_DIR", run)
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	host := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true, MCP: zip.MCPConfig{Disabled: true}})
	apps := make([]string, 0, len(at))
	for name := range at {
		apps = append(apps, name)
	}
	edge := func(app string) (addr, path string, err error) {
		sock, ok := at[app]
		if !ok {
			return "", "", &net.AddrError{Err: "no instance running", Addr: app}
		}
		return sock, manifest.FrameworkMCPPath, nil
	}
	d := fleet.Use(host, manifest.MCPPath, apps, edge)
	// nil would mean the catalog THIS binary embeds, which is the real fleet's —
	// it does not carry these children, so the MCP server would list nothing for
	// them and every assertion below would read as "the agent was offered 0".
	d.Catalog = func(app string) []fleet.Op { return declared[app] }

	door := zip.New(zip.Config{AppName: "plane", DisableStartupMessage: true})
	d.Serve(door, manifest.MCPPath, func(app string) (addr, path string, err error) {
		if sock, ok := inside[app]; ok {
			return sock, manifest.MCPPath, nil
		}
		return edge(app)
	})
	path := zip.SocketPath(plane.HostApp)
	go func() { _ = door.Listen(path) }()
	t.Cleanup(func() { _ = door.Shutdown() })
	accepts(t, path)
}

// noFleetDoor points the run directory at an empty one: nothing is listening, so
// this process is the whole fleet.
func noFleetDoor(t *testing.T) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", shortDir(t))
	plane.Unbind()
	t.Cleanup(plane.Unbind)
}

// shortDir is a temp directory with a SHORT name, because a unix socket path is
// capped near 104 bytes and t.TempDir() spends most of that on the test's name.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ag")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func accepts(t *testing.T, sock string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never accepted", sock)
}

// TestAgentResolvesItsToolsFromTheFleetDoor is the whole claim: a declared name
// resolves to the OWNING subsystem's own descriptor, across a process boundary,
// with nothing in this binary that knows what that subsystem serves.
func TestAgentResolvesItsToolsFromTheFleetDoor(t *testing.T) {
	fleetDoor(t, map[string]string{
		"alpha": subsystem(t, "alpha", "alpha_echo", "alpha_other"),
		"beta":  subsystem(t, "beta", "beta_echo"),
	}, nil)

	door := doorTools{}
	defs := door.catalog(context.Background(), "acme", "acme/u-1", []string{"alpha_echo", "beta_echo"})
	if len(defs) != 2 {
		t.Fatalf("the agent declared two tools the fleet serves and was offered %d: %+v", len(defs), defs)
	}
	got := map[string]bool{}
	for _, d := range defs {
		got[d.Name] = true
		if d.Description == "" {
			t.Errorf("%s came back with no description, so the model is offered a tool it cannot choose", d.Name)
		}
		if len(d.Schema) == 0 {
			t.Errorf("%s came back with no schema, so the model cannot fill its arguments", d.Name)
		}
	}
	if !got["alpha_echo"] || !got["beta_echo"] {
		t.Fatalf("offered %v, want alpha_echo and beta_echo", got)
	}
}

// A declared name NOTHING in the fleet serves is absent, never offered. Offering
// a tool that would be refused at dispatch teaches the model a lie.
func TestUnservedNamesAreNotOffered(t *testing.T) {
	fleetDoor(t, map[string]string{"alpha": subsystem(t, "alpha", "alpha_echo")}, nil)

	door := doorTools{}
	defs := door.catalog(context.Background(), "acme", "acme/u-1",
		[]string{"alpha_echo", "slack_post_message"})
	if len(defs) != 1 || defs[0].Name != "alpha_echo" {
		t.Fatalf("offered %+v, want alpha_echo alone", defs)
	}
}

// TestTheAgentInheritsTheDoorsDenylist is the security bar, as a test.
//
// The curation rule lives in fleet/surface.go and is applied inside gather,
// where the routing table is written. An agent reaching the MCP server through
// any other path would have seen a surface external MCP clients cannot — so this
// asserts BOTH halves: the credential-minting op is not offered, and naming it
// anyway does not run it.
func TestTheAgentInheritsTheDoorsDenylist(t *testing.T) {
	fleetDoor(t, map[string]string{
		"iam": subsystem(t, "iam", "CreateServiceAccountKey", "GetRole"),
	}, nil)
	door := doorTools{}
	ctx := context.Background()

	defs := door.catalog(ctx, "acme", "acme/u-1", []string{"CreateServiceAccountKey", "GetRole"})
	if len(defs) != 1 || defs[0].Name != "GetRole" {
		t.Fatalf("the agent was offered %+v; the MCP server projects GetRole and refuses CreateServiceAccountKey", defs)
	}
	if _, err := door.call(ctx, "acme", "acme/u-1", "CreateServiceAccountKey", `{"say":"hi"}`); err == nil {
		t.Fatal("a refused tool RAN for an agent that named it directly — the denylist is a suggestion, not a boundary")
	}
}

// TestADispatchCarriesTheRunsOrg: the tenant reaches the subsystem that owns the
// tool, and it comes from the run rather than from anything the model emitted.
func TestADispatchCarriesTheRunsOrg(t *testing.T) {
	fleetDoor(t, map[string]string{"alpha": subsystem(t, "alpha", "alpha_echo")}, nil)

	door := doorTools{}
	out, err := door.call(context.Background(), "acme", "acme/u-1", "alpha_echo", `{"say":"pong"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	for _, want := range []string{`"app":"alpha"`, `"say":"pong"`, `"org":"acme"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("alpha answered %q, which does not carry %s", out, want)
		}
	}
}

// guarded is one subsystem in its PRODUCTION shape, which is the shape the
// fixture above deliberately is not: the identity boundary in front of it, and its
// tool resolving the tenant the way every org-scoped op does — principal.OrgFrom,
// never a header. It returns its EDGE socket and its PLANE socket.
//
// The bare fixture answers zip.CallerOf directly, so it reports the org for any
// caller that names one. That is why every test above passed while every tool the
// @hanzo Slack agent called refused it: production has a boundary, the boundary
// deletes an authority header no credential backs, and nothing in this file had
// one.
func guarded(t *testing.T, name, op string) (edge, plane string) {
	t.Helper()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	// Publishing is a separate act from serving, and this child owes it for the
	// same reason subsystem's do: the MCP server lists from the catalog and
	// never asks.
	declared[name] = []fleet.Op{{ID: op, Doc: "what " + name + " does at " + op}}
	t.Cleanup(func() { delete(declared, name) })

	app := zip.New(zip.Config{AppName: name, DisableStartupMessage: true})
	cloud.Identify(app, &cloud.Config{})
	zip.Post(app, "/v1/"+name+"/"+op, func(ctx context.Context, in *echoIn) (*echoOut, error) {
		org, ok := principal.OrgFrom(ctx)
		if !ok {
			return nil, principal.RefusedFrom(ctx)
		}
		return &echoOut{App: name, Say: in.Say, Org: org}, nil
	}, zip.WithOperationID(op), zip.WithSummary("what "+name+" does at "+op))
	cloud.Door(app)

	dir := shortDir(t)
	edge, plane = dir+"/"+name+".sock", dir+"/"+name+"-plane.sock"
	go func() { _ = app.Listen(edge) }()
	go func() { _ = cloud.Plane().Listen(plane) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	accepts(t, edge)
	accepts(t, plane)
	return edge, plane
}

// TestATenantedToolAnswersTheRun is the @hanzo Slack failure, as a test: a run
// with a principal the fleet resolved server-side calls a tool that scopes by
// tenant, and gets an answer instead of a refusal.
//
// It is the same call TestADispatchCarriesTheRunsOrg makes, against a subsystem
// that has the boundary production has. Point the MCP server's internal reach
// at the EDGE address instead and it fails exactly the way the deployed fleet
// did.
func TestATenantedToolAnswersTheRun(t *testing.T) {
	edge, plane := guarded(t, "alpha", "alpha_tenant")
	fleetDoor(t, map[string]string{"alpha": edge}, map[string]string{"alpha": plane})

	out, err := doorTools{}.call(context.Background(), "acme", "acme/u-1", "alpha_tenant", `{"say":"pong"}`)
	if err != nil {
		t.Fatalf("a tool that scopes by tenant refused the run it belongs to: %v", err)
	}
	if !strings.Contains(out, `"org":"acme"`) {
		t.Fatalf("alpha answered %q, which does not carry the run's tenant", out)
	}
}

// A tool the MCP server cannot route is an ERROR the model reads, never a
// silent empty result and never a killed turn.
func TestAnUnroutableToolIsAnErrorNotAnEmptyResult(t *testing.T) {
	fleetDoor(t, map[string]string{"alpha": subsystem(t, "alpha", "alpha_echo")}, nil)

	door := doorTools{}
	out, err := door.call(context.Background(), "acme", "acme/u-1", "nobody_serves_this", `{}`)
	if err == nil {
		t.Fatalf("an unroutable tool answered %q with no error", out)
	}
	if got := dispatchOne(context.Background(), "acme", "acme/u-1",
		types.ToolCall{ID: "c1", Name: "nobody_serves_this", Arguments: `{}`}, "run_test", 0); !strings.Contains(got, "error:") {
		t.Fatalf("the model was handed %q for a tool that cannot run", got)
	}
}

// Arguments that are not a JSON object are refused HERE, before the wire, and
// the model is told so — the same sentence the co-resident plane gives it.
func TestMalformedArgumentsNeverReachTheDoor(t *testing.T) {
	fleetDoor(t, map[string]string{"alpha": subsystem(t, "alpha", "alpha_echo")}, nil)

	door := doorTools{}
	if _, err := door.call(context.Background(), "acme", "acme/u-1", "alpha_echo", `["not","an","object"]`); err == nil {
		t.Fatal("a JSON array was accepted as a tool's arguments")
	}
}

// With no router on this host, this process IS the fleet: doorTools falls back
// to the in-process registry rather than reporting an outage — and a run in a
// single-app binary keeps working instead of crashing.
func TestNoFleetDoorFallsBackToThisProcesssRegistry(t *testing.T) {
	noFleetDoor(t)
	door := doorTools{}
	ctx := context.Background()

	if defs := door.catalog(ctx, "acme", "acme/u-1", []string{"alpha_echo"}); len(defs) != 0 {
		t.Fatalf("this process serves no such tool, so nothing may be offered: %+v", defs)
	}
	if _, err := door.call(ctx, "acme", "acme/u-1", "alpha_echo", `{}`); err == nil {
		t.Fatal("a tool nothing in this process registers reported success")
	}
}
