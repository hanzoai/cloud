package sandbox

// owned_test.go — a sandbox is its LESSEE'S. The org decides which store a row is
// in; the lessee decides who in the org may act on it. A member who did not lease
// it is answered exactly as a stranger is — 404 — on every way in: the resource
// surface, the agent's verbs, the fs routes, both tickets, pause, resume and end.
// The lessee and an admin of the org are not, and an admin crossing into somebody
// else's sandbox is written to the trail before the act.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// member is one caller of the org acme: a subject, and the two admin bits the
// identity boundary would have minted for them. unkeyed is a credential whose
// user id is no subject to hold a sandbox by (a token with no `sub`), which only
// the minting harness below reads.
type member struct {
	user            string
	orgAdmin, super bool
	unkeyed         bool
}

var (
	alice = member{user: "alice"}
	bob   = member{user: "bob"}
	carol = member{user: "carol", orgAdmin: true}
	dave  = member{user: "dave", super: true}
)

// owning is the resource surface and the agent's verbs over a cluster whose pods
// come up at once, with a trail to write to.
func owning(t *testing.T) (*zip.App, *Service) {
	t.Helper()
	s, _, _ := parking(t)
	rec, err := audit.Open(t.TempDir(), "audit-sandbox", nil)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	s.State.audit = rec
	app := zip.New(zip.Config{Logger: luxlog.New("owned"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	Routes(app, s)
	prior := mounted.Swap(s)
	t.Cleanup(func() { mounted.Store(prior) })
	return app, s
}

// as sends one request as m, in the org acme.
func as(t *testing.T, app *zip.App, m member, method, path, body string) (int, []byte) {
	t.Helper()
	return asIn(t, app, "acme", m, method, path, body)
}

// asIn sends one request as m, in org.
func asIn(t *testing.T, app *zip.App, org string, m member, method, path, body string) (int, []byte) {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("X-Org-Id", org)
	r.Header.Set("X-User-Id", m.user)
	if m.orgAdmin {
		r.Header.Set("X-User-IsOrgAdmin", "true")
	}
	if m.super {
		r.Header.Set("X-User-IsAdmin", "true")
	}
	if m.unkeyed {
		r.Header.Set(unkeyedHeader, "true")
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// leaseAs takes a sandbox over HTTP as m and answers it.
func leaseAs(t *testing.T, app *zip.App, m member) Sandbox {
	t.Helper()
	code, body := as(t, app, m, http.MethodPost, "/v1/sandbox", `{"class":"exec"}`)
	if code != http.StatusCreated {
		t.Fatalf("POST /v1/sandbox as %s = %d %s", m.user, code, body)
	}
	var out Sandbox
	if err := json.Unmarshal(body, &out); err != nil || out.ID == "" {
		t.Fatalf("lease reply %s: %v", body, err)
	}
	return out
}

// listed is the ids GET /v1/sandbox answers m with.
func listed(t *testing.T, app *zip.App, m member) []string {
	t.Helper()
	code, body := as(t, app, m, http.MethodGet, "/v1/sandbox", "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/sandbox as %s = %d %s", m.user, code, body)
	}
	var out sandboxList
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("list reply %s: %v", body, err)
	}
	ids := []string{}
	for _, row := range out.Sandboxes {
		ids = append(ids, row.ID)
	}
	return ids
}

// leasedBy writes a running sandbox leased by actor into one org's store.
func leasedBy(t *testing.T, s *Service, org, id, actor string) Sandbox {
	t.Helper()
	m := seed(t, s, org, id)
	store, err := storeFor(s, org)
	if err != nil {
		t.Fatalf("storeFor: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE sandbox SET actor=$1 WHERE org=$2 AND id=$3`, actor, org, id); err != nil {
		t.Fatalf("leasedBy: %v", err)
	}
	m.Actor = actor
	return m
}

// Every door into a sandbox answers a member who did not lease it the way it
// answers an id that does not exist, and the sandbox is left as it was.
func TestAMemberReachesNoSandboxAnotherMemberLeased(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := owning(t)
		m := leaseAs(t, app, alice)
		if m.Actor != "alice" {
			t.Fatalf("the lease recorded actor %q, want the caller who took it", m.Actor)
		}
		id := m.ID
		for _, c := range []struct{ method, path, body string }{
			{http.MethodGet, "/v1/sandbox/" + id, ""},
			{http.MethodPost, "/v1/sandbox/" + id + "/exec", `{"argv":["cat","/tmp/hanzo-run/key"]}`},
			{http.MethodGet, "/v1/sandbox/" + id + "/fs?path=/tmp/hanzo-run/env", ""},
			{http.MethodPost, "/v1/sandbox/" + id + "/fs?path=x", "planted"},
			{http.MethodPost, "/v1/sandbox/" + id + "/terminal/ticket", ""},
			{http.MethodPost, "/v1/sandbox/" + id + "/screen/ticket", ""},
			{http.MethodPost, "/v1/sandbox/" + id + "/pause", ""},
			{http.MethodPost, "/v1/sandbox/" + id + "/resume", ""},
			{http.MethodDelete, "/v1/sandbox/" + id, ""},
			{http.MethodPost, "/v1/sandbox/run", `{"id":"` + id + `","argv":["env"]}`},
			{http.MethodPost, "/v1/sandbox/read", `{"id":"` + id + `","path":"/tmp/hanzo-run/key"}`},
			{http.MethodPost, "/v1/sandbox/write", `{"id":"` + id + `","path":"x","data":"cGxhbnRlZA=="}`},
			{http.MethodPost, "/v1/sandbox/stop", `{"id":"` + id + `"}`},
			{http.MethodPost, "/v1/sandbox/end", `{"id":"` + id + `"}`},
		} {
			code, body := as(t, app, bob, c.method, c.path, c.body)
			if code != http.StatusNotFound {
				t.Errorf("%s %s as another member = %d %s, want 404", c.method, c.path, code, body)
			}
		}
		if slices.Contains(listed(t, app, bob), id) {
			t.Error("another member's listing names the sandbox")
		}

		// Naming it in a lease is not a resume: bob gets a sandbox of his own.
		code, body := as(t, app, bob, http.MethodPost, "/v1/sandbox/lease", `{"id":"`+id+`","class":"exec"}`)
		if code != http.StatusOK {
			t.Fatalf("lease naming another member's sandbox = %d %s, want a fresh lease", code, body)
		}
		var got client.Leased
		_ = json.Unmarshal(body, &got)
		if got.ID == "" || got.ID == id {
			t.Fatalf("lease naming another member's sandbox answered %q — it handed theirs over", got.ID)
		}

		// THE CONTROL: all of that was refused as bob and not as anybody, because
		// alice still holds a running sandbox she can use.
		code, body = as(t, app, alice, http.MethodGet, "/v1/sandbox/"+id, "")
		if code != http.StatusOK || !strings.Contains(string(body), `"status":"running"`) {
			t.Fatalf("the lessee reads her sandbox = %d %s, want it running and untouched", code, body)
		}
		if code, body := as(t, app, alice, http.MethodPost, "/v1/sandbox/"+id+"/exec", `{"argv":["true"]}`); code != http.StatusOK {
			t.Fatalf("the lessee's exec = %d %s", code, body)
		}
		if code, body := as(t, app, alice, http.MethodPost, "/v1/sandbox/"+id+"/terminal/ticket", ""); code != http.StatusCreated {
			t.Fatalf("the lessee's ticket = %d %s", code, body)
		}
		// The fs routes are untyped, and they find the lessee all the same.
		if code, body := as(t, app, alice, http.MethodPost, "/v1/sandbox/"+id+"/fs?path=notes", "hers"); code != http.StatusOK {
			t.Fatalf("the lessee's fs write = %d %s", code, body)
		}
		if code, body := as(t, app, alice, http.MethodGet, "/v1/sandbox/"+id+"/fs?path=notes", ""); code != http.StatusOK || string(body) != "hers" {
			t.Fatalf("the lessee's fs read = %d %q", code, body)
		}
		if !slices.Contains(listed(t, app, alice), id) {
			t.Fatal("the lessee's listing does not name her sandbox")
		}
		if slices.Contains(listed(t, app, alice), got.ID) {
			t.Fatal("the lessee's listing names the sandbox another member leased")
		}
		if code, body := as(t, app, alice, http.MethodDelete, "/v1/sandbox/"+id, ""); code != http.StatusNoContent && code != http.StatusOK {
			t.Fatalf("the lessee's end = %d %s", code, body)
		}
	})
}

// An admin of the org, and a SuperAdmin, act on a member's sandbox — and each such
// act is on the trail, naming the admin and the lessee, before it happens.
func TestAnAdminActsOnAMembersSandboxOnTheRecord(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := owning(t)
		m := leaseAs(t, app, alice)
		for _, admin := range []member{carol, dave} {
			if code, body := as(t, app, admin, http.MethodPost, "/v1/sandbox/"+m.ID+"/exec", `{"argv":["true"]}`); code != http.StatusOK {
				t.Fatalf("%s exec = %d %s", admin.user, code, body)
			}
			if code, body := as(t, app, admin, http.MethodPost, "/v1/sandbox/"+m.ID+"/terminal/ticket", ""); code != http.StatusCreated {
				t.Fatalf("%s ticket = %d %s", admin.user, code, body)
			}
			if !slices.Contains(listed(t, app, admin), m.ID) {
				t.Fatalf("%s's listing does not name the org's sandbox", admin.user)
			}
			rows, _, err := s.State.audit.Query(context.Background(), audit.Filter{Org: "acme", Sub: admin.user, ResourceID: m.ID})
			if err != nil {
				t.Fatalf("query trail: %v", err)
			}
			acts := map[string]bool{}
			for _, r := range rows {
				acts[r.Action] = true
				if !strings.Contains(string(r.After), `"lessee":"alice"`) {
					t.Errorf("%s's %s does not name the lessee: %s", admin.user, r.Action, r.After)
				}
			}
			if !acts["sandbox.exec"] || !acts["sandbox.terminal.ticket"] {
				t.Fatalf("%s's acts on alice's sandbox are not on the trail: %v", admin.user, acts)
			}
		}
		// The lessee acting on her own writes nothing: the trail records crossings.
		if code, _ := as(t, app, alice, http.MethodPost, "/v1/sandbox/"+m.ID+"/exec", `{"argv":["true"]}`); code != http.StatusOK {
			t.Fatalf("the lessee's exec = %d", code)
		}
		if rows, _, _ := s.State.audit.Query(context.Background(), audit.Filter{Sub: "alice"}); len(rows) != 0 {
			t.Fatalf("the lessee's own act was recorded as a crossing: %+v", rows)
		}
	})
}

