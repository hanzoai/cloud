package platform

// projects_canonical_test.go — the canonical project read, over the peer plane.
//
// What this replaced was an HTTP client with a per-org minted credential, and its
// tests were mostly ABOUT that credential: that it was resolved once, that a nil
// identity provider failed closed, that no request reached IAM unauthenticated.
// None of those questions exist any more. The org rides the call, so there is no
// credential to cache, to fail closed on, or to forget — which is the point, and
// is why deleting those tests is the change rather than a gap in it.
//
// What remains is what still can be wrong: the selector, the wire, and honesty
// about a peer that is not there. Nothing on the path is stubbed — a real unix
// socket, real ZAP frames, the real generated client, the real Ask.

import (
	"context"
	"errors"
	"net"
	"os"
	"unsafe"

	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"

	"github.com/zap-proto/zip"
)

// servePeer stands the iam peer up on a real socket with the rows it should
// answer, and returns a stop.
//
// The handler here is a stand-in for apps/iam's, deliberately: this file is the
// CALLER's half, so the callee asserts nothing and simply answers. What is real
// is everything between them — the socket, the frames, the op name, the types.
func servePeer(t *testing.T, rows []plane.Project) func() error {
	t.Helper()
	cloud.ResetPlane()
	zip.Post[struct{}, plane.Projects](cloud.Plane(), "/iam/projects",
		func(ctx context.Context, _ *struct{}) (*plane.Projects, error) {
			if cloud.Who(ctx).Org == "" {
				return nil, zip.ErrUnauthorized("projects: no org on the call")
			}
			return &plane.Projects{Projects: rows}, nil
		},
		zip.WithOperationID(plane.IAMProjects))
	stop, err := cloud.ServePlane("iam", nil)
	if err != nil {
		t.Fatalf("ServePlane(iam): %v", err)
	}
	// ServePlane FREEZES the plane app: a later Mount registering an op on it
	// panics ("plane was frozen ... it appears in a built generation"). A process
	// serves once, so that is right in production and wrong for a test binary,
	// which mounts many times. Dropping it here is what ResetPlane is for.
	t.Cleanup(cloud.ResetPlane)
	for i := 0; i < 200; i++ {
		if c, derr := net.Dial("unix", zip.SocketPath("iam")); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return stop
}

// TestNewProjectStoreSelector pins the ONE selector: a deployment that names a
// separate IAM reads it as a peer; its absence means this binary IS the IAM and
// the embedded store stays. Neither branch takes an address or a credential, so
// the selector is the whole of the decision.
func TestNewProjectStoreSelector(t *testing.T) {
	t.Setenv("IAM_URL", "")
	if _, ok := newProjectStore().(iamProjects); !ok {
		t.Fatal("no IAM_URL: the embedded store is the canonical one")
	}
	t.Setenv("IAM_URL", "http://iam.hanzo.svc")
	if _, ok := newProjectStore().(canonicalProjects); !ok {
		t.Fatal("IAM_URL set: the iam peer must be selected")
	}
}

// TestCanonicalProjectsRoundTrip drives the real thing: a real socket, a real ZAP
// frame, the real generated client, and IAM's model rebuilt from the contract on
// the far side.
func TestCanonicalProjectsRoundTrip(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	plane.Unbind()
	stop := servePeer(t, []plane.Project{
		{Owner: "acme", Name: "web", DisplayName: "Web", Description: "the site", CreatedTime: "2026-08-01T00:00:00Z"},
		{Owner: "acme", Name: "api"},
	})
	t.Cleanup(func() { _ = stop() })

	c := canonicalProjects{}
	rows, err := c.List(context.Background(), "acme")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("List returned %d rows, want 2", len(rows))
	}
	// Every field of the projection has to survive the wire. ZAP's layout is
	// POSITIONAL — a field is its offset — so a reordered contract scrambles values
	// across fields rather than failing to decode, and only checking each one
	// catches that.
	got := rows[0]
	if got.Owner != "acme" || got.Name != "web" || got.DisplayName != "Web" ||
		got.Description != "the site" || got.CreatedTime != "2026-08-01T00:00:00Z" {
		t.Fatalf("a field was lost or scrambled crossing the plane: %+v", got)
	}

	// Get and Exists are derived from the same read.
	p, err := c.Get(context.Background(), "acme", "api")
	if err != nil || p == nil || p.Name != "api" {
		t.Fatalf("Get(api) = %+v, %v", p, err)
	}
	if p, err := c.Get(context.Background(), "acme", "nope"); err != nil || p != nil {
		t.Fatalf("an absent project must be (nil, nil), got %+v, %v", p, err)
	}
	if ok, err := c.Exists(context.Background(), "acme", "web"); err != nil || !ok {
		t.Fatalf("Exists(web) = %v, %v", ok, err)
	}
}

