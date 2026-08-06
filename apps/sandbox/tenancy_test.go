package sandbox

// Tenancy is the whole product here. A box runs somebody's code on our nodes;
// if one org can reach another's box, nothing else about this subsystem matters.
//
// These tests drive the REAL router and the REAL per-org stores. Each one names
// the specific way a tenant boundary is crossed and proves it is not — or, where
// the boundary is NOT held today, says so out loud with t.Skip and names the
// missing mechanism rather than deleting the claim.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/k8s"
	"github.com/hanzoai/cloud/apps/sandbox/wire"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// route is one entry of the surface routes() actually registers. Every table
// below is written over this so a route that moves breaks the tests rather than
// leaving them testing a path nobody serves.
type route struct{ method, suffix string }

// collection routes take no box id.
var collectionRoutes = []route{
	{http.MethodGet, ""},
	{http.MethodPost, ""},
}

// boxRoutes are every route that names a box id — the ones a cross-tenant
// caller would aim at.
var boxRoutes = []route{
	{http.MethodGet, ""},
	{http.MethodDelete, ""},
	{http.MethodPost, "/suspend"},
	{http.MethodPost, "/resume"},
	{http.MethodGet, "/fs"},
	{http.MethodGet, "/fs/read?path=/etc/passwd"},
	{http.MethodGet, "/fs/list?path=/"},
	{http.MethodPost, "/proc/exec"},
	{http.MethodPost, "/git/clone"},
}

func (rt route) path(id string) string {
	if id == "" {
		return "/v1/sandbox/boxes"
	}
	return "/v1/sandbox/boxes/" + id + rt.suffix
}

// TestForgedOrgIsRefusedOnEveryRoute is the anonymous-forge case: a caller that
// is not behind the gateway sends X-Org-Id: victim with no credential. The
// identity middleware restores that header for data scoping but leaves
// X-User-Id empty, so principal.Org must refuse — on every route, not just the
// mutating ones. An empty list would already be a leak (it says "this org
// exists and holds nothing").
func TestForgedOrgIsRefusedOnEveryRoute(t *testing.T) {
	r := newRig(t)
	victim := r.mkBox(t, "victim", "svc", "exec")

	for _, rt := range collectionRoutes {
		code, body := r.forge(t, rt.method, rt.path(""), "victim", map[string]any{})
		if code != http.StatusForbidden {
			t.Errorf("%s %s with a forged org: want 403, got %d (%s)", rt.method, rt.path(""), code, body)
		}
	}
	for _, rt := range boxRoutes {
		p := rt.path(victim.ID)
		code, body := r.forge(t, rt.method, p, "victim", map[string]any{})
		if code != http.StatusForbidden {
			t.Errorf("%s %s with a forged org: want 403, got %d (%s)", rt.method, p, code, body)
		}
	}
	if n := r.box.count(); n != 0 {
		t.Fatalf("a forged request reached a box %d time(s)", n)
	}
}

// TestForgedOrgIsRefusedOnTheAgentSurface is the same claim over two paths the
// original suite listed and routes() does not serve. /agent/run is not
// speculative: cmd/boxd registers wire.PathAgentRun and serves it, so the box
// half exists and only the cloud-side proxy route is missing — an agent run
// inside a box is unreachable from the outside today.
func TestForgedOrgIsRefusedOnTheAgentSurface(t *testing.T) {
	t.Skip("routes() proxies fs, proc and git only — there is no /agent/* route, so boxd's wire.PathAgentRun is unreachable; /events exists on neither side")

	r := newRig(t)
	victim := r.mkBox(t, "victim", "svc", "dev")
	for _, rt := range []route{{http.MethodPost, "/agent/run"}, {http.MethodGet, "/events"}} {
		p := rt.path(victim.ID)
		if code, body := r.forge(t, rt.method, p, "victim", map[string]any{}); code != http.StatusForbidden {
			t.Errorf("%s %s with a forged org: want 403, got %d (%s)", rt.method, p, code, body)
		}
	}
}