// An admin act the trail cannot record is not taken.
func TestAnAdminActTheTrailCannotRecordIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := owning(t)
		m := leaseAs(t, app, alice)
		_ = s.State.audit.Close()
		code, body := as(t, app, carol, http.MethodPost, "/v1/sandbox/"+m.ID+"/exec", `{"argv":["true"]}`)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("an unrecordable admin exec = %d %s, want 503", code, body)
		}
	})
}

// On the plane, the caller is what the sibling stated. The org's own background
// work states no person, and holds only what no person leased.
func TestTheOrgsOwnWorkHoldsNoPersonsSandbox(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := owning(t)
		ready(t, s)
		theirs := leasedBy(t, s, "acme", "m_theirs", "alice")
		nobodys := leasedBy(t, s, "acme", "m_nobodys", "")

		org := cloud.For(context.Background(), "acme")
		if _, err := planeRun(org, &client.RunIn{ID: theirs.ID, Argv: []string{"env"}}); !notFound(err) {
			t.Fatalf("the org's own work ran in alice's sandbox: %v", err)
		}
		if _, err := planeRun(org, &client.RunIn{ID: nobodys.ID, Argv: []string{"true"}}); err != nil {
			t.Fatalf("the org's own work in its own sandbox: %v", err)
		}
		asAlice := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "alice"})
		if _, err := planeRun(asAlice, &client.RunIn{ID: theirs.ID, Argv: []string{"true"}}); err != nil {
			t.Fatalf("alice stated on the plane, in her own sandbox: %v", err)
		}
		asBob := zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "bob"})
		if _, err := planeRead(asBob, &client.PathIn{ID: theirs.ID, Path: "x"}); !notFound(err) {
			t.Fatalf("bob stated on the plane read alice's sandbox: %v", err)
		}
		if _, err := planeEnd(asBob, &client.EndIn{ID: theirs.ID}); !notFound(err) {
			t.Fatalf("bob stated on the plane ended alice's sandbox: %v", err)
		}
	})
}