// TestCanonicalProjectsCarriesNoScope is the structural half. The op's input is the
// empty struct: there is no field an org could be named in, so a caller cannot ask
// about another tenant even by mistake. The HTTP client this replaced put the org
// in the QUERY STRING and had to mint a credential to stop it mattering.
func TestCanonicalProjectsCarriesNoScope(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	plane.Unbind()
	var sawOrg string
	cloud.ResetPlane()
	zip.Post[struct{}, plane.Projects](cloud.Plane(), "/iam/projects",
		func(ctx context.Context, _ *struct{}) (*plane.Projects, error) {
			sawOrg = cloud.Who(ctx).Org
			return &plane.Projects{}, nil
		},
		zip.WithOperationID(plane.IAMProjects))
	stop, err := cloud.ServePlane("iam", nil)
	if err != nil {
		t.Fatalf("ServePlane(iam): %v", err)
	}
	t.Cleanup(func() { _ = stop(); cloud.ResetPlane() })
	for i := 0; i < 200; i++ {
		if c, derr := net.Dial("unix", zip.SocketPath("iam")); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := (canonicalProjects{}).List(context.Background(), "acme"); err != nil {
		t.Fatalf("List: %v", err)
	}
	if sawOrg != "acme" {
		t.Fatalf("the callee read org %q; the tenant must ride the call", sawOrg)
	}
}

// TestCanonicalProjectsNoPeerIsAnError: an iam that is not running must be an
// error, never an empty list. "This org has no projects" and "I could not ask" are
// different facts, and the PaaS acts on the first by offering to create one that
// already exists.
func TestCanonicalProjectsNoPeerIsAnError(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	plane.Unbind()
	cloud.ResetPlane() // nothing listening

	rows, err := (canonicalProjects{}).List(context.Background(), "acme")
	if err == nil {
		t.Fatalf("an absent iam ANSWERED with %d rows; it must be an error", len(rows))
	}
	if !errors.Is(err, cloud.ErrNoPeer) {
		t.Fatalf("an absent peer must report ErrNoPeer, got: %v", err)
	}
	if _, err := (canonicalProjects{}).Exists(context.Background(), "acme", "web"); err == nil {
		t.Fatal("Exists against an absent iam must error")
	}
}

// TestCanonicalProjectsSocketIsLoadBearing is the mutation that proves the wire is
// the wire. The call succeeds; the socket is then UNLINKED with the peer still
// alive and the call fails; the peer rebinds and it succeeds again.
//
// It exists because a transport that never carries a byte looks exactly like one
// that does from every angle except this one — which is the lesson clients/rpc.go
// cost, and the reason plane_only_transport_test.go stands.
func TestCanonicalProjectsSocketIsLoadBearing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	stop := servePeer(t, []plane.Project{{Owner: "acme", Name: "web"}})

	c := canonicalProjects{}
	if _, err := c.List(context.Background(), "acme"); err != nil {
		t.Fatalf("with the socket bound, List must succeed: %v", err)
	}

	// The peer is still alive; only its door is gone.
	sock := zip.SocketPath("iam")
	if err := os.Remove(sock); err != nil {
		t.Fatalf("unlink %s: %v", sock, err)
	}
	if _, err := c.List(context.Background(), "acme"); err == nil {
		t.Fatal("the socket was unlinked and the call SUCCEEDED — nothing is crossing it")
	}

	// Rebind and it works again: the socket is the whole dependency.
	_ = stop()
	stop = servePeer(t, []plane.Project{{Owner: "acme", Name: "web"}})
	t.Cleanup(func() { _ = stop() })
	rows, err := c.List(context.Background(), "acme")
	if err != nil {
		t.Fatalf("after rebinding, List must succeed again: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "web" {
		t.Fatalf("after rebinding, List returned %+v", rows)
	}
}

// TestCanonicalProjectsHoldsNoAddress is the knob check. A peer is addressed by
// NAME, so the store has nothing to hold: no base URL, no http.Client, no
// credential, no cache. unsafe.Sizeof is the whole assertion — it is zero exactly
// while the struct stays empty, and a reviewer who adds a field has to come here
// and say why this deployment needs one.
func TestCanonicalProjectsHoldsNoAddress(t *testing.T) {
	if sz := unsafe.Sizeof(canonicalProjects{}); sz != 0 {
		t.Fatalf("canonicalProjects grew to %d bytes; a peer is addressed by NAME, so "+
			"there is no address, credential or cache for it to hold", sz)
	}
}
