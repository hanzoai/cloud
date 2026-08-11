package tel

import (
	"context"
	"sort"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"

	// A test binary keys itself: cek reads no environment, so a process with no
	// KMS mints its own master. One import per package, not a TestMain per file.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

/*
The surface, as an EXACT set — the ingress shape, because this package is the same
kind of thing: uniform CRUD over three resources with nothing wire-bound in it.

An exact set rather than a floor, so a route added here goes red whether or not
anybody remembers this file. Every one is a typed op, which is what makes each an
OpenAPI operation, an MCP tool, a CLI command and an SDK method from one
registration — and, for the agent plane, a callable tool with no hand-written
wrapper.

There is no untyped-by-design ledger because nothing here refuses: no raw bytes,
no multi-status, no verbatim relay. The day one appears, it is named here with its
wire fact, and the sum still has to match.
*/
var typedOps = []string{
	"GET /v1/tel/summary",
	"GET /v1/tel/numbers/available",
	"GET /v1/tel/numbers",
	"POST /v1/tel/numbers",
	"DELETE /v1/tel/numbers/:id",
	"GET /v1/tel/calls",
	"POST /v1/tel/calls",
	"DELETE /v1/tel/calls/:id",
	"GET /v1/tel/messages",
	"POST /v1/tel/messages",
}

func newOpsApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

func TestSurfaceIsRegistered(t *testing.T) {
	app := newOpsApp(t)

	live := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes(true) {
		if r.Method == "HEAD" { // fiber mirrors every GET; not a surface of ours
			continue
		}
		if !strings.HasPrefix(r.Path, "/v1/tel") {
			continue // zip's own routes, not this subsystem's
		}
		live[r.Method+" "+r.Path] = true
	}
	// serve.go auto-registers /v1/tel/health for every subsystem that does not own
	// its own; it is the fleet's route, not this app's surface.
	delete(live, "GET /v1/tel/health")

	if len(live) != len(typedOps) {
		t.Errorf("live /v1/tel routes = %d, want %d — a route nobody decided on", len(live), len(typedOps))
		for op := range live {
			t.Logf("  live: %s", op)
		}
	}
	for _, op := range typedOps {
		if !live[op] {
			t.Errorf("declared op %s is not a live route", op)
		}
	}

	// The registry, read through the CLI projection — the same registry the OpenAPI
	// document and the MCP tool list are built from. A route that is live and absent
	// here is a route with no schema, no prose and no tool.
	got := make([]string, 0, len(typedOps))
	for _, c := range app.Commands() {
		if strings.HasPrefix(c.Path, "/v1/tel") {
			got = append(got, c.Method+" "+c.Path)
		}
	}
	sort.Strings(got)
	want := append([]string(nil), typedOps...)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("registry projection differs from the surface:\n got: %v\nwant: %v", got, want)
	}
}

// The carrier is an interface so the surface runs without one. A deployment with
// no credential must SERVE, not fail to start — that is what makes this app
// runnable in the suite and in a sandbox.
func TestMountsWithoutACarrier(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("a deployment with no carrier credential must still serve: %v", err)
	}
	defer func() { _ = Shutdown() }()

	if mounted == nil || mounted.State.carrier == nil {
		t.Fatal("no carrier stood in for the absent one")
	}
	if _, ok := mounted.State.carrier.(*stubCarrier); !ok {
		t.Fatalf("expected the stub, got %T", mounted.State.carrier)
	}
}

/*
Isolation, asserted against the store rather than argued in a comment: a number one
org holds is invisible to another, and `NumberByE164` — the check that decides
whether a call may present a caller ID — refuses the number it does not own.
Without that read, one tenant could put another's number on a call, which is
spoofing with our own inventory.
*/
func TestOneOrgCannotSeeOrSendAsAnother(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if err := s.PutNumber(ctx, Number{Org: "acme", ID: "n1", E164: "+15551234567"}); err != nil {
		t.Fatalf("put: %v", err)
	}

	if held, _ := s.Numbers(ctx, "other"); len(held) != 0 {
		t.Errorf("another org sees %d of acme's numbers", len(held))
	}
	if n, _ := s.Number(ctx, "other", "n1"); n.ID != "" {
		t.Error("another org read acme's number by id")
	}
	if n, _ := s.NumberByE164(ctx, "other", "+15551234567"); n.ID != "" {
		t.Error("another org could present acme's number as its caller ID")
	}
	if n, _ := s.NumberByE164(ctx, "acme", "+15551234567"); n.ID != "n1" {
		t.Error("the owning org could not present its own number")
	}
}

// A message the carrier ACCEPTED is queued, never delivered. Recording acceptance
// as delivery is how a message that never arrived shows up as one that did.
func TestAcceptanceIsNotDelivery(t *testing.T) {
	m, err := newStub().Send(context.Background(), SMSRequest{From: "+15550000000", To: "+15551111111", Text: "hi"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if m.Status != "queued" {
		t.Errorf("status = %q, want queued — the carrier accepted it, it did not deliver it", m.Status)
	}
}
