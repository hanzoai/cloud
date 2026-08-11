package sandbox

// The TRUST half of the derivation: who owns the code a sandbox runs, and what
// that buys it.
//
// These are not input-hygiene cases. runc IS the node's kernel, so getting this
// wrong does not lose a volume — it puts a stranger's prompt one kernel bug away
// from the node every other tenant's sandbox is on. The refusal has to happen in
// runtimeFor because there is no later moment at which it could: by the time the
// pod exists the boundary has already been chosen.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/k8s"
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// leased is a sandbox as api.go builds one: the org is the CALLER's, the volume
// comes from the project. Both facts arrive here already decided, which is why
// runtimeFor asks the Sandbox and never its own arguments.
func leased(org, volume string) Sandbox {
	return Sandbox{ID: "m_1", Org: org, Class: "dev", Project: "p", Volume: volume}
}

// TestRuntimeForAsksWhoOwnsTheCodeAndWhatItKeeps is the two-axis derivation,
// stated as the cases that reach it. `contained` is whether the cluster keeps
// our own boundary to nodes of its own — see confine; without it there is no
// such boundary to take and our code lands where everybody else's does.
func TestRuntimeForAsksWhoOwnsTheCodeAndWhatItKeeps(t *testing.T) {
	const other = "acme"
	for _, c := range []struct {
		name      string
		deploy    string // the fleet's own setting
		contained bool
		ask       string // what a caller asked for, empty for none
		m         Sandbox
		want      string
		wantErr   string
	}{
		// OURS, on a pool of its own. Both facts are read at once: the volume
		// does not cost us the fast boundary, because runc shares a filesystem.
		{"ours, contained, volumeless", "gvisor", true, "", leased(authz.AdminOrg, ""), "runc", ""},
		{"ours, contained, with a volume", "gvisor", true, "", leased(authz.AdminOrg, "v"), "runc", ""},

		// OURS, with nowhere to put it. Fail to gvisor rather than run a model's
		// output on a kernel shared with whatever else the scheduler chose.
		{"ours, uncontained, volumeless", "gvisor", false, "", leased(authz.AdminOrg, ""), "gvisor", ""},
		{"ours, uncontained, with a volume", "gvisor", false, "", leased(authz.AdminOrg, "v"), "gvisor", ""},

		// EVERYBODY ELSE, whatever the topology. A pool of our own does not make
		// a stranger's code ours.
		{"another org, contained", "gvisor", true, "", leased(other, ""), "gvisor", ""},
		{"another org, contained, with a volume", "gvisor", true, "", leased(other, "v"), "gvisor", ""},

		// THE SETTING CANNOT HAND THE FLEET AWAY. A deployment that names runc
		// still gets a kernel for everybody who is not us — one env var was the
		// whole boundary, and now it is not.
		{"the deployment names runc: ours takes it", "runc", true, "", leased(authz.AdminOrg, ""), "runc", ""},
		{"the deployment names runc: a tenant does not", "runc", true, "", leased(other, ""), "gvisor", ""},
		{"the deployment names runc: a tenant with a volume does not", "runc", true, "", leased(other, "v"), "gvisor", ""},

		// A CALLER states a request, so a contradiction is refused rather than
		// corrected — a runtime nobody asked for is the same silence one level up.
		{"a tenant asking for runc is refused", "gvisor", true, "runc", leased(other, ""), "",
			"only for code of ours"},
		{"a tenant asking for runc with a volume is refused", "gvisor", true, "runc", leased(other, "v"), "",
			"only for code of ours"},
		{"we may ask for runc", "gvisor", true, "runc", leased(authz.AdminOrg, ""), "runc", ""},
		{"we may ask for more than we need", "gvisor", true, "kata-clh", leased(authz.AdminOrg, "v"), "kata-clh", ""},
		{"even we cannot put a volume on kata-fc", "gvisor", true, "kata-fc", leased(authz.AdminOrg, "v"), "",
			"cannot mount project volume"},

		// AN UNCONTAINED runc IS NOT ONE WE RUN, FOR US EITHER. The name is in
		// the table; the topology is not, so neither path selects it — and the
		// path that lets us NAME it is the one that would otherwise put it on a
		// pool it does not own.
		{"asking for runc with nowhere to put it", "gvisor", false, "runc", leased(authz.AdminOrg, ""), "",
			"does not keep it to a pool"},
		{"deriving runc with nowhere to put it", "gvisor", false, "", leased(authz.AdminOrg, ""), "gvisor", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &runtime{}
			if c.contained {
				r.bare = bare()
			}
			got, err := r.runtimeFor(c.m, c.ask, c.deploy)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("runtimeFor(%+v, %q) = %q, want a refusal", c.m, c.ask, got)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("refusal %q does not say %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("runtimeFor(%+v, %q): %v", c.m, c.ask, err)
			}
			if got != c.want {
				t.Fatalf("runtimeFor(%+v, %q) = %q, want %q", c.m, c.ask, got, c.want)
			}
		})
	}
}

