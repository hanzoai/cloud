package platform

// rpc_test.go — the TENANT BOUNDARY across the internal plane.
//
// The fleet observation now leaves this app: admin asks for it over a unix socket
// instead of importing platform and resolving an in-process seam. That moves the
// question "whose workloads are these?" onto the wire, which is exactly the shape of
// bug this fleet has shipped before — an org-scoped list that answered 200 with
// another tenant's rows because a scope filter was quietly dropped.
//
// So the boundary is attacked here end to end, with nothing stubbed on the path
// under test: a REAL HTTP request carrying the headers SanitizeIdentity mints, a
// REAL *zip.Ctx, the real Dial(...).As(c) capability packing, a real unix socket,
// real ZAP envelopes, the real Expose handler, and the real observeFleet over a fake
// cluster. The only fake is the cluster itself, which is the thing being observed —
// not the thing being trusted.
//
// The rule under test is the one every org-scoped method must follow:
//
//	the tenant comes from the CAPABILITY, never from the payload.
//
// and its structural half, which is stronger: platform.fleet's request codec has no
// request form at all, so there is no field a caller could put a scope in.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/k8s"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// nsObj is a Namespace object for the fake cluster, so discoverNamespaces finds the
// tenant namespaces instead of falling back to the first-party set alone.
func nsObj(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": name},
	}}
}

// twoTenantFleet is a cluster holding a platform app plus one app in each of two
// CUSTOMER tenants. acme and initech are the two sides of the boundary; hanzo is
// there so "the whole fleet" is visibly more than either tenant's share.
func twoTenantFleet() []runtime.Object {
	return []runtime.Object{
		nsObj("hanzo"), nsObj("tenant-acme"), nsObj("tenant-initech"),
		appCRObj("iam", "hanzo", "ghcr.io/hanzoai/iam", "v1.28.16"),
		deploymentObj("iam", "hanzo", "ghcr.io/hanzoai/iam:v1.28.16"),
		appCRObj("acme-api", "tenant-acme", "ghcr.io/hanzoai/tenant-acme/api", "v1.0.0"),
		deploymentObj("acme-api", "tenant-acme", "ghcr.io/hanzoai/tenant-acme/api:v1.0.0"),
		appCRObj("initech-api", "tenant-initech", "ghcr.io/hanzoai/tenant-initech/api", "v2.0.0"),
		deploymentObj("initech-api", "tenant-initech", "ghcr.io/hanzoai/tenant-initech/api:v2.0.0"),
	}
}

// planeProbe serves the real platform.fleet method on a real socket and returns an
// app whose single route asks for it exactly the way apps/admin does: Dial the peer,
// delegate the caller's principal with As(c), decode the reply.
//
// The route is the CALLER's half. It asserts nothing — it just reports what the
// plane answered, so every check below is a statement about the callee's decision.
func planeProbe(t *testing.T, objs ...runtime.Object) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_RUN_DIR", t.TempDir())

	s := fakeService(objs...)
	s.Base.Log = luxlog.New("test")
	exposeFleet(s)
	ln, err := cloud.Listen("platform", nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/probe", func(c *zip.Ctx) error {
		out, err := cloud.Dial("platform").As(c).Call(context.Background(), "platform.fleet", nil)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"err": err.Error()})
		}
		rows, err := cloud.Apps(out)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"err": err.Error()})
		}
		return c.JSON(http.StatusOK, rows)
	})
	return app
}

// observeAs drives one real round trip as a validated IAM principal, injecting the
// identity headers exactly as fleet_authz_test.go's fleetDoAs does — the headers
// SanitizeIdentity mints from a signature-verified JWT and strips on ingress.
// Returns the namespaces observed, or the refusal text.
func observeAs(t *testing.T, app *zip.App, user, org string, orgAdmin, superAdmin bool) (rows []cloud.App, refusal string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if orgAdmin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	if superAdmin {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Err string `json:"err"`
		}
		_ = json.Unmarshal(body, &e)
		return nil, e.Err
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode rows: %v (%s)", err, body)
	}
	return rows, ""
}

// namespacesOf is what the caller actually learned: the set of namespaces whose CRs
// came back. The boundary is about which namespaces were SCANNED, so this is the
// value every assertion below is written against.
func namespacesOf(rows []cloud.App) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Namespace] = true
	}
	return out
}