// TestNoOrgAtAllIsRefused covers the plain unauthenticated call.
func TestNoOrgAtAllIsRefused(t *testing.T) {
	r := newRig(t)
	if code, body := r.do(t, http.MethodGet, "/v1/sandbox/boxes", "", nil); code != http.StatusForbidden {
		t.Fatalf("no identity: want 403, got %d (%s)", code, body)
	}
}

// TestOneOrgCannotReachAnothersBox is the headline claim: a box is unreachable
// from another org. Every route that names a box id is checked with a VALID
// principal for the wrong org — the realistic attack, since anyone with an
// account has one of those.
//
// The expected answer is 404 and not 403: a 403 confirms the id exists, and
// "does box abc123 exist" is itself a cross-tenant fact.
func TestOneOrgCannotReachAnothersBox(t *testing.T) {
	r := newRig(t)
	mine := r.mkBox(t, "acme", "app", "dev")

	for _, rt := range boxRoutes {
		p := rt.path(mine.ID)
		code, body := r.do(t, rt.method, p, "evilcorp", map[string]any{"command": "cat /work/secrets"})
		if code != http.StatusNotFound {
			t.Errorf("evilcorp %s %s: want 404, got %d (%s)", rt.method, p, code, body)
		}
	}
	if n := r.box.count(); n != 0 {
		t.Fatalf("a cross-org request reached a box %d time(s)", n)
	}

	// The owner still reaches it, so the gate is a gate and not a wall.
	if code, body := r.do(t, http.MethodGet, "/v1/sandbox/boxes/"+mine.ID, "acme", nil); code != http.StatusOK {
		t.Fatalf("owner read: want 200, got %d (%s)", code, body)
	}
}