func notFound(err error) bool {
	he, ok := err.(*zip.HTTPError)
	return ok && he.Status == http.StatusNotFound
}

// A SuperAdmin who resumes a member's parked sandbox gives it a pod, and the pod
// holds nothing of the SuperAdmin's: no fleet kubeconfig, which the member could
// read from their own shell. The control is the SuperAdmin's own parked sandbox,
// which is handed the fleet on resume exactly as on the lease.
func TestResumingAnotherMembersSandboxHandsItNoCredential(t *testing.T) {
	for _, c := range []struct {
		name, lessee string
		kube         bool
	}{
		{"a member's sandbox", "member", false},
		{"the SuperAdmin's own sandbox", "dave", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
				away(t)
				t.Setenv("CLOUD_DATA_DIR", t.TempDir())
				s, err := New(cloud.Deps{})
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				rec := &recorder{}
				r := fakeRuntime()
				r.str = rec
				r.startTimeout, r.execTimeout = 2*time.Second, 2*time.Second
				r.attached = &fakeAttached{org: "admin", name: "lab", kube: []byte(labKube)}
				r.dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("create", "pods",
					func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
						pod := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
						_ = unstructured.SetNestedField(pod.Object, "Running", "status", "phase")
						return false, nil, nil
					})
				s.State.rt = r
				store, err := storeFor(s, "admin")
				if err != nil {
					t.Fatalf("storeFor: %v", err)
				}
				now := time.Now().Unix()
				m := Sandbox{ID: "m_parked", Org: "admin", Kind: KindSandbox, Actor: c.lessee, Class: "dev",
					Project: "ops", Volume: volumeName("admin", "", "ops"), Status: statusParked, Pod: podName("m_parked"),
					CreatedAt: now, LastUsedAt: now, ExpiresAt: now + 3600}
				if err := store.Put(context.Background(), m); err != nil {
					t.Fatalf("Put: %v", err)
				}
				asDave := zip.WithCaller(context.Background(), zip.Caller{Org: "admin", User: "dave", Admin: true})
				got, err := Lease(s, asDave, "admin", account.Org("admin"), true, "", Spec{ID: m.ID})
				if err != nil || got.ID != m.ID || got.Status != statusRunning {
					t.Fatalf("resume = %+v, %v", got, err)
				}
				if wroteKube(rec) != c.kube {
					t.Fatalf("the resumed pod was handed the fleet kubeconfig = %v, want %v", wroteKube(rec), c.kube)
				}
			})
		})
	}
}