// NEGATIVE CONTROL (b): a caller who is not us can never obtain a boundary that
// shares the node's kernel.
//
// Exhaustive over every input runtimeFor has — the deployment's setting, the
// caller's request, whether the sandbox keeps anything, and whether the cluster
// contains our boundary — for an org that is not the reserved one. Stated as a
// property rather than as rows, because the interesting case is the combination
// nobody thought to write down.
//
// The carve-out is honest and it is the one residual: an EMPTY answer is the
// node's default runtime, which the deployment declined to name at all. That is
// a deployment fact rather than a derivation fact — production pins
// a fleet set to gvisor — and the property below is the exact one the
// derivation owns: it never NAMES a kernel-sharing boundary for code that is
// not ours.
func TestOnlyOurOwnCodeReachesTheNodesKernel(t *testing.T) {
	orgs := []string{"acme", "hanzo", "zoo", "Admin", "admin ", "admin\n", ""}
	asks := append([]string{""}, sorted()...)
	for _, org := range orgs {
		for _, deploy := range append([]string{""}, sorted()...) {
			for _, ask := range asks {
				for _, vol := range []string{"", "m-acme-p-abc"} {
					for _, contained := range []bool{false, true} {
						r := &runtime{}
						if contained {
							r.bare = bare()
						}
						got, err := r.runtimeFor(leased(org, vol), ask, deploy)
						if err != nil {
							continue // refused, which is the other acceptable answer
						}
						if got == "" {
							// The node's default, and ONLY when the deployment
							// named no boundary of its own. Anything else is a
							// silent downgrade out from under a setting.
							if deploy != "" {
								t.Fatalf("org=%q deploy=%q ask=%q answered the node default, which the deployment did not ask for",
									org, deploy, ask)
							}
							continue
						}
						if !runtimes[got].kernel {
							t.Fatalf("org=%q deploy=%q ask=%q vol=%q contained=%v answered %q, "+
								"which is the node's own kernel", org, deploy, ask, vol, contained, got)
						}
					}
				}
			}
		}
	}
}

// THE INVARIANT ACROSS BOTH PATHS, and the one that catches what a table of
// cases cannot: whatever runtimeFor answers, this deployment can actually place
// it. A boundary with a kernel of its own may be named anywhere; the one without
// exists only where the cluster keeps it to nodes of its own.
//
// It is written as a property because the gap it found was a gap of SYMMETRY —
// the derived path consulted the topology and the by-name path did not, so the
// one caller entitled to ask for the node's kernel was the one caller who could
// get it on a pool it did not own. Two paths, one fact, checked here.
func TestRuntimeForNeverAnswersABoundaryTheClusterCannotPlace(t *testing.T) {
	for _, org := range []string{"acme", authz.AdminOrg} {
		for _, deploy := range append([]string{""}, sorted()...) {
			for _, ask := range append([]string{""}, sorted()...) {
				for _, vol := range []string{"", "m-acme-p-abc"} {
					for _, contained := range []bool{false, true} {
						r := &runtime{}
						if contained {
							r.bare = bare()
						}
						got, err := r.runtimeFor(leased(org, vol), ask, deploy)
						if err != nil || got == "" {
							continue
						}
						if !runtimes[got].kernel && got != r.bare {
							t.Fatalf("org=%q deploy=%q ask=%q contained=%v answered %q, which this "+
								"cluster does not keep to a pool of its own", org, deploy, ask, contained, got)
						}
					}
				}
			}
		}
	}
}