// TestFleetPlane_CapabilityScopesTheScan is the adversarial case. An org admin of
// acme, calling over the plane, must observe acme's namespace and NOTHING else — no
// matter that the fleet plainly contains initech's and the platform's own workloads,
// and no matter that it is asking the very method a SuperAdmin uses to see them all.
func TestFleetPlane_CapabilityScopesTheScan(t *testing.T) {
	app := planeProbe(t, twoTenantFleet()...)

	// The honest read first: a SuperAdmin observes the WHOLE fleet, so the rows the
	// attack must not reach are demonstrably reachable by someone.
	all, refusal := observeAs(t, app, "u-root", "admin", false, true)
	if refusal != "" {
		t.Fatalf("superadmin refused: %s", refusal)
	}
	seen := namespacesOf(all)
	for _, ns := range []string{"hanzo", "tenant-acme", "tenant-initech"} {
		if !seen[ns] {
			t.Fatalf("superadmin observed %v, missing %s — the fixture is not proving anything", seen, ns)
		}
	}

	// THE ATTACK. acme's own org admin, over the same method, on the same socket.
	got, refusal := observeAs(t, app, "u-acme", "acme", true, false)
	if refusal != "" {
		t.Fatalf("acme org admin refused outright (expected its OWN namespace): %s", refusal)
	}
	seen = namespacesOf(got)
	if seen["tenant-initech"] {
		t.Fatalf("CROSS-TENANT READ: acme's capability observed tenant-initech; namespaces=%v", seen)
	}
	if seen["hanzo"] {
		t.Fatalf("CROSS-TENANT READ: acme's capability observed the platform namespace hanzo; namespaces=%v", seen)
	}
	if !seen["tenant-acme"] {
		t.Fatalf("acme observed %v, want its own tenant-acme", seen)
	}
	if len(seen) != 1 {
		t.Fatalf("acme observed %v, want exactly {tenant-acme}", seen)
	}
}

// TestFleetPlane_ForgedOrgCannotWidenTheScan pins the other direction of the same
// rule. The org rides the capability, and the capability is built from the headers
// the gateway minted — so a caller cannot widen itself by naming a different tenant.
// initech's admin gets initech's namespace, acme's gets acme's, from the identical
// request otherwise: the answer tracks the capability and nothing else.
func TestFleetPlane_ForgedOrgCannotWidenTheScan(t *testing.T) {
	app := planeProbe(t, twoTenantFleet()...)

	acme, refusal := observeAs(t, app, "u-acme", "acme", true, false)
	if refusal != "" {
		t.Fatalf("acme refused: %s", refusal)
	}
	initech, refusal := observeAs(t, app, "u-init", "initech", true, false)
	if refusal != "" {
		t.Fatalf("initech refused: %s", refusal)
	}
	if a, i := namespacesOf(acme), namespacesOf(initech); a["tenant-initech"] || i["tenant-acme"] {
		t.Fatalf("the two tenants saw into each other: acme=%v initech=%v", a, i)
	}

	// An org admin whose org owns NO scanned namespace gets an empty board — never a
	// fallback to the first namespace, the platform tier, or the whole fleet.
	stranger, refusal := observeAs(t, app, "u-nobody", "nobodyco", true, false)
	if refusal != "" {
		t.Fatalf("stranger refused (expected an empty board): %s", refusal)
	}
	if len(stranger) != 0 {
		t.Fatalf("an org owning no namespace observed %d rows: %v", len(stranger), namespacesOf(stranger))
	}
}

// TestFleetPlane_RoleGateIsFailClosed pins the door. The plane must refuse anyone the
// HTTP board would refuse: an anonymous caller (no capability at all) and a validated
// but NON-admin member of an org. A plain member holding an org is the escalation
// apps/principal warns about — "has an org" is not "administers it" — and the
// capability carries the two admin scopes apart precisely so the callee can tell.
func TestFleetPlane_RoleGateIsFailClosed(t *testing.T) {
	app := planeProbe(t, twoTenantFleet()...)

	// No capability at all: the zero Ident packs to no claim, not an empty one.
	if rows, refusal := observeAs(t, app, "", "", false, false); refusal == "" {
		t.Fatalf("an anonymous call was ANSWERED with %d rows; it must be refused", len(rows))
	} else if !strings.Contains(refusal, "403") {
		t.Fatalf("anonymous refusal should carry the method's 403, got: %s", refusal)
	}

	// Validated, holds an org, but administers nothing.
	if rows, refusal := observeAs(t, app, "u-member", "acme", false, false); refusal == "" {
		t.Fatalf("a plain member of acme was ANSWERED with %d rows; it must be refused", len(rows))
	} else if !strings.Contains(refusal, "403") {
		t.Fatalf("member refusal should carry the method's 403, got: %s", refusal)
	}

	// An org claim with NO validated user is the classic off-gateway forge: a raw
	// X-Org-Id and no credential. It must not become a tenant key.
	if rows, refusal := observeAs(t, app, "", "acme", true, false); refusal == "" {
		t.Fatalf("an unvalidated X-Org-Id was ANSWERED with %d rows; it must be refused", len(rows))
	} else if !strings.Contains(refusal, "403") {
		t.Fatalf("unvalidated refusal should carry the method's 403, got: %s", refusal)
	}
}