const labKube = `apiVersion: v1
kind: Config
clusters:
- name: default
  cluster: {server: "https://8.8.8.8:6443"}
contexts:
- name: default
  context: {cluster: default, user: default}
current-context: default
users:
- name: default
  user: {token: LABTOKEN}
`

// NAMING A MEMBER'S SANDBOX IN A LEASE IS A RESUME, and an admin's resume of one
// hands over the lessee's session: it is on the trail before it is handed over,
// running or parked, and one the trail cannot record is refused.
func TestAnAdminsResumeOfAMembersSandboxIsOnTheRecord(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := owning(t)
		m := leaseAs(t, app, alice)
		resumed := func(who member) string {
			t.Helper()
			code, body := as(t, app, who, http.MethodPost, "/v1/sandbox/lease", `{"id":"`+m.ID+`","class":"exec"}`)
			if code != http.StatusOK {
				t.Fatalf("%s resuming alice's sandbox = %d %s", who.user, code, body)
			}
			var got client.Leased
			_ = json.Unmarshal(body, &got)
			return got.ID
		}
		resumes := func(who member) []audit.Record {
			t.Helper()
			rows, _, err := s.State.audit.Query(context.Background(), audit.Filter{Org: "acme", Sub: who.user, ResourceID: m.ID, Action: "sandbox.resume"})
			if err != nil {
				t.Fatalf("query trail: %v", err)
			}
			return rows
		}

		if id := resumed(carol); id != m.ID {
			t.Fatalf("the org admin's resume answered %q, want alice's %q", id, m.ID)
		}
		if rows := resumes(carol); len(rows) != 1 || !strings.Contains(string(rows[0].After), `"lessee":"alice"`) {
			t.Fatalf("the org admin's resume of a running sandbox is not on the trail: %+v", rows)
		}

		// Parked, it comes back as itself for a SuperAdmin, on the record too.
		if code, body := as(t, app, alice, http.MethodPost, "/v1/sandbox/"+m.ID+"/pause", ""); code != http.StatusOK {
			t.Fatalf("the lessee's pause = %d %s", code, body)
		}
		if id := resumed(dave); id != m.ID {
			t.Fatalf("the SuperAdmin's resume answered %q, want alice's %q", id, m.ID)
		}
		if rows := resumes(dave); len(rows) != 1 {
			t.Fatalf("the SuperAdmin's resume of a parked sandbox is not on the trail: %+v", rows)
		}

		// The lessee taking back her own records nothing: the trail records crossings.
		if id := resumed(alice); id != m.ID {
			t.Fatalf("the lessee's resume answered %q", id)
		}
		if rows := resumes(alice); len(rows) != 0 {
			t.Fatalf("the lessee's own resume was recorded as a crossing: %+v", rows)
		}

		_ = s.State.audit.Close()
		if code, body := as(t, app, carol, http.MethodPost, "/v1/sandbox/lease", `{"id":"`+m.ID+`","class":"exec"}`); code != http.StatusServiceUnavailable {
			t.Fatalf("an unrecordable resume = %d %s, want 503", code, body)
		}
	})
}