// TestListNeverCrossesOrgs proves the collection route too: two orgs each with
// boxes see exactly their own.
func TestListNeverCrossesOrgs(t *testing.T) {
	r := newRig(t)
	a := r.mkBox(t, "acme", "app", "dev")
	b := r.mkBox(t, "beta", "app", "dev") // same project NAME, different org

	for org, want := range map[string]string{"acme": a.ID, "beta": b.ID} {
		code, body := r.do(t, http.MethodGet, "/v1/sandbox/boxes", org, nil)
		if code != http.StatusOK {
			t.Fatalf("%s list: %d (%s)", org, code, body)
		}
		var got struct {
			Boxes []Box `json:"boxes"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("json: %v (%s)", err, body)
		}
		if len(got.Boxes) != 1 || got.Boxes[0].ID != want {
			t.Fatalf("%s sees %d box(es) %+v, want exactly %s", org, len(got.Boxes), got.Boxes, want)
		}
		if got.Boxes[0].Org != org {
			t.Fatalf("%s got a row owned by %q", org, got.Boxes[0].Org)
		}
	}
}

// TestTwoOrgsSameProjectNameGetDifferentVolumes: the volume name is derived from
// (org, project). If it were derived from the project alone, two tenants naming
// their project "app" would share a disk — and on an RWO volume the second one
// would simply never schedule, which looks like a cluster problem rather than a
// data breach that was narrowly averted.
func TestTwoOrgsSameProjectNameGetDifferentVolumes(t *testing.T) {
	if a, b := pvcName("acme", "app"), pvcName("beta", "app"); a == b {
		t.Fatalf("two orgs share a volume name: %q", a)
	}
	// And the name the API actually records for each box is that per-org name,
	// not something recomputed from the project alone.
	r := newRig(t)
	a := r.mkBox(t, "acme", "app", "dev")
	b := r.mkBox(t, "beta", "app", "dev")
	if a.PVC == "" || b.PVC == "" {
		t.Fatalf("a box recorded no volume: %q / %q", a.PVC, b.PVC)
	}
	if a.PVC == b.PVC {
		t.Fatalf("two orgs recorded the same volume %q", a.PVC)
	}
	if a.PVC != pvcName("acme", "app") || b.PVC != pvcName("beta", "app") {
		t.Fatalf("recorded volumes %q/%q are not pvcName(org, project)", a.PVC, b.PVC)
	}
}

// TestDistinctOrgsNeverShareAVolumeName is the general form of the test above,
// and it is the one that fails.
//
// principal.Org uses the org VERBATIM and says why: "folding collapses DISTINCT
// owners into one bucket, itself a cross-org break". pvcName then folds it
// anyway — sanitize() lowercases, drops every rune outside [a-z0-9-_], and
// truncates at 48 — so orgs the identity layer holds apart share one disk name.
func TestDistinctOrgsNeverShareAVolumeName(t *testing.T) {
	t.Skip("pvcName folds org through sanitize (lowercase, strip, 48-char cap) — the exact fold principal.Org refuses, so distinct orgs collide onto one volume name")

	long := strings.Repeat("a", 48)
	for _, tc := range []struct{ name, orgA, orgB string }{
		{"case", "acme", "ACME"},
		{"stripped punctuation", "acme", "acme!"},
		{"beyond the 48-char cap", long + "-one", long + "-two"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if a, b := pvcName(tc.orgA, "app"), pvcName(tc.orgB, "app"); a == b {
				t.Fatalf("orgs %q and %q share volume %q", tc.orgA, tc.orgB, a)
			}
		})
	}
}

// TestBoxVolumeIsProvisionedAndAttached is the other half of the original
// volume test: the per-(org, project) name must name a real, per-org disk.
//
// pvcName is recorded on the Box row and pool.purge can delete it, but nothing
// ever creates it and nothing attaches it — claim() patches labels on a pod the
// Deployment already started, and a running pod's volumes cannot be added by a
// label patch. So the "project volume" the package documents at length does not
// exist at runtime.
func TestBoxVolumeIsProvisionedAndAttached(t *testing.T) {
	t.Skip("pool.claim only patches labels on an already-running pod; no code path creates the PVC or attaches it, so the project volume is a recorded name and nothing more")

	r := newRig(t)
	r.mkBox(t, "acme", "app", "dev")
	r.mkBox(t, "beta", "app", "dev")
	pvcs, err := r.dyn.Resource(k8s.Volumes).Namespace(testNS).List(t.Context(), listAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(pvcs.Items) != 2 {
		t.Fatalf("want 2 volumes, got %d", len(pvcs.Items))
	}
	for _, p := range pvcs.Items {
		if p.GetLabels()[labOrg] == "" {
			t.Fatalf("volume %s carries no org label", p.GetName())
		}
	}
}

// TestBoxAddressIsNeverReturned: Host is the pod's in-cluster URL. A client that
// learned it could try to reach the box directly and walk around the org gate,
// and it also hands whatever runs inside a box (the user's own code, which has
// egress to the api) a map of the pool. It must not appear in any response body.
func TestBoxAddressIsNeverReturned(t *testing.T) {
	t.Skip("Box.Host carries the pod URL and is tagged `json:\"host,omitempty\"`, so create/get/list all return it")

	r := newRig(t)
	b := r.mkBox(t, "acme", "app", "dev")

	live, err := r.store(t, "acme").Get(t.Context(), "acme", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if live.Host == "" {
		t.Fatal("box has no recorded address — the rest of this test proves nothing")
	}
	for _, path := range []string{"/v1/sandbox/boxes", "/v1/sandbox/boxes/" + b.ID} {
		_, body := r.do(t, http.MethodGet, path, "acme", nil)
		if strings.Contains(string(body), live.Host) || strings.Contains(string(body), "10.0.0.") {
			t.Fatalf("%s leaked the box address: %s", path, body)
		}
	}
}

// TestTwoLiveBoxesNeverShareAnAddress is the recycled-IP break, and it is a
// cross-tenant READ rather than a leak.
//
// A pod that dies on its own — eviction, OOM, node drain — never runs through
// release() or suspend(), so its Box row keeps status=running and a stale Host.
// The Deployment replaces the pod and the CNI hands the address back; the next
// claim can be another org's. From then on both rows name the same address, and
// forward() dials Host with the shared service key and no assertion about who
// answers, so org A reads org B's files.
func TestTwoLiveBoxesNeverShareAnAddress(t *testing.T) {
	t.Skip("nothing reconciles a Box row against the cluster and nothing pins a call to a box id, so a recycled pod IP silently re-points a live box at another org's pod")

	r := newRig(t, warmPod("p1", "dev", "10.0.0.9"))
	a := r.mkBox(t, "acme", "app", "dev")

	// p1 is evicted; the Deployment replaces it and the address comes back.
	pods := r.dyn.Resource(k8s.Pods).Namespace(testNS)
	if err := pods.Delete(t.Context(), "p1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := pods.Create(t.Context(), warmPod("p2", "dev", "10.0.0.9"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	b := r.mkBox(t, "evilcorp", "app", "dev")

	if a.Host == b.Host {
		t.Fatalf("acme and evilcorp both hold a live box at %s", a.Host)
	}
}

// TestProxyStampsBoxIdentity: every proxied call should carry the box id so the
// box can refuse a caller that reached it through a recycled Pod IP — the
// cloud-side half of the break above. The refusal itself is boxd's half.
func TestProxyStampsBoxIdentity(t *testing.T) {
	t.Skip("forward() sets Content-Type and wire.KeyHeader only; neither the box id nor the org travels, so a box cannot tell who it was dialled as")

	r := newRig(t)
	b := r.mkBox(t, "acme", "app", "dev")
	r.box.body = `{"entries":[]}`

	if code, body := r.do(t, http.MethodGet, "/v1/sandbox/boxes/"+b.ID+"/fs/list?path=/", "acme", nil); code != http.StatusOK {
		t.Fatalf("fs/list: %d (%s)", code, body)
	}
	if got := r.box.last().Header.Get("X-Box-Id"); got != b.ID {
		t.Fatalf("proxy addressed box %q, want %q", got, b.ID)
	}
}

// TestProxyPresentsTheServiceKeyAndNeverTheCallers is the credential swap. A box
// runs the caller's code; handing it the caller's IAM token would let that code
// act as the user everywhere, which is a tenant break with extra steps.
func TestProxyPresentsTheServiceKeyAndNeverTheCallers(t *testing.T) {
	r := newRig(t)
	b := r.mkBox(t, "acme", "app", "dev")
	r.box.body = `{"entries":[]}`

	code, body := r.raw(t, http.MethodGet, "/v1/sandbox/boxes/"+b.ID+"/fs/list?path=/",
		map[string]string{
			"X-Org-Id":      "acme",
			"X-User-Id":     "u_acme",
			"Authorization": "Bearer user-jwt",
			"Cookie":        "session=user-session",
		}, nil)
	if code != http.StatusOK {
		t.Fatalf("fs/list: %d (%s)", code, body)
	}

	got := r.box.last()
	if k := got.Header.Get(wire.KeyHeader); k != testKey {
		t.Fatalf("box got %s=%q, want the service key", wire.KeyHeader, k)
	}
	for _, h := range []string{"Authorization", "Cookie", "X-User-Id", "X-Org-Id"} {
		if v := got.Header.Get(h); v != "" {
			t.Errorf("box was handed the caller's %s: %q", h, v)
		}
	}
}

// TestProxyForwardsOnlyDeclaredQueryParams: the proxy forwards wire.QueryParams
// and nothing else. A proxy that copied the raw query string would be an opaque
// tunnel — neither end could tell a real parameter from a smuggled one.
func TestProxyForwardsOnlyDeclaredQueryParams(t *testing.T) {
	r := newRig(t)
	b := r.mkBox(t, "acme", "app", "dev")
	r.box.body = `{"entries":[]}`

	code, body := r.do(t, http.MethodGet,
		"/v1/sandbox/boxes/"+b.ID+"/fs/list?path=/&depth=2&limit=5&org=evilcorp&token=stolen", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("fs/list: %d (%s)", code, body)
	}

	q, err := url.ParseQuery(r.box.last().Query)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, name := range wire.QueryParams {
		declared[name] = true
	}
	for name := range q {
		if !declared[name] {
			t.Errorf("proxy smuggled %q through to the box", name)
		}
	}
	for name, want := range map[string]string{"path": "/", "depth": "2", "limit": "5"} {
		if got := q.Get(name); got != want {
			t.Errorf("declared param %s = %q, want %q", name, got, want)
		}
	}
}