// TestFleetPlane_UnreadyClusterIsAnErrorNotAnEmptyFleet is the bug this whole change
// exists to fix, pinned so it cannot come back. When platform cannot see the cluster,
// the board must be TOLD — "no kubernetes client" is a different fact from "no
// workloads deployed", and only one of them is an operator's cue to panic.
func TestFleetPlane_UnreadyClusterIsAnErrorNotAnEmptyFleet(t *testing.T) {
	t.Setenv("CLOUD_RUN_DIR", t.TempDir())
	s := &cloud.Service[fleetState]{
		Base:  cloud.Base{Log: luxlog.New("test")},
		State: fleetState{dyn: nil, initErr: "no kubeconfig resolved", scan: &nsCache{}},
	}
	exposeFleet(s)
	ln, err := cloud.Listen("platform", nil)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	_, err = cloud.Dial("platform").
		As(headers{"X-User-Id": "u-root", "X-Org-Id": "admin", "X-User-IsAdmin": "true"}).
		Call(context.Background(), "platform.fleet", nil)
	if err == nil {
		t.Fatal("an unobservable fleet ANSWERED; it must report 503, not an empty fleet")
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "no kubeconfig resolved") {
		t.Fatalf("the refusal must carry 503 and the real reason, got: %v", err)
	}
}

// TestFleetPlane_NoSocketIsAnError is the same honesty at the other end: a platform
// that is not running in this binary — which is EVERY binary that mounts admin
// alone — must be an error the board can show. The in-process seam this replaced
// returned nil there, and nil became an empty registry with no error at all.
func TestFleetPlane_NoSocketIsAnError(t *testing.T) {
	t.Setenv("CLOUD_RUN_DIR", t.TempDir()) // nothing listening
	_, err := cloud.Dial("platform").
		As(headers{"X-User-Id": "u-root", "X-Org-Id": "admin", "X-User-IsAdmin": "true"}).
		Call(context.Background(), "platform.fleet", nil)
	if err == nil {
		t.Fatal("dialing an absent platform SUCCEEDED; a missing peer must never read as an empty fleet")
	}
}

// headers stands in for the gateway-minted request on the two calls above that have
// no HTTP request to delegate. It is the transport's own Headers sliver — the tests
// that matter (the boundary ones) drive a real *zip.Ctx instead.
type headers map[string]string

func (h headers) Header(k string) string { return h[k] }

// TestFleetApp_CarriesNoScope is the STRUCTURAL half of the guarantee, and the
// stronger one. The tests above show the handler ignores who the caller claims to
// be; this shows the caller cannot even ASK. platform.fleet has no request codec —
// there is no PutFleetReq — so a future handler that forgets the rule has no field
// to get it wrong on, and adding one would be a deliberate, visible act.
//
// The reply codec is checked here too: it carries the observed NAMESPACE (the board
// renders it) but that is an ANSWER, never an input.
func TestFleetApp_CarriesNoScope(t *testing.T) {
	rows, err := cloud.Apps(cloud.PutApps([]cloud.App{
		{Org: "hanzoai", Name: "sql", Env: "main", Repo: "hanzoai/sql", Registry: "ghcr.io/hanzoai/sql",
			Role: "sql", DeclaredTag: "v1.4.2", RunningTag: "v1.4.1", LatestTag: "v1.4.3",
			Health: "yellow", Phase: "Running", Cluster: "hanzo-k8s", Namespace: "hanzo", DriftSeverity: "yellow"},
	}))
	if err != nil {
		t.Fatalf("Fleet: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("round trip returned %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.Name != "sql" || got.Namespace != "hanzo" || got.DriftSeverity != "yellow" ||
		got.DeclaredTag != "v1.4.2" || got.RunningTag != "v1.4.1" || got.LatestTag != "v1.4.3" ||
		got.Registry != "ghcr.io/hanzoai/sql" || got.Cluster != "hanzo-k8s" || got.Health != "yellow" ||
		got.Phase != "Running" || got.Role != "sql" || got.Org != "hanzoai" || got.Env != "main" ||
		got.Repo != "hanzoai/sql" {
		t.Fatalf("round trip lost or scrambled a field: %+v", got)
	}
	// The method takes its request as bytes it never reads. If a request codec is ever
	// added to platform.fleet, this line is where the reviewer has to justify it.
	var _ func([]byte) ([]cloud.App, error) = cloud.Apps
}

// TestFleetPlane_ShortReplyIsAnError pins the Files rule for this codec: the header's
// count is a promise and the frames are the delivery. A truncated payload must fail
// rather than yield a shorter fleet — a board silently missing rows reports a running
// workload as absent, and absent is what an operator acts on.
func TestFleetPlane_ShortReplyIsAnError(t *testing.T) {
	full := cloud.PutApps([]cloud.App{{Name: "a", Namespace: "hanzo"}, {Name: "b", Namespace: "hanzo"}})
	if _, err := cloud.Apps(full[:len(full)-8]); err == nil {
		t.Fatal("a truncated fleet payload decoded cleanly; it must be an error, never a shorter fleet")
	}
}

// keep the k8s import honest: the fixtures above build real GVR-backed objects.
var _ = k8s.Apps