// NEGATIVE CONTROL (c), first half: there is nothing on a request that could
// name an org or a runtime.
//
// A reflective test rather than a prose promise, because the field that gets
// added later is exactly the one nobody re-reads this comment for. Spec.ID and
// Spec.RuntimeClass are what a Go caller in this process may state; the WIRE
// types carry neither an org nor a runtime, so no request can reach either
// axis of the derivation.
// IDENTITY IS NOT A REQUEST, and that is the whole of this rule.
//
// An org on the body would BE the escalation. There is no second source of
// truth to check it against — the value the derivation reads would be the value
// the caller typed — so accepting the field is accepting the claim, and no code
// downstream can undo it. Hence: never on the wire, enforced by shape rather
// than by remembering.
//
// A RUNTIME IS A DIFFERENT AXIS and it is deliberately not in this list, though
// it was. The server holds the entire policy: runtimeFor derives `kernel` from
// m.Org — the validated principal, never a field — and `shares` from the volume,
// and answers a request it cannot honour with a refusal rather than with a
// substitution. So a caller may ASK for runc and cannot GET it, which is not the
// same shape of hazard at all.
//
// Barring the field outright had a cost and no benefit. The benefit was
// imaginary: `want` still flows into runtimeFor from the deployment, and every
// refusal branch under `want != ""` — the two runc ones directly above — became
// unreachable by any real caller, which is to say untested in production
// forever. The cost was real: comparing two boundaries on one task needed a
// redeploy, so nobody compared them.
//
// What replaces it is the test below, which asserts the thing actually worth
// asserting — that asking does not get.
func TestNoWireFieldCanNameAnOrg(t *testing.T) {
	for _, in := range []any{plane.LeaseIn{}, createBody{}} {
		ty := reflect.TypeOf(in)
		for i := 0; i < ty.NumField(); i++ {
			switch n := strings.ToLower(ty.Field(i).Name); {
			case strings.Contains(n, "org"), strings.Contains(n, "owner"),
				strings.Contains(n, "tenant"):
				t.Fatalf("%s.%s would let a caller state its own %s", ty.Name(), ty.Field(i).Name, n)
			}
		}
	}
}

// ASKING IS NOT GETTING — the trust axis, held at the DOOR.
//
// runc is the node's own kernel. The derivation refuses it to anyone outside the
// reserved org, and this proves the refusal survives the trip through a request
// body: a stranger POSTing `{"runtime":"runc"}` is answered 400 with the reason,
// not 201 with a pod beside every other tenant's.
//
// The controls are what make it a proof rather than a coincidence. The SAME body
// from the SAME org without `runtime` reaches the cluster (503, there is none
// here) — so the 400 is a refusal of the ASK and not of the route or the org. And
// the reserved org is refused too, because this cluster keeps no pool of its own
// (`bare` is empty), which is the second half of the derivation: even ours only
// gets that boundary where the topology holds.
func TestAskingForTheNodesKernelOverHTTPDoesNotGetIt(t *testing.T) {
	app := door(t)
	for _, c := range []struct {
		name, org, body string
		want            int
	}{
		{"a stranger asks for the node's kernel", "acme", `{"class":"exec","runtime":"runc"}`, http.StatusBadRequest},
		{"ours asks, on a cluster that keeps no pool", authz.AdminOrg, `{"class":"exec","runtime":"runc"}`, http.StatusBadRequest},
		{"an invented runtime", "acme", `{"class":"exec","runtime":"firecracker"}`, http.StatusBadRequest},
		{"a volume under a boundary that shares nothing", "acme", `{"class":"dev","project":"p","runtime":"kata-fc"}`, http.StatusBadRequest},

		// THE CONTROLS. Both reach the cluster, which this test does not have —
		// so 503 is "the request was fine", and it is what makes every 400 above
		// a statement about the runtime rather than about the request at large.
		{"the control: no runtime named", "acme", `{"class":"exec"}`, http.StatusServiceUnavailable},
		{"the control: a boundary anyone may take", "acme", `{"class":"exec","runtime":"gvisor"}`, http.StatusServiceUnavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			if code := ask(t, app, c.org, "u-"+c.org, c.body); code != c.want {
				t.Fatalf("POST /v1/sandboxes org=%q body=%s = %d, want %d", c.org, c.body, code, c.want)
			}
		})
	}
}