// WATCHING A PROJECT stamps the watcher's own sandbox, or the org's for an admin:
// a colleague's sandbox on the project is answered as none is — not kept alive by
// it and not named — and so is a beat that states only the org.
func TestAttachStampsOnlyTheWatchersSandbox(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := owning(t)
		m := leasedBy(t, s, "acme", "sbx_atlas", "alice")
		store, _ := storeFor(s, "acme")
		if _, err := store.db.Exec(`UPDATE sandbox SET project='atlas' WHERE org='acme' AND id=$1`, m.ID); err != nil {
			t.Fatal(err)
		}
		stamp := func() int64 {
			t.Helper()
			got, err := store.Get(context.Background(), "acme", m.ID)
			if err != nil {
				t.Fatal(err)
			}
			return got.ConnectedAt
		}
		for name, ctx := range map[string]context.Context{
			"another member":     zip.WithCaller(context.Background(), zip.Caller{Org: "acme", User: "bob"}),
			"a beat for the org": cloud.For(context.Background(), "acme"),
		} {
			got, err := planeAttach(ctx, &client.AttachIn{Project: "atlas"})
			if err != nil || got.ID != "" {
				t.Fatalf("%s attached = %+v %v, want nothing", name, got, err)
			}
			if at := stamp(); at != 0 {
				t.Fatalf("%s stamped alice's sandbox watched at %d", name, at)
			}
		}
		for _, who := range []zip.Caller{
			{Org: "acme", User: "alice"},
			{Org: "acme", User: "carol", OrgAdmin: true},
		} {
			got, err := planeAttach(zip.WithCaller(context.Background(), who), &client.AttachIn{Project: "atlas"})
			if err != nil || got.ID != m.ID {
				t.Fatalf("%s attached = %+v %v, want alice's sandbox", who.User, got, err)
			}
		}
		if stamp() == 0 {
			t.Fatal("the lessee's watching did not stamp her sandbox")
		}
	})
}

// A PROJECT'S ONE SANDBOX is named, with its state, only to a caller who holds it:
// a colleague asking for the project's disk is refused without either.
func TestAProjectsSandboxIsNamedOnlyToItsHolder(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := owning(t)
		m := leasedBy(t, s, "acme", "sbx_atlas", "alice")
		store, _ := storeFor(s, "acme")
		if _, err := store.db.Exec(`UPDATE sandbox SET project='atlas' WHERE org='acme' AND id=$1`, m.ID); err != nil {
			t.Fatal(err)
		}
		code, body := as(t, app, bob, http.MethodPost, "/v1/sandbox", `{"class":"exec","project":"atlas"}`)
		if code != http.StatusConflict || strings.Contains(string(body), m.ID) || strings.Contains(string(body), "running") {
			t.Fatalf("another member asking for the project = %d %s, want 409 naming nothing of alice's", code, body)
		}
		for _, who := range []member{alice, carol} {
			code, body := as(t, app, who, http.MethodPost, "/v1/sandbox", `{"class":"exec","project":"atlas"}`)
			if code != http.StatusConflict || !strings.Contains(string(body), m.ID) {
				t.Fatalf("%s asking for the project = %d %s, want 409 naming the sandbox", who.user, code, body)
			}
		}
	})
}

