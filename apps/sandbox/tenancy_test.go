package sandbox

// Tenancy is the whole product here. A box runs somebody's code on our nodes;
// if one org can reach another's box, nothing else about this subsystem matters.
//
// These tests drive the REAL router and the REAL per-org stores. Each one names
// the specific way a tenant boundary is crossed and proves it is not.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestForgedOrgIsRefusedOnEveryRoute is the anonymous-forge case: a caller that
// is not behind the gateway sends X-Org-Id: victim with no credential. The
// identity middleware restores that header for data scoping but leaves
// X-User-Id empty, so principal.Org must refuse — on every route, not just the
// mutating ones. An empty list would already be a leak (it says "this org
// exists and holds nothing").
func TestForgedOrgIsRefusedOnEveryRoute(t *testing.T) {
	r := newRig(t)
	victim := r.mkBox(t, "victim", "svc", classExec)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/sandbox/boxes"},
		{http.MethodPost, "/v1/sandbox/boxes"},
		{http.MethodGet, "/v1/sandbox/boxes/" + victim.ID},
		{http.MethodDelete, "/v1/sandbox/boxes/" + victim.ID},
		{http.MethodPost, "/v1/sandbox/boxes/" + victim.ID + "/suspend"},
		{http.MethodPost, "/v1/sandbox/boxes/" + victim.ID + "/resume"},
		{http.MethodGet, "/v1/sandbox/boxes/" + victim.ID + "/events"},
		{http.MethodGet, "/v1/sandbox/boxes/" + victim.ID + "/fs/list"},
		{http.MethodPost, "/v1/sandbox/boxes/" + victim.ID + "/proc/exec"},
		{http.MethodPost, "/v1/sandbox/boxes/" + victim.ID + "/git/clone"},
		{http.MethodPost, "/v1/sandbox/boxes/" + victim.ID + "/agent/run"},
	} {
		code, body := r.forge(t, tc.method, tc.path, "victim", map[string]any{})
		if code != http.StatusForbidden {
			t.Errorf("%s %s with a forged org: want 403, got %d (%s)", tc.method, tc.path, code, body)
		}
	}
	// And nothing reached the box.
	if n := len(r.box.calls); n != 0 {
		t.Fatalf("a forged request reached a box %d time(s)", n)
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
	mine := r.mkBox(t, "acme", "app", classDev)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/sandbox/boxes/" + mine.ID},
		{http.MethodDelete, "/v1/sandbox/boxes/" + mine.ID},
		{http.MethodPost, "/v1/sandbox/boxes/" + mine.ID + "/suspend"},
		{http.MethodPost, "/v1/sandbox/boxes/" + mine.ID + "/resume"},
		{http.MethodGet, "/v1/sandbox/boxes/" + mine.ID + "/events"},
		{http.MethodGet, "/v1/sandbox/boxes/" + mine.ID + "/fs/read?path=/etc/passwd"},
		{http.MethodPost, "/v1/sandbox/boxes/" + mine.ID + "/proc/exec"},
	} {
		code, body := r.do(t, tc.method, tc.path, "evilcorp", map[string]any{"command": "cat /work/secrets"})
		if code != http.StatusNotFound {
			t.Errorf("evilcorp %s %s: want 404, got %d (%s)", tc.method, tc.path, code, body)
		}
	}
	if n := len(r.box.calls); n != 0 {
		t.Fatalf("a cross-org request reached a box %d time(s)", n)
	}

	// The owner still reaches it, so the gate is a gate and not a wall.
	if code, _ := r.do(t, http.MethodGet, "/v1/sandbox/boxes/"+mine.ID, "acme", nil); code != http.StatusOK {
		t.Fatalf("owner read: want 200, got %d", code)
	}
}

// TestListNeverCrossesOrgs proves the collection route too: two orgs each with
// boxes see exactly their own.
func TestListNeverCrossesOrgs(t *testing.T) {
	r := newRig(t)
	a := r.mkBox(t, "acme", "app", classDev)
	b := r.mkBox(t, "beta", "app", classDev) // same project NAME, different org

	for org, want := range map[string]string{"acme": a.ID, "beta": b.ID} {
		code, body := r.do(t, http.MethodGet, "/v1/sandbox/boxes", org, nil)
		if code != http.StatusOK {
			t.Fatalf("%s list: %d", org, code)
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
	r := newRig(t)
	r.mkBox(t, "acme", "app", classDev)
	r.mkBox(t, "beta", "app", classDev)
	pvcs, err := r.cs.CoreV1().PersistentVolumeClaims("hanzo-boxes").List(t.Context(), listAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(pvcs.Items) != 2 {
		t.Fatalf("want 2 volumes, got %d", len(pvcs.Items))
	}
	for _, p := range pvcs.Items {
		if p.Labels[labOrg] == "" {
			t.Fatalf("volume %s carries no org label", p.Name)
		}
	}
}

// TestBoxAddressIsNeverReturned: Addr is the pod's in-cluster URL. A client that
// learned it could try to reach the box directly and walk around the org gate,
// so it must not appear in any response body.
func TestBoxAddressIsNeverReturned(t *testing.T) {
	r := newRig(t)
	b := r.mkBox(t, "acme", "app", classDev)

	live, err := r.store(t, "acme").Get("acme", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if live.Addr == "" {
		t.Fatal("box has no recorded address — the rest of this test proves nothing")
	}
	for _, path := range []string{"/v1/sandbox/boxes", "/v1/sandbox/boxes/" + b.ID} {
		_, body := r.do(t, http.MethodGet, path, "acme", nil)
		if strings.Contains(string(body), live.Addr) || strings.Contains(string(body), "10.0.0.") {
			t.Fatalf("%s leaked the box address: %s", path, body)
		}
	}
}

// TestProxyStampsBoxIdentity: every proxied call carries X-Box-Id so the box can
// refuse a caller that reached it through a recycled Pod IP. Here we assert the
// control plane hands the box the identity it believes it is talking to; the
// refusal itself is boxd's half.
func TestProxyStampsBoxIdentity(t *testing.T) {
	r := newRig(t)
	b := r.mkBox(t, "acme", "app", classDev)
	r.box.body = `{"entries":[]}`

	if code, _ := r.do(t, http.MethodGet, "/v1/sandbox/boxes/"+b.ID+"/fs/list?path=/", "acme", nil); code != http.StatusOK {
		t.Fatalf("fs/list: %d", code)
	}
	got := r.box.last()
	if got.Box.ID != b.ID {
		t.Fatalf("proxy addressed box %q, want %q", got.Box.ID, b.ID)
	}
	if got.Box.Org != "acme" {
		t.Fatalf("proxy carried org %q, want acme", got.Box.Org)
	}
}