// NEGATIVE CONTROL (c), second half: naming the reserved org on the wire does
// not put a caller in it.
//
// The org rides a VALIDATED principal. A client that sends X-Org-Id: admin with
// nothing to back it is anonymous, and an anonymous caller has no org at all —
// so the request is refused before a sandbox exists to have a boundary. This is
// the whole reason runtimeFor reads m.Org: the value it reads has already been
// through here.
func TestClaimingTheReservedOrgWithoutAPrincipalIsRefused(t *testing.T) {
	app := door(t)
	for _, c := range []struct {
		name, org, user string
		want            int
	}{
		{"the reserved org, unvalidated", authz.AdminOrg, "", http.StatusForbidden},
		{"any org, unvalidated", "acme", "", http.StatusForbidden},
		// The CONTROL. The same request with a principal is accepted as far as
		// the cluster — 503, because these tests have none — which is what makes
		// the 403 above a refusal of the CLAIM and not of the route.
		{"the reserved org, validated", authz.AdminOrg, "u-admin", http.StatusServiceUnavailable},
		{"another org, validated", "acme", "u-acme", http.StatusServiceUnavailable},
	} {
		t.Run(c.name, func(t *testing.T) {
			if code := ask(t, app, c.org, c.user, `{"class":"exec"}`); code != c.want {
				t.Fatalf("POST /v1/sandboxes org=%q user=%q = %d, want %d", c.org, c.user, code, c.want)
			}
		})
	}
}

// The RESUME path returns a row before the derivation runs, so a sandbox
// created under one boundary outlives a deployment that changed its mind. That
// is correct for the pod — a running pod's runtime cannot be changed by
// answering differently — and it cannot cross the trust axis, which is what
// this proves: the store is opened PER ORG, so the only rows a caller can name
// are ones its own org created, and a row created for us is not in another
// org's store to be resumed at all.
func TestResumeCannotCrossTheTrustBoundary(t *testing.T) {
	away(t)
	s, err := New(cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	// A running sandbox of OURS, on the boundary only we may take.
	store, err := storeFor(s, authz.AdminOrg)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	m := Sandbox{ID: "m_ours", Org: authz.AdminOrg, Kind: KindSandbox, Class: "exec",
		Status: "running", Pod: podName("m_ours"), CreatedAt: time.Now().Unix()}
	if err := store.Put(ctx, m); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The CONTROL: we can resume it, so the id is real and the row is live.
	//
	// `super` is false even though the org IS the reserved one, because these are
	// two different facts and this test is about the first. The org names WHOSE
	// store a row lives in; the SuperAdmin attestation says who the caller is. A
	// test that conflated them would pass for the wrong reason.
	got, err := Lease(s, ctx, authz.AdminOrg, authz.AdminOrg, false, Spec{ID: m.ID})
	if err != nil || got.ID != m.ID {
		t.Fatalf("Lease(admin, resume) = %+v, %v — the control did not resume", got, err)
	}

	// ANOTHER ORG NAMING THE SAME ID gets a sandbox of its own, never ours. It
	// fails at the cluster (there is none here), which is already past the point
	// where a resume would have handed over a running pod.
	if _, err = Lease(s, ctx, "acme", "acme", false, Spec{ID: m.ID}); err == nil {
		t.Fatal("Lease(acme) resumed a sandbox belonging to the reserved org")
	}
	if !strings.Contains(err.Error(), "start sandbox") {
		t.Fatalf("Lease(acme) failed with %v, want the failure of a NEW sandbox's start", err)
	}
	out, err := List(s, ctx, "acme", "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, row := range out {
		if row.ID == m.ID {
			t.Fatalf("acme's store holds %s, which belongs to %s", row.ID, authz.AdminOrg)
		}
	}
}

// confine is the containment requirement, and it is the reason runc is a table
// entry rather than a switch. Each case is a topology that has been mistaken for
// containment.
func TestConfineRequiresAPoolOfItsOwn(t *testing.T) {
	pool := func(k, v string) map[string]any { return map[string]any{k: v} }
	taint := []any{map[string]any{"key": "dedicated", "operator": "Equal",
		"value": "sandbox", "effect": "NoSchedule"}}

	for _, c := range []struct {
		name    string
		classes []*unstructured.Unstructured
		want    bool
	}{
		{"no such class", nil, false},
		{"a class with no scheduling at all",
			[]*unstructured.Unstructured{class("runc", nil, nil)}, false},
		{"a pool with no taint — anything may join it",
			[]*unstructured.Unstructured{class("runc", pool("pool", "sandbox"), nil)}, false},
		{"a taint with no pool — it may still land anywhere",
			[]*unstructured.Unstructured{class("runc", nil, taint)}, false},
		{"a pool of its own",
			[]*unstructured.Unstructured{class("runc", pool("pool", "sandbox"), taint)}, true},
		{"a pool it shares with a boundary other tenants take",
			[]*unstructured.Unstructured{
				class("runc", pool("pool", "code-exec"), taint),
				class("gvisor", pool("pool", "code-exec"), taint),
			}, false},
		{"its own pool, beside a boundary on another",
			[]*unstructured.Unstructured{
				class("runc", pool("pool", "sandbox"), taint),
				class("gvisor", pool("pool", "code-exec"), taint),
			}, true},
		{"its own pool, beside a boundary pinned nowhere",
			[]*unstructured.Unstructured{
				class("runc", pool("pool", "sandbox"), taint),
				class("kata-clh", nil, nil),
			}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &runtime{dyn: fakeClasses(c.classes...)}
			if got := r.confine(context.Background(), "runc"); got != c.want {
				t.Fatalf("confine = %v, want %v", got, c.want)
			}
		})
	}

	// NO ANSWER IS A NO. A cluster we cannot read — no client, or an RBAC grant
	// we do not have — leaves our boundary unoffered rather than assumed.
	if (&runtime{}).confine(context.Background(), "runc") {
		t.Fatal("confine said yes with no cluster client")
	}
	if (&runtime{dyn: fakeClasses()}).confine(context.Background(), "") {
		t.Fatal("confine said yes for a boundary with no name")
	}
}