// unkeyedHeader is how a test request says its credential names no subject: the
// minting harness mints it that way. Only this harness reads it.
const unkeyedHeader = "X-Test-Unkeyed"

// minting is owning behind an identity boundary: every request is minted as the
// principal its headers name, as the one in every app process mints it from a
// verified token (principal.Mint) — Stable unless it says it is unkeyed.
func minting(t *testing.T) (*zip.App, *Service) {
	t.Helper()
	s, _, _ := parking(t)
	app := zip.New(zip.Config{Logger: luxlog.New("minting"), DisableStartupMessage: true})
	app.Use(zip.Handler(func(c *zip.Ctx) error {
		principal.Mint(c, principal.Principal{Org: c.Header("X-Org-Id"), User: c.User(), Stable: c.Header(unkeyedHeader) == ""})
		return c.Continue()
	}))
	app.Use(cloud.Bridge())
	Routes(app, s)
	prior := mounted.Swap(s)
	t.Cleanup(func() { mounted.Store(prior) })
	return app, s
}

// A SANDBOX IS HELD BY A SUBJECT NOBODY ELSE CAN PRESENT. A credential that names
// none — a token with no `sub`, whose user id is its username or its display name
// — leases nothing, and holds nothing of anyone who shares that name, nor of the
// org's own background work. An admin holding such a credential still holds the
// org's, and still leases nothing.
func TestASandboxIsHeldOnlyByAStableSubject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := minting(t)
		zach := member{user: "zach"}
		namesake := member{user: "zach", unkeyed: true}
		m := leaseAs(t, app, zach)
		if m.Actor != "zach" {
			t.Fatalf("the lease recorded actor %q", m.Actor)
		}
		bg := leasedBy(t, s, "acme", "sbx_bg", "")

		for _, id := range []string{m.ID, bg.ID} {
			if code, body := as(t, app, namesake, http.MethodGet, "/v1/sandbox/"+id, ""); code != http.StatusNotFound {
				t.Fatalf("a namesake with no subject read %s = %d %s, want 404", id, code, body)
			}
			if code, body := as(t, app, namesake, http.MethodPost, "/v1/sandbox/"+id+"/exec", `{"argv":["true"]}`); code != http.StatusNotFound {
				t.Fatalf("a namesake with no subject ran in %s = %d %s, want 404", id, code, body)
			}
		}
		if ids := listed(t, app, namesake); len(ids) != 0 {
			t.Fatalf("a namesake with no subject is listed %v", ids)
		}
		for _, who := range []member{namesake, {user: "carol", orgAdmin: true, unkeyed: true}} {
			if code, body := as(t, app, who, http.MethodPost, "/v1/sandbox", `{"class":"exec"}`); code != http.StatusForbidden {
				t.Fatalf("%s with no subject leased = %d %s, want 403", who.user, code, body)
			}
		}
		if code, body := as(t, app, member{user: "carol", orgAdmin: true, unkeyed: true}, http.MethodGet, "/v1/sandbox/"+m.ID, ""); code != http.StatusOK {
			t.Fatalf("an org admin with no subject reading the org's sandbox = %d %s", code, body)
		}
		if code, body := as(t, app, zach, http.MethodGet, "/v1/sandbox/"+m.ID, ""); code != http.StatusOK {
			t.Fatalf("the lessee reads her sandbox = %d %s", code, body)
		}
	})
}

