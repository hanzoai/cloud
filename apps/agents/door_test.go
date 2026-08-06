package agents

// door_test.go — the tool plane, over the wire it actually uses.
//
// Nothing is stubbed at the seam under test. Every test here brings up real
// subsystem apps on real ZAP sockets, composes the REAL fleet.Door over them,
// publishes it on the router's socket exactly as cmd/cloud/wake.go does, and
// then drives doorTools — so what is asserted is what a deployed agent gets.
//
// A fake door would have proved nothing: the two properties worth having are
// that the agent inherits the door's CURATION and that the run's org reaches the
// owning subsystem, and both live in code a stub would have replaced.

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

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

// subsystem starts one app serving the named ops on its own socket, the shape
// cloud.Serve gives every plugin binary. Each op echoes its input AND the org it
// was reached as, so a test can prove the run's tenant travelled the whole way.
func subsystem(t *testing.T, name string, ops ...string) string {
	t.Helper()
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

// fleetDoor composes the real door over those apps and puts it where a child
// looks for it — plane.HostApp's socket, at manifest.MCPPath. This is
// serveWake's two lines, not a reimplementation of them.
func fleetDoor(t *testing.T, at map[string]string) {
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
	d := fleet.Mount(host, manifest.MCPPath, apps, func(app string) (string, error) {
		sock, ok := at[app]
		if !ok {
			return "", &net.AddrError{Err: "no instance running", Addr: app}
		}
		return sock, nil
	})

	door := zip.New(zip.Config{AppName: "plane", DisableStartupMessage: true})
	d.Serve(door, manifest.MCPPath)
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
	})

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
	fleetDoor(t, map[string]string{"alpha": subsystem(t, "alpha", "alpha_echo")})

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
// where the routing table is written. An agent reaching the door through any
// other path would have seen a surface external MCP clients cannot — so this
// asserts BOTH halves: the credential-minting op is not offered, and naming it
// anyway does not run it.
func TestTheAgentInheritsTheDoorsDenylist(t *testing.T) {
	fleetDoor(t, map[string]string{
		"iam": subsystem(t, "iam", "CreateServiceAccountKey", "GetRole"),
	})
	door := doorTools{}
	ctx := context.Background()

	defs := door.catalog(ctx, "acme", "acme/u-1", []string{"CreateServiceAccountKey", "GetRole"})
	if len(defs) != 1 || defs[0].Name != "GetRole" {
		t.Fatalf("the agent was offered %+v; the door projects GetRole and refuses CreateServiceAccountKey", defs)
	}
	if _, err := door.call(ctx, "acme", "acme/u-1", "CreateServiceAccountKey", `{"say":"hi"}`); err == nil {
		t.Fatal("a refused tool RAN for an agent that named it directly — the denylist is a suggestion, not a boundary")
	}
}

// TestADispatchCarriesTheRunsOrg: the tenant reaches the subsystem that owns the
// tool, and it comes from the run rather than from anything the model emitted.
func TestADispatchCarriesTheRunsOrg(t *testing.T) {
	fleetDoor(t, map[string]string{"alpha": subsystem(t, "alpha", "alpha_echo")})

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

// A tool the door cannot route is an ERROR the model reads, never a silent empty
// result and never a killed turn.
func TestAnUnroutableToolIsAnErrorNotAnEmptyResult(t *testing.T) {
	fleetDoor(t, map[string]string{"alpha": subsystem(t, "alpha", "alpha_echo")})

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
	fleetDoor(t, map[string]string{"alpha": subsystem(t, "alpha", "alpha_echo")})

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