// THE CONTAINMENT PREDICATE, asked of the cluster it will actually be asked
// about. The cases above are fakes and prove the RULE; this proves the rule is
// reading the same objects an operator does.
//
//	SANDBOX_LIVE=1 go test ./apps/sandbox/ -run TestLiveConfine -v
//
// gVisor is the CONTROL, and it is what makes a `false` for runc mean something:
// on hanzo-k8s the gvisor RuntimeClass pins to code-exec-pool with a taint, so
// it must read true. Every boundary reading false would be a broken read, not an
// uncontained fleet, and the two are indistinguishable without a control.
func TestLiveConfineReadsTheRealTopology(t *testing.T) {
	if os.Getenv("SANDBOX_LIVE") != "1" {
		t.Skip("set SANDBOX_LIVE=1 to run against a real cluster")
	}
	r := newRuntime()
	if err := r.ready(); err != nil {
		t.Fatalf("no cluster: %v", err)
	}
	ctx := context.Background()
	for _, name := range sorted() {
		t.Logf("confine(%-9q) = %v", name, r.confine(ctx, name))
	}
	if !r.confine(ctx, shared) {
		t.Fatalf("confine(%q) is false on a cluster that pins it — the read is broken, "+
			"so every other answer here means nothing", shared)
	}
	t.Logf("our boundary %q is %s", bare(),
		map[bool]string{true: "contained", false: "NOT contained — nothing will select it"}[r.bare != ""])
}

// class builds a RuntimeClass as the apiserver stores it.
func class(name string, sel map[string]any, tol []any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "node.k8s.io/v1",
		"kind":       "RuntimeClass",
		"metadata":   map[string]any{"name": name},
		"handler":    name,
	}}
	s := map[string]any{}
	if len(sel) > 0 {
		s["nodeSelector"] = sel
	}
	if len(tol) > 0 {
		s["tolerations"] = tol
	}
	if len(s) > 0 {
		u.Object["scheduling"] = s
	}
	return u
}

func fakeClasses(objs ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
	list := map[schema.GroupVersionResource]string{k8s.RuntimeClasses: "RuntimeClassList"}
	os := make([]k8sruntime.Object, 0, len(objs))
	for _, o := range objs {
		os = append(os, o)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(k8sruntime.NewScheme(), list, os...)
}

// away puts the cluster out of REACH for the length of a test, and it is not
// tidiness. Every case in this file is decided before the first call to one, so
// a developer's own kubeconfig makes that claim untestable — and it does worse
// than that: the first run of these tests created real pods in the real sandbox
// namespace and sat two minutes each waiting for them. A test that proves a
// refusal must not be able to succeed.
func away(t *testing.T) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "none"))
}

// door mounts the HTTP surface with no cluster behind it, which is all these
// need: every case is decided before the first call to one.
func door(t *testing.T) *zip.App {
	t.Helper()
	away(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func ask(t *testing.T, app *zip.App, org, user, body string) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/sandboxes", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if org != "" {
		r.Header.Set("X-Org-Id", org)
	}
	if user != "" {
		r.Header.Set("X-User-Id", user)
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("POST /v1/sandboxes: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}