// An admin whose credential names no subject still crosses into the org's
// sandboxes, and the trail names them by the user id their token did carry: an
// act recorded with no actor is one nobody can be asked about.
func TestAnUnkeyedAdminsCrossingNamesThem(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := minting(t)
		rec, err := audit.Open(t.TempDir(), "audit-sandbox", nil)
		if err != nil {
			t.Fatalf("audit: %v", err)
		}
		t.Cleanup(func() { _ = rec.Close() })
		s.State.audit = rec
		m := leaseAs(t, app, member{user: "zach"})
		carol := member{user: "Carol Admin", orgAdmin: true, unkeyed: true}
		if code, body := as(t, app, carol, http.MethodPost, "/v1/sandbox/"+m.ID+"/exec", `{"argv":["true"]}`); code != http.StatusOK {
			t.Fatalf("an unkeyed admin's exec = %d %s", code, body)
		}
		rows, _, err := rec.Query(context.Background(), audit.Filter{Org: "acme", ResourceID: m.ID, Action: "sandbox.exec"})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].Actor.Sub != "Carol Admin" {
			t.Fatalf("the crossing is recorded as %+v", rows)
		}
	})
}

// TestSandboxListReturnsCallingOrgSandboxesOnly verifies that GET /v1/sandbox
// returns only the calling org's own sandboxes (empty when none are leased,
// never sample/demo fixtures or another org's sandboxes).
func TestSandboxListReturnsCallingOrgSandboxesOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := owning(t)

		// Lease a sandbox in org "acme".
		acmeSbx := leaseAs(t, app, alice)

		// Query GET /v1/sandbox as org "jamessburnss1313" before leasing anything:
		// must return 200 OK with an empty list (never acme's sandbox or sample fixtures).
		req := httptest.NewRequest(http.MethodGet, "/v1/sandbox", nil)
		req.Header.Set("X-Org-Id", "jamessburnss1313")
		req.Header.Set("X-User-Id", "jb")
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("GET /v1/sandbox as jamessburnss1313: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /v1/sandbox status = %d %s, want 200", resp.StatusCode, body)
		}
		var emptyOut sandboxList
		if err := json.Unmarshal(body, &emptyOut); err != nil {
			t.Fatalf("decode empty list: %v (%s)", err, body)
		}
		if len(emptyOut.Sandboxes) != 0 {
			t.Fatalf("new org want 0 sandboxes, got %+v", emptyOut.Sandboxes)
		}

		// Lease a sandbox in org "jamessburnss1313" and verify GET /v1/sandbox returns only it.
		leaseReq := httptest.NewRequest(http.MethodPost, "/v1/sandbox", strings.NewReader(`{"class":"dev","project":"app"}`))
		leaseReq.Header.Set("Content-Type", "application/json")
		leaseReq.Header.Set("X-Org-Id", "jamessburnss1313")
		leaseReq.Header.Set("X-User-Id", "jb")
		leaseResp, err := app.Test(leaseReq, zip.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("POST /v1/sandbox as jamessburnss1313: %v", err)
		}
		leaseBody, _ := io.ReadAll(leaseResp.Body)
		_ = leaseResp.Body.Close()
		if leaseResp.StatusCode != http.StatusCreated {
			t.Fatalf("POST /v1/sandbox status = %d %s, want 201", leaseResp.StatusCode, leaseBody)
		}
		var jbSbx Sandbox
		if err := json.Unmarshal(leaseBody, &jbSbx); err != nil || jbSbx.ID == "" {
			t.Fatalf("decode leased sandbox: %v (%s)", err, leaseBody)
		}

		listReq := httptest.NewRequest(http.MethodGet, "/v1/sandbox", nil)
		listReq.Header.Set("X-Org-Id", "jamessburnss1313")
		listReq.Header.Set("X-User-Id", "jb")
		listResp, err := app.Test(listReq, zip.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("GET /v1/sandbox after lease: %v", err)
		}
		listBody, _ := io.ReadAll(listResp.Body)
		_ = listResp.Body.Close()
		var jbOut sandboxList
		if err := json.Unmarshal(listBody, &jbOut); err != nil {
			t.Fatalf("decode list after lease: %v (%s)", err, listBody)
		}
		if len(jbOut.Sandboxes) != 1 || jbOut.Sandboxes[0].ID != jbSbx.ID || jbOut.Sandboxes[0].ID == acmeSbx.ID {
			t.Fatalf("jamessburnss1313 list = %+v, want only [%s]", jbOut.Sandboxes, jbSbx.ID)
		}
	})
}
